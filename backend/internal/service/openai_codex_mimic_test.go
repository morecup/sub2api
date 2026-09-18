package service

import (
	"bytes"
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
// 不同账号互不相同的稳定 UUIDv4 外形，缺失种子时回退实抓固定值。
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
	require.Equal(t, uuid.Version(4), parsed.Version())

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
// compaction 画像（0.155.0-alpha.2.6 实抓），无 workspaces，body 经 sync 后与头部一致。
func TestApplyCodexOAuthMimicHeadersCompactionMetadata(t *testing.T) {
	inboundCompaction := `{"trigger":"manual","reason":"user_requested","implementation":"responses_compaction_v2","phase":"standalone_turn","strategy":"memento"}`
	inboundMeta := `{"installation_id":"inbound-should-be-overwritten","session_id":"inbound-should-be-overwritten","request_kind":"compaction","compaction":` + inboundCompaction + `,"workspaces":{"/foo/bar":{}}}`
	body := []byte(`{"model":"gpt-5.6","input":"compact me","prompt_cache_key":"client-original"}`)
	req := httptest.NewRequest(http.MethodPost, "https://chatgpt.com/backend-api/codex/responses", strings.NewReader(string(body)))
	req.Header.Set("x-codex-turn-metadata", inboundMeta)
	applyCodexOAuthMimicHeaders(req, 7, 0, "sess-seed-compaction", "", codexDesktopOriginator, false, false, "gpt-5.5")

	meta := req.Header.Get("x-codex-turn-metadata")
	sessionID := req.Header.Get("session-id")
	require.Len(t, sessionID, 36)
	require.Equal(t, "compaction", gjson.Get(meta, "request_kind").String())
	require.Equal(t, sessionID, gjson.Get(meta, "session_id").String())
	require.Equal(t, sessionID, gjson.Get(meta, "thread_id").String())
	require.NotEmpty(t, gjson.Get(meta, "turn_id").String())
	require.Equal(t, sessionID+":0", gjson.Get(meta, "window_id").String())
	require.Equal(t, "user", gjson.Get(meta, "thread_source").String())
	require.Equal(t, "/root", gjson.Get(meta, "agent_name").String())
	require.Equal(t, "none", gjson.Get(meta, "sandbox").String())
	require.Equal(t, "danger-full-access", gjson.Get(meta, "sandbox_mode").String())
	require.Zero(t, gjson.Get(meta, "window_number").Int())
	require.NotEmpty(t, gjson.Get(meta, "context_window_id").String())
	require.Equal(t, gjson.Get(meta, "turn_id").String(), gjson.Get(meta, "root_turn_id").String())
	require.False(t, gjson.Get(meta, "turn_trigger").Exists())
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
	require.Equal(t, "model=gpt-5.5", req.Header.Get("x-codex-routing-hint"))
	require.Zero(t, gjson.Get(req.Header.Get("x-oai-attestation"), "s").Int())
	require.NotEmpty(t, gjson.Get(req.Header.Get("x-oai-attestation"), "t").String())

	// body 同步：client_metadata.x-codex-turn-metadata 与头部一致，prompt_cache_key 对齐 session_id。
	updated, err := syncCodexOAuthMimicRequestBody(req, body, false)
	require.NoError(t, err)
	require.Equal(t, meta, gjson.GetBytes(updated, "client_metadata.x-codex-turn-metadata").String())
	require.Equal(t, sessionID, gjson.GetBytes(updated, "prompt_cache_key").String())
}

func TestBuildCodexTurnMetadataThreadTitleProfile(t *testing.T) {
	sessionID := "01a052d7-b018-7630-ad5b-f23494429b7a"
	workspaces := map[string]any{"D:\\repo": map[string]any{"has_changes": true}}
	inbound := `{"agent_name":"/root","thread_source":"thread_title","turn_trigger":"thread_title","sandbox":"windows_elevated","sandbox_mode":"read-only","auto_review_enabled":false,"node_repl_auto_review_required":false,"node_repl_disabled":false}`

	meta := buildCodexTurnMetadata(sessionID, sessionID+":0", workspaces, "", inbound)
	require.Equal(t, "/root", gjson.Get(meta, "agent_name").String())
	require.Equal(t, "thread_title", gjson.Get(meta, "thread_source").String())
	require.Equal(t, "thread_title", gjson.Get(meta, "turn_trigger").String())
	require.Equal(t, "windows_elevated", gjson.Get(meta, "sandbox").String())
	require.Equal(t, "read-only", gjson.Get(meta, "sandbox_mode").String())
	require.True(t, gjson.Get(meta, "workspaces").IsObject())
	require.False(t, gjson.Get(meta, "workspace_kind").Exists())
	require.Equal(t, gjson.Get(meta, "turn_id").String(), gjson.Get(meta, "root_turn_id").String())

	prewarm := buildCodexWSPrewarmMetadata(sessionID, sessionID+":0", "", inbound)
	require.Equal(t, "thread_title", gjson.Get(prewarm, "thread_source").String())
	require.Equal(t, "windows_elevated", gjson.Get(prewarm, "sandbox").String())
	require.Equal(t, "read-only", gjson.Get(prewarm, "sandbox_mode").String())
	require.False(t, gjson.Get(prewarm, "turn_trigger").Exists())
	require.False(t, gjson.Get(prewarm, "root_turn_id").Exists())
	require.False(t, gjson.Get(prewarm, "workspaces").Exists())
	require.Equal(t, gjson.Get(meta, "context_window_id").String(), gjson.Get(prewarm, "context_window_id").String())
}

func TestApplyCodexWSRequestClientMetadataLiteInPayloadOnly(t *testing.T) {
	lite := map[string]any{"model": "gpt-5.6-terra"}
	require.True(t, applyCodexWSRequestClientMetadata(lite, "gpt-5.6-terra"))
	liteMetadata := lite["client_metadata"].(map[string]any)
	require.Equal(t, "true", liteMetadata[responsesLiteWSMetadataKey])
	require.NotEmpty(t, liteMetadata["x-codex-ws-stream-request-start-ms"])

	nonLite := map[string]any{"client_metadata": map[string]any{responsesLiteWSMetadataKey: "true"}}
	require.True(t, applyCodexWSRequestClientMetadata(nonLite, "gpt-5.5"))
	nonLiteMetadata := nonLite["client_metadata"].(map[string]any)
	require.NotContains(t, nonLiteMetadata, responsesLiteWSMetadataKey)
	require.NotEmpty(t, nonLiteMetadata["x-codex-ws-stream-request-start-ms"])
}

func TestApplyCodexOAuthMimicHeadersFixedSessionNamespace(t *testing.T) {
	fixedSessionID := "019ff4d1-0567-7630-ba3d-e564a4a519ac"
	sessions := make(map[string]string)
	for _, seed := range []string{"client-session-a", "client-session-b", "client-session-a"} {
		req := httptest.NewRequest(http.MethodPost, "https://chatgpt.com/backend-api/codex/responses", strings.NewReader(`{"model":"gpt-5.6-sol"}`))
		applyCodexOAuthMimicHeaders(req, 7, 42, seed, fixedSessionID, codexDesktopOriginator, false, true)

		sessionID := req.Header.Get("session-id")
		require.NotEmpty(t, sessionID)
		require.NotEqual(t, fixedSessionID, sessionID)
		if previous := sessions[seed]; previous != "" {
			require.Equal(t, previous, sessionID)
		}
		sessions[seed] = sessionID
		require.Equal(t, sessionID, req.Header.Get("thread-id"))
		require.Equal(t, sessionID, req.Header.Get("x-client-request-id"))
		require.Equal(t, sessionID+":0", req.Header.Get("x-codex-window-id"))
		require.Equal(t, sessionID, gjson.Get(req.Header.Get("x-codex-turn-metadata"), "session_id").String())
	}
	require.NotEqual(t, sessions["client-session-a"], sessions["client-session-b"])
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

// codexOAIAttestationForAccount：app_session_id 按账号在进程内随机生成 UUIDv4，
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

	// 结构：JSON 外壳 v=1, s=0, t="v1."+base64url(CBOR)；CBOR 内含
	// 同一设备画像的 app_session_id、语言、时区和屏幕信号。
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
	profile1 := codexDeviceProfileForAccount(1, "")
	profile2 := codexDeviceProfileForAccount(2, "")
	appSessionID, err := uuid.Parse(profile1.AppSessionID)
	require.NoError(t, err)
	require.Equal(t, uuid.Version(4), appSessionID.Version())
	require.Contains(t, string(payload), profile1.AppSessionID)
	require.NotContains(t, string(payload), profile2.AppSessionID)
	require.Contains(t, string(payload), "zh-CN")
	require.Contains(t, string(payload), "Asia/Shanghai")
	require.True(t, bytes.Contains(payload, []byte{0x19, 0x13, 0x10}))
	require.True(t, bytes.Contains(payload, []byte{0xfb, 0x3f, 0xf0, 0, 0, 0, 0, 0, 0}))
}
