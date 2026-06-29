package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestComputeClaudeCodeFingerprintMatchesCapturedTTYProfile(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{
			name: "title request keeps session block",
			body: `{"messages":[{"role":"user","content":[{"type":"text","text":"<session>\ninteractive tty header capture prompt\n</session>\n\nWrite the title in the language the user wrote in, regardless of the language of the examples above."}]}]}`,
			want: "c2a",
		},
		{
			name: "main request skips flattened system reminders",
			body: `{"messages":[{"role":"user","content":[{"type":"text","text":"<system-reminder>\nThe following deferred tools are now available via ToolSearch.\n</system-reminder>"},{"type":"text","text":"<system-reminder>\nAvailable agent types for the Agent tool:\n- claude: Catch-all for any task.\n</system-reminder>"},{"type":"text","text":"<system-reminder>\nThe following skills are available for use with the Skill tool.\n</system-reminder>"},{"type":"text","text":"<system-reminder>\nAs you answer the user's questions, you can use the following context.\n</system-reminder>"},{"type":"text","text":"interactive tty header capture prompt"}]}]}`,
			want: "3b4",
		},
		{
			name: "preload main request skips reminders",
			body: `{"messages":[{"role":"user","content":[{"type":"text","text":"<system-reminder>\nThe following deferred tools are now available via ToolSearch.\n</system-reminder>"},{"type":"text","text":"<system-reminder>\nAvailable agent types for the Agent tool.\n</system-reminder>"},{"type":"text","text":"fixed cch preload probe"}]}]}`,
			want: "610",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, computeClaudeCodeFingerprint([]byte(tt.body), "2.1.191"))
		})
	}
}

func TestBuildBillingAttributionTextIncludesCCHPlaceholder(t *testing.T) {
	body := []byte(`{"messages":[{"role":"user","content":[{"type":"text","text":"fixed cch preload probe"}]}]}`)

	text, err := buildBillingAttributionText(body, "2.1.191")

	require.NoError(t, err)
	require.Contains(t, text, "x-anthropic-billing-header:")
	require.Contains(t, text, "cc_version=2.1.191.")
	require.Contains(t, text, "cc_entrypoint=cli;")
	require.Contains(t, text, "cch=00000;")
}

func TestComputeClaudeCodeCCHMatchesNativeTrace(t *testing.T) {
	body := []byte(`{"model":"claude-sonnet-4-6","messages":[{"role":"user","content":[{"type":"text","text":"hello"}]}],"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.191.8d2; cc_entrypoint=sdk-cli; cch=00000;"},{"type":"text","text":"You are a Claude agent."}],"max_tokens":20000,"thinking":{"type":"adaptive"},"stream":true}`)

	cch, digitsAt, ok := computeClaudeCodeCCH(body)

	require.True(t, ok)
	require.Equal(t, "67144", cch)
	require.Equal(t, 213, digitsAt)
}

func TestApplyClaudeCodeCCHPatchesPlaceholder(t *testing.T) {
	body := []byte(`{"model":"claude-sonnet-4-6","messages":[{"role":"user","content":[{"type":"text","text":"hello"}]}],"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.191.8d2; cc_entrypoint=sdk-cli; cch=00000;"},{"type":"text","text":"You are a Claude agent."}],"max_tokens":20000,"thinking":{"type":"adaptive"},"stream":true}`)

	patched := applyClaudeCodeCCH(body)

	require.Contains(t, string(patched), "cch=67144;")
	require.NotContains(t, string(patched), "cch=00000;")
	require.Contains(t, string(patched), `"model":"claude-sonnet-4-6"`)
}

func TestComputeClaudeCodeCCHSkipsNativeFallbackFields(t *testing.T) {
	bodyA := []byte(`{"model":"claude-a","fallbacks":["one",["nested"]],"messages":[{"role":"user","content":[{"type":"text","text":"hello"}]}],"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.191.8d2; cc_entrypoint=sdk-cli; cch=00000;"}],"max_tokens":1,"fallback_credit_token":"token-a","stream":true}`)
	bodyB := []byte(`{"model":"claude-b","fallbacks":["different",["still skipped"]],"messages":[{"role":"user","content":[{"type":"text","text":"hello"}]}],"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.191.8d2; cc_entrypoint=sdk-cli; cch=00000;"}],"max_tokens":999999,"fallback_credit_token":"token-b","stream":true}`)
	bodyC := []byte(`{"model":"claude-b","fallbacks":["different",["still skipped"]],"messages":[{"role":"user","content":[{"type":"text","text":"hello"}]}],"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.191.8d2; cc_entrypoint=sdk-cli; cch=00000;"}],"max_tokens":999999,"fallback_credit_token":"token-b","stream":false}`)

	cchA, _, okA := computeClaudeCodeCCH(bodyA)
	cchB, _, okB := computeClaudeCodeCCH(bodyB)
	cchC, _, okC := computeClaudeCodeCCH(bodyC)

	require.True(t, okA)
	require.True(t, okB)
	require.True(t, okC)
	require.Equal(t, cchA, cchB)
	require.NotEqual(t, cchA, cchC)
}

func TestComputeClaudeCodeCCHSkipsNativeFieldsWithLeadingComma(t *testing.T) {
	base := []byte(`{"model":"claude-a","messages":[{"role":"user","content":[{"type":"text","text":"hello"}]}],"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.191.8d2; cc_entrypoint=sdk-cli; cch=00000;"}],"stream":true}`)
	withMaxTokensLast := []byte(`{"model":"claude-b","messages":[{"role":"user","content":[{"type":"text","text":"hello"}]}],"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.191.8d2; cc_entrypoint=sdk-cli; cch=00000;"}],"stream":true,"max_tokens":999999}`)
	withFallbacksLast := []byte(`{"model":"claude-c","messages":[{"role":"user","content":[{"type":"text","text":"hello"}]}],"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.191.8d2; cc_entrypoint=sdk-cli; cch=00000;"}],"stream":true,"fallbacks":["different",["still skipped"]]}`)
	withFallbackTokenLast := []byte(`{"model":"claude-d","messages":[{"role":"user","content":[{"type":"text","text":"hello"}]}],"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.191.8d2; cc_entrypoint=sdk-cli; cch=00000;"}],"stream":true,"fallback_credit_token":"token-d"}`)

	baseCCH, _, ok := computeClaudeCodeCCH(base)
	require.True(t, ok)

	for _, body := range [][]byte{withMaxTokensLast, withFallbacksLast, withFallbackTokenLast} {
		cch, _, ok := computeClaudeCodeCCH(body)
		require.True(t, ok)
		require.Equal(t, baseCCH, cch)
	}
}

func TestExtractFirstUserTextSkipsMetaOnlyMessages(t *testing.T) {
	body := []byte(`{"messages":[{"role":"user","content":"<system-reminder>\nmeta\n</system-reminder>"},{"role":"assistant","content":"ok"},{"role":"user","content":"real prompt"}]}`)

	require.Equal(t, "real prompt", extractFirstUserText(body))
}
