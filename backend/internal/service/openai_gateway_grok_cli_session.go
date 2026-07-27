package service

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"strconv"
	"strings"

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

// grokCompactionThresholds maps an upstream model to the compaction threshold
// the CLI advertises for it.
//
// The CLI derives this from the model's context window (captured: 400000 for
// grok-4.5, whose window is 500000). Only measured models are listed on purpose:
// advertising "compact at 400k" for a 256k-window model would be internally
// inconsistent, which is worse than omitting the pair.
var grokCompactionThresholds = map[string]int{
	"grok-4.5": 400000,
}

// grokCompactionsRemaining is the CLI's fresh-session compaction budget. Unlike
// the identifiers below this is config-derived and identical across installs, so
// a constant is faithful rather than a value that links accounts together.
const grokCompactionsRemaining = 1

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
func applyGrokCLISessionHeaders(headers http.Header, account *Account, body []byte, conversationID string) {
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
		if threshold, ok := grokCompactionThresholds[strings.ToLower(model)]; ok {
			headers.Set(grokCompactionsLeftHeader, strconv.Itoa(grokCompactionsRemaining))
			headers.Set(grokCompactionAtHeader, strconv.Itoa(threshold))
		}
	}

	conversationID = strings.TrimSpace(conversationID)
	if conversationID == "" {
		return
	}
	// The capture shows session id and conversation id carrying the same value.
	headers.Set(grokSessionIDHeader, conversationID)
	headers.Set(grokTurnIndexHeader, strconv.Itoa(grokTurnIndexFromBody(body)))
}

// grokTurnIndexFromBody approximates the CLI's per-session turn counter.
//
// Sub2API holds no per-conversation turn state, but a conversation's user turns
// accumulate in the request body, so counting them grows monotonically within a
// conversation exactly as the CLI's counter does. The captured first turn carried
// x-grok-turn-idx: 1 with a single user message.
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
