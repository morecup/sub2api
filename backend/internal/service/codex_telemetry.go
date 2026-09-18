package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/tidwall/gjson"
)

// Capture 2026-09-17: OTLP JSON, delta sums/histograms, scope=codex.
// Only proxy-observable events are recorded. Static exporter/resource fields
// follow the same Desktop compatibility profile as the inference requests.
const codexTelemetryMaxSeries = 2048
const codexTelemetryMaxFileBytes = 10 << 20
const codexTelemetrySDKVersion = "0.31.0"
const codexTelemetryUserAgent = "OTel-OTLP-Exporter-Rust/" + codexTelemetrySDKVersion

var codexTelemetryBounds = []float64{0, 5, 10, 25, 50, 75, 100, 250, 500, 750, 1000, 1250, 1500, 1750, 2000, 2250, 2500, 3000, 3500, 4000, 4500, 5000, 6000, 7000, 7500, 8000, 9000, 10000, 12000, 15000, 20000, 30000, 60000, 120000}

// Routing information never enters the serialized payload. In particular,
// proxy credentials, account IDs and inference credentials are not attributes.
type codexTelemetryRoute struct {
	accountID int64
	proxyURL  string
	profile   *tlsfingerprint.Profile
	client    CodexClientProfile
}

func codexTelemetryRouteForAccount(account *Account, proxyURL string, profile *tlsfingerprint.Profile) codexTelemetryRoute {
	return codexTelemetryRoute{accountID: account.ID, proxyURL: proxyURL, profile: profile, client: codexClientProfileForAccount(account)}
}

type codexTelemetryKey struct {
	route                            codexTelemetryRouteKey
	name, model, kind, success       string
	status, compactType, fromWireAPI string
}

type codexTelemetryRouteKey struct {
	accountID            int64
	proxyURL, profileKey string
	clientKey            string
}

type codexTelemetryAggregate struct {
	route         codexTelemetryRoute
	start         time.Time
	count         uint64
	sum, min, max float64
	buckets       []uint64
}

type codexTelemetryExporter struct {
	config    config.CodexTelemetryConfig
	send      func(*http.Request, codexTelemetryRoute) (*http.Response, error)
	mu        sync.Mutex
	series    map[codexTelemetryKey]*codexTelemetryAggregate
	closed    bool
	stop      chan struct{}
	done      chan struct{}
	cancel    context.CancelFunc
	ctx       context.Context
	closeOnce sync.Once
	dropped   atomic.Uint64
	failed    atomic.Uint64
}

func (s *OpenAIGatewayService) getCodexTelemetry() *codexTelemetryExporter {
	if s == nil || s.cfg == nil {
		return nil
	}
	cfg := s.cfg.Gateway.CodexTelemetry.WithDefaults()
	if cfg.Mode == "off" {
		return nil
	}
	s.codexTelemetryOnce.Do(func() {
		if err := cfg.Validate(); err != nil {
			slog.Warn("Codex telemetry configuration invalid; exporter disabled")
			return
		}
		t := &codexTelemetryExporter{config: cfg, series: make(map[codexTelemetryKey]*codexTelemetryAggregate), stop: make(chan struct{}), done: make(chan struct{})}
		t.ctx, t.cancel = context.WithCancel(context.Background())
		t.send = func(req *http.Request, route codexTelemetryRoute) (*http.Response, error) {
			if s.httpUpstream == nil {
				return nil, fmt.Errorf("telemetry transport unavailable")
			}
			// Direct use avoids instrumenting the export itself. Preserve the account's
			// selected proxy; never retry telemetry using a direct connection.
			return s.httpUpstream.DoWithTLS(req, route.proxyURL, route.accountID, 0, route.profile)
		}
		s.codexTelemetry = t
		go t.run()
	})
	return s.codexTelemetry
}

func (t *codexTelemetryExporter) record(route codexTelemetryRoute, name, model, kind string, success bool, duration time.Duration) {
	if name != "codex.sse_event" && name != "codex.websocket.event" && name != "codex.websocket.request" {
		return
	}
	if name != "codex.websocket.request" && !codexTelemetryEventKind(kind) {
		return
	}
	if name == "codex.websocket.request" {
		kind = ""
	}
	key := codexTelemetryKey{name: name, model: model, kind: kind, success: strconv.FormatBool(success)}
	t.recordPair(route, key, duration)
}

func (t *codexTelemetryExporter) recordAPIRequest(route codexTelemetryRoute, model string, status int, success bool, duration time.Duration) {
	label := "unknown"
	if status >= 100 && status <= 599 {
		label = strconv.Itoa(status)
	}
	t.recordPair(route, codexTelemetryKey{name: "codex.api_request", model: model, status: label, success: strconv.FormatBool(success)}, duration)
}

func (t *codexTelemetryExporter) recordPair(route codexTelemetryRoute, key codexTelemetryKey, duration time.Duration) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.recordSampleLocked(route, key, false, 1)
	key.name += ".duration_ms"
	t.recordSampleLocked(route, key, true, math.Max(float64(duration.Milliseconds()), 0))
}

// Standalone histograms must not create an accompanying counter or invent a
// success label. Only capture-verified numeric measurements enter this path.
func (t *codexTelemetryExporter) recordDuration(route codexTelemetryRoute, name, model, status string, milliseconds float64) {
	if t == nil || math.IsNaN(milliseconds) || math.IsInf(milliseconds, 0) || milliseconds < 0 || milliseconds > float64((1<<63-1)/int64(time.Millisecond)) {
		return
	}
	switch name {
	case "codex.responses_api_overhead.duration_ms", "codex.responses_api_inference_time.duration_ms", "codex.responses_api_engine_iapi_tbt.duration_ms", "codex.remote_models.fetch_update.duration_ms":
		status = ""
	default:
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.recordSampleLocked(route, codexTelemetryKey{name: name, model: model, status: status}, true, milliseconds)
}

func (t *codexTelemetryExporter) recordCompaction(route codexTelemetryRoute, model string) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.recordSampleLocked(route, codexTelemetryKey{name: "codex.task.compact", model: model, compactType: "remote_v2"}, false, 1)
}

func (t *codexTelemetryExporter) recordHTTPFallback(route codexTelemetryRoute, model string) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.recordSampleLocked(route, codexTelemetryKey{name: "codex.transport.fallback_to_http", model: model, fromWireAPI: "responses_websocket"}, false, 1)
}

// Caller holds mu; names and labels have already passed the typed entry points.
func (t *codexTelemetryExporter) recordSampleLocked(route codexTelemetryRoute, key codexTelemetryKey, histogram bool, value float64) {
	if t.closed {
		return
	}
	if !isCodexTelemetryModel(key.model) {
		key.model = "other"
	}
	if route.client.SchemaVersion == 0 {
		route.client = defaultCodexClientProfile()
	}
	key.route = codexTelemetryRouteKey{accountID: route.accountID, proxyURL: route.proxyURL, clientKey: route.client.cacheKey()}
	if route.profile != nil {
		key.route.profileKey = route.profile.CacheKey()
	}
	a := t.series[key]
	if a == nil {
		if len(t.series) >= codexTelemetryMaxSeries {
			t.dropped.Add(1)
			return
		}
		a = &codexTelemetryAggregate{start: time.Now(), route: route}
		if histogram {
			a.buckets = make([]uint64, len(codexTelemetryBounds)+1)
		}
		t.series[key] = a
	}
	if a.count == 0 || value < a.min {
		a.min = value
	}
	if a.count == 0 || value > a.max {
		a.max = value
	}
	a.count++
	a.sum += value
	if a.buckets != nil {
		a.buckets[sort.SearchFloat64s(codexTelemetryBounds, value)]++
	}
}

func isCodexTelemetryModel(model string) bool {
	switch model {
	case "gpt-5.6-sol", "gpt-6-astra", "gpt-5.6-terra", "gpt-5.6-luna", "gpt-5.5", "gpt-5.4", "gpt-5.4-mini", "gpt-5.3-codex", "gpt-5.3-codex-spark", "gpt-5.2", "gpt-5.2-codex":
		return true
	}
	return false
}

func codexTelemetryAttr(key, value string) map[string]any {
	return map[string]any{"key": key, "value": map[string]string{"stringValue": value}}
}

func codexTelemetryPayload(series map[codexTelemetryKey]*codexTelemetryAggregate, now time.Time) []byte {
	// flush groups by account, route AND immutable client snapshot. A profile
	// edit cannot relabel measurements collected using the previous environment.
	client := defaultCodexClientProfile()
	for _, aggregate := range series {
		if aggregate.route.client.SchemaVersion != 0 {
			client = aggregate.route.client
		}
		break
	}
	keys := make([]codexTelemetryKey, 0, len(series))
	for key := range series {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		a, b := keys[i], keys[j]
		return a.name+"\x00"+a.model+"\x00"+a.kind+"\x00"+a.success+"\x00"+a.status+"\x00"+a.compactType+"\x00"+a.fromWireAPI < b.name+"\x00"+b.model+"\x00"+b.kind+"\x00"+b.success+"\x00"+b.status+"\x00"+b.compactType+"\x00"+b.fromWireAPI
	})
	metrics := make([]any, 0, len(keys))
	byName := make(map[string]map[string]any)
	for _, k := range keys {
		a := series[k]
		attrs := []any{codexTelemetryAttr("app.version", client.CodexVersion), codexTelemetryAttr("auth_mode", "Chatgpt"), codexTelemetryAttr("model", k.model), codexTelemetryAttr("originator", "Codex_Desktop"), codexTelemetryAttr("service_name", "codex_desktop"), codexTelemetryAttr("session_source", "vscode")}
		if k.success != "" {
			attrs = append(attrs, codexTelemetryAttr("success", k.success))
		}
		if k.kind != "" {
			attrs = append(attrs, codexTelemetryAttr("kind", k.kind))
		}
		if k.status != "" {
			attrs = append(attrs, codexTelemetryAttr("status", k.status))
		}
		if k.compactType != "" {
			attrs = append(attrs, codexTelemetryAttr("type", k.compactType))
		}
		if k.fromWireAPI != "" {
			attrs = append(attrs, codexTelemetryAttr("from_wire_api", k.fromWireAPI))
		}
		dp := map[string]any{"attributes": attrs, "startTimeUnixNano": strconv.FormatInt(a.start.UnixNano(), 10), "timeUnixNano": strconv.FormatInt(now.UnixNano(), 10), "exemplars": []any{}, "flags": 0}
		m := map[string]any{"name": k.name, "description": "", "unit": ""}
		if a.buckets == nil {
			dp["asInt"] = a.count
			m["sum"] = map[string]any{"dataPoints": []any{dp}, "aggregationTemporality": 1, "isMonotonic": true}
		} else {
			dp["count"], dp["sum"], dp["min"], dp["max"] = a.count, a.sum, a.min, a.max
			dp["bucketCounts"], dp["explicitBounds"] = a.buckets, codexTelemetryBounds
			m["description"], m["unit"] = "Duration in milliseconds.", "ms"
			m["histogram"] = map[string]any{"dataPoints": []any{dp}, "aggregationTemporality": 1}
		}
		if previous := byName[k.name]; previous != nil {
			metricType := "sum"
			if a.buckets != nil {
				metricType = "histogram"
			}
			data := previous[metricType].(map[string]any)
			data["dataPoints"] = append(data["dataPoints"].([]any), dp)
		} else {
			byName[k.name] = m
			metrics = append(metrics, m)
		}
	}
	resource := map[string]any{"attributes": []any{
		codexTelemetryAttr("service.name", "codex-app-server"),
		codexTelemetryAttr("service.version", client.CodexVersion),
		codexTelemetryAttr("os", client.OS),
		codexTelemetryAttr("os_version", client.OSVersion),
		codexTelemetryAttr("env", "dev"),
		codexTelemetryAttr("telemetry.sdk.name", "opentelemetry"),
		codexTelemetryAttr("telemetry.sdk.version", client.OTelSDKVersion),
		codexTelemetryAttr("telemetry.sdk.language", "rust"),
	}, "droppedAttributesCount": 0, "entityRefs": []any{}}
	body, _ := json.Marshal(map[string]any{"resourceMetrics": []any{map[string]any{"resource": resource, "scopeMetrics": []any{map[string]any{"scope": map[string]any{"name": "codex", "version": "", "attributes": []any{}, "droppedAttributesCount": 0}, "metrics": metrics}}}}})
	return body
}

func (t *codexTelemetryExporter) run() {
	defer close(t.done)
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			t.flush(t.ctx)
		case <-t.stop:
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			t.flush(ctx)
			cancel()
			return
		}
	}
}

func (t *codexTelemetryExporter) Close() {
	if t == nil {
		return
	}
	t.closeOnce.Do(func() {
		t.mu.Lock()
		t.closed = true
		t.mu.Unlock()
		t.cancel()
		close(t.stop)
		<-t.done
	})
}

func (t *codexTelemetryExporter) flush(ctx context.Context) {
	t.mu.Lock()
	series := t.series
	t.series = make(map[codexTelemetryKey]*codexTelemetryAggregate)
	t.mu.Unlock()
	groups := make(map[codexTelemetryRouteKey]map[codexTelemetryKey]*codexTelemetryAggregate)
	routes := make(map[codexTelemetryRouteKey]codexTelemetryRoute)
	for k, a := range series {
		if groups[k.route] == nil {
			groups[k.route] = make(map[codexTelemetryKey]*codexTelemetryAggregate)
		}
		groups[k.route][k] = a
		routes[k.route] = a.route
	}
	for route, group := range groups {
		if ctx.Err() != nil {
			return
		}
		body := codexTelemetryPayload(group, time.Now())
		var err error
		if t.config.Mode == "local" {
			err = t.writeLocal(body)
		} else {
			err = t.export(ctx, routes[route], body)
		}
		if err != nil {
			t.failed.Add(1)
		}
	}
	if failures := t.failed.Swap(0); failures > 0 {
		slog.Warn("Codex telemetry export failed; inference unaffected", "batches", failures)
	}
	if dropped := t.dropped.Swap(0); dropped > 0 {
		slog.Warn("Codex telemetry series limit reached", "dropped", dropped)
	}
}

func (t *codexTelemetryExporter) export(ctx context.Context, route codexTelemetryRoute, body []byte) (resultErr error) {
	started := time.Now()
	status, rejected := 0, int64(0)
	result := "request_error"
	defer func() {
		level := slog.LevelInfo
		if resultErr != nil {
			level = slog.LevelWarn
		}
		// Local diagnostics only: no credentials, proxy URL, payload or response
		// text. A 2xx acknowledgement is not proof of later collector processing.
		slog.Log(ctx, level, "Codex telemetry batch delivery",
			"account_id", route.accountID, "proxy_enabled", route.proxyURL != "",
			"status_code", status, "result", result, "rejected_data_points", rejected,
			"payload_bytes", len(body), "duration_ms", time.Since(started).Milliseconds())
	}()
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cfg := t.config.WithDefaults()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.Endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("content-type", "application/json")
	req.Header.Set("accept", "*/*")
	client := route.client
	if client.SchemaVersion == 0 {
		client = defaultCodexClientProfile()
	}
	req.Header.Set("user-agent", "OTel-OTLP-Exporter-Rust/"+client.OTelSDKVersion)
	if cfg.StatsigAPIKey != "" {
		req.Header.Set("statsig-api-key", cfg.StatsigAPIKey)
	}
	resp, err := t.send(req, route)
	if resp != nil && resp.Body != nil {
		defer resp.Body.Close()
	}
	if err != nil {
		result = "transport_error"
		return err
	}
	if resp == nil {
		result = "empty_response"
		return fmt.Errorf("empty telemetry response")
	}
	status = resp.StatusCode
	var responseBody []byte
	if resp.Body != nil {
		responseBody, err = io.ReadAll(io.LimitReader(resp.Body, 32<<10))
		if err != nil {
			result = "response_read_error"
			return err
		}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		result = "http_error"
		return fmt.Errorf("telemetry status %d", resp.StatusCode)
	}
	if gjson.ValidBytes(responseBody) {
		rejected = gjson.GetBytes(responseBody, "partialSuccess.rejectedDataPoints").Int()
		if rejected == 0 {
			rejected = gjson.GetBytes(responseBody, "partial_success.rejected_data_points").Int()
		}
		if rejected > 0 {
			result = "partial_rejection"
			return fmt.Errorf("telemetry collector rejected %d data points", rejected)
		}
	}
	result = "acknowledged"
	return nil
}

func (t *codexTelemetryExporter) writeLocal(body []byte) error {
	path := t.config.LocalPath
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	if info, err := os.Stat(path); err == nil && info.Size()+int64(len(body))+1 > codexTelemetryMaxFileBytes {
		if err := os.Remove(path + ".1"); err != nil && !os.IsNotExist(err) {
			return err
		}
		if err := os.Rename(path, path+".1"); err != nil {
			return err
		}
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	_, err = f.Write(append(body, '\n'))
	closeErr := f.Close()
	if err != nil {
		return err
	}
	return closeErr
}
