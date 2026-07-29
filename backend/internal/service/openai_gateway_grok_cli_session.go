package service

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/tidwall/gjson"
)

// Per-turn headers captured from the official Grok Build CLI 0.2.112 on
// POST /v1/responses against cli-chat-proxy.grok.com:
//
//	x-compactions-remaining = 1
//	x-compaction-at         = 400000
//	x-grok-user-id          = <OAuth user uuid>
//	traceparent             = 00-<32 hex>-<16 hex>-01
//	x-grok-conv-id          = <uuid>
//	x-grok-req-id           = <uuid, fresh per request>
//	x-grok-model-override   = grok-4.5
//	x-grok-session-id       = <same uuid as x-grok-conv-id>
//	x-grok-agent-id         = <uuid, stable across sessions and runs>
//	x-grok-turn-idx         = 1
//
// Sub2API previously sent only x-grok-conv-id, so a relayed request carried 6
// headers where the official client carries 18. That shape is easier to match on
// than anything at the TLS layer.
const (
	grokUserIDHeader          = "X-Grok-User-Id"
	grokSessionIDHeader       = "X-Grok-Session-Id"
	grokAgentIDHeader         = "X-Grok-Agent-Id"
	grokRequestIDHeader       = "X-Grok-Req-Id"
	grokTurnIndexHeader       = "X-Grok-Turn-Idx"
	grokModelOverrideHeader   = "X-Grok-Model-Override"
	grokCompactionsLeftHeader = "X-Compactions-Remaining"
	grokCompactionAtHeader    = "X-Compaction-At"
	traceParentHeader         = "traceparent"
)

// grokCompactionModel is the per-model input to the CLI's x-compaction-at
// calculation.
type grokCompactionModel struct {
	contextWindow    uint64
	thresholdPercent uint64
}

// grokCompactionModels holds the models whose compaction budget is established.
//
// The official client computes the header as
// context_window * auto_compact_threshold_percent / 100
// (xai-grok-sampling-types CompactionAtTokens::resolve), with both inputs coming
// from per-model remote config. grok-4.5's captured value is 400000, which pins
// its window at 500000 and its threshold at 80 rather than the 85 default
// (xai-grok-compaction DEFAULT_AUTO_COMPACT_THRESHOLD_PERCENT).
//
// Unlisted models send no compaction pair. That is a shape the official client
// produces too: the same config can disable the header per model, whereas a value
// derived from a guessed window would contradict the catalog xAI itself serves.
var grokCompactionModels = map[string]grokCompactionModel{
	"grok-4.5": {contextWindow: 500_000, thresholdPercent: 80},
}

// grokCompactionAt resolves the x-compaction-at token count for a model.
func grokCompactionAt(model string) (uint64, bool) {
	entry, ok := grokCompactionModels[strings.ToLower(strings.TrimSpace(model))]
	if !ok || entry.contextWindow == 0 || entry.thresholdPercent == 0 {
		return 0, false
	}
	return entry.contextWindow * entry.thresholdPercent / 100, true
}

// grokCompactionSummaryMarker wraps a restored compaction summary on the way to
// xAI (see convertOpenAICompactInputsForGrok).
const grokCompactionSummaryMarker = "<conversation_summary>"

// grokCompactionsRemainingForBody resolves x-compactions-remaining.
//
// The official value is dynamic: 1 while the session still carries its
// uncompacted prefix and 0 once it has compacted
// (CompactionsRemaining::resolve(has_compaction_summary)). A request whose input
// carries a compaction summary is the same "already compacted" state.
func grokCompactionsRemainingForBody(body []byte) int {
	if requestCarriesGrokCompactionSummary(body) {
		return 0
	}
	return 1
}

func requestCarriesGrokCompactionSummary(body []byte) bool {
	if len(body) == 0 {
		return false
	}
	// The marker has to be read through a JSON decode rather than scanned for in
	// the raw bytes: encoding/json escapes the angle brackets to \u003c and
	// \u003e, so a byte-level search would never match the body actually sent.
	for _, field := range []string{"input", "messages"} {
		for _, item := range gjson.GetBytes(body, field).Array() {
			// The client-sent form, before Grok body normalization runs.
			if isOpenAICompactionType(item.Get("type").String()) {
				return true
			}
			if grokContentCarriesCompactionSummary(item.Get("content")) {
				return true
			}
		}
	}
	return false
}

func grokContentCarriesCompactionSummary(content gjson.Result) bool {
	if content.IsArray() {
		for _, part := range content.Array() {
			if strings.Contains(part.Get("text").String(), grokCompactionSummaryMarker) {
				return true
			}
		}
		return false
	}
	return strings.Contains(content.String(), grokCompactionSummaryMarker)
}

// grokAgentIDNamespace anchors the per-account agent id derivation.
//
// The captured agent id (0fca2568-5847-50f5-aec9-161b1068bfcb) carries version
// nibble 5, i.e. the CLI derives it as a name-based UUIDv5 from something stable
// about the install. Deriving ours the same way keeps the version nibble faithful
// instead of emitting a v4 shape the official client never produces. The
// namespace value itself is arbitrary and only has to stay stable.
var grokAgentIDNamespace = uuid.MustParse("1b4b2f24-6a17-4c3d-9a2e-5f0c8d7e6b91")

// grokAgentIDForAccount derives the stable per-install identifier the CLI sends.
func grokAgentIDForAccount(accountID int64) string {
	return uuid.NewSHA1(grokAgentIDNamespace, []byte(fmt.Sprintf("grok-agent-id:v1:%d", accountID))).String()
}

// applyGrokCLISessionHeaders reproduces the per-turn session headers the
// official CLI attaches to an inference request.
//
// conversationID is the already-derived Grok conversation identity; when it is
// empty the request is not a conversation turn (probes, /responses/compact) and
// the session headers are skipped rather than invented.

// GrokTurnIndexStore is optionally implemented by the shared gateway cache.
// Keeping it separate from GatewayCache avoids forcing every test cache and
// cache decorator to grow a Grok-specific method.
type GrokTurnIndexStore interface {
	ObserveGrokTurnIndex(ctx context.Context, accountID int64, conversationID string, derived int, ttl time.Duration) (int, error)
}

func applyGrokCLISessionHeaders(headers http.Header, account *Account, body []byte, conversationID string) {
	applyGrokCLISessionHeadersWithStore(context.Background(), headers, account, body, conversationID, nil)
}

func applyGrokCLISessionHeadersWithStore(
	ctx context.Context,
	headers http.Header,
	account *Account,
	body []byte,
	conversationID string,
	store GrokTurnIndexStore,
) {
	applyGrokCLISessionHeadersWithState(ctx, headers, account, body, conversationID, store, nil)
}

func applyGrokCLISessionHeadersWithState(
	ctx context.Context,
	headers http.Header,
	account *Account,
	body []byte,
	conversationID string,
	store GrokTurnIndexStore,
	catalog *GrokCompactionCatalog,
) {
	if headers == nil || account == nil {
		return
	}

	// The OAuth subject is the user id the CLI reports, and it is the identity
	// the bearer token already carries, so it cannot disagree with the token.
	if userID := strings.TrimSpace(account.GetCredential("sub")); userID != "" {
		headers.Set(grokUserIDHeader, userID)
	}

	// A fresh id per request, matching the CLI's per-request identifier.
	headers.Set(grokRequestIDHeader, uuid.NewString())

	// The captured agent id was identical across separate CLI runs on one
	// machine, so it identifies the install rather than the session. Deriving it
	// per account keeps that stability without making one constant that would
	// link every account of a deployment together.
	if account.ID > 0 {
		headers.Set(grokAgentIDHeader, grokAgentIDForAccount(account.ID))
	}

	headers.Set(traceParentHeader, newTraceParentHeader())

	if model := strings.TrimSpace(gjson.GetBytes(body, "model").String()); model != "" {
		// Mirrors the body so the header can never route to a different model
		// than the one the request actually asks for.
		headers.Set(grokModelOverrideHeader, model)
		applyGrokCompactionHeaders(headers, model, body, catalog)
	}

	conversationID = strings.TrimSpace(conversationID)
	if conversationID == "" {
		return
	}
	// The capture shows session id and conversation id carrying the same value.
	headers.Set(grokSessionIDHeader, conversationID)
	turn := resolveGrokTurnIndex(ctx, store, account.ID, conversationID, grokTurnIndexFromBody(body), time.Now())
	headers.Set(grokTurnIndexHeader, strconv.Itoa(turn))
}

func applyGrokCompactionHeaders(headers http.Header, model string, body []byte, catalog *GrokCompactionCatalog) {
	if headers == nil {
		return
	}
	if catalog == nil {
		// The only fallback is the value observed on a real official request.
		// Guessed values for other models would be internally inconsistent.
		if compactionAt, ok := grokCompactionAt(model); ok {
			headers.Set(grokCompactionsLeftHeader, strconv.Itoa(grokCompactionsRemainingForBody(body)))
			headers.Set(grokCompactionAtHeader, strconv.FormatUint(compactionAt, 10))
		}
		return
	}

	config, ok := catalog.Models[strings.ToLower(strings.TrimSpace(model))]
	if !ok {
		return
	}
	// These controls are independent in the official model schema. One can be
	// enabled while the other is absent or false.
	if config.CompactionAt.Enabled {
		headers.Set(grokCompactionAtHeader, strconv.FormatUint(config.CompactionAt.Value, 10))
	}
	if config.CompactionsRemaining.Enabled {
		remaining := config.CompactionsRemaining.Value
		if config.CompactionsRemaining.Dynamic {
			remaining = uint8(grokCompactionsRemainingForBody(body))
		}
		headers.Set(grokCompactionsLeftHeader, strconv.FormatUint(uint64(remaining), 10))
	}
}

func resolveGrokTurnIndex(
	ctx context.Context,
	store GrokTurnIndexStore,
	accountID int64,
	conversationID string,
	derived int,
	now time.Time,
) int {
	key := fmt.Sprintf("%d:%s", accountID, conversationID)
	// Seed the shared max with the process-local max. This prevents a temporary
	// Redis loss or a Redis restart from making this process report a lower turn.
	local := defaultGrokTurnTracker.observe(key, derived, now)
	if store == nil {
		return local
	}
	shared, err := store.ObserveGrokTurnIndex(ctx, accountID, conversationID, local, grokTurnTrackerTTL)
	if err != nil || shared < 1 {
		return local
	}
	return defaultGrokTurnTracker.observe(key, shared, now)
}

// grokTurnIndexFromBody derives a turn index from the request body.
//
// A conversation's user turns accumulate in the body, so counting them tracks
// the CLI's per-session counter for as long as the client replays full history.
// The captured first turn carried x-grok-turn-idx: 1 with a single user message.
// Clients that trim history would make this number fall, which is what
// grokTurnTracker exists to prevent.
func grokTurnIndexFromBody(body []byte) int {
	turns := countGrokUserTurns(gjson.GetBytes(body, "input"))
	if turns == 0 {
		turns = countGrokUserTurns(gjson.GetBytes(body, "messages"))
	}
	if turns < 1 {
		return 1
	}
	return turns
}

func countGrokUserTurns(items gjson.Result) int {
	if !items.IsArray() {
		return 0
	}
	turns := 0
	for _, item := range items.Array() {
		if strings.EqualFold(strings.TrimSpace(item.Get("role").String()), "user") {
			turns++
		}
	}
	return turns
}

// newTraceParentHeader builds a W3C trace context header in the shape the CLI
// sends: version 00, a fresh trace and span id, sampled flag set.
func newTraceParentHeader() string {
	var ids [24]byte
	if _, err := rand.Read(ids[:]); err != nil {
		// Falling back to a UUID-derived id keeps the header well formed rather
		// than emitting an all-zero id, which the spec forbids.
		sum := strings.ReplaceAll(uuid.NewString(), "-", "") + strings.ReplaceAll(uuid.NewString(), "-", "")
		return fmt.Sprintf("00-%s-%s-01", sum[:32], sum[32:48])
	}
	return fmt.Sprintf("00-%s-%s-01", hex.EncodeToString(ids[:16]), hex.EncodeToString(ids[16:]))
}

// Turn index bookkeeping.
//
// The CLI's x-grok-turn-idx is a per-session counter that only ever grows. A
// body-derived count tracks it while the client replays full history, but a
// client that trims older turns (or a Responses request continued through
// previous_response_id) would send fewer user items and make the reported index
// fall - a session whose turn index goes backwards is not something the official
// client can produce.
//
// The highest index seen per conversation is therefore remembered in Redis when
// the concrete gateway cache supports GrokTurnIndexStore. The local tracker is a
// hot fallback for tests and Redis outages; successful shared observations are
// fed back into it so a later outage cannot move the process backwards.
const (
	grokTurnTrackerTTL        = 12 * time.Hour
	grokTurnTrackerMaxEntries = 8192
)

type grokTurnEntry struct {
	turn      int
	expiresAt time.Time
}

type grokTurnTracker struct {
	mu         sync.Mutex
	entries    map[string]grokTurnEntry
	ttl        time.Duration
	maxEntries int
}

func newGrokTurnTracker(ttl time.Duration, maxEntries int) *grokTurnTracker {
	return &grokTurnTracker{
		entries:    make(map[string]grokTurnEntry),
		ttl:        ttl,
		maxEntries: maxEntries,
	}
}

var defaultGrokTurnTracker = newGrokTurnTracker(grokTurnTrackerTTL, grokTurnTrackerMaxEntries)

// observe records derived for key and returns the turn index to report, which is
// never lower than one already reported for the same conversation.
//
// Retrying the same turn reports the same index because the derived value is
// unchanged, matching the CLI: a retry is still one turn.
func (t *grokTurnTracker) observe(key string, derived int, now time.Time) int {
	if t == nil || strings.TrimSpace(key) == "" {
		return derived
	}
	if derived < 1 {
		derived = 1
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	turn := derived
	if existing, ok := t.entries[key]; ok && !existing.expiresAt.Before(now) && existing.turn > turn {
		turn = existing.turn
	}
	t.entries[key] = grokTurnEntry{turn: turn, expiresAt: now.Add(t.ttl)}
	t.evictLocked(now)
	return turn
}

// evictLocked drops expired entries and, if the map is still over its cap, the
// entries closest to expiry. A uniform TTL makes that the least recently used.
func (t *grokTurnTracker) evictLocked(now time.Time) {
	if len(t.entries) <= t.maxEntries {
		return
	}
	for key, entry := range t.entries {
		if entry.expiresAt.Before(now) {
			delete(t.entries, key)
		}
	}
	if len(t.entries) <= t.maxEntries {
		return
	}
	keys := make([]string, 0, len(t.entries))
	for key := range t.entries {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		return t.entries[keys[i]].expiresAt.Before(t.entries[keys[j]].expiresAt)
	})
	for _, key := range keys[:len(t.entries)-t.maxEntries] {
		delete(t.entries, key)
	}
}

// Conversation identity shape.
//
// The captured x-grok-conv-id (019fa403-f32f-79e3-847e-1f2f97589a86) is a
// UUIDv7: a 48-bit millisecond timestamp followed by random bits, version nibble
// 7. Sub2API derives its conversation identity from a hash so multi-turn traffic
// keeps hitting the same server-side prompt cache, and the generic helper used
// for that forces version 4 - a shape the official client never emits, and one a
// gateway can match on with a single nibble comparison.
const grokConversationIDWindow = 24 * time.Hour

// grokConversationUUID formats seed as a UUIDv7 that stays stable for as long as
// the prompt cache can plausibly be reused.
//
// The timestamp cannot simply be "now": the value has to survive across the turns
// of one conversation or the prompt-cache routing it exists for breaks. It is
// therefore derived from the seed inside a window anchored to the current day,
// which keeps the id stable within a UTC day and always in the past - a v7
// timestamp in the future would be a giveaway on its own. Rotating daily costs at
// most one cold prompt cache per conversation that spans midnight.
func grokConversationUUID(seed string, now time.Time) string {
	digest := sha256.Sum256([]byte("grok-conv-id:v7:" + seed))

	windowMillis := int64(grokConversationIDWindow / time.Millisecond)
	// Anchor one window back so the offset can never land after now.
	anchor := (now.UTC().UnixMilli()/windowMillis - 1) * windowMillis
	offset := int64(binary.BigEndian.Uint64(digest[16:24]) % uint64(windowMillis))
	timestamp := anchor + offset

	var raw [16]byte
	raw[0] = byte(timestamp >> 40)
	raw[1] = byte(timestamp >> 32)
	raw[2] = byte(timestamp >> 24)
	raw[3] = byte(timestamp >> 16)
	raw[4] = byte(timestamp >> 8)
	raw[5] = byte(timestamp)
	copy(raw[6:], digest[:10])
	// Version 7 in the high nibble of byte 6, RFC 9562 variant in byte 8.
	raw[6] = (raw[6] & 0x0f) | 0x70
	raw[8] = (raw[8] & 0x3f) | 0x80

	return uuid.UUID(raw).String()
}
