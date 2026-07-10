package service

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
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

// applyCodexClientMetadata：用固定 Codex installation id 注入 installation 标识，幂等并覆盖冲突值。
func TestApplyCodexClientMetadata(t *testing.T) {
	body := map[string]any{}
	require.True(t, applyCodexClientMetadata(body))
	cm, ok := body["client_metadata"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, codexInstallationID, cm["x-codex-installation-id"])
	// 幂等
	require.False(t, applyCodexClientMetadata(body))

	// 既有 client_metadata（如 turn metadata）保留，补 installation 键
	body3 := map[string]any{"client_metadata": map[string]any{"x-codex-turn-metadata": "t"}}
	require.True(t, applyCodexClientMetadata(body3))
	cm3, _ := body3["client_metadata"].(map[string]any)
	require.Equal(t, "t", cm3["x-codex-turn-metadata"])
	require.Equal(t, codexInstallationID, cm3["x-codex-installation-id"])

	// 客户端传入冲突 installation id 时，由伪装层改回固定值。
	body4 := map[string]any{"client_metadata": map[string]any{"x-codex-installation-id": "client-supplied"}}
	require.True(t, applyCodexClientMetadata(body4))
	cm4, _ := body4["client_metadata"].(map[string]any)
	require.Equal(t, codexInstallationID, cm4["x-codex-installation-id"])

	// 0.144 完整 turn profile 会把 header metadata 同步到 body。
	turnMetadata := `{"installation_id":"` + codexInstallationID + `","session_id":"session-1","thread_id":"thread-1","turn_id":"turn-1","window_id":"window-1","request_kind":"turn","thread_source":"user","sandbox":"none","workspaces":{},"turn_started_at_unix_ms":1}`
	body5 := map[string]any{"client_metadata": map[string]any{"keep": "value"}}
	require.True(t, applyCodexClientMetadata(body5, turnMetadata))
	cm5, _ := body5["client_metadata"].(map[string]any)
	require.Equal(t, "value", cm5["keep"])
	require.Equal(t, "session-1", cm5["session_id"])
	require.Equal(t, "thread-1", cm5["thread_id"])
	require.Equal(t, "turn-1", cm5["turn_id"])
	require.Equal(t, codexInstallationID, cm5["x-codex-installation-id"])
	require.Equal(t, "window-1", cm5["x-codex-window-id"])
	require.Equal(t, turnMetadata, cm5["x-codex-turn-metadata"])
	require.False(t, applyCodexClientMetadata(body5, turnMetadata))

	body6 := map[string]any{"client_metadata": map[string]any(nil)}
	require.True(t, applyCodexClientMetadata(body6))
	cm6, _ := body6["client_metadata"].(map[string]any)
	require.Equal(t, codexInstallationID, cm6["x-codex-installation-id"])
}

func TestBuildCodexOAIAttestationMatchesDesktopWindowsEnvelope(t *testing.T) {
	const appSessionID = "eeb98e1c-5890-479a-a8db-3516fa5338e6"
	const captured = `{"v":1,"s":0,"t":"v1.o2plcnJvcl9jb2RlAWlidW5kbGVfaWRwY29tLm9wZW5haS5jb2RleGFmWFanAAEBgWV6aC1DTgJlemgtQ04DbUFzaWEvU2hhbmdoYWkEGQevBfs_-AAAAAAAAAZ4JGVlYjk4ZTFjLTU4OTAtNDc5YS1hOGRiLTM1MTZmYTUzMzhlNg"}`
	require.Equal(t, captured, buildCodexOAIAttestation(appSessionID))
}

// defaultCodexSynthInstructions：按模型选用真实 Codex base prompt。
func TestDefaultCodexSynthInstructionsModelAware(t *testing.T) {
	require.True(t, strings.Contains(defaultCodexSynthInstructions("gpt-5-codex"), "You are Codex, based on GPT-5"))
	require.True(t, strings.Contains(defaultCodexSynthInstructions("gpt-5.5"), "You are Codex, a coding agent based on GPT-5"))
	require.False(t, strings.Contains(defaultCodexSynthInstructions("gpt-5.5"), "You are GPT-5.1 running in the Codex CLI"))
	require.True(t, strings.Contains(defaultCodexSynthInstructions("gpt-5.2"), "You are GPT-5.2 running in the Codex CLI"))
	require.True(t, strings.Contains(defaultCodexSynthInstructions("gpt-5.1"), "You are GPT-5.1 running in the Codex CLI"))
}
