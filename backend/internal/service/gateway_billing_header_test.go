package service

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/tidwall/gjson"
)

func TestSyncBillingHeaderVersion(t *testing.T) {
	tests := []struct {
		name      string
		body      string
		userAgent string
		wantSub   string // substring expected in result
		unchanged bool   // expect body to remain the same
	}{
		{
			name:      "replaces cc_version preserving message-derived suffix",
			body:      `{"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.81.df2; cc_entrypoint=cli; cch=00000;"},{"type":"text","text":"You are Claude Code.","cache_control":{"type":"ephemeral"}}],"messages":[]}`,
			userAgent: "claude-cli/2.1.22 (external, cli)",
			wantSub:   "cc_version=2.1.22.df2",
		},
		{
			name:      "no billing header in system",
			body:      `{"system":[{"type":"text","text":"You are Claude Code."}],"messages":[]}`,
			userAgent: "claude-cli/2.1.22",
			unchanged: true,
		},
		{
			name:      "no system field",
			body:      `{"messages":[]}`,
			userAgent: "claude-cli/2.1.22",
			unchanged: true,
		},
		{
			name:      "user-agent without version",
			body:      `{"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.81; cc_entrypoint=cli; cch=00000;"}],"messages":[]}`,
			userAgent: "Mozilla/5.0",
			unchanged: true,
		},
		{
			name:      "empty user-agent",
			body:      `{"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.81; cc_entrypoint=cli; cch=00000;"}],"messages":[]}`,
			userAgent: "",
			unchanged: true,
		},
		{
			name:      "version already matches",
			body:      `{"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.22; cc_entrypoint=cli; cch=00000;"}],"messages":[]}`,
			userAgent: "claude-cli/2.1.22",
			unchanged: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := syncBillingHeaderVersion([]byte(tt.body), tt.userAgent)
			if tt.unchanged {
				assert.Equal(t, tt.body, string(result), "body should remain unchanged")
			} else {
				assert.Contains(t, string(result), tt.wantSub)
				// Ensure old semver is gone
				assert.NotContains(t, string(result), "cc_version=2.1.81")
			}
		})
	}
}

func TestRefreshClaudeCodeBillingAttribution_RecomputesVersionAndKeepsLaterSystemBlocks(t *testing.T) {
	body := []byte(`{"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.22.old; cc_entrypoint=sdk-cli; cch=abcde; injected=bad; cc_workload=cron_job-1; cc_is_subagent=true;","cache_control":{"type":"ephemeral"},"extra":"bad"},{"type":"text","text":"keep identity"},{"type":"text","text":"keep task"}],"messages":[{"role":"user","content":"hello billing"}]}`)

	result := refreshClaudeCodeBillingAttribution(body, "2.1.191")
	text := gjson.GetBytes(result, "system.0.text").String()

	assert.Contains(t, text, "cc_version=2.1.191.")
	assert.Contains(t, text, "cc_entrypoint=cli")
	assert.NotContains(t, text, "cc_entrypoint=sdk-cli")
	assert.Contains(t, text, "cch=00000;")
	assert.Contains(t, text, "cc_workload=cron_job-1;")
	assert.Contains(t, text, "cc_is_subagent=true;")
	assert.NotContains(t, text, "cc_version=2.1.22.old")
	assert.NotContains(t, text, "injected=bad")
	assert.False(t, gjson.GetBytes(result, "system.0.cache_control").Exists())
	assert.False(t, gjson.GetBytes(result, "system.0.extra").Exists())
	assert.Equal(t, "keep identity", gjson.GetBytes(result, "system.1.text").String())
	assert.Equal(t, "keep task", gjson.GetBytes(result, "system.2.text").String())
}

func TestRefreshClaudeCodeBillingAttribution_DropsInvalidOptionalFields(t *testing.T) {
	body := []byte(`{"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.22.old; cc_entrypoint=cli; cch=abcde; cc_workload=bad.value; cc_is_subagent=false;"}],"messages":[{"role":"user","content":"hello billing"}]}`)

	result := refreshClaudeCodeBillingAttribution(body, "2.1.191")
	text := gjson.GetBytes(result, "system.0.text").String()

	assert.Contains(t, text, "cc_version=2.1.191.")
	assert.Contains(t, text, "cc_entrypoint=cli")
	assert.Contains(t, text, "cch=00000;")
	assert.NotContains(t, text, "cc_workload=")
	assert.NotContains(t, text, "cc_is_subagent=")
}
