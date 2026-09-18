package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	coderws "github.com/coder/websocket"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func newTestCodexTelemetry() *codexTelemetryExporter {
	return &codexTelemetryExporter{series: make(map[codexTelemetryKey]*codexTelemetryAggregate)}
}

func TestCodexTelemetryOTLPDeltaAndPrivacy(t *testing.T) {
	e := newTestCodexTelemetry()
	for _, duration := range []time.Duration{5 * time.Millisecond, 6 * time.Millisecond, 121 * time.Second} {
		// The profile resolver returns fresh allocations. They must aggregate.
		e.record(codexTelemetryRoute{accountID: 817, proxyURL: "http://user:SECRET@proxy.invalid:80", profile: tlsfingerprint.CodexDesktopProfile()}, "codex.websocket.request", "gpt-6-astra", "", true, duration)
	}
	e.record(codexTelemetryRoute{accountID: 817, proxyURL: "http://user:SECRET@proxy.invalid:80", profile: tlsfingerprint.CodexDesktopProfile()}, "codex.websocket.request", "SECRET-file-name", "", false, time.Millisecond)
	require.Len(t, e.series, 4)
	var body map[string]any
	raw := codexTelemetryPayload(e.series, time.Now())
	require.NoError(t, json.Unmarshal(raw, &body))
	for _, secret := range []string{"SECRET", "proxy.invalid", "account_id", "Authorization", "Cookie", "prompt", "input"} {
		require.NotContains(t, string(raw), secret)
	}
	metrics := gjson.GetBytes(raw, "resourceMetrics.0.scopeMetrics.0.metrics").Array()
	require.Len(t, metrics, 2, "one metric with multiple data points, not duplicate metric definitions")
	for _, metric := range metrics {
		if metric.Get("sum").Exists() {
			require.Equal(t, int64(1), metric.Get("sum.aggregationTemporality").Int())
			require.True(t, metric.Get("sum.isMonotonic").Bool())
			require.Len(t, metric.Get("sum.dataPoints").Array(), 2)
		} else {
			for _, point := range metric.Get("histogram.dataPoints").Array() {
				var total uint64
				for _, n := range point.Get("bucketCounts").Array() {
					total += n.Uint()
				}
				require.Equal(t, point.Get("count").Uint(), total)
				require.Len(t, point.Get("bucketCounts").Array(), len(codexTelemetryBounds)+1)
				if total == 3 {
					require.Equal(t, float64(121011), point.Get("sum").Float())
					require.Equal(t, uint64(1), point.Get("bucketCounts.1").Uint())
					require.Equal(t, uint64(1), point.Get("bucketCounts.2").Uint())
				}
			}
		}
	}
}

func TestCodexTelemetrySSEPreservesFragmentedStream(t *testing.T) {
	stream := "event: response.created\r\ndata: {\"type\":\"response.created\"}\r\n\r\n" +
		"data: {\"type\":\"response.output_text.delta\",\"delta\":\"" + strings.Repeat("SENSITIVE", 2048) + "\"}\n\n" +
		"event: response.completed\ndata: {\"type\":\"response.completed\"}\n\n"
	for _, size := range []int{1, 17, 8192} {
		e := newTestCodexTelemetry()
		b := &codexTelemetrySSEBody{ReadCloser: io.NopCloser(strings.NewReader(stream)), exporter: e, model: "gpt-5.5"}
		var actual bytes.Buffer
		buf := make([]byte, size)
		for {
			n, err := b.Read(buf)
			actual.Write(buf[:n])
			if err != nil {
				require.ErrorIs(t, err, io.EOF)
				break
			}
		}
		require.Equal(t, stream, actual.String())
		require.LessOrEqual(t, cap(b.line), 8192)
		require.Len(t, e.series, 6)
		for key, a := range e.series {
			require.NotEqual(t, "read_error", key.kind)
			require.Equal(t, uint64(1), a.count)
		}
		require.NotContains(t, string(codexTelemetryPayload(e.series, time.Now())), "SENSITIVE")
	}
}

type codexTelemetryFailReader struct{}

func (codexTelemetryFailReader) Read([]byte) (int, error) { return 0, context.Canceled }
func (codexTelemetryFailReader) Close() error             { return nil }

func TestCodexTelemetryCancelIsNotCompletion(t *testing.T) {
	e := newTestCodexTelemetry()
	b := &codexTelemetrySSEBody{ReadCloser: codexTelemetryFailReader{}, exporter: e, model: "gpt-5.6-luna"}
	for range 2 {
		_, err := b.Read(make([]byte, 8))
		require.ErrorIs(t, err, context.Canceled)
	}
	require.Len(t, e.series, 2)
	for k, a := range e.series {
		require.Equal(t, "read_error", k.kind)
		require.Equal(t, "false", k.success)
		require.Equal(t, uint64(1), a.count)
	}
}

func TestCodexTelemetryHTTPMissingContentTypeAndIsolation(t *testing.T) {
	cfg := &config.Config{}
	cfg.Gateway.CodexTelemetry = config.CodexTelemetryConfig{Mode: "local", LocalPath: filepath.Join(t.TempDir(), "metrics.jsonl")}
	s := &OpenAIGatewayService{cfg: cfg}
	t.Cleanup(s.CloseOpenAIWSPool)
	req := httptest.NewRequest(http.MethodPost, "https://chatgpt.com/backend-api/codex/responses", nil)
	req.Header.Set("accept", "text/event-stream")
	req.Header.Set("x-codex-routing-hint", "model=gpt-6-astra")
	for _, kind := range []string{AccountTypeAPIKey, AccountTypeOAuth} {
		resp := &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("data: {\"type\":\"response.completed\"}\n\n"))}
		s.observeCodexHTTPResponse(req, resp, &Account{ID: 19, Platform: PlatformOpenAI, Type: kind}, "http://test-proxy", nil)
		_, wrapped := resp.Body.(*codexTelemetrySSEBody)
		require.Equal(t, kind == AccountTypeOAuth, wrapped)
		_, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
	}
	s.CloseOpenAIWSPool()
	raw, err := os.ReadFile(cfg.Gateway.CodexTelemetry.LocalPath)
	require.NoError(t, err)
	require.Contains(t, string(raw), "codex.sse_event")
	require.NotContains(t, string(raw), "test-proxy")
}

func TestCodexTelemetryRemoteAccountRoutingAndFailure(t *testing.T) {
	e := newTestCodexTelemetry()
	e.config = config.CodexTelemetryConfig{Mode: "remote", Endpoint: "https://ab.chatgpt.com/otlp/v1/metrics", StatsigAPIKey: "test-only-key"}
	seen := map[int64]string{}
	e.send = func(req *http.Request, route codexTelemetryRoute) (*http.Response, error) {
		require.Equal(t, "test-only-key", req.Header.Get("statsig-api-key"))
		require.Empty(t, req.Header.Get("authorization"))
		require.Empty(t, req.Header.Get("cookie"))
		_, ok := req.Context().Deadline()
		require.True(t, ok)
		body, err := io.ReadAll(req.Body)
		require.NoError(t, err)
		require.NotContains(t, string(body), route.proxyURL)
		seen[route.accountID] = route.proxyURL
		return nil, errors.New("test export failure")
	}
	for _, id := range []int64{1, 2} {
		e.record(codexTelemetryRoute{accountID: id, proxyURL: "http://proxy-" + string(rune('0'+id))}, "codex.websocket.request", "gpt-5.5", "", true, time.Millisecond)
	}
	e.flush(context.Background())
	require.Equal(t, map[int64]string{1: "http://proxy-1", 2: "http://proxy-2"}, seen)
	require.Empty(t, e.series, "failed batches are not retried into an unbounded backlog")
}

func TestCodexTelemetryLocalRotationAndBound(t *testing.T) {
	e := newTestCodexTelemetry()
	e.config = config.CodexTelemetryConfig{Mode: "local", LocalPath: filepath.Join(t.TempDir(), "metrics.jsonl")}
	require.NoError(t, os.WriteFile(e.config.LocalPath, make([]byte, codexTelemetryMaxFileBytes), 0600))
	e.send = func(*http.Request, codexTelemetryRoute) (*http.Response, error) {
		t.Fatal("local mode must never contact an upstream")
		return nil, nil
	}
	for i := 0; i < codexTelemetryMaxSeries; i++ {
		e.record(codexTelemetryRoute{accountID: int64(i)}, "codex.websocket.request", "gpt-5.5", "", true, time.Millisecond)
	}
	require.Len(t, e.series, codexTelemetryMaxSeries)
	require.Positive(t, e.dropped.Load())
	e.flush(context.Background())
	info, err := os.Stat(e.config.LocalPath + ".1")
	require.NoError(t, err)
	require.Equal(t, int64(codexTelemetryMaxFileBytes), info.Size())
	info, err = os.Stat(e.config.LocalPath)
	require.NoError(t, err)
	require.Less(t, info.Size(), int64(codexTelemetryMaxFileBytes))
}

func TestCodexTelemetryWSRealFramesAndModelSwitch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := coderws.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		for range 2 {
			_, _, err := conn.Read(r.Context())
			if err != nil {
				return
			}
			if err := conn.Write(r.Context(), coderws.MessageText, []byte(`{"type":"response.completed","response":{"output":"PRIVATE"}}`)); err != nil {
				return
			}
		}
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, _, err := coderws.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http"), nil)
	require.NoError(t, err)
	defer conn.CloseNow()
	e := newTestCodexTelemetry()
	c := &coderOpenAIWSClientConn{conn: conn, telemetry: &codexTelemetryWS{exporter: e}}
	require.NoError(t, c.WriteJSON(ctx, map[string]any{"type": "response.create", "model": "gpt-6-astra", "input": "PRIVATE"}))
	_, err = c.ReadMessage(ctx)
	require.NoError(t, err)
	require.NoError(t, c.WriteFrame(ctx, coderws.MessageText, []byte(`{"type":"response.create","model":"gpt-5.5"}`)))
	_, _, err = c.ReadFrame(ctx)
	require.NoError(t, err)
	require.Len(t, e.series, 8)
	for _, model := range []string{"gpt-6-astra", "gpt-5.5"} {
		require.Contains(t, string(codexTelemetryPayload(e.series, time.Now())), model)
	}
	require.NotContains(t, string(codexTelemetryPayload(e.series, time.Now())), "PRIVATE")
}

func TestCodexTelemetryConcurrentRecord(t *testing.T) {
	e := newTestCodexTelemetry()
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			for range 100 {
				e.record(codexTelemetryRoute{}, "codex.websocket.request", "gpt-5.5", "", true, time.Millisecond)
			}
		})
	}
	wg.Wait()
	for _, a := range e.series {
		require.Equal(t, uint64(800), a.count)
	}
}

func TestCodexTelemetryZeroConfigExporter(t *testing.T) {
	s := &OpenAIGatewayService{cfg: &config.Config{}}
	exporter := s.getCodexTelemetry()
	require.NotNil(t, exporter, "omitting the telemetry section must enable the captured defaults")
	require.Equal(t, "remote", exporter.config.Mode)
	t.Cleanup(s.CloseOpenAIWSPool)
	var sent int
	exporter.send = func(req *http.Request, route codexTelemetryRoute) (*http.Response, error) {
		sent++
		require.Equal(t, "https://ab.chatgpt.com/otlp/v1/metrics", req.URL.String())
		require.Equal(t, "OTel-OTLP-Exporter-Rust/0.31.0", req.Header.Get("user-agent"))
		require.Equal(t, config.DefaultCodexTelemetryClientKey, req.Header.Get("statsig-api-key"))
		require.Equal(t, "application/json", req.Header.Get("content-type"))
		require.Equal(t, "*/*", req.Header.Get("accept"))
		require.Empty(t, req.Header.Get("authorization"))
		require.Empty(t, req.Header.Get("cookie"))
		raw, err := io.ReadAll(req.Body)
		require.NoError(t, err)
		attrs := map[string]string{}
		for _, attr := range gjson.GetBytes(raw, "resourceMetrics.0.resource.attributes").Array() {
			attrs[attr.Get("key").String()] = attr.Get("value.stringValue").String()
		}
		require.Equal(t, map[string]string{"service.name": "codex-app-server", "service.version": codexDesktopVersion, "os": "Windows", "os_version": "10.0.26100", "env": "dev", "telemetry.sdk.name": "opentelemetry", "telemetry.sdk.version": "0.31.0", "telemetry.sdk.language": "rust"}, attrs)
		require.NotContains(t, string(raw), "session_source\",\"value\":{\"stringValue\":\"gateway")
		return &http.Response{StatusCode: 202, Body: http.NoBody}, nil
	}
	exporter.record(codexTelemetryRoute{accountID: 99}, "codex.websocket.request", "gpt-6-astra", "", true, time.Millisecond)
	exporter.flush(context.Background())
	require.Equal(t, 1, sent)
}

func TestCodexTelemetryExplicitOffStillDisables(t *testing.T) {
	cfg := &config.Config{}
	cfg.Gateway.CodexTelemetry.Mode = "off"
	s := &OpenAIGatewayService{cfg: cfg}
	require.Nil(t, s.getCodexTelemetry())
	s.CloseOpenAIWSPool()
}

func TestCodexTelemetryCollectorAcknowledgementAndPrivateDiagnostics(t *testing.T) {
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	for _, tc := range []struct {
		name      string
		status    int
		body      string
		result    string
		rejected  int64
		wantError bool
	}{
		{"accepted", 202, "", "acknowledged", 0, false},
		{"camel partial", 200, `{"partialSuccess":{"rejectedDataPoints":"3","errorMessage":"SECRET collector detail"}}`, "partial_rejection", 3, true},
		{"snake partial", 200, `{"partial_success":{"rejected_data_points":2}}`, "partial_rejection", 2, true},
		{"http failure", 403, "SECRET collector detail", "http_error", 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			logs.Reset()
			e := newTestCodexTelemetry()
			e.send = func(*http.Request, codexTelemetryRoute) (*http.Response, error) {
				return &http.Response{StatusCode: tc.status, Body: io.NopCloser(strings.NewReader(tc.body))}, nil
			}
			err := e.export(context.Background(), codexTelemetryRoute{accountID: 817, proxyURL: "http://user:SECRET@proxy.invalid"}, []byte(`{"fixture":"SECRET payload"}`))
			require.Equal(t, tc.wantError, err != nil)
			var entry map[string]any
			require.NoError(t, json.Unmarshal(logs.Bytes(), &entry))
			require.Equal(t, tc.result, entry["result"])
			require.EqualValues(t, tc.status, entry["status_code"])
			require.EqualValues(t, tc.rejected, entry["rejected_data_points"])
			require.EqualValues(t, 817, entry["account_id"])
			require.Equal(t, true, entry["proxy_enabled"])
			require.NotContains(t, logs.String(), "SECRET")
			require.NotContains(t, logs.String(), "proxy.invalid")
		})
	}
}
