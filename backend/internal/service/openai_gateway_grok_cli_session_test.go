package service

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/xai"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

type grokTurnIndexStoreStub struct {
	turn  int
	err   error
	calls int
}

func (s *grokTurnIndexStoreStub) ObserveGrokTurnIndex(_ context.Context, _ int64, _ string, derived int, _ time.Duration) (int, error) {
	s.calls++
	if s.err != nil {
		return 0, s.err
	}
	if s.turn < derived {
		s.turn = derived
	}
	return s.turn, nil
}

// uuidV7Time reads the 48-bit millisecond timestamp of a UUIDv7. uuid.UUID.Time
// decodes the v1 Gregorian layout, which is not what v7 stores.
func uuidV7Time(id uuid.UUID) time.Time {
	millis := int64(id[0])<<40 | int64(id[1])<<32 | int64(id[2])<<24 |
		int64(id[3])<<16 | int64(id[4])<<8 | int64(id[5])
	return time.UnixMilli(millis).UTC()
}

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

// TestGrokAgentIDMatchesCapturedUUIDVersion pins the version nibble. The captured
// agent id 0fca2568-5847-50f5-aec9-161b1068bfcb is a name-based UUIDv5; emitting a
// v4 shape would be a marker the official client never produces.
func TestGrokAgentIDMatchesCapturedUUIDVersion(t *testing.T) {
	t.Parallel()

	got, err := uuid.Parse(grokAgentIDForAccount(7120))
	require.NoError(t, err)
	require.Equal(t, uuid.Version(5), got.Version())
	require.Equal(t, uuid.RFC4122, got.Variant())
	require.Equal(t, got.String(), grokAgentIDForAccount(7120))

	captured, err := uuid.Parse("0fca2568-5847-50f5-aec9-161b1068bfcb")
	require.NoError(t, err)
	require.Equal(t, captured.Version(), got.Version())
	require.Equal(t, captured.Variant(), got.Variant())
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

	for index, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			headers := make(http.Header)
			// A distinct conversation per case: the reported index is remembered
			// per conversation, so sharing one would leak between cases.
			conversationID := fmt.Sprintf("conv-turn-%d", index)
			applyGrokCLISessionHeaders(headers, grokCLISessionTestAccount(7108, "user"), []byte(tc.body), conversationID)
			require.Equal(t, tc.want, headers.Get(grokTurnIndexHeader))
		})
	}
}

// TestApplyGrokCLISessionHeadersTurnIndexNeverFallsBack covers a client that trims
// history: the CLI's counter only grows, so a session whose turn index goes
// backwards is not something the official client can produce.
func TestApplyGrokCLISessionHeadersTurnIndexNeverFallsBack(t *testing.T) {
	t.Parallel()

	account := grokCLISessionTestAccount(7130, "user")
	fullHistory := []byte(`{"model":"grok-4.5","input":[
		{"type":"message","role":"user","content":"one"},
		{"type":"message","role":"assistant","content":"a"},
		{"type":"message","role":"user","content":"two"},
		{"type":"message","role":"assistant","content":"b"},
		{"type":"message","role":"user","content":"three"}
	]}`)
	trimmed := []byte(`{"model":"grok-4.5","input":[{"type":"message","role":"user","content":"three"}]}`)

	grown := make(http.Header)
	applyGrokCLISessionHeaders(grown, account, fullHistory, "conv-trim")
	require.Equal(t, "3", grown.Get(grokTurnIndexHeader))

	retried := make(http.Header)
	applyGrokCLISessionHeaders(retried, account, fullHistory, "conv-trim")
	// A retry of the same turn is still one turn.
	require.Equal(t, "3", retried.Get(grokTurnIndexHeader))

	after := make(http.Header)
	applyGrokCLISessionHeaders(after, account, trimmed, "conv-trim")
	require.Equal(t, "3", after.Get(grokTurnIndexHeader))

	// A different conversation starts over.
	other := make(http.Header)
	applyGrokCLISessionHeaders(other, account, trimmed, "conv-trim-other")
	require.Equal(t, "1", other.Get(grokTurnIndexHeader))
}

func TestApplyGrokCLISessionHeadersUsesSharedTurnMaximum(t *testing.T) {
	t.Parallel()

	account := grokCLISessionTestAccount(7131, "user")
	store := &grokTurnIndexStoreStub{turn: 7}
	headers := make(http.Header)
	applyGrokCLISessionHeadersWithStore(
		context.Background(),
		headers,
		account,
		[]byte(`{"model":"grok-4.5","input":[{"role":"user","content":"trimmed"}]}`),
		"conv-shared-max",
		store,
	)

	require.Equal(t, "7", headers.Get(grokTurnIndexHeader))
	require.Equal(t, 1, store.calls)
}

func TestApplyGrokCLISessionHeadersFallsBackWhenSharedStoreFails(t *testing.T) {
	t.Parallel()

	account := grokCLISessionTestAccount(7132, "user")
	store := &grokTurnIndexStoreStub{err: errors.New("redis unavailable")}
	headers := make(http.Header)
	applyGrokCLISessionHeadersWithStore(
		context.Background(),
		headers,
		account,
		[]byte(`{"model":"grok-4.5","input":[{"role":"user","content":"one"},{"role":"user","content":"two"}]}`),
		"conv-shared-fallback",
		store,
	)

	require.Equal(t, "2", headers.Get(grokTurnIndexHeader))
	require.Equal(t, 1, store.calls)
}

func TestGrokTurnTrackerEvictsExpiredAndOldestEntries(t *testing.T) {
	t.Parallel()

	tracker := newGrokTurnTracker(time.Minute, 2)
	base := time.Now()

	require.Equal(t, 5, tracker.observe("a", 5, base))
	require.Equal(t, 5, tracker.observe("a", 2, base))
	require.Equal(t, 3, tracker.observe("b", 3, base))
	require.Equal(t, 7, tracker.observe("c", 7, base))
	require.LessOrEqual(t, len(tracker.entries), 2)

	// Past the TTL the conversation is forgotten, so a fresh session starts over
	// rather than inheriting a stale counter.
	require.Equal(t, 1, tracker.observe("c", 1, base.Add(2*time.Minute)))
}

// TestGrokConversationUUIDMatchesCapturedShape pins the conv-id shape: the captured
// 019fa403-f32f-79e3-847e-1f2f97589a86 is a UUIDv7, and a v4 shape would be a
// single-nibble giveaway.
func TestGrokConversationUUIDMatchesCapturedShape(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 7, 27, 15, 4, 5, 0, time.UTC)
	got, err := uuid.Parse(grokConversationUUID("seed-a", now))
	require.NoError(t, err)
	require.Equal(t, uuid.Version(7), got.Version())
	require.Equal(t, uuid.RFC4122, got.Variant())

	captured, err := uuid.Parse("019fa403-f32f-79e3-847e-1f2f97589a86")
	require.NoError(t, err)
	require.Equal(t, captured.Version(), got.Version())
	require.Equal(t, captured.Variant(), got.Variant())

	// A v7 timestamp in the future would give the derivation away on its own.
	require.False(t, uuidV7Time(got).After(now), "timestamp %s must not be after %s", uuidV7Time(got), now)
	require.True(t, uuidV7Time(got).After(now.Add(-48*time.Hour)))

	// Stable within the day so multi-turn traffic keeps hitting one prompt cache.
	require.Equal(t, got.String(), grokConversationUUID("seed-a", now.Add(2*time.Hour)))
	require.NotEqual(t, got.String(), grokConversationUUID("seed-b", now))
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

// TestGrokCompactionAtUsesOfficialFormula pins the derivation the official client
// uses: context_window * auto_compact_threshold_percent / 100.
func TestGrokCompactionAtUsesOfficialFormula(t *testing.T) {
	t.Parallel()

	got, ok := grokCompactionAt("grok-4.5")
	require.True(t, ok)
	require.Equal(t, uint64(400000), got, "captured value for the CLI's default model")

	entry := grokCompactionModels["grok-4.5"]
	require.Equal(t, entry.contextWindow*entry.thresholdPercent/100, got)

	_, ok = grokCompactionAt("GROK-4.5")
	require.True(t, ok, "model lookup must be case-insensitive")

	// A model whose window is not established sends no pair, which is the same
	// shape the official client produces when its config disables the header.
	_, ok = grokCompactionAt("grok-build-0.1")
	require.False(t, ok)
	_, ok = grokCompactionAt("")
	require.False(t, ok)
}

// TestGrokCompactionsRemainingIsDynamic covers the official rule: 1 on the
// uncompacted prefix, 0 once the session has compacted.
func TestGrokCompactionsRemainingIsDynamic(t *testing.T) {
	t.Parallel()

	fresh := []byte(`{"model":"grok-4.5","input":[{"type":"message","role":"user","content":"hi"}]}`)
	require.Equal(t, 1, grokCompactionsRemainingForBody(fresh))
	require.Equal(t, 1, grokCompactionsRemainingForBody(nil))

	// Client-sent compaction items, before Grok body normalization runs.
	clientSent := []byte(`{"model":"grok-4.5","input":[{"type":"compaction","encrypted_content":"x","summary":[{"type":"summary_text","text":"s"}]}]}`)
	require.Equal(t, 0, grokCompactionsRemainingForBody(clientSent))

	// The restored form actually sent upstream.
	restored, err := convertOpenAICompactInputsForGrok(clientSent)
	require.NoError(t, err)
	// encoding/json escapes the angle brackets, which is exactly why the marker
	// must be read through a JSON decode and not scanned for in the raw bytes.
	require.NotContains(t, string(restored), grokCompactionSummaryMarker)
	require.Contains(t, gjson.GetBytes(restored, "input.1.content.0.text").String(), grokCompactionSummaryMarker)
	require.Equal(t, 0, grokCompactionsRemainingForBody(restored))

	headers := make(http.Header)
	applyGrokCLISessionHeaders(headers, grokCLISessionTestAccount(7140, "user"), restored, "conv-compacted")
	require.Equal(t, "0", headers.Get(grokCompactionsLeftHeader))
	require.Equal(t, "400000", headers.Get(grokCompactionAtHeader))
}
