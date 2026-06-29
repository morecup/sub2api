package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestClassifyClaudeMessagesBody_BillingSystem0AllowsSideQueryWithoutIdentity(t *testing.T) {
	body := []byte(`{"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.191.8d2; cc_entrypoint=cli; cch=00000;"},{"type":"text","text":"Classify a small side query."}],"messages":[],"max_tokens":256,"thinking":{"type":"disabled"}}`)

	got := classifyClaudeMessagesBody(body)

	require.True(t, got.isClaudeCodeFamily())
	require.Equal(t, "cli", got.BillingEntryPoint)
	require.False(t, got.HasIdentity)
	require.Equal(t, claudeCodeBodyProfileSideQuery, got.Profile)
}

func TestClassifyClaudeMessagesBody_MainTTYKeepsLaterSystemBlocksAsMainSignals(t *testing.T) {
	body := []byte(`{"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.191.8d2; cc_entrypoint=cli; cch=00000;"},{"type":"text","text":"You are Claude Code, Anthropic's official CLI for Claude."},{"type":"text","text":"static","cache_control":{"type":"ephemeral","scope":"global"}},{"type":"text","text":"dynamic","cache_control":{"type":"ephemeral"}}],"messages":[],"max_tokens":64000,"thinking":{"type":"adaptive"},"context_management":{"edits":[]}}`)

	got := classifyClaudeMessagesBody(body)

	require.True(t, got.isClaudeCodeFamily())
	require.True(t, got.HasIdentity)
	require.True(t, got.HasGlobalSystemCache)
	require.True(t, got.HasContextManagement)
	require.Equal(t, claudeCodeBodyProfileMainTTY, got.Profile)
}
