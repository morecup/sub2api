package service

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestCodexLifecycleHTTPToolContinuationAndCompaction(t *testing.T) {
	session, turn, firstContext, nextContext := newCodexUUIDV7(), newCodexUUIDV7(), newCodexUUIDV7(), newCodexUUIDV7()
	started := time.Now().Add(-30 * time.Second).UnixMilli()
	var upstreamThread string
	for window, contextID := range []string{firstContext, nextContext} {
		metadata := map[string]any{"session_id": session, "thread_id": session, "turn_id": turn, "root_turn_id": turn, "turn_started_at_unix_ms": started, "window_number": window, "window_id": session + ":ignored", "context_window_id": contextID, "request_kind": "turn", "thread_source": "user", "turn_trigger": "composer"}
		b, err := json.Marshal(metadata)
		require.NoError(t, err)
		for i := 0; i < 5; i++ {
			req, err := http.NewRequest(http.MethodPost, "https://chatgpt.com/backend-api/codex/responses", nil)
			require.NoError(t, err)
			req.Header.Set("x-codex-turn-metadata", string(b))
			applyCodexOAuthMimicHeaders(req, 817, 11, session, "", codexDesktopOriginator, false, true, "gpt-6-astra")
			md := req.Header.Get("x-codex-turn-metadata")
			require.Equal(t, turn, gjson.Get(md, "turn_id").String())
			require.Equal(t, started, gjson.Get(md, "turn_started_at_unix_ms").Int())
			require.EqualValues(t, window, gjson.Get(md, "window_number").Int())
			require.Equal(t, contextID, gjson.Get(md, "context_window_id").String())
			require.Equal(t, gjson.Get(md, "window_id").String(), req.Header.Get("x-codex-window-id"))
			require.Equal(t, req.Header.Get("session-id")+":"+string(rune('0'+window)), req.Header.Get("x-codex-window-id"))
			if upstreamThread == "" {
				upstreamThread = req.Header.Get("thread-id")
			}
			require.Equal(t, upstreamThread, req.Header.Get("thread-id"))
			body, err := syncCodexOAuthMimicRequestBody(req, []byte(`{"model":"gpt-6-astra","input":[]}`), false)
			require.NoError(t, err)
			require.Equal(t, turn, gjson.GetBytes(body, "client_metadata.turn_id").String())
		}
	}
}

func TestCodexLifecycleWSAndCompactionMetadata(t *testing.T) {
	thread, turn, contextID := newCodexUUIDV7(), newCodexUUIDV7(), newCodexUUIDV7()
	metadata := `{"turn_id":"` + turn + `","root_turn_id":"` + turn + `","window_number":2,"context_window_id":"` + contextID + `","turn_started_at_unix_ms":1700000000000}`
	headers := make(http.Header)
	applyCodexOAuthWSMimicHeaders(headers, 817, 11, thread, "", codexDesktopOriginator, metadata, "gpt-5.5")
	prewarm := headers.Get("x-codex-turn-metadata")
	require.EqualValues(t, 2, gjson.Get(prewarm, "window_number").Int())
	require.Equal(t, contextID, gjson.Get(prewarm, "context_window_id").String())
	require.Equal(t, "", gjson.Get(prewarm, "turn_id").String(), "prewarm has no active user turn")
	compact := buildCodexCompactionMetadata(headers.Get("session-id"), headers.Get("x-codex-window-id"), "", nil, metadata)
	require.Equal(t, turn, gjson.Get(compact, "turn_id").String())
	require.EqualValues(t, 1700000000000, gjson.Get(compact, "turn_started_at_unix_ms").Int())
	require.EqualValues(t, 2, gjson.Get(compact, "window_number").Int())
	require.Equal(t, contextID, gjson.Get(compact, "context_window_id").String())
}

func TestCodexLifecycleFixedNamespaceIsolation(t *testing.T) {
	fixed, task := newCodexUUIDV7(), newCodexUUIDV7()
	a := resolveCodexSessionUUID(1, 2, task, fixed)
	require.Equal(t, a, resolveCodexSessionUUID(1, 2, task, fixed))
	require.NotEqual(t, a, resolveCodexSessionUUID(1, 2, newCodexUUIDV7(), fixed))
	require.NotEqual(t, a, resolveCodexSessionUUID(1, 3, task, fixed))
	require.NotEqual(t, a, resolveCodexSessionUUID(2, 2, task, fixed))
	require.NotEqual(t, resolveCodexSessionUUID(1, 2, "", fixed), resolveCodexSessionUUID(1, 2, "", fixed))
	require.Equal(t, task, codexSessionSeedFromMetadata("", `{"thread_id":"`+task+`"}`))
}

func TestCodexLifecycleMissingContextRotatesByWindow(t *testing.T) {
	session := newCodexUUIDV7()
	_, _, first := codexMetadataWindow(session, session+":0", codexTurnMetadataProfile{})
	_, n, next := codexMetadataWindow(session, session+":1", codexTurnMetadataProfile{})
	require.Equal(t, 1, n)
	require.NotEqual(t, first, next)
	_, _, again := codexMetadataWindow(session, session+":1", codexTurnMetadataProfile{})
	require.Equal(t, next, again)
	profile := codexTurnMetadataProfileFromInbound(`{"turn_id":"invalid","context_window_id":"invalid","window_number":-1,"turn_started_at_unix_ms":-1}`)
	require.Empty(t, profile.TurnID)
	require.False(t, profile.HasWindowNumber)
	require.Empty(t, profile.ContextWindowID)
	require.Zero(t, profile.TurnStartedAtUnixMs)
}
