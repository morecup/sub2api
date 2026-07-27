package service

import (
	"context"
	"net/http"
	"regexp"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/xai"
	"github.com/stretchr/testify/require"
)

var traceParentPattern = regexp.MustCompile(`^00-[0-9a-f]{32}-[0-9a-f]{16}-01$`)

func grokCLISessionTestAccount(id int64, sub string) *Account {
	return &Account{
		ID:       id,
		Platform: PlatformGrok,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"sub":      sub,
			"base_url": xai.DefaultCLIBaseURL,
		},
	}
}

// TestApplyGrokCLISessionHeadersMatchesCapturedShape pins the header set that
// grok 0.2.112 was observed sending on POST /v1/responses.
func TestApplyGrokCLISessionHeadersMatchesCapturedShape(t *testing.T) {
	t.Parallel()

	account := grokCLISessionTestAccount(7101, "407fe4e0-8714-45f9-a2cb-c3fec68de80a")
	headers := make(http.Header)
	body := []byte(`{"model":"grok-4.5","stream":true,"input":[{"type":"message","role":"user","content":"hi"}]}`)

	applyGrokCLISessionHeaders(headers, account, body, "019fa403-f32f-79e3-847e-1f2f97589a86")

	require.Equal(t, "407fe4e0-8714-45f9-a2cb-c3fec68de80a", headers.Get(grokUserIDHeader))
	require.Equal(t, "019fa403-f32f-79e3-847e-1f2f97589a86", headers.Get(grokSessionIDHeader))
	require.Equal(t, "grok-4.5", headers.Get(grokModelOverrideHeader))
	require.Equal(t, "1", headers.Get(grokTurnIndexHeader))
	require.Equal(t, "1", headers.Get(grokCompactionsLeftHeader))
	require.Equal(t, "400000", headers.Get(grokCompactionAtHeader))
	require.Regexp(t, traceParentPattern, headers.Get(traceParentHeader))
	require.NotEmpty(t, headers.Get(grokAgentIDHeader))
	require.NotEmpty(t, headers.Get(grokRequestIDHeader))
}

func TestApplyGrokCLISessionHeadersRequestIDIsFreshPerRequest(t *testing.T) {
	t.Parallel()

	account := grokCLISessionTestAccount(7102, "user-uuid")
	body := []byte(`{"model":"grok-4.5"}`)

	first := make(http.Header)
	second := make(http.Header)
	applyGrokCLISessionHeaders(first, account, body, "conv-1")
	applyGrokCLISessionHeaders(second, account, body, "conv-1")

	require.NotEqual(t, first.Get(grokRequestIDHeader), second.Get(grokRequestIDHeader))
	require.NotEqual(t, first.Get(traceParentHeader), second.Get(traceParentHeader))
	// The captured agent id was identical across separate CLI runs, so it must
	// stay stable while the per-request ids rotate.
	require.Equal(t, first.Get(grokAgentIDHeader), second.Get(grokAgentIDHeader))
}

func TestApplyGrokCLISessionHeadersAgentIDIsPerAccount(t *testing.T) {
	t.Parallel()

	body := []byte(`{"model":"grok-4.5"}`)
	first := make(http.Header)
	second := make(http.Header)
	applyGrokCLISessionHeaders(first, grokCLISessionTestAccount(7103, "a"), body, "conv-1")
	applyGrokCLISessionHeaders(second, grokCLISessionTestAccount(7104, "b"), body, "conv-1")

	// One shared constant would tell the upstream that every account of a
	// deployment runs on a single install.
	require.NotEqual(t, first.Get(grokAgentIDHeader), second.Get(grokAgentIDHeader))
}

func TestApplyGrokCLISessionHeadersSkipsSessionHeadersWithoutConversation(t *testing.T) {
	t.Parallel()

	headers := make(http.Header)
	applyGrokCLISessionHeaders(headers, grokCLISessionTestAccount(7105, "user-uuid"), []byte(`{"model":"grok-4.5"}`), "  ")

	// Probes and /responses/compact are not conversation turns; inventing a
	// session id and turn index there would be worse than omitting them.
	require.Empty(t, headers.Get(grokSessionIDHeader))
	require.Empty(t, headers.Get(grokTurnIndexHeader))
	require.NotEmpty(t, headers.Get(grokUserIDHeader))
	require.NotEmpty(t, headers.Get(grokRequestIDHeader))
}

func TestApplyGrokCLISessionHeadersOmitsCompactionPairForUnmeasuredModel(t *testing.T) {
	t.Parallel()

	headers := make(http.Header)
	applyGrokCLISessionHeaders(headers, grokCLISessionTestAccount(7106, "user-uuid"), []byte(`{"model":"grok-build-0.1"}`), "conv-1")

	// Advertising "compact at 400000" for a model with a smaller context window
	// is internally inconsistent, so the pair is only sent where it was measured.
	require.Equal(t, "grok-build-0.1", headers.Get(grokModelOverrideHeader))
	require.Empty(t, headers.Get(grokCompactionAtHeader))
	require.Empty(t, headers.Get(grokCompactionsLeftHeader))
}

func TestApplyGrokCLISessionHeadersToleratesMissingInputs(t *testing.T) {
	t.Parallel()

	require.NotPanics(t, func() {
		applyGrokCLISessionHeaders(nil, grokCLISessionTestAccount(7107, "user"), nil, "conv")
		applyGrokCLISessionHeaders(make(http.Header), nil, nil, "conv")
	})

	headers := make(http.Header)
	applyGrokCLISessionHeaders(headers, &Account{ID: 0, Platform: PlatformGrok, Type: AccountTypeOAuth}, nil, "conv-1")
	require.Empty(t, headers.Get(grokUserIDHeader))
	require.Empty(t, headers.Get(grokAgentIDHeader))
	require.Empty(t, headers.Get(grokModelOverrideHeader))
	require.Equal(t, "1", headers.Get(grokTurnIndexHeader))
}

func TestGrokTurnIndexFromBody(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		body string
		want string
	}{
		{
			name: "first responses turn matches the captured value",
			body: `{"input":[{"type":"message","role":"user","content":"hi"}]}`,
			want: "1",
		},
		{
			name: "grows with accumulated user turns",
			body: `{"input":[
				{"type":"message","role":"user","content":"one"},
				{"type":"message","role":"assistant","content":"ok"},
				{"type":"function_call","name":"shell"},
				{"type":"message","role":"user","content":"two"},
				{"type":"message","role":"assistant","content":"ok"},
				{"type":"message","role":"user","content":"three"}
			]}`,
			want: "3",
		},
		{
			name: "falls back to chat completions messages",
			body: `{"messages":[{"role":"system","content":"s"},{"role":"user","content":"one"},{"role":"assistant","content":"a"},{"role":"user","content":"two"}]}`,
			want: "2",
		},
		{
			name: "never reports a zero turn",
			body: `{"model":"grok-4.5"}`,
			want: "1",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			headers := make(http.Header)
			applyGrokCLISessionHeaders(headers, grokCLISessionTestAccount(7108, "user"), []byte(tc.body), "conv-1")
			require.Equal(t, tc.want, headers.Get(grokTurnIndexHeader))
		})
	}
}

func TestNewTraceParentHeaderIsWellFormedAndUnique(t *testing.T) {
	t.Parallel()

	seen := make(map[string]struct{}, 64)
	for i := 0; i < 64; i++ {
		got := newTraceParentHeader()
		require.Regexp(t, traceParentPattern, got)
		require.NotContains(t, seen, got)
		seen[got] = struct{}{}
	}
}

func TestBuildGrokResponsesRequestCarriesCLISessionHeadersForOAuth(t *testing.T) {
	t.Parallel()

	account := grokCLISessionTestAccount(7109, "407fe4e0-8714-45f9-a2cb-c3fec68de80a")
	body := []byte(`{"model":"grok-4.5","stream":true,"input":[{"type":"message","role":"user","content":"hi"}]}`)

	req, err := buildGrokResponsesRequest(context.Background(), nil, account, body, "access-token", "conv-9", nil)
	require.NoError(t, err)

	// The CLI's sampler only streams, so a streaming relay must match it exactly.
	require.Equal(t, "text/event-stream", req.Header.Get("Accept"))
	require.Equal(t, "407fe4e0-8714-45f9-a2cb-c3fec68de80a", req.Header.Get(grokUserIDHeader))
	require.Equal(t, "conv-9", req.Header.Get(grokSessionIDHeader))
	require.Equal(t, "conv-9", req.Header.Get(grokConversationIDHeader))
	require.Equal(t, "grok-4.5", req.Header.Get(grokModelOverrideHeader))
	require.Equal(t, "1", req.Header.Get(grokTurnIndexHeader))
	require.Equal(t, "400000", req.Header.Get(grokCompactionAtHeader))
	require.Regexp(t, traceParentPattern, req.Header.Get(traceParentHeader))
	require.NotEmpty(t, req.Header.Get(grokAgentIDHeader))
	require.NotEmpty(t, req.Header.Get(grokRequestIDHeader))
}

func TestBuildGrokResponsesRequestKeepsJSONAcceptWhenNotStreaming(t *testing.T) {
	t.Parallel()

	account := grokCLISessionTestAccount(7110, "user-uuid")
	req, err := buildGrokResponsesRequest(context.Background(), nil, account, []byte(`{"model":"grok-4.5"}`), "access-token", "conv-9", nil)
	require.NoError(t, err)

	// Aligning Accept must not risk an SSE body reaching the non-streaming
	// response parser.
	require.Equal(t, "application/json, text/event-stream", req.Header.Get("Accept"))
}

func TestBuildGrokResponsesRequestSkipsCLISessionHeadersForAPIKeyAccount(t *testing.T) {
	t.Parallel()

	account := &Account{
		ID:       7111,
		Platform: PlatformGrok,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			"sub":      "user-uuid",
			"base_url": "https://grok.example.test/v1/",
		},
	}

	req, err := buildGrokResponsesRequest(context.Background(), nil, account, []byte(`{"model":"grok-4.5","stream":true}`), "api-key", "conv-9", nil)
	require.NoError(t, err)

	// API-key accounts talk to xAI as themselves; there is no CLI identity to
	// impersonate.
	require.Empty(t, req.Header.Get(grokUserIDHeader))
	require.Empty(t, req.Header.Get(grokAgentIDHeader))
	require.Empty(t, req.Header.Get(grokSessionIDHeader))
	require.Empty(t, req.Header.Get(traceParentHeader))
}
