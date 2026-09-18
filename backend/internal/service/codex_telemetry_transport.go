package service

import (
	"bytes"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/tidwall/gjson"
)

// Do not infer Windows sandbox/tool/plugin activity from model traffic. These
// observers record only actual successful writes and received protocol events.
func codexTelemetryEventKind(kind string) bool {
	switch kind {
	case "codex.rate_limits", "codex.response.metadata", "responsesapi.websocket_timing",
		"response.compaction.compacting", "response.completed", "response.created", "response.in_progress",
		"response.failed", "response.incomplete", "error", "read_error",
		"response.content_part.added", "response.content_part.done",
		"response.custom_tool_call_input.delta", "response.custom_tool_call_input.done",
		"response.function_call_arguments.delta", "response.function_call_arguments.done",
		"response.output_item.added", "response.output_item.done",
		"response.output_text.delta", "response.output_text.done",
		"response.reasoning_summary_text.delta", "response.reasoning_summary_text.done",
		"response.reasoning_summary_part.added", "response.reasoning_summary_part.done":
		return true
	}
	return false
}

func codexTelemetryHTTPModelRequest(req *http.Request, account *Account) bool {
	return req != nil && req.URL != nil && account != nil && account.IsOpenAIOAuth() &&
		req.Method == http.MethodPost && req.URL.Scheme == "https" && req.URL.Hostname() == "chatgpt.com" &&
		(req.URL.Path == "/backend-api/codex/responses" || req.URL.Path == "/backend-api/codex/responses/compact")
}

func (s *OpenAIGatewayService) observeCodexHTTPRequest(req *http.Request, resp *http.Response, account *Account, proxyURL string, profile *tlsfingerprint.Profile, duration time.Duration, err error) {
	if !codexTelemetryHTTPModelRequest(req, account) {
		return
	}
	status := 0
	if resp != nil {
		status = resp.StatusCode
	}
	t := s.getCodexTelemetry()
	t.recordAPIRequest(codexTelemetryRouteForAccount(account, proxyURL, profile),
		strings.TrimPrefix(req.Header.Get("x-codex-routing-hint"), "model="), status,
		err == nil && status >= 200 && status < 300, duration)
	if err == nil {
		s.observeCodexHTTPResponse(req, resp, account, proxyURL, profile)
	}
}

// These mappings are verified against all 103 timing samples and the matching
// captured OTLP histograms. Missing/null values are not zero measurements.
func (t *codexTelemetryExporter) recordServerTiming(route codexTelemetryRoute, model string, payload []byte) {
	if t == nil || !gjson.ValidBytes(payload) || gjson.GetBytes(payload, "type").String() != "responsesapi.websocket_timing" {
		return
	}
	for _, metric := range []struct {
		field, name string
		round       bool
	}{
		{"responses_duration_excl_engine_and_client_tool_time_ms", "codex.responses_api_overhead.duration_ms", true},
		{"engine_service_total_ms", "codex.responses_api_inference_time.duration_ms", true},
		{"engine_iapi_tbt_across_engine_calls_ms", "codex.responses_api_engine_iapi_tbt.duration_ms", false},
	} {
		value := gjson.GetBytes(payload, "timing_metrics."+metric.field)
		if value.Type != gjson.Number || value.Float() < 0 {
			continue
		}
		milliseconds := value.Float()
		if metric.round {
			milliseconds = math.Round(milliseconds)
		}
		t.recordDuration(route, metric.name, model, "", milliseconds)
	}
}

func (s *OpenAIGatewayService) observeCodexHTTPResponse(req *http.Request, resp *http.Response, account *Account, proxyURL string, profile *tlsfingerprint.Profile) {
	if req == nil || req.URL == nil || resp == nil || resp.Body == nil || account == nil ||
		account.Platform != PlatformOpenAI || account.Type != AccountTypeOAuth ||
		req.URL.Hostname() != "chatgpt.com" || req.URL.Path != "/backend-api/codex/responses" ||
		resp.StatusCode < 200 || resp.StatusCode >= 300 || !strings.Contains(req.Header.Get("accept"), "text/event-stream") {
		return
	}
	// Lite responses in the capture omit Content-Type. The request's Accept
	// remains authoritative in that case; a known non-SSE response is untouched.
	if ct := resp.Header.Get("content-type"); ct != "" && !strings.Contains(ct, "text/event-stream") {
		return
	}
	t := s.getCodexTelemetry()
	if t == nil {
		return
	}
	resp.Body = &codexTelemetrySSEBody{ReadCloser: resp.Body, exporter: t,
		route: codexTelemetryRouteForAccount(account, proxyURL, profile),
		model: strings.TrimPrefix(req.Header.Get("x-codex-routing-hint"), "model=")}
}

// At most 4 KiB of an unfinished SSE line is held. The original stream is
// returned immediately and byte-for-byte, including on cancellation and EOF.
type codexTelemetrySSEBody struct {
	io.ReadCloser
	exporter     *codexTelemetryExporter
	route        codexTelemetryRoute
	model        string
	line         []byte
	overflow     bool
	event        string
	hasData      bool
	data         []byte
	dataOverflow bool
	compacted    bool
	errRecorded  bool
	waitDuration time.Duration
}

func (b *codexTelemetrySSEBody) Read(dst []byte) (int, error) {
	start := time.Now()
	n, err := b.ReadCloser.Read(dst)
	b.waitDuration += time.Since(start)
	remaining := dst[:n]
	for len(remaining) > 0 {
		index := bytes.IndexByte(remaining, '\n')
		part := remaining
		if index >= 0 {
			part = remaining[:index]
		}
		room := 4096 - len(b.line)
		if room > 0 {
			b.line = append(b.line, part[:min(room, len(part))]...)
		}
		if len(part) > room {
			b.overflow = true
		}
		if index < 0 {
			break
		}
		line := bytes.TrimSuffix(b.line, []byte{'\r'})
		if len(line) == 0 && !b.overflow {
			if b.hasData && codexTelemetryEventKind(b.event) {
				b.exporter.record(b.route, "codex.sse_event", b.model, b.event, true, b.waitDuration)
				if b.event == "response.compaction.compacting" && !b.compacted {
					b.exporter.recordCompaction(b.route, b.model)
					b.compacted = true
				}
				if b.event == "responsesapi.websocket_timing" && !b.dataOverflow {
					b.exporter.recordServerTiming(b.route, b.model, b.data)
				}
			}
			b.waitDuration = 0
			b.event = ""
			b.hasData = false
			b.data = b.data[:0]
			b.dataOverflow = false
		} else if bytes.HasPrefix(line, []byte("event:")) {
			kind := strings.TrimSpace(string(line[len("event:"):]))
			if codexTelemetryEventKind(kind) {
				b.event = kind
			}
		} else if bytes.HasPrefix(line, []byte("data:")) {
			b.hasData = true
			data := bytes.TrimPrefix(line[len("data:"):], []byte{' '})
			if len(b.data)+len(data)+1 <= 4096 && !b.overflow && !b.dataOverflow {
				b.data = append(b.data, data...)
				b.data = append(b.data, '\n')
			} else {
				b.dataOverflow = true
			}
			if b.event == "" {
				b.event = gjson.GetBytes(b.data, "type").String()
				if b.event == "" {
					b.event = gjson.GetBytes(data, "type").String()
				}
			}
		}
		b.line = b.line[:0]
		b.overflow = false
		remaining = remaining[index+1:]
	}
	if err != nil && err != io.EOF && !b.errRecorded {
		b.errRecorded = true
		b.exporter.record(b.route, "codex.sse_event", b.model, "read_error", false, b.waitDuration)
	}
	return n, err
}

type codexTelemetryWS struct {
	exporter        *codexTelemetryExporter
	route           codexTelemetryRoute
	model           atomic.Value // string; writes and reads may run concurrently
	mu              sync.Mutex
	requests        []*codexTelemetryWSRequest
	active          *codexTelemetryWSRequest
	readErrRecorded bool
}

type codexTelemetryWSRequest struct {
	model, eventModel, responseID string
	compacted, terminal           bool
}

// Register before writing, since the read goroutine may receive response.created
// before Write returns. Keep only bounded correlation data, never request bodies.
func (o *codexTelemetryWS) prepareWrite(value any) *codexTelemetryWSRequest {
	if o == nil {
		return nil
	}
	var payload []byte
	switch v := value.(type) {
	case []byte:
		payload = v
	case json.RawMessage:
		payload = v
	default:
		payload, _ = json.Marshal(value)
	}
	if gjson.GetBytes(payload, "type").String() != "response.create" {
		return nil
	}
	model := gjson.GetBytes(payload, "model").String()
	if !isCodexTelemetryModel(model) {
		model = "other"
	}
	model = strings.Clone(model)
	o.model.Store(model)
	r := &codexTelemetryWSRequest{model: model, eventModel: model}
	o.mu.Lock()
	if len(o.requests) >= 32 {
		copy(o.requests, o.requests[1:])
		o.requests = o.requests[:31]
	}
	o.requests = append(o.requests, r)
	o.mu.Unlock()
	return r
}

func (o *codexTelemetryWS) written(request *codexTelemetryWSRequest, duration time.Duration, err error) {
	if o == nil || request == nil {
		return
	}
	o.exporter.record(o.route, "codex.websocket.request", request.model, "", err == nil, duration)
	if err != nil {
		o.mu.Lock()
		request.terminal = true
		o.mu.Unlock()
	}
}

func (o *codexTelemetryWS) received(payload []byte, duration time.Duration, err error) {
	if o == nil {
		return
	}
	model, _ := o.model.Load().(string)
	kind := gjson.GetBytes(payload, "type").String()
	if err != nil {
		kind = "read_error"
	}
	// Opaque/binary frames are passed through untouched. Do not claim a decoded
	// event or count a completion that the observer cannot establish.
	if !codexTelemetryEventKind(kind) {
		return
	}
	o.mu.Lock()
	if err != nil {
		if o.readErrRecorded {
			o.mu.Unlock()
			return
		}
		o.readErrRecorded = true
	} else {
		o.readErrRecorded = false
	}
	responseID := gjson.GetBytes(payload, "response.id").String()
	if responseID == "" {
		responseID = gjson.GetBytes(payload, "response_id").String()
	}
	if kind == "responsesapi.websocket_timing" {
		responseID = gjson.GetBytes(payload, "timing_metrics.response_id").String()
	}
	invalidResponseID := len(responseID) > 256
	if invalidResponseID {
		responseID = ""
	}
	var request *codexTelemetryWSRequest
	if responseID != "" {
		for _, candidate := range o.requests {
			if candidate.responseID == responseID {
				request = candidate
				break
			}
		}
	}
	if request == nil && !invalidResponseID && (kind == "response.created" || responseID == "") {
		request = o.active
		if kind == "response.created" || request == nil || request.terminal {
			request = nil
			pending := 0
			for _, candidate := range o.requests {
				if candidate.responseID == "" && !candidate.terminal {
					pending++
					if request == nil {
						request = candidate
					}
				}
			}
			if request != nil && kind == "response.created" && pending > 1 {
				// Concurrent writes have no guaranteed registration/wire order.
				// Prefer the upstream model below; otherwise keep attribution unknown.
				request.eventModel = "other"
			}
			if request == nil && kind != "response.created" {
				request = o.active
			}
		}
		if request != nil && kind == "response.created" {
			request.responseID = strings.Clone(responseID)
			if actualModel := gjson.GetBytes(payload, "response.model").String(); isCodexTelemetryModel(actualModel) {
				request.eventModel = strings.Clone(actualModel)
			}
			o.active = request
		}
	}
	compaction := false
	if request != nil {
		model = request.eventModel
		if kind == "response.compaction.compacting" && !request.compacted {
			compaction = true
			request.compacted = true
		}
		if kind == "response.completed" || kind == "response.failed" || kind == "response.incomplete" || kind == "error" {
			request.terminal = true
		}
	} else if responseID != "" || invalidResponseID {
		model = "other" // An evicted/unknown response must not inherit another model.
	}
	o.mu.Unlock()
	o.exporter.record(o.route, "codex.websocket.event", model, kind, err == nil, duration)
	if compaction {
		o.exporter.recordCompaction(o.route, model)
	}
	if err == nil && kind == "responsesapi.websocket_timing" {
		o.exporter.recordServerTiming(o.route, model, payload)
	}
}
