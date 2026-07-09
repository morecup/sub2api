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
}

// defaultCodexSynthInstructions：按模型选用真实 Codex base prompt。
func TestDefaultCodexSynthInstructionsModelAware(t *testing.T) {
	require.True(t, strings.Contains(defaultCodexSynthInstructions("gpt-5-codex"), "You are Codex, based on GPT-5"))
	require.True(t, strings.Contains(defaultCodexSynthInstructions("gpt-5.5"), "You are Codex, a coding agent based on GPT-5"))
	require.False(t, strings.Contains(defaultCodexSynthInstructions("gpt-5.5"), "You are GPT-5.1 running in the Codex CLI"))
	require.True(t, strings.Contains(defaultCodexSynthInstructions("gpt-5.2"), "You are GPT-5.2 running in the Codex CLI"))
	require.True(t, strings.Contains(defaultCodexSynthInstructions("gpt-5.1"), "You are GPT-5.1 running in the Codex CLI"))
}
