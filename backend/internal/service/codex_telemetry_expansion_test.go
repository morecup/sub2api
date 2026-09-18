package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/httpclient"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestCodexTelemetryReplayCapturedTimingHistograms(t *testing.T) {
	var fixture struct {
		Samples  []map[string]float64 `json:"samples"`
		Expected map[string]struct {
			Count         uint64 `json:"count"`
			Sum, Min, Max float64
			Bounds        []float64 `json:"bounds"`
			Buckets       []uint64  `json:"buckets"`
		} `json:"expected"`
	}
	raw, err := os.ReadFile("testdata/codex_telemetry_timing_0155.json")
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(raw, &fixture))
	require.Len(t, fixture.Samples, 103)
	e := newTestCodexTelemetry()
	for _, sample := range fixture.Samples {
		payload, err := json.Marshal(map[string]any{"type": "responsesapi.websocket_timing", "timing_metrics": sample})
		require.NoError(t, err)
		e.recordServerTiming(codexTelemetryRoute{}, "gpt-6-astra", payload)
	}
	require.Len(t, e.series, 3, "timing histograms do not invent counters")
	for key, actual := range e.series {
		want, ok := fixture.Expected[key.name]
		require.True(t, ok, key.name)
		require.Equal(t, want.Count, actual.count, key.name)
		require.InDelta(t, want.Sum, actual.sum, 1e-8, key.name)
		require.Equal(t, want.Min, actual.min, key.name)
		require.Equal(t, want.Max, actual.max, key.name)
		require.Equal(t, want.Bounds, codexTelemetryBounds)
		require.Equal(t, want.Buckets, actual.buckets, key.name)
		require.Empty(t, key.success)
	}
	for _, metric := range gjson.GetBytes(codexTelemetryPayload(e.series, time.Now()), "resourceMetrics.0.scopeMetrics.0.metrics").Array() {
		require.True(t, metric.Get("histogram").Exists())
		require.Equal(t, "ms", metric.Get("unit").String())
	}
}

func TestCodexTelemetryTimingMissingInvalidAndZero(t *testing.T) {
	e := newTestCodexTelemetry()
	for _, payload := range []string{
		`{"type":"responsesapi.websocket_timing","timing_metrics":{}}`,
		`{"type":"responsesapi.websocket_timing","timing_metrics":{"engine_service_total_ms":null,"engine_iapi_tbt_across_engine_calls_ms":"SECRET","responses_duration_excl_engine_and_client_tool_time_ms":-1}}`,
		`{"type":"responsesapi.websocket_timing","timing_metrics":{"engine_service_total_ms":1e1000}}`,
		`{"type":"responsesapi.websocket_timing","timing_metrics":{"engine_service_total_ms":1e308}}`,
		`{"type":"responsesapi.websocket_timing","timing_metrics":{"engine_service_total_ms":12}`,
		`{"type":"other","timing_metrics":{"engine_service_total_ms":12}}`,
	} {
		e.recordServerTiming(codexTelemetryRoute{}, "gpt-5.5", []byte(payload))
	}
	require.Empty(t, e.series)
	e.recordServerTiming(codexTelemetryRoute{}, "gpt-5.5", []byte(`{"type":"responsesapi.websocket_timing","timing_metrics":{"engine_service_total_ms":0}}`))
	require.Len(t, e.series, 1)
	for _, a := range e.series {
		require.Equal(t, uint64(1), a.count)
		require.Zero(t, a.sum)
	}
}

func TestCodexTelemetryRejectsUnboundedNamesAndLabels(t *testing.T) {
	e := newTestCodexTelemetry()
	e.record(codexTelemetryRoute{}, "SECRET", "gpt-5.5", "", true, time.Millisecond)
	e.record(codexTelemetryRoute{}, "codex.sse_event", "gpt-5.5", "SECRET", true, time.Millisecond)
	e.recordDuration(codexTelemetryRoute{}, "SECRET", "gpt-5.5", "SECRET", 2)
	for _, value := range []float64{math.NaN(), math.Inf(1), math.Inf(-1), -1} {
		e.recordDuration(codexTelemetryRoute{}, "codex.responses_api_overhead.duration_ms", "gpt-5.5", "", value)
	}
	require.Empty(t, e.series)
	e.record(codexTelemetryRoute{}, "codex.websocket.request", "SECRET", "SECRET", true, time.Millisecond)
	e.recordDuration(codexTelemetryRoute{}, "codex.responses_api_overhead.duration_ms", "SECRET", "SECRET", 2)
	e.recordAPIRequest(codexTelemetryRoute{}, "SECRET", 100000, false, -time.Second)
	raw := string(codexTelemetryPayload(e.series, time.Now()))
	require.NotContains(t, raw, "SECRET")
	require.NotContains(t, raw, "100000")
	require.Contains(t, raw, "unknown")
}

func TestCodexTelemetryHTTPAttemptIntegration(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		err    error
	}{
		{"success", 200, nil}, {"unauthorized", 401, nil}, {"limited", 429, nil},
		{"transport", 0, context.DeadlineExceeded}, {"response_and_error", 502, context.Canceled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &config.Config{}
			cfg.Gateway.CodexTelemetry = config.CodexTelemetryConfig{Mode: "local", LocalPath: filepath.Join(t.TempDir(), "metrics.jsonl")}
			calls := 0
			upstream := &cookieIntegrationUpstream{send: func(*http.Request) (*http.Response, error) {
				calls++
				if tc.status == 0 {
					return nil, tc.err
				}
				return &http.Response{StatusCode: tc.status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("PRIVATE"))}, tc.err
			}}
			svc := &OpenAIGatewayService{cfg: cfg, httpUpstream: upstream}
			t.Cleanup(svc.CloseOpenAIWSPool)
			req := httptest.NewRequest(http.MethodPost, "https://chatgpt.com/backend-api/codex/responses", nil)
			req.Header.Set("x-codex-routing-hint", "model=gpt-6-astra")
			resp, err := svc.doUpstreamRequest(req, "http://user:SECRET@proxy.invalid", &Account{ID: 9, Platform: PlatformOpenAI, Type: AccountTypeOAuth})
			require.Equal(t, tc.err, err)
			require.Equal(t, 1, calls, "telemetry must not add model retries")
			if resp != nil {
				raw, err := io.ReadAll(resp.Body)
				require.NoError(t, err)
				require.Equal(t, "PRIVATE", string(raw))
				require.NoError(t, resp.Body.Close())
			}
			e := svc.getCodexTelemetry()
			require.Len(t, e.series, 2)
			for key, value := range e.series {
				require.Equal(t, uint64(1), value.count)
				require.Equal(t, "gpt-6-astra", key.model)
				require.Equal(t, tc.err == nil && tc.status == 200, key.success == "true")
				if tc.status == 0 {
					require.Equal(t, "unknown", key.status)
				} else {
					require.Equal(t, fmt.Sprint(tc.status), key.status)
				}
				require.Equal(t, "http://user:SECRET@proxy.invalid", value.route.proxyURL)
			}
			payload := string(codexTelemetryPayload(e.series, time.Now()))
			require.NotContains(t, payload, "PRIVATE")
			require.NotContains(t, payload, "SECRET")
		})
	}
}

func TestCodexTelemetryHTTPGatesAndOff(t *testing.T) {
	for _, tc := range []struct{ endpoint, accountType, mode string }{
		{"https://api.openai.com/v1/responses", AccountTypeAPIKey, "local"},
		{"https://chatgpt.com/backend-api/codex/responses", AccountTypeAPIKey, "local"},
		{"https://other.invalid/backend-api/codex/responses", AccountTypeOAuth, "local"},
		{"https://chatgpt.com/backend-api/codex/responses", AccountTypeOAuth, "off"},
	} {
		cfg := &config.Config{}
		cfg.Gateway.CodexTelemetry.Mode = tc.mode
		svc := &OpenAIGatewayService{cfg: cfg}
		req := httptest.NewRequest(http.MethodPost, tc.endpoint, nil)
		svc.observeCodexHTTPRequest(req, nil, &Account{ID: 9, Platform: PlatformOpenAI, Type: tc.accountType}, "", nil, time.Millisecond, context.Canceled)
		require.Nil(t, svc.codexTelemetry)
	}
}

func TestCodexTelemetrySSETimingMultilineAndCompactionOnce(t *testing.T) {
	stream := "event: response.compaction.compacting\ndata: {}\n\n" +
		"event: response.compaction.compacting\ndata: {}\n\n" +
		"data: {\"type\":\"responsesapi.websocket_timing\",\n" +
		"data: \"timing_metrics\":{\"engine_service_total_ms\":12.6}}\n\n" +
		"data: {\"type\":\"response.completed\"}\n\n"
	for _, size := range []int{1, 17, 8192} {
		e := newTestCodexTelemetry()
		b := &codexTelemetrySSEBody{ReadCloser: io.NopCloser(strings.NewReader(stream)), exporter: e, model: "gpt-5.5"}
		var out bytes.Buffer
		buf := make([]byte, size)
		for {
			n, err := b.Read(buf)
			out.Write(buf[:n])
			if err != nil {
				require.ErrorIs(t, err, io.EOF)
				break
			}
		}
		require.Equal(t, stream, out.String())
		var timing, compact bool
		for key, a := range e.series {
			switch key.name {
			case "codex.task.compact":
				compact = true
				require.Equal(t, uint64(1), a.count)
				require.Equal(t, "remote_v2", key.compactType)
			case "codex.responses_api_inference_time.duration_ms":
				timing = true
				require.Equal(t, float64(13), a.sum)
			}
		}
		require.True(t, compact)
		require.True(t, timing)
		require.LessOrEqual(t, cap(b.line), 8192)
		require.LessOrEqual(t, cap(b.data), 8192)
	}
}

func TestCodexTelemetryWSCorrelatesLateTimingAndCompaction(t *testing.T) {
	e := newTestCodexTelemetry()
	o := &codexTelemetryWS{exporter: e}
	request := o.prepareWrite([]byte(`{"type":"response.create","model":"gpt-6-astra"}`))
	// A read can complete before the write returns.
	o.received([]byte(`{"type":"response.created","response":{"id":"resp_private_old"}}`), time.Millisecond, nil)
	o.written(request, time.Millisecond, nil)
	for range 2 {
		o.received([]byte(`{"type":"response.compaction.compacting"}`), time.Millisecond, nil)
	}
	o.received([]byte(`{"type":"response.completed","response":{"id":"resp_private_old"}}`), time.Millisecond, nil)
	next := o.prepareWrite([]byte(`{"type":"response.create","model":"gpt-5.5"}`))
	o.written(next, time.Millisecond, nil)
	o.received([]byte(`{"type":"response.created","response":{"id":"resp_private_new"}}`), time.Millisecond, nil)
	o.received([]byte(`{"type":"responsesapi.websocket_timing","timing_metrics":{"response_id":"resp_private_old","engine_service_total_ms":18}}`), time.Millisecond, nil)
	o.received(nil, time.Millisecond, context.Canceled)
	o.received(nil, time.Millisecond, context.Canceled)
	var timing, compact, readError bool
	for key, a := range e.series {
		switch key.name {
		case "codex.responses_api_inference_time.duration_ms":
			timing = true
			require.Equal(t, "gpt-6-astra", key.model)
			require.Equal(t, float64(18), a.sum)
		case "codex.task.compact":
			compact = true
			require.Equal(t, uint64(1), a.count)
			require.Equal(t, "gpt-6-astra", key.model)
		case "codex.websocket.event":
			if key.kind == "read_error" {
				readError = true
				require.Equal(t, uint64(1), a.count)
			}
		}
	}
	require.True(t, timing && compact && readError)
	require.NotContains(t, string(codexTelemetryPayload(e.series, time.Now())), "private")
}

func TestCodexTelemetryWSCorrelationBoundAndUnknownResponse(t *testing.T) {
	e := newTestCodexTelemetry()
	o := &codexTelemetryWS{exporter: e}
	for i := 0; i < 80; i++ {
		request := o.prepareWrite([]byte(`{"type":"response.create","model":"gpt-5.5"}`))
		o.written(request, time.Millisecond, nil)
		o.received([]byte(fmt.Sprintf(`{"type":"response.created","response":{"id":"resp_%d"}}`, i)), time.Millisecond, nil)
		o.received([]byte(fmt.Sprintf(`{"type":"response.completed","response":{"id":"resp_%d"}}`, i)), time.Millisecond, nil)
	}
	require.Len(t, o.requests, 32)
	o.received([]byte(`{"type":"responsesapi.websocket_timing","timing_metrics":{"response_id":"resp_0","engine_service_total_ms":1}}`), time.Millisecond, nil)
	for key := range e.series {
		if key.name == "codex.responses_api_inference_time.duration_ms" {
			require.Equal(t, "other", key.model)
		}
	}
}

type codexTelemetryRoundTripper func(*http.Request) (*http.Response, error)

func (f codexTelemetryRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestCodexTelemetryModelManifestActualFetch(t *testing.T) {
	// Replace a uniquely keyed cached client's transport: all traffic stays in
	// this fixture even though the request has the real endpoint's URL shape.
	proxy := "http://telemetry-model-fixture.invalid:18471"
	client, err := httpclient.GetClient(httpclient.Options{ProxyURL: proxy, Timeout: codexModelsManifestRequestTimeout, ResponseHeaderTimeout: 10 * time.Second})
	require.NoError(t, err)
	old := client.Transport
	t.Cleanup(func() { client.Transport = old })
	status := 200
	calls := 0
	client.Transport = codexTelemetryRoundTripper(func(req *http.Request) (*http.Response, error) {
		calls++
		require.Equal(t, "chatgpt.com", req.URL.Hostname())
		return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"models":[]}`))}, nil
	})
	cfg := &config.Config{}
	cfg.Gateway.CodexTelemetry = config.CodexTelemetryConfig{Mode: "local", LocalPath: filepath.Join(t.TempDir(), "metrics.jsonl")}
	svc := &OpenAIGatewayService{cfg: cfg}
	t.Cleanup(svc.CloseOpenAIWSPool)
	request := codexModelsManifestRequest{url: "https://chatgpt.com/backend-api/codex/models", proxyURL: proxy, accountID: 9, credentialAccount: &Account{ID: 9, Platform: PlatformOpenAI, Type: AccountTypeOAuth}}
	for _, code := range []int{200, 304} {
		status = code
		manifest, err := svc.fetchCodexModelsManifestUpstream(context.Background(), request, "")
		require.NoError(t, err)
		require.NotNil(t, manifest)
	}
	require.Equal(t, 2, calls)
	e := svc.getCodexTelemetry()
	require.Len(t, e.series, 1)
	for key, a := range e.series {
		require.Equal(t, "codex.remote_models.fetch_update.duration_ms", key.name)
		require.Equal(t, uint64(2), a.count)
		require.Equal(t, proxy, a.route.proxyURL)
		require.Empty(t, key.success)
	}
}

func TestCodexTelemetryHTTPBridgeRecordsActualSwitch(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cfg := &config.Config{}
	cfg.Gateway.CodexTelemetry = config.CodexTelemetryConfig{Mode: "local", LocalPath: filepath.Join(t.TempDir(), "metrics.jsonl")}
	calls := 0
	upstream := &cookieIntegrationUpstream{send: func(req *http.Request) (*http.Response, error) {
		calls++
		require.Equal(t, http.MethodPost, req.Method)
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_bridge_fixture\",\"model\":\"gpt-6-astra\",\"usage\":{\"input_tokens\":3,\"output_tokens\":1}}}\n\n"))}, nil
	}}
	svc := &OpenAIGatewayService{cfg: cfg, httpUpstream: upstream}
	t.Cleanup(svc.CloseOpenAIWSPool)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/responses", nil)
	payload := []byte(`{"type":"response.create","model":"gpt-6-astra","input":"fixture","stream":true}`)
	_, err := svc.proxyOpenAIWSHTTPBridgeTurn(context.Background(), c, &Account{ID: 9, Platform: PlatformOpenAI, Type: AccountTypeOAuth}, "fixture-only", payload, len(payload), "gpt-6-astra", "", "", "", "", 1, func([]byte) error { return nil })
	require.NoError(t, err)
	require.Equal(t, 1, calls)
	e := svc.getCodexTelemetry()
	found := false
	for key, a := range e.series {
		if key.name == "codex.transport.fallback_to_http" {
			found = true
			require.Equal(t, uint64(1), a.count)
			require.Equal(t, "responses_websocket", key.fromWireAPI)
			require.Nil(t, a.buckets)
		}
		require.NotEqual(t, "codex.transport.fallback_to_http.duration_ms", key.name)
	}
	require.True(t, found)
}
