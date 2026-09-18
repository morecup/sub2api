package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	coderws "github.com/coder/websocket"
	"github.com/stretchr/testify/require"
)

func TestCodexTelemetryPoolObservesConsumedFramesWithoutIdleCloseErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := coderws.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		if _, _, err = conn.Read(r.Context()); err != nil {
			return
		}
		if err = conn.Write(r.Context(), coderws.MessageText, []byte(`{"type":"response.completed","response":{"id":"resp_audit"}}`)); err != nil {
			return
		}
		_, _, _ = conn.Read(r.Context())
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	socket, _, err := coderws.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http"), nil)
	require.NoError(t, err)
	exporter := newTestCodexTelemetry()
	client := &coderOpenAIWSClientConn{conn: socket, telemetry: &codexTelemetryWS{exporter: exporter}}
	pooled := newOpenAIWSConn("audit", 7315, client, nil)
	t.Cleanup(pooled.close)
	require.NoError(t, client.WriteJSON(ctx, map[string]any{"type": "response.create", "model": "gpt-5.6-sol"}))
	require.Eventually(t, func() bool { return len(pooled.readerLoopResults) > 0 }, time.Second, time.Millisecond)
	assertCodexPoolEventCount(t, exporter, "response.completed", 0)
	// Leave the prefetched frame queued. Its foreground read duration must not
	// include this delay or the idle lifetime of the background reader.
	time.Sleep(50 * time.Millisecond)
	started := time.Now()
	_, err = pooled.readMessageWithContextTimeout(ctx, time.Second)
	foregroundDuration := time.Since(started)
	require.NoError(t, err)
	assertCodexPoolEventCount(t, exporter, "response.completed", 1)
	var measured float64
	foundDuration := false
	exporter.mu.Lock()
	for key, aggregate := range exporter.series {
		if key.name == "codex.websocket.event.duration_ms" && key.kind == "response.completed" {
			measured, foundDuration = aggregate.sum, true
		}
	}
	exporter.mu.Unlock()
	require.True(t, foundDuration)
	require.LessOrEqual(t, measured, float64(foregroundDuration)/float64(time.Millisecond)+1)
	pooled.close()
	require.Eventually(t, func() bool {
		pooled.readerLoopErrMu.Lock()
		defer pooled.readerLoopErrMu.Unlock()
		return pooled.readerLoopErr != nil
	}, time.Second, time.Millisecond)
	assertCodexPoolEventCount(t, exporter, "read_error", 0)
}

func TestCodexTelemetryPoolRecordsForegroundReadTimeoutOnce(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := coderws.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		_, _, _ = conn.Read(r.Context())
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	socket, _, err := coderws.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http"), nil)
	require.NoError(t, err)
	exporter := newTestCodexTelemetry()
	observer := &codexTelemetryWS{exporter: exporter}
	observer.model.Store("gpt-5.6-sol")
	pooled := newOpenAIWSConn("audit-timeout", 7315, &coderOpenAIWSClientConn{conn: socket, telemetry: observer}, nil)
	t.Cleanup(pooled.close)
	_, err = pooled.readMessageWithContextTimeout(ctx, 20*time.Millisecond)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Eventually(t, func() bool {
		pooled.readerLoopErrMu.Lock()
		defer pooled.readerLoopErrMu.Unlock()
		return pooled.readerLoopErr != nil
	}, time.Second, time.Millisecond)
	assertCodexPoolEventCount(t, exporter, "read_error", 1)
}

func assertCodexPoolEventCount(t *testing.T, exporter *codexTelemetryExporter, kind string, want uint64) {
	t.Helper()
	exporter.mu.Lock()
	defer exporter.mu.Unlock()
	var count uint64
	for key, aggregate := range exporter.series {
		if key.name == "codex.websocket.event" && key.kind == kind {
			count += aggregate.count
		}
	}
	require.Equal(t, want, count, "event %s", kind)
}
