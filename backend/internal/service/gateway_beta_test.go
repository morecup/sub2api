package service

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/claude"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestMergeAnthropicBeta(t *testing.T) {
	got := mergeAnthropicBeta(
		[]string{"oauth-2025-04-20", "interleaved-thinking-2025-05-14"},
		"foo, oauth-2025-04-20,bar, foo",
	)
	require.Equal(t, "oauth-2025-04-20,interleaved-thinking-2025-05-14,foo,bar", got)
}

func TestMergeAnthropicBeta_EmptyIncoming(t *testing.T) {
	got := mergeAnthropicBeta(
		[]string{"oauth-2025-04-20", "interleaved-thinking-2025-05-14"},
		"",
	)
	require.Equal(t, "oauth-2025-04-20,interleaved-thinking-2025-05-14", got)
}

func TestStripBetaTokens(t *testing.T) {
	tests := []struct {
		name   string
		header string
		tokens []string
		want   string
	}{
		{
			name:   "single token in middle",
			header: "oauth-2025-04-20,context-1m-2025-08-07,interleaved-thinking-2025-05-14",
			tokens: []string{"context-1m-2025-08-07"},
			want:   "oauth-2025-04-20,interleaved-thinking-2025-05-14",
		},
		{
			name:   "single token at start",
			header: "context-1m-2025-08-07,oauth-2025-04-20,interleaved-thinking-2025-05-14",
			tokens: []string{"context-1m-2025-08-07"},
			want:   "oauth-2025-04-20,interleaved-thinking-2025-05-14",
		},
		{
			name:   "single token at end",
			header: "oauth-2025-04-20,interleaved-thinking-2025-05-14,context-1m-2025-08-07",
			tokens: []string{"context-1m-2025-08-07"},
			want:   "oauth-2025-04-20,interleaved-thinking-2025-05-14",
		},
		{
			name:   "token not present",
			header: "oauth-2025-04-20,interleaved-thinking-2025-05-14",
			tokens: []string{"context-1m-2025-08-07"},
			want:   "oauth-2025-04-20,interleaved-thinking-2025-05-14",
		},
		{
			name:   "empty header",
			header: "",
			tokens: []string{"context-1m-2025-08-07"},
			want:   "",
		},
		{
			name:   "with spaces",
			header: "oauth-2025-04-20, context-1m-2025-08-07 , interleaved-thinking-2025-05-14",
			tokens: []string{"context-1m-2025-08-07"},
			want:   "oauth-2025-04-20,interleaved-thinking-2025-05-14",
		},
		{
			name:   "only token",
			header: "context-1m-2025-08-07",
			tokens: []string{"context-1m-2025-08-07"},
			want:   "",
		},
		{
			name:   "nil tokens",
			header: "oauth-2025-04-20,interleaved-thinking-2025-05-14",
			tokens: nil,
			want:   "oauth-2025-04-20,interleaved-thinking-2025-05-14",
		},
		{
			name:   "multiple tokens removed",
			header: "oauth-2025-04-20,context-1m-2025-08-07,interleaved-thinking-2025-05-14,fast-mode-2026-02-01",
			tokens: []string{"context-1m-2025-08-07", "fast-mode-2026-02-01"},
			want:   "oauth-2025-04-20,interleaved-thinking-2025-05-14",
		},
		{
			name:   "DroppedBetas is empty (filtering moved to configurable beta policy)",
			header: "oauth-2025-04-20,context-1m-2025-08-07,fast-mode-2026-02-01,interleaved-thinking-2025-05-14",
			tokens: claude.DroppedBetas,
			want:   "oauth-2025-04-20,context-1m-2025-08-07,fast-mode-2026-02-01,interleaved-thinking-2025-05-14",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := stripBetaTokens(tt.header, tt.tokens)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestMergeAnthropicBetaDropping_Context1M(t *testing.T) {
	required := []string{"oauth-2025-04-20", "interleaved-thinking-2025-05-14"}
	incoming := "context-1m-2025-08-07,foo-beta,oauth-2025-04-20"
	drop := map[string]struct{}{"context-1m-2025-08-07": {}}

	got := mergeAnthropicBetaDropping(required, incoming, drop)
	require.Equal(t, "oauth-2025-04-20,interleaved-thinking-2025-05-14,foo-beta", got)
	require.NotContains(t, got, "context-1m-2025-08-07")
}

func TestMergeAnthropicBetaDropping_DroppedBetas(t *testing.T) {
	required := []string{"oauth-2025-04-20", "interleaved-thinking-2025-05-14"}
	incoming := "context-1m-2025-08-07,fast-mode-2026-02-01,foo-beta,oauth-2025-04-20"
	// DroppedBetas is now empty — filtering moved to configurable beta policy.
	// Without a policy filter set, nothing gets dropped from the static set.
	drop := droppedBetaSet()

	got := mergeAnthropicBetaDropping(required, incoming, drop)
	require.Equal(t, "oauth-2025-04-20,interleaved-thinking-2025-05-14,context-1m-2025-08-07,fast-mode-2026-02-01,foo-beta", got)
	require.Contains(t, got, "context-1m-2025-08-07")
	require.Contains(t, got, "fast-mode-2026-02-01")
}

func TestFullClaudeCodeMimicryBetas_MatchesTTYMainBetas(t *testing.T) {
	required := claude.FullClaudeCodeMimicryBetas()

	require.Equal(t, []string{
		claude.BetaClaudeCode,
		claude.BetaInterleavedThinking,
		claude.BetaRedactThinking,
		claude.BetaThinkingTokenCount,
		claude.BetaContextManagement,
		claude.BetaPromptCachingScope,
		claude.BetaAdvancedToolUse,
		claude.BetaEffort,
	}, required)
}

func TestClaudeCodeMainBetasForModel_MatchesTTYBranches(t *testing.T) {
	require.Equal(t, claude.HaikuBetaHeader, claude.ClaudeCodeMainBetaHeaderForModel("claude-haiku-4-5"))

	sonnet := claude.ClaudeCodeMainBetaHeaderForModel("claude-sonnet-4-6")
	require.Equal(t, claude.DefaultBetaHeader, sonnet)
	require.NotContains(t, sonnet, claude.BetaOAuth)
	require.NotContains(t, sonnet, claude.BetaMidConversation)

	opus48 := claude.ClaudeCodeMainBetaHeaderForModel("claude-opus-4-8")
	require.Contains(t, opus48, claude.BetaMidConversation)
	require.Equal(t,
		"claude-code-20250219,interleaved-thinking-2025-05-14,redact-thinking-2026-02-12,thinking-token-count-2026-05-13,context-management-2025-06-27,prompt-caching-scope-2026-01-05,mid-conversation-system-2026-04-07,advanced-tool-use-2025-11-20,effort-2025-11-24",
		opus48,
	)

	fable := claude.ClaudeCodeMainBetaHeaderForModel("claude-fable-5")
	require.Equal(t,
		"claude-code-20250219,interleaved-thinking-2025-05-14,redact-thinking-2026-02-12,thinking-token-count-2026-05-13,context-management-2025-06-27,prompt-caching-scope-2026-01-05,mid-conversation-system-2026-04-07,advanced-tool-use-2025-11-20,effort-2025-11-24,fallback-credit-2026-06-01",
		fable,
	)
	require.Contains(t, fable, claude.BetaFallbackCredit)

	fableToolSearchOff := strings.Join(claude.ClaudeCodeMainToolSearchOffBetasForModel("claude-fable-5"), ",")
	require.NotContains(t, fableToolSearchOff, claude.BetaAdvancedToolUse)
	require.Contains(t, fableToolSearchOff, claude.BetaFallbackCredit)
	require.Equal(t,
		"claude-code-20250219,interleaved-thinking-2025-05-14,redact-thinking-2026-02-12,thinking-token-count-2026-05-13,context-management-2025-06-27,prompt-caching-scope-2026-01-05,mid-conversation-system-2026-04-07,effort-2025-11-24,fallback-credit-2026-06-01",
		fableToolSearchOff,
	)
}

func TestClaudeCodeTitleBetas_MatchesTTYTitleQuery(t *testing.T) {
	require.Equal(t,
		"interleaved-thinking-2025-05-14,redact-thinking-2026-02-12,thinking-token-count-2026-05-13,context-management-2025-06-27,prompt-caching-scope-2026-01-05,structured-outputs-2025-12-15",
		claude.TitleBetaHeader,
	)
	require.Equal(t, claude.TitleBetaHeader, strings.Join(claude.ClaudeCodeTitleBetas(), ","))
	require.Equal(t,
		"claude-code-20250219,interleaved-thinking-2025-05-14,redact-thinking-2026-02-12,thinking-token-count-2026-05-13,context-management-2025-06-27,prompt-caching-scope-2026-01-05,effort-2025-11-24,structured-outputs-2025-12-15",
		claude.ClaudeCodeTitleBetaHeaderForModel("claude-sonnet-5"),
	)
	require.Equal(t,
		"claude-code-20250219,interleaved-thinking-2025-05-14,redact-thinking-2026-02-12,thinking-token-count-2026-05-13,context-management-2025-06-27,prompt-caching-scope-2026-01-05,mid-conversation-system-2026-04-07,effort-2025-11-24,structured-outputs-2025-12-15",
		claude.ClaudeCodeTitleBetaHeaderForModel("claude-fable-5"),
	)
}

func TestDefaultHeaders_MatchTTYFingerprint(t *testing.T) {
	require.Equal(t, "claude-cli/2.1.201 (external, cli)", claude.DefaultHeaders["User-Agent"])
	require.Equal(t, "cli", claude.DefaultHeaders["X-App"])
	require.Equal(t, "js", claude.DefaultHeaders["X-Stainless-Lang"])
	require.Equal(t, "0.94.0", claude.DefaultHeaders["X-Stainless-Package-Version"])
	require.Equal(t, "Linux", claude.DefaultHeaders["X-Stainless-OS"])
	require.Equal(t, "x64", claude.DefaultHeaders["X-Stainless-Arch"])
	require.Equal(t, "node", claude.DefaultHeaders["X-Stainless-Runtime"])
	require.Equal(t, "v26.3.0", claude.DefaultHeaders["X-Stainless-Runtime-Version"])
	require.Equal(t, "0", claude.DefaultHeaders["X-Stainless-Retry-Count"])
	require.Equal(t, "600", claude.DefaultHeaders["X-Stainless-Timeout"])
	require.Equal(t, "true", claude.DefaultHeaders["Anthropic-Dangerous-Direct-Browser-Access"])
}

func TestClaudeCodeBodyDrivenBetaTokens_DerivesCapabilityTokens(t *testing.T) {
	body := []byte(`{"context_management":{"edits":[]},"output_config":{"effort":"high","format":{"type":"json_schema"}},"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.191.8d2; cc_entrypoint=cli; cch=00000;"},{"type":"text","text":"You are Claude Code, Anthropic's official CLI for Claude."},{"type":"text","text":"s","cache_control":{"type":"ephemeral","scope":"global"}}],"max_tokens":64000}`)

	got := strings.Join(claudeCodeBodyDrivenBetaTokens("claude-opus-4-8", body), ",")

	require.Contains(t, got, claude.BetaContextManagement)
	require.Contains(t, got, claude.BetaPromptCachingScope)
	require.Contains(t, got, claude.BetaMidConversation)
	require.Contains(t, got, claude.BetaEffort)
	require.Contains(t, got, claude.BetaStructuredOutputs)
	require.NotContains(t, got, claude.BetaOAuth)
}

func TestClaudeCodeBodyDrivenBetaTokens_DerivesStructuredOutputFromLegacyOutputFormat(t *testing.T) {
	body := []byte(`{"output_format":{"type":"json_schema","schema":{"type":"object"}}}`)

	got := strings.Join(claudeCodeBodyDrivenBetaTokens("claude-sonnet-4-6", body), ",")

	require.Contains(t, got, claude.BetaStructuredOutputs)
}

func TestClaudeCodeBodyDrivenBetaTokens_OmitsContextManagementWithoutBodyField(t *testing.T) {
	body := []byte(`{"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.191.8d2; cc_entrypoint=cli; cch=00000;"},{"type":"text","text":"Classify this side query."}],"messages":[],"max_tokens":256,"thinking":{"type":"disabled"}}`)

	got := strings.Join(claudeCodeBodyDrivenBetaTokens("claude-sonnet-4-6", body), ",")

	require.Contains(t, got, claude.BetaClaudeCode)
	require.False(t, anthropicBetaTokensContains(got, claude.BetaContextManagement))
	require.False(t, anthropicBetaTokensContains(got, claude.BetaPromptCachingScope))
	require.False(t, anthropicBetaTokensContains(got, claude.BetaEffort))
}

func TestClaudeCodeBodyDrivenBetaTokens_GenericBodyDoesNotInventClaudeCode(t *testing.T) {
	got := strings.Join(claudeCodeBodyDrivenBetaTokens("claude-sonnet-4-6", []byte(`{"messages":[]}`)), ",")

	require.Empty(t, got)
}

func TestClaudeCodeBodyDrivenBetaTokens_MergesBodyBetas(t *testing.T) {
	body := []byte(`{"betas":["fast-mode-2026-02-01"],"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.191.8d2; cc_entrypoint=cli; cch=00000;"},{"type":"text","text":"Classify this side query."}],"messages":[],"max_tokens":256}`)

	got := strings.Join(claudeCodeBodyDrivenBetaTokens("claude-sonnet-4-6", body), ",")

	require.Contains(t, got, claude.BetaFastMode)
	require.Contains(t, got, claude.BetaClaudeCode)
}

func TestApplyClaudeCodeMimicHeaders_DoesNotInventHelperMethod(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)

	applyClaudeCodeMimicHeaders(req)

	require.Equal(t, "application/json", getHeaderRaw(req.Header, "Accept"))
	require.Equal(t, "claude-cli/2.1.201 (external, cli)", getHeaderRaw(req.Header, "User-Agent"))
	require.Equal(t, "Linux", getHeaderRaw(req.Header, "X-Stainless-OS"))
	require.Equal(t, "x64", getHeaderRaw(req.Header, "X-Stainless-Arch"))
	require.Equal(t, "v26.3.0", getHeaderRaw(req.Header, "X-Stainless-Runtime-Version"))
	require.Empty(t, getHeaderRaw(req.Header, "x-stainless-helper-method"))
	require.NotEmpty(t, getHeaderRaw(req.Header, "x-client-request-id"))
}

func TestApplyClaudeCodeFamilyHeaders_CLITitlePlatformAndRemoteSession(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	clientHeaders := http.Header{}
	clientHeaders.Set("X-Stainless-OS", "Windows")
	clientHeaders.Set("x-claude-remote-session-id", "remote-from-client")
	profile := claudeCodeBodyClassification{
		OfficialProfile:   claudeCodeOfficialProfileCLITitle,
		BillingEntryPoint: "cli",
		HasBilling:        true,
	}

	applyClaudeCodeFamilyHeaders(req, profile, nil, clientHeaders)

	require.Equal(t, "Windows", getHeaderRaw(req.Header, "X-Stainless-OS"))
	require.Equal(t, "remote-from-client", getHeaderRaw(req.Header, "x-claude-remote-session-id"))
	require.Equal(t, "claude-cli/2.1.201 (external, cli)", getHeaderRaw(req.Header, "User-Agent"))
}

func TestRefineClaudeCodeMessagesProfileForHTTPRequest_CLITitleSignals(t *testing.T) {
	gin.SetMode(gin.TestMode)
	profile := claudeCodeBodyClassification{
		Profile:           claudeCodeBodyProfileTitle,
		OfficialProfile:   claudeCodeOfficialProfileCLITitle,
		SystemProfile:     claudeCodeSystemProfileCLITitle,
		HasBilling:        true,
		BillingEntryPoint: "cli",
	}
	headers := http.Header{}
	headers.Set("User-Agent", "claude-cli/2.1.201 (external, cli)")

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages?beta=true", nil)

	got := refineClaudeCodeMessagesProfileForHTTPRequest(profile, c, headers)
	require.Equal(t, claudeCodeOfficialProfileCLITitle, got.OfficialProfile)

	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages/count_tokens?beta=true", nil)
	got = refineClaudeCodeMessagesProfileForHTTPRequest(profile, c, headers)
	require.Equal(t, claudeCodeOfficialProfileUnknown, got.OfficialProfile)

	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages?beta=true", nil)
	headers.Set("User-Agent", "curl/8.0")
	got = refineClaudeCodeMessagesProfileForHTTPRequest(profile, c, headers)
	require.Equal(t, claudeCodeOfficialProfileUnknown, got.OfficialProfile)
}

func TestClaudeCodeBodyProfileBetaTokens_CLITitleRequiresOfficialProfile(t *testing.T) {
	profile := claudeCodeBodyClassification{
		Profile:             claudeCodeBodyProfileTitle,
		OfficialProfile:     claudeCodeOfficialProfileUnknown,
		HasBilling:          true,
		HasStructuredOutput: true,
	}

	got := claudeCodeBodyProfileBetaTokens("claude-sonnet-5", profile)

	require.Equal(t, []string{claude.BetaClaudeCode, claude.BetaStructuredOutputs}, got)
}

func TestResolveClaudeCodeStainlessOS_Fallbacks(t *testing.T) {
	body := []byte(`{"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.201.055; cc_entrypoint=cli;"},{"type":"text","text":"You are Claude Code, Anthropic's official CLI for Claude."},{"type":"text","text":"# Environment\n - Platform: win32\n - OS Version: Windows 11"}]}`)

	require.Equal(t, "Windows", resolveClaudeCodeStainlessOS(body, nil))
	require.Equal(t, "Linux", resolveClaudeCodeStainlessOS(nil, nil))
}

func TestSyncClaudeCodeSessionIDHeader_UsesMetadataJSON(t *testing.T) {
	const sessionID = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	body := []byte(`{"metadata":{"user_id":"{\"device_id\":\"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb\",\"account_uuid\":\"\",\"session_id\":\"` + sessionID + `\"}"}}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)

	syncClaudeCodeSessionIDHeader(req, body, true)

	require.Equal(t, sessionID, getHeaderRaw(req.Header, "X-Claude-Code-Session-Id"))
}

func TestMergeAnthropicBetaDropping_PreservesIncomingRedactThinking(t *testing.T) {
	required := claude.FullClaudeCodeMimicryBetas()
	incoming := claude.BetaRedactThinking

	got := mergeAnthropicBetaDropping(required, incoming, droppedBetaSet())

	require.Contains(t, got, claude.BetaRedactThinking)
}

func TestDroppedBetaSet(t *testing.T) {
	// Base set contains DroppedBetas (now empty — filtering moved to configurable beta policy)
	base := droppedBetaSet()
	require.Len(t, base, len(claude.DroppedBetas))

	// With extra tokens
	extended := droppedBetaSet(claude.BetaClaudeCode)
	require.Contains(t, extended, claude.BetaClaudeCode)
	require.Len(t, extended, len(claude.DroppedBetas)+1)
}

func TestBuildBetaTokenSet(t *testing.T) {
	got := buildBetaTokenSet([]string{"foo", "", "bar", "foo"})
	require.Len(t, got, 2)
	require.Contains(t, got, "foo")
	require.Contains(t, got, "bar")
	require.NotContains(t, got, "")

	empty := buildBetaTokenSet(nil)
	require.Empty(t, empty)
}

func TestContainsBetaToken(t *testing.T) {
	tests := []struct {
		name   string
		header string
		token  string
		want   bool
	}{
		{"present in middle", "oauth-2025-04-20,fast-mode-2026-02-01,interleaved-thinking-2025-05-14", "fast-mode-2026-02-01", true},
		{"present at start", "fast-mode-2026-02-01,oauth-2025-04-20", "fast-mode-2026-02-01", true},
		{"present at end", "oauth-2025-04-20,fast-mode-2026-02-01", "fast-mode-2026-02-01", true},
		{"only token", "fast-mode-2026-02-01", "fast-mode-2026-02-01", true},
		{"not present", "oauth-2025-04-20,interleaved-thinking-2025-05-14", "fast-mode-2026-02-01", false},
		{"with spaces", "oauth-2025-04-20, fast-mode-2026-02-01 , interleaved-thinking-2025-05-14", "fast-mode-2026-02-01", true},
		{"empty header", "", "fast-mode-2026-02-01", false},
		{"empty token", "fast-mode-2026-02-01", "", false},
		{"partial match", "fast-mode-2026-02-01-extra", "fast-mode-2026-02-01", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := containsBetaToken(tt.header, tt.token)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestStripBetaTokensWithSet_EmptyDropSet(t *testing.T) {
	header := "oauth-2025-04-20,interleaved-thinking-2025-05-14"
	got := stripBetaTokensWithSet(header, map[string]struct{}{})
	require.Equal(t, header, got)
}

func TestIsCountTokensUnsupported404(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		body       string
		want       bool
	}{
		{
			name:       "exact endpoint not found",
			statusCode: 404,
			body:       `{"error":{"message":"Not found: /v1/messages/count_tokens","type":"not_found_error"}}`,
			want:       true,
		},
		{
			name:       "contains count_tokens and not found",
			statusCode: 404,
			body:       `{"error":{"message":"count_tokens route not found","type":"not_found_error"}}`,
			want:       true,
		},
		{
			name:       "generic 404",
			statusCode: 404,
			body:       `{"error":{"message":"resource not found","type":"not_found_error"}}`,
			want:       false,
		},
		{
			name:       "404 with empty error message",
			statusCode: 404,
			body:       `{"error":{"message":"","type":"not_found_error"}}`,
			want:       false,
		},
		{
			name:       "non-404 status",
			statusCode: 400,
			body:       `{"error":{"message":"Not found: /v1/messages/count_tokens","type":"invalid_request_error"}}`,
			want:       false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isCountTokensUnsupported404(tt.statusCode, []byte(tt.body))
			require.Equal(t, tt.want, got)
		})
	}
}

// TestDefaultBetaPolicy_Context1M_Sonnet5Whitelist 验证默认策略下 context-1m-2025-08-07 的分模型行为：
//   - claude-sonnet-5 及后续版本：pass（放行），保留 1M 上下文能力
//   - 其他 sonnet 版本（4.x 及以下）、opus、haiku：filter（过滤），因为上游不支持
func TestDefaultBetaPolicy_Context1M_Sonnet5Whitelist(t *testing.T) {
	settings := DefaultBetaPolicySettings()

	// 找到 context-1m-2025-08-07 规则
	var rule *BetaPolicyRule
	for i := range settings.Rules {
		if settings.Rules[i].BetaToken == "context-1m-2025-08-07" {
			rule = &settings.Rules[i]
			break
		}
	}
	require.NotNil(t, rule, "default policy must include context-1m-2025-08-07 rule")
	require.Equal(t, BetaPolicyActionPass, rule.Action, "primary action for whitelisted models is pass")
	require.Equal(t, BetaPolicyActionFilter, rule.FallbackAction, "non-whitelisted models must be filtered")
	require.NotEmpty(t, rule.ModelWhitelist, "context-1m must be scoped to sonnet-5+ via whitelist")

	// 表驱动：模型 → 期望 action
	// 覆盖每种上游路径下的模型 ID 变形：直连 Anthropic API、Vertex AI（"@YYYYMMDD" 后缀）、
	// AWS Bedrock 跨区域推理（us./eu./apac./jp./au./us-gov./global./anthropic. 前缀）。
	cases := []struct {
		model      string
		wantAction string
		desc       string
	}{
		// —— 直连 Anthropic API —— sonnet-5 系列应放行
		{"claude-sonnet-5", BetaPolicyActionPass, "sonnet-5 canonical"},
		{"claude-sonnet-5-20260701", BetaPolicyActionPass, "sonnet-5 dated variant matches wildcard"},
		{"claude-sonnet-5-thinking", BetaPolicyActionPass, "sonnet-5 thinking variant matches wildcard"},
		// —— Vertex AI 归一化后的 sonnet-5 —— 也应放行
		{"claude-sonnet-5@20260701", BetaPolicyActionPass, "sonnet-5 Vertex-normalized dated form"},
		// —— AWS Bedrock 各跨区域前缀 sonnet-5 —— 也应放行
		{"us.anthropic.claude-sonnet-5-v1", BetaPolicyActionPass, "bedrock us. sonnet-5"},
		{"eu.anthropic.claude-sonnet-5-20260701-v1:0", BetaPolicyActionPass, "bedrock eu. sonnet-5 dated"},
		{"apac.anthropic.claude-sonnet-5-v1", BetaPolicyActionPass, "bedrock apac. sonnet-5"},
		{"jp.anthropic.claude-sonnet-5-v1", BetaPolicyActionPass, "bedrock jp. sonnet-5"},
		{"au.anthropic.claude-sonnet-5-v1", BetaPolicyActionPass, "bedrock au. sonnet-5"},
		{"us-gov.anthropic.claude-sonnet-5-v1", BetaPolicyActionPass, "bedrock us-gov. sonnet-5"},
		{"global.anthropic.claude-sonnet-5-v1", BetaPolicyActionPass, "bedrock global. sonnet-5"},
		{"anthropic.claude-sonnet-5-v1", BetaPolicyActionPass, "bedrock no-region sonnet-5"},

		// —— sonnet-4.x 及以下必须过滤 ——
		{"claude-sonnet-4-6", BetaPolicyActionFilter, "sonnet-4.6 must be filtered"},
		{"claude-sonnet-4-5-20250929", BetaPolicyActionFilter, "sonnet-4.5 dated must be filtered"},
		{"claude-sonnet-4", BetaPolicyActionFilter, "sonnet-4 must be filtered"},
		{"claude-sonnet-4-5@20250929", BetaPolicyActionFilter, "sonnet-4.5 Vertex format must be filtered"},
		{"us.anthropic.claude-sonnet-4-6", BetaPolicyActionFilter, "bedrock us. sonnet-4.6 must be filtered"},
		{"us.anthropic.claude-sonnet-4-5-20250929-v1:0", BetaPolicyActionFilter, "bedrock us. sonnet-4.5 must be filtered"},
		// —— Opus / Haiku 必须过滤（无 1M） ——
		{"claude-opus-4-8", BetaPolicyActionFilter, "opus must be filtered"},
		{"claude-opus-4-7", BetaPolicyActionFilter, "opus 4.7 must be filtered"},
		{"us.anthropic.claude-opus-4-8-v1", BetaPolicyActionFilter, "bedrock opus 4.8 must be filtered"},
		{"claude-haiku-4-5", BetaPolicyActionFilter, "haiku must be filtered"},
		{"us.anthropic.claude-haiku-4-5-20251001-v1:0", BetaPolicyActionFilter, "bedrock haiku must be filtered"},
		{"claude-3-5-sonnet-20241022", BetaPolicyActionFilter, "legacy sonnet 3.5 must be filtered"},
		// —— 特殊边界：不应把 "claude-sonnet-50" / "claude-sonnet-5.1" 之类意外命名误放行 ——
		{"claude-sonnet-50", BetaPolicyActionFilter, "must not over-match a hypothetical sonnet-50"},
	}

	for _, tc := range cases {
		t.Run(tc.model, func(t *testing.T) {
			action, _ := resolveRuleAction(*rule, tc.model)
			require.Equal(t, tc.wantAction, action, tc.desc)
		})
	}
}
