package service

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// codexInstallationIDForAccount：按账号确定性派生 installation_id，
// 不同账号互不相同的稳定 UUID，缺失种子时回退实抓固定值。
func TestCodexInstallationIDForAccount(t *testing.T) {
	// 不同账号 ID 派生出不同 installation_id。
	id1 := codexInstallationIDForAccount(1, "")
	id2 := codexInstallationIDForAccount(2, "")
	require.NotEqual(t, id1, id2)
	require.NotEqual(t, codexInstallationID, id1)

	// 同一账号 ID 多次派生结果一致且是合法 UUID。
	require.Equal(t, id1, codexInstallationIDForAccount(1, ""))
	parsed, err := uuid.Parse(id1)
	require.NoError(t, err)
	require.Equal(t, uuid.Version(5), parsed.Version())

	// accountID=0 且 chatgptAccountID 非空时用 chatgpt 种子。
	idChatgpt := codexInstallationIDForAccount(0, "chatgpt-acc")
	require.NotEqual(t, codexInstallationID, idChatgpt)
	require.NotEqual(t, id1, idChatgpt)
	require.Equal(t, idChatgpt, codexInstallationIDForAccount(0, "  chatgpt-acc  "))
	_, err = uuid.Parse(idChatgpt)
	require.NoError(t, err)

	// 账号 ID 优先于 chatgpt-account-id。
	require.Equal(t, id1, codexInstallationIDForAccount(1, "chatgpt-acc"))

	// 两者皆空时回退 codexInstallationID。
	require.Equal(t, codexInstallationID, codexInstallationIDForAccount(0, ""))
	require.Equal(t, codexInstallationID, codexInstallationIDForAccount(0, "   "))
}

// 手动压缩（request_kind=compaction）入站 metadata：出站 x-codex-turn-metadata 保留
// compaction 画像（0.145.0-alpha.27 实抓），无 workspaces，body 经 sync 后与头部一致。
func TestApplyCodexOAuthMimicHeadersCompactionMetadata(t *testing.T) {
	inboundCompaction := `{"trigger":"manual","reason":"user_requested","implementation":"responses_compaction_v2","phase":"standalone_turn","strategy":"memento"}`
	inboundMeta := `{"installation_id":"inbound-should-be-overwritten","session_id":"inbound-should-be-overwritten","request_kind":"compaction","compaction":` + inboundCompaction + `,"workspaces":{"/foo/bar":{}}}`
	body := []byte(`{"model":"gpt-5.6","input":"compact me","prompt_cache_key":"client-original"}`)
	req := httptest.NewRequest(http.MethodPost, "https://chatgpt.com/backend-api/codex/responses", strings.NewReader(string(body)))
	req.Header.Set("x-codex-turn-metadata", inboundMeta)
	applyCodexOAuthMimicHeaders(req, 7, 0, "sess-seed-compaction", codexDesktopOriginator, false)

	meta := req.Header.Get("x-codex-turn-metadata")
	sessionID := req.Header.Get("session-id")
	require.Len(t, sessionID, 36)
	require.Equal(t, "compaction", gjson.Get(meta, "request_kind").String())
	require.Equal(t, sessionID, gjson.Get(meta, "session_id").String())
	require.Equal(t, sessionID, gjson.Get(meta, "thread_id").String())
	require.NotEmpty(t, gjson.Get(meta, "turn_id").String())
	require.Equal(t, sessionID+":0", gjson.Get(meta, "window_id").String())
	require.Equal(t, "user", gjson.Get(meta, "thread_source").String())
	require.Equal(t, "none", gjson.Get(meta, "sandbox").String())
	require.Greater(t, gjson.Get(meta, "turn_started_at_unix_ms").Int(), int64(0))
	// accountID=7：installation_id 按账号派生（而非回退固定值）。
	require.Equal(t, codexInstallationIDForAccount(7, ""), gjson.Get(meta, "installation_id").String())
	// compaction 对象原样保留入站值；compaction 请求不含 workspaces。
	require.Equal(t, "manual", gjson.Get(meta, "compaction.trigger").String())
	require.Equal(t, "user_requested", gjson.Get(meta, "compaction.reason").String())
	require.Equal(t, "responses_compaction_v2", gjson.Get(meta, "compaction.implementation").String())
	require.Equal(t, "standalone_turn", gjson.Get(meta, "compaction.phase").String())
	require.Equal(t, "memento", gjson.Get(meta, "compaction.strategy").String())
	require.False(t, gjson.Get(meta, "workspaces").Exists())

	// body 同步：client_metadata.x-codex-turn-metadata 与头部一致，prompt_cache_key 对齐 session_id。
	updated, err := syncCodexOAuthMimicRequestBody(req, body, false)
	require.NoError(t, err)
	require.Equal(t, meta, gjson.GetBytes(updated, "client_metadata.x-codex-turn-metadata").String())
	require.Equal(t, sessionID, gjson.GetBytes(updated, "prompt_cache_key").String())
}

// 入站 compaction metadata 缺省 compaction 对象时回退实抓默认画像。
func TestBuildCodexCompactionMetadataDefaultProfile(t *testing.T) {
	compaction, isCompaction := extractCodexCompactionRequest(`{"request_kind":"compaction"}`)
	require.True(t, isCompaction)
	require.Nil(t, compaction)

	meta := buildCodexCompactionMetadata("019f85a3-654e-7542-8941-95713900af32", "019f85a3-654e-7542-8941-95713900af32:0", "", nil)
	require.Equal(t, "compaction", gjson.Get(meta, "request_kind").String())
	require.Equal(t, codexInstallationID, gjson.Get(meta, "installation_id").String())
	require.Equal(t, "manual", gjson.Get(meta, "compaction.trigger").String())
	require.Equal(t, "user_requested", gjson.Get(meta, "compaction.reason").String())
	require.Equal(t, "responses_compaction_v2", gjson.Get(meta, "compaction.implementation").String())
	require.Equal(t, "standalone_turn", gjson.Get(meta, "compaction.phase").String())
	require.Equal(t, "memento", gjson.Get(meta, "compaction.strategy").String())
	require.False(t, gjson.Get(meta, "workspaces").Exists())

	// 非 compaction 的入站 metadata 不触发压缩画像。
	_, isCompaction = extractCodexCompactionRequest(`{"request_kind":"turn"}`)
	require.False(t, isCompaction)
	_, isCompaction = extractCodexCompactionRequest(`{"workspaces":{}}`)
	require.False(t, isCompaction)
	_, isCompaction = extractCodexCompactionRequest("not-json")
	require.False(t, isCompaction)
}

// codexOAIAttestationForAccount：app_session_id 按账号派生（UUIDv5，进程盐参与），
// 同账号进程内恒定、跨账号不同、无种子回退进程级全局值。
func TestCodexOAIAttestationForAccount(t *testing.T) {
	att1 := codexOAIAttestationForAccount(1, "")
	att2 := codexOAIAttestationForAccount(2, "")
	// 不同账号派生不同 attestation（app_session_id 不同）。
	require.NotEqual(t, att1, att2)
	// 同账号多次派生一致（含缓存路径）。
	require.Equal(t, att1, codexOAIAttestationForAccount(1, ""))
	// accountID 优先于 chatgpt-account-id。
	require.Equal(t, att1, codexOAIAttestationForAccount(1, "chatgpt-acc"))
	// accountID=0 时用 chatgpt 种子，且与账号派生值不同。
	attChatgpt := codexOAIAttestationForAccount(0, "chatgpt-acc")
	require.NotEqual(t, att1, attChatgpt)
	require.Equal(t, attChatgpt, codexOAIAttestationForAccount(0, "chatgpt-acc"))
	// 无种子回退进程级全局值。
	require.Equal(t, codexOAIAttestation, codexOAIAttestationForAccount(0, ""))

	// 结构：JSON 外壳 v=1, s=0, t="v1."+base64url(CBOR)；CBOR 内含派生的 app_session_id。
	type attHeader struct {
		V int    `json:"v"`
		S int    `json:"s"`
		T string `json:"t"`
	}
	var h attHeader
	require.NoError(t, json.Unmarshal([]byte(att1), &h))
	require.Equal(t, 1, h.V)
	require.Equal(t, 0, h.S)
	require.True(t, strings.HasPrefix(h.T, "v1."))
	payload, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(h.T, "v1."))
	require.NoError(t, err)
	wantSessionID := uuid.NewSHA1(uuid.NameSpaceURL, []byte("sub2api:codex-app-session:"+codexProcessSalt+":account:1")).String()
	require.Contains(t, string(payload), wantSessionID)
	require.NotContains(t, string(payload), uuid.NewSHA1(uuid.NameSpaceURL, []byte("sub2api:codex-app-session:"+codexProcessSalt+":account:2")).String())
}
