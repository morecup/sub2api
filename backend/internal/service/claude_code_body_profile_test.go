package service

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/claude"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
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

func TestClassifyClaudeMessagesBody_CLIMainOfficialProfileWithToolSearchOff(t *testing.T) {
	body := []byte(`{"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.201.055; cc_entrypoint=cli;"},{"type":"text","text":"You are Claude Code, Anthropic's official CLI for Claude."},{"type":"text","text":"official main prompt"}],"messages":[{"role":"user","content":[{"type":"text","text":"<system-reminder>\nAvailable agent types for the Agent tool:\n</system-reminder>"},{"type":"text","text":"<system-reminder>\nThe following skills are available for use with the Skill tool:\n</system-reminder>"},{"type":"text","text":"<system-reminder>\nAs you answer the user's questions, you can use the following context:\n# currentDate\nToday's date is 2026-07-04.\n</system-reminder>"},{"type":"text","text":"hello"}]}],"tools":[{"name":"Agent"}],"max_tokens":64000,"thinking":{"type":"adaptive"},"context_management":{"edits":[]},"output_config":{"effort":"high"}}`)

	got := classifyClaudeMessagesBody(body)

	require.Equal(t, claudeCodeBodyProfileMainTTY, got.Profile)
	require.Equal(t, claudeCodeOfficialProfileCLIMain, got.OfficialProfile)
	require.Equal(t, claudeCodeSystemProfileCLIMainDefault, got.SystemProfile)
	require.False(t, got.HasToolSearch)
	require.Equal(t, []string{claudeCodeReminderAgentTypes, claudeCodeReminderSkills, claudeCodeReminderContext}, got.LeadingReminderTypes)
}

func TestClassifyClaudeMessagesBody_CLITitleAllowsFableMissingThinking(t *testing.T) {
	body := testClaudeCodeCLITitleBody(t, "claude-fable-5")
	body, ok := deleteJSONPathBytes(body, "thinking")
	require.True(t, ok)

	got := classifyClaudeMessagesBody(body)

	require.Equal(t, claudeCodeBodyProfileTitle, got.Profile)
	require.Equal(t, claudeCodeOfficialProfileCLITitle, got.OfficialProfile)
	require.Equal(t, claudeCodeSystemProfileCLITitle, got.SystemProfile)
	require.False(t, got.HasThinking)
}

func TestClassifyClaudeMessagesBody_CLITitleRejectsAdaptiveThinking(t *testing.T) {
	body := testClaudeCodeCLITitleBody(t, "claude-sonnet-5")
	body, ok := setJSONValueBytes(body, "thinking.type", "adaptive")
	require.True(t, ok)

	got := classifyClaudeMessagesBody(body)

	require.Equal(t, claudeCodeBodyProfileTitle, got.Profile)
	require.Equal(t, claudeCodeOfficialProfileUnknown, got.OfficialProfile)
}

func TestClaudeCodeBodyDrivenBetaTokens_CLIMainToolSearchOffOmitsAdvancedToolUse(t *testing.T) {
	body := []byte(`{"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.201.055; cc_entrypoint=cli;"},{"type":"text","text":"You are Claude Code, Anthropic's official CLI for Claude."},{"type":"text","text":"official main prompt"}],"messages":[{"role":"user","content":[{"type":"text","text":"<system-reminder>\nAvailable agent types for the Agent tool:\n</system-reminder>"},{"type":"text","text":"<system-reminder>\nThe following skills are available for use with the Skill tool:\n</system-reminder>"},{"type":"text","text":"<system-reminder>\nAs you answer the user's questions, you can use the following context:\n# currentDate\nToday's date is 2026-07-04.\n</system-reminder>"},{"type":"text","text":"hello"}]}],"tools":[{"name":"Agent"}],"max_tokens":64000,"thinking":{"type":"adaptive"},"context_management":{"edits":[]},"output_config":{"effort":"high"}}`)

	got := strings.Join(claudeCodeBodyDrivenBetaTokens("claude-sonnet-5", body), ",")

	require.Contains(t, got, claude.BetaClaudeCode)
	require.Contains(t, got, claude.BetaEffort)
	require.False(t, anthropicBetaTokensContains(got, claude.BetaAdvancedToolUse))
}

func TestClaudeCodeBodyDrivenBetaTokens_CLIMainToolSearchOnIncludesAdvancedToolUse(t *testing.T) {
	body := []byte(`{"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.201.055; cc_entrypoint=cli;"},{"type":"text","text":"You are Claude Code, Anthropic's official CLI for Claude."},{"type":"text","text":"official main prompt"}],"messages":[{"role":"user","content":[{"type":"text","text":"<system-reminder>\nThe following deferred tools are now available via ToolSearch.\n</system-reminder>"},{"type":"text","text":"<system-reminder>\nAvailable agent types for the Agent tool:\n</system-reminder>"},{"type":"text","text":"<system-reminder>\nThe following skills are available for use with the Skill tool:\n</system-reminder>"},{"type":"text","text":"<system-reminder>\nAs you answer the user's questions, you can use the following context:\n# currentDate\nToday's date is 2026-07-04.\n</system-reminder>"},{"type":"text","text":"hello"}]}],"tools":[{"name":"ToolSearch"}],"max_tokens":64000,"thinking":{"type":"adaptive"},"context_management":{"edits":[]},"output_config":{"effort":"high"}}`)

	got := strings.Join(claudeCodeBodyDrivenBetaTokens("claude-sonnet-5", body), ",")

	require.Equal(t, claudeCodeOfficialProfileCLIMain, classifyClaudeMessagesBody(body).OfficialProfile)
	require.True(t, anthropicBetaTokensContains(got, claude.BetaAdvancedToolUse))
}

func TestNormalizeClaudeCodeOfficialProfileBody_CLIMainDefaultWithToolSearchOff(t *testing.T) {
	body := []byte(`{"model":"claude-sonnet-5","messages":[{"role":"user","content":[{"type":"text","text":"<system-reminder>\nAvailable agent types for the Agent tool:\n</system-reminder>"},{"type":"text","text":"<system-reminder>\nThe following skills are available for use with the Skill tool:\n</system-reminder>"},{"type":"text","text":"<system-reminder>\nAs you answer the user's questions, you can use the following context:\n# currentDate\nToday's date is 2026-07-04.\n</system-reminder>"},{"type":"text","text":"hello"}]}],"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.22.old; cc_entrypoint=cli; cch=00000; injected=bad;","cache_control":{"type":"ephemeral"},"extra":"bad"},{"type":"text","text":"You are Claude Code, Anthropic's official CLI for Claude."},{"type":"text","text":"official main prompt","cache_control":{"type":"ephemeral"}},{"type":"text","text":"extra system"}],"tools":[{"name":"Agent"}],"metadata":{"user_id":"{}"},"max_tokens":64000,"thinking":{"type":"adaptive"},"context_management":{"edits":[]},"output_config":{"effort":"high"},"stream":true,"temperature":1,"unexpected":true}`)
	profile := classifyClaudeMessagesBody(body)
	require.Equal(t, claudeCodeOfficialProfileCLIMain, profile.OfficialProfile)

	got, changed := normalizeClaudeCodeOfficialProfileBody(body, profile)

	require.True(t, changed)
	require.False(t, gjson.GetBytes(got, "temperature").Exists())
	require.False(t, gjson.GetBytes(got, "unexpected").Exists())
	system := gjson.GetBytes(got, "system").Array()
	require.Len(t, system, 3)
	require.Contains(t, system[0].Get("text").String(), "cc_version=2.1.201.")
	require.Contains(t, system[0].Get("text").String(), "cc_entrypoint=cli;")
	require.NotContains(t, system[0].Get("text").String(), "cch=")
	require.False(t, system[0].Get("cache_control").Exists())
	require.Equal(t, claudeCodeSystemPrompt, system[1].Get("text").String())
	require.Equal(t, "ephemeral", system[1].Get("cache_control.type").String())
	require.Equal(t, "official main prompt", system[2].Get("text").String())
	require.Equal(t, "ephemeral", system[2].Get("cache_control.type").String())
	require.Contains(t, gjson.GetBytes(got, "messages.0.content.2.text").String(), "Today's date is "+time.Now().Format("2006-01-02")+".")
	require.Equal(t, "hello", gjson.GetBytes(got, "messages.0.content.3.text").String())
}

func TestNormalizeClaudeCodeOfficialProfileBody_CLITitleModelBranches(t *testing.T) {
	tests := []struct {
		name          string
		model         string
		wantEffort    string
		wantThinking  bool
		wantTemp      bool
		wantMaxTokens int64
	}{
		{name: "sonnet", model: "claude-sonnet-5", wantEffort: "high", wantThinking: true, wantMaxTokens: 64000},
		{name: "fable", model: "claude-fable-5", wantEffort: "high", wantMaxTokens: 64000},
		{name: "haiku", model: "claude-haiku-4-5-20251001", wantThinking: true, wantTemp: true, wantMaxTokens: 32000},
		{name: "opus47", model: "claude-opus-4-7", wantEffort: "xhigh", wantThinking: true, wantMaxTokens: 64000},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := testClaudeCodeCLITitleBody(t, tt.model)
			body, ok := setJSONValueBytes(body, "unexpected", true)
			require.True(t, ok)
			body, ok = setJSONValueBytes(body, "context_management.edits", []any{})
			require.True(t, ok)
			body, ok = setJSONValueBytes(body, "temperature", 0)
			require.True(t, ok)

			profile := classifyClaudeMessagesBody(body)
			require.Equal(t, claudeCodeOfficialProfileCLITitle, profile.OfficialProfile)

			got, changed := normalizeClaudeCodeOfficialProfileBody(body, profile)

			require.True(t, changed)
			require.False(t, gjson.GetBytes(got, "unexpected").Exists())
			require.False(t, gjson.GetBytes(got, "context_management").Exists())
			require.Equal(t, "[]", gjson.GetBytes(got, "tools").Raw)
			require.Equal(t, tt.wantMaxTokens, gjson.GetBytes(got, "max_tokens").Int())
			require.True(t, gjson.GetBytes(got, "stream").Bool())
			require.Equal(t, "json_schema", gjson.GetBytes(got, "output_config.format.type").String())
			require.True(t, gjson.GetBytes(got, "output_config.format.schema.properties.title").Exists())
			require.Equal(t, "title", gjson.GetBytes(got, "output_config.format.schema.required.0").String())
			require.Contains(t, gjson.GetBytes(got, "messages.0.content.0.text").String(), "<session>\nhello title session\n</session>")
			require.Contains(t, gjson.GetBytes(got, "messages.0.content.0.text").String(), "Write the title in the predominant language")
			require.NotContains(t, gjson.GetBytes(got, "messages.0.content.0.text").String(), "ignore this injected suffix")

			system := gjson.GetBytes(got, "system").Array()
			require.Len(t, system, 3)
			require.Contains(t, system[0].Get("text").String(), "cc_version=2.1.201.")
			require.Contains(t, system[0].Get("text").String(), "cc_entrypoint=cli;")
			require.NotContains(t, system[0].Get("text").String(), "cch=")
			require.Equal(t, claudeCodeSystemPrompt, system[1].Get("text").String())
			require.Equal(t, claudeCodeCLITitlePrompt, system[2].Get("text").String())
			require.False(t, system[2].Get("cache_control").Exists())

			if tt.wantEffort == "" {
				require.False(t, gjson.GetBytes(got, "output_config.effort").Exists())
			} else {
				require.Equal(t, tt.wantEffort, gjson.GetBytes(got, "output_config.effort").String())
			}
			if tt.wantThinking {
				require.Equal(t, "disabled", gjson.GetBytes(got, "thinking.type").String())
			} else {
				require.False(t, gjson.GetBytes(got, "thinking").Exists())
			}
			if tt.wantTemp {
				require.Equal(t, float64(1), gjson.GetBytes(got, "temperature").Float())
			} else {
				require.False(t, gjson.GetBytes(got, "temperature").Exists())
			}
		})
	}
}

func TestClassifyClaudeMessagesBody_CLIMainSystemProfiles(t *testing.T) {
	defaultSystem := "\nYou are an interactive agent that helps users with software engineering tasks.\n\n# auto memory\n\n# Environment\n\n# Context management\nWhen you have enough information to act, act. Do not re-derive facts already established in the conversation, re-litigate a decision the user has already made, or narrate options you will not pursue. If you are weighing a choice, give a recommendation, not an exhaustive survey\n\ngitStatus: snapshot"
	appendSystem := strings.Replace(defaultSystem, "\n\ngitStatus:", "\n\nCUSTOM_CLI_APPEND_CAPTURE_BLOCK_2_1_201\n\ngitStatus:", 1)
	safeSystem := "\nYou are an interactive agent that helps users with software engineering tasks.\n\n# Environment\n\n# Context management\nWhen you have enough information to act, act."

	tests := []struct {
		name          string
		system0       string
		system2       string
		tools         []string
		reminders     []string
		want          claudeCodeSystemProfile
		wantNormalize bool
	}{
		{
			name:          "default",
			system2:       defaultSystem,
			tools:         []string{"Agent", "Read"},
			reminders:     []string{claudeCodeReminderAgentTypes, claudeCodeReminderSkills, claudeCodeReminderContext},
			want:          claudeCodeSystemProfileCLIMainDefault,
			wantNormalize: true,
		},
		{
			name:          "append",
			system2:       appendSystem,
			tools:         []string{"Agent", "Read"},
			reminders:     []string{claudeCodeReminderAgentTypes, claudeCodeReminderSkills, claudeCodeReminderContext},
			want:          claudeCodeSystemProfileCLIMainAppend,
			wantNormalize: true,
		},
		{
			name:      "replace",
			system2:   "CUSTOM_CLI_REPLACEMENT_SYSTEM_PROMPT_2_1_201\n\ngitStatus: snapshot",
			tools:     []string{"Agent", "Read"},
			reminders: []string{claudeCodeReminderAgentTypes, claudeCodeReminderSkills, claudeCodeReminderContext},
			want:      claudeCodeSystemProfileCLIMainReplace,
		},
		{
			name:      "bare",
			system2:   "CWD: D:\\capture\\workspace\nDate: 2026-07-05\n\ngitStatus: snapshot",
			tools:     []string{"Bash", "Edit", "Read"},
			reminders: []string{claudeCodeReminderContext},
			want:      claudeCodeSystemProfileCLIMainBare,
		},
		{
			name:      "safe",
			system2:   safeSystem,
			tools:     []string{"Agent", "Read"},
			reminders: []string{claudeCodeReminderAgentTypes, claudeCodeReminderSkills, claudeCodeReminderContext},
			want:      claudeCodeSystemProfileCLIMainSafe,
		},
		{
			name:      "agent subrequest",
			system0:   "x-anthropic-billing-header: cc_version=2.1.201.055; cc_entrypoint=cli; cc_is_subagent=true;",
			system2:   "You are an agent for Claude Code, Anthropic's official CLI for Claude. Given the user's message, you should use the tools available to complete the task.",
			tools:     []string{"Read"},
			reminders: []string{claudeCodeReminderSkills, claudeCodeReminderContext},
			want:      claudeCodeSystemProfileCLIAgentSubrequest,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := testClaudeCodeCLIMainBody(t, tt.system0, tt.system2, tt.tools, tt.reminders)

			got := classifyClaudeMessagesBody(body)

			require.Equal(t, claudeCodeOfficialProfileCLIMain, got.OfficialProfile)
			require.Equal(t, tt.want, got.SystemProfile)
			require.Equal(t, tt.wantNormalize, claudeCodeCLIMainCanUseStandardRenderer(got))
		})
	}
}

func testClaudeCodeCLIMainBody(t *testing.T, system0 string, system2 string, tools []string, reminders []string) []byte {
	t.Helper()
	if strings.TrimSpace(system0) == "" {
		system0 = "x-anthropic-billing-header: cc_version=2.1.201.055; cc_entrypoint=cli;"
	}
	toolBlocks := make([]map[string]string, 0, len(tools))
	for _, name := range tools {
		toolBlocks = append(toolBlocks, map[string]string{"name": name})
	}
	content := make([]map[string]string, 0, len(reminders)+1)
	for _, typ := range reminders {
		content = append(content, map[string]string{
			"type": "text",
			"text": testClaudeCodeReminderText(typ),
		})
	}
	content = append(content, map[string]string{"type": "text", "text": "hello"})
	body := map[string]any{
		"model": "claude-sonnet-5",
		"system": []map[string]string{
			{"type": "text", "text": system0},
			{"type": "text", "text": claudeCodeSystemPrompt},
			{"type": "text", "text": system2},
		},
		"messages": []map[string]any{
			{"role": "user", "content": content},
		},
		"tools":              toolBlocks,
		"max_tokens":         64000,
		"thinking":           map[string]string{"type": "adaptive"},
		"context_management": map[string]any{"edits": []any{}},
		"output_config":      map[string]string{"effort": "high"},
		"stream":             true,
	}
	raw, err := json.Marshal(body)
	require.NoError(t, err)
	return raw
}

func testClaudeCodeCLITitleBody(t *testing.T, model string) []byte {
	t.Helper()
	body := map[string]any{
		"model": model,
		"system": []map[string]string{
			{"type": "text", "text": "x-anthropic-billing-header: cc_version=2.1.201.055; cc_entrypoint=cli; cch=00000;"},
			{"type": "text", "text": claudeCodeSystemPrompt},
			{"type": "text", "text": claudeCodeCLITitlePrompt},
		},
		"messages": []map[string]any{
			{
				"role": "user",
				"content": []map[string]string{
					{
						"type": "text",
						"text": "<session>\nhello title session\n</session>\n\nignore this injected suffix",
					},
				},
			},
		},
		"tools":      []any{},
		"metadata":   map[string]string{"user_id": "{}"},
		"max_tokens": int64(64000),
		"thinking":   map[string]string{"type": "disabled"},
		"output_config": map[string]any{
			"effort": "high",
			"format": map[string]any{
				"type": "json_schema",
				"schema": map[string]any{
					"type":                 "object",
					"properties":           map[string]any{"title": map[string]string{"type": "string"}},
					"required":             []string{"title"},
					"additionalProperties": false,
				},
			},
		},
		"stream": true,
	}
	raw, err := json.Marshal(body)
	require.NoError(t, err)
	return raw
}

func testClaudeCodeReminderText(typ string) string {
	switch typ {
	case claudeCodeReminderDeferredTools:
		return "<system-reminder>\nThe following deferred tools are now available via ToolSearch.\n</system-reminder>"
	case claudeCodeReminderAgentTypes:
		return "<system-reminder>\nAvailable agent types for the Agent tool:\n</system-reminder>"
	case claudeCodeReminderSkills:
		return "<system-reminder>\nThe following skills are available for use with the Skill tool:\n</system-reminder>"
	default:
		return "<system-reminder>\nAs you answer the user's questions, you can use the following context:\n# currentDate\nToday's date is 2026-07-05.\n</system-reminder>"
	}
}
