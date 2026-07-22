package service

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// ensureCodexReasoningInclude：带 reasoning 时补齐 include，幂等且保留既有项。
func TestEnsureCodexReasoningInclude(t *testing.T) {
	// reasoning 存在、include 缺失 → 注入
	body := map[string]any{"reasoning": map[string]any{"effort": "medium"}}
	require.True(t, ensureCodexReasoningInclude(body))
	require.Equal(t, []any{"reasoning.encrypted_content"}, body["include"])
	// 幂等：再次调用不重复
	require.False(t, ensureCodexReasoningInclude(body))

	// 无 reasoning → 不动
	body2 := map[string]any{}
	require.False(t, ensureCodexReasoningInclude(body2))
	_, ok := body2["include"]
	require.False(t, ok)

	// 既有 include 保留并追加
	body3 := map[string]any{
		"reasoning": map[string]any{"effort": "high"},
		"include":   []any{"foo"},
	}
	require.True(t, ensureCodexReasoningInclude(body3))
	require.Equal(t, []any{"foo", "reasoning.encrypted_content"}, body3["include"])
}

// applyCodexClientMetadata：注入 installation 标识（默认回退固定值），幂等并覆盖冲突值。
func TestApplyCodexClientMetadata(t *testing.T) {
	body := map[string]any{}
	require.True(t, applyCodexClientMetadata(body, ""))
	cm, ok := body["client_metadata"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, codexInstallationID, cm["x-codex-installation-id"])
	// 幂等
	require.False(t, applyCodexClientMetadata(body, ""))

	// 既有 client_metadata（如 turn metadata）保留，补 installation 键
	body3 := map[string]any{"client_metadata": map[string]any{"x-codex-turn-metadata": "t"}}
	require.True(t, applyCodexClientMetadata(body3, ""))
	cm3, _ := body3["client_metadata"].(map[string]any)
	require.Equal(t, "t", cm3["x-codex-turn-metadata"])
	require.Equal(t, codexInstallationID, cm3["x-codex-installation-id"])

	// 客户端传入冲突 installation id 时，由伪装层改回固定值。
	body4 := map[string]any{"client_metadata": map[string]any{"x-codex-installation-id": "client-supplied"}}
	require.True(t, applyCodexClientMetadata(body4, ""))
	cm4, _ := body4["client_metadata"].(map[string]any)
	require.Equal(t, codexInstallationID, cm4["x-codex-installation-id"])

	// 0.144 完整 turn profile 会把 header metadata 同步到 body。
	turnMetadata := `{"installation_id":"` + codexInstallationID + `","session_id":"session-1","thread_id":"thread-1","turn_id":"turn-1","window_id":"window-1","request_kind":"turn","thread_source":"user","sandbox":"none","workspaces":{},"turn_started_at_unix_ms":1}`
	body5 := map[string]any{"client_metadata": map[string]any{"keep": "value"}}
	require.True(t, applyCodexClientMetadata(body5, "", turnMetadata))
	cm5, _ := body5["client_metadata"].(map[string]any)
	require.Equal(t, "value", cm5["keep"])
	require.Equal(t, "session-1", cm5["session_id"])
	require.Equal(t, "thread-1", cm5["thread_id"])
	require.Equal(t, "turn-1", cm5["turn_id"])
	require.Equal(t, codexInstallationID, cm5["x-codex-installation-id"])
	require.Equal(t, "window-1", cm5["x-codex-window-id"])
	require.Equal(t, turnMetadata, cm5["x-codex-turn-metadata"])
	require.False(t, applyCodexClientMetadata(body5, "", turnMetadata))

	body6 := map[string]any{"client_metadata": map[string]any(nil)}
	require.True(t, applyCodexClientMetadata(body6, ""))
	cm6, _ := body6["client_metadata"].(map[string]any)
	require.Equal(t, codexInstallationID, cm6["x-codex-installation-id"])
}

func TestBuildCodexOAIAttestationMatchesDesktopWindowsEnvelope(t *testing.T) {
	const appSessionID = "eeb98e1c-5890-479a-a8db-3516fa5338e6"
	const captured = `{"v":1,"s":0,"t":"v1.o2plcnJvcl9jb2RlAWlidW5kbGVfaWRwY29tLm9wZW5haS5jb2RleGFmWFanAAEBgWV6aC1DTgJlemgtQ04DbUFzaWEvU2hhbmdoYWkEGQevBfs_-AAAAAAAAAZ4JGVlYjk4ZTFjLTU4OTAtNDc5YS1hOGRiLTM1MTZmYTUzMzhlNg"}`
	require.Equal(t, captured, buildCodexOAIAttestation(appSessionID))
}

func TestSyncCodexOAuthMimicRequestBodyPreservesCompactBody(t *testing.T) {
	body := []byte(`{"model":"gpt-5.4","input":"compact me"}`)
	req := httptest.NewRequest(http.MethodPost, "https://chatgpt.com/backend-api/codex/responses/compact", strings.NewReader(string(body)))
	applyCodexOAuthMimicHeaders(req, 0, 1, "compact-session", codexDesktopOriginator, true, false)

	updated, err := syncCodexOAuthMimicRequestBody(req, body, true)
	require.NoError(t, err)
	require.Equal(t, body, updated)
	require.False(t, strings.Contains(string(updated), "client_metadata"))
}

// defaultCodexSynthInstructions：按模型选用真实 Codex base prompt。
func TestDefaultCodexSynthInstructionsModelAware(t *testing.T) {
	require.True(t, strings.Contains(defaultCodexSynthInstructions("gpt-5-codex"), "You are Codex, based on GPT-5"))
	require.True(t, strings.Contains(defaultCodexSynthInstructions("gpt-5.5"), "You are Codex, a coding agent based on GPT-5"))
	require.False(t, strings.Contains(defaultCodexSynthInstructions("gpt-5.5"), "You are GPT-5.1 running in the Codex CLI"))
	require.True(t, strings.Contains(defaultCodexSynthInstructions("gpt-5.2"), "You are GPT-5.2 running in the Codex CLI"))
	require.True(t, strings.Contains(defaultCodexSynthInstructions("gpt-5.1"), "You are GPT-5.1 running in the Codex CLI"))
}

// stream_options：0.145 实抓非 compact 请求恒定 {"reasoning_summary_delivery":"sequential_cutoff"}，
// 客户端自带其它内容（如 include_usage）时覆盖为实抓值；compact 分支不设置。
func TestApplyCodexOAuthTransformStreamOptionsNormalization(t *testing.T) {
	want := map[string]any{"reasoning_summary_delivery": codexStreamOptionsReasoningSummaryDelivery}

	// 客户端带 {"include_usage":true} 时被覆盖。
	body := map[string]any{
		"model":          "gpt-5.6",
		"stream_options": map[string]any{"include_usage": true},
		"input":          []any{map[string]any{"role": "user", "content": "hi"}},
	}
	applyCodexOAuthTransform(body, false, false)
	require.Equal(t, want, body["stream_options"])

	// 缺失时补齐。
	body2 := map[string]any{
		"model": "gpt-5.6",
		"input": []any{map[string]any{"role": "user", "content": "hi"}},
	}
	applyCodexOAuthTransform(body2, false, false)
	require.Equal(t, want, body2["stream_options"])

	// compact 分支不设置。
	body3 := map[string]any{
		"model": "gpt-5.6",
		"input": []any{map[string]any{"role": "user", "content": "hi"}},
	}
	applyCodexOAuthTransform(body3, false, true)
	_, ok := body3["stream_options"]
	require.False(t, ok)
}

// prompt_cache_key：有可解析 session_id 的 turn metadata 时强制与 session_id 对齐
// （0.145 实抓二者恒等），覆盖客户端原值；无 metadata 时不处理。
func TestApplyCodexClientMetadataAlignsPromptCacheKey(t *testing.T) {
	turnMetadata := `{"installation_id":"` + codexInstallationID + `","session_id":"sess-1","thread_id":"sess-1","turn_id":"turn-1","window_id":"sess-1:0","request_kind":"turn","thread_source":"user","sandbox":"none","workspaces":{},"turn_started_at_unix_ms":1}`

	body := map[string]any{"prompt_cache_key": "client-original"}
	require.True(t, applyCodexClientMetadata(body, "", turnMetadata))
	require.Equal(t, "sess-1", body["prompt_cache_key"])
	// 幂等：再次调用不重复修改。
	require.False(t, applyCodexClientMetadata(body, "", turnMetadata))

	// 无 metadata 时不动 prompt_cache_key。
	body2 := map[string]any{"prompt_cache_key": "keep-me"}
	require.True(t, applyCodexClientMetadata(body2, ""))
	require.Equal(t, "keep-me", body2["prompt_cache_key"])

	// Bytes 版本同样对齐。
	updated, modified, err := applyCodexClientMetadataBytes([]byte(`{"model":"gpt-5.6","prompt_cache_key":"orig"}`), turnMetadata)
	require.NoError(t, err)
	require.True(t, modified)
	require.Equal(t, "sess-1", gjson.GetBytes(updated, "prompt_cache_key").String())
}
