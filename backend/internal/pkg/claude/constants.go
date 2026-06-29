// Package claude provides constants and helpers for Claude API integration.
package claude

import "strings"

// Claude Code 客户端相关常量

// Beta header 常量
//
// 这里的常量对齐真实 Claude Code 2.1.191 交互 TTY 流量（截至 2026-06）。
// 选型参考：本机 Claude Code 2.1.191 交互 TTY 抓包，
// 原因：Anthropic 上游会基于 anthropic-beta 的完整集合判定请求来源；
// 缺少任何"官方 Claude Code 请求才会带"的 beta，都会被降级到第三方额度，
// 对应报错：`Third-party apps now draw from your extra usage, not your plan limits.`
const (
	BetaOAuth                    = "oauth-2025-04-20"
	BetaClaudeCode               = "claude-code-20250219"
	BetaInterleavedThinking      = "interleaved-thinking-2025-05-14"
	BetaFineGrainedToolStreaming = "fine-grained-tool-streaming-2025-05-14"
	BetaTokenCounting            = "token-counting-2024-11-01"
	BetaContext1M                = "context-1m-2025-08-07"
	BetaFastMode                 = "fast-mode-2026-02-01"

	// 新增（对齐官方 CLI 2.1.9x 以来的流量）
	BetaPromptCachingScope = "prompt-caching-scope-2026-01-05"
	BetaEffort             = "effort-2025-11-24"
	BetaRedactThinking     = "redact-thinking-2026-02-12"
	BetaThinkingTokenCount = "thinking-token-count-2026-05-13"
	BetaContextManagement  = "context-management-2025-06-27"
	BetaExtendedCacheTTL   = "extended-cache-ttl-2025-04-11"
	BetaAdvancedToolUse    = "advanced-tool-use-2025-11-20"
	BetaMidConversation    = "mid-conversation-system-2026-04-07"
	BetaStructuredOutputs  = "structured-outputs-2025-12-15"
)

// DroppedBetas 是转发时需要从 anthropic-beta header 中移除的 beta token 列表。
// 这些 token 是客户端特有的，不应透传给上游 API。
var DroppedBetas = []string{}

// DefaultBetaHeader Claude Code 客户端默认的 anthropic-beta header
const DefaultBetaHeader = BetaClaudeCode + "," + BetaInterleavedThinking + "," + BetaRedactThinking + "," + BetaThinkingTokenCount + "," + BetaContextManagement + "," + BetaPromptCachingScope + "," + BetaAdvancedToolUse + "," + BetaEffort

// MessageBetaHeaderNoTools /v1/messages 在无工具时的 beta header
//
// NOTE: Claude Code OAuth credentials are scoped to Claude Code. When we "mimic"
// Claude Code for non-Claude-Code clients, we must include the claude-code beta
// even if the request doesn't use tools, otherwise upstream may reject the
// request as a non-Claude-Code API request.
const MessageBetaHeaderNoTools = DefaultBetaHeader

// MessageBetaHeaderWithTools /v1/messages 在有工具时的 beta header
const MessageBetaHeaderWithTools = DefaultBetaHeader

// CountTokensBetaHeader count_tokens 请求使用的基础 anthropic-beta header。
// 真实路径会在模型 main beta 基础上追加 token-counting；该常量保留给非模型化旧路径。
const CountTokensBetaHeader = DefaultBetaHeader + "," + BetaTokenCounting

// HaikuBetaHeader Haiku 模型使用的 anthropic-beta header。
//
// TTY 抓包中 Haiku main 请求的顺序不同于 Sonnet/Opus：claude-code 位于
// prompt-caching-scope 之后，且没有 effort beta。
const HaikuBetaHeader = BetaInterleavedThinking + "," + BetaRedactThinking + "," + BetaThinkingTokenCount + "," + BetaContextManagement + "," + BetaPromptCachingScope + "," + BetaClaudeCode + "," + BetaAdvancedToolUse

// TitleBetaHeader Claude Code TTY title query 使用的 anthropic-beta header。
//
// TTY 抓包中 title query 使用 Haiku 线模型生成标题，但 beta 集合不是 Haiku main：
// 它不携带 claude-code，也没有 advanced-tool-use/effort，只携带 structured outputs
// 及 thinking/context/prompt-cache 相关能力位。
const TitleBetaHeader = BetaInterleavedThinking + "," + BetaRedactThinking + "," + BetaThinkingTokenCount + "," + BetaContextManagement + "," + BetaPromptCachingScope + "," + BetaStructuredOutputs

// APIKeyBetaHeader API-key 账号建议使用的 anthropic-beta header。
const APIKeyBetaHeader = DefaultBetaHeader

// APIKeyHaikuBetaHeader Haiku 模型在 API-key 账号下使用的 anthropic-beta header。
const APIKeyHaikuBetaHeader = HaikuBetaHeader

// DefaultCacheControlTTL 是旧版代理默认 cache_control ttl。
// TTY 兼容路径不应默认写 ttl；该常量仅为兼容旧配置/旧测试路径保留。
const DefaultCacheControlTTL = "5m"

// CLICurrentVersion 是 sub2api 当前对外伪装的 Claude Code CLI 版本号（三段 semver）。
// 用于 billing attribution block 中的 cc_version=X.Y.Z.{fp} 前缀以及 fingerprint 计算。
// 必须与 DefaultHeaders["User-Agent"] 中的版本号严格一致；不一致会被 Anthropic 判第三方。
const CLICurrentVersion = "2.1.191"

// FullClaudeCodeMimicryBetas 返回最像真实 Claude Code 交互 TTY 主请求的完整 beta 列表，
// 用于 OAuth 账号伪装成 Claude Code 时使用。
// 顺序与真实 CLI 抓包一致。
//
// 使用建议：
//   - OAuth 账号 + 非 haiku：追加这整份列表，再按需保留 client 带来的 beta。
//   - OAuth 账号 + haiku：使用 HaikuBetaHeader。
//   - API-key 账号：不要使用本函数，参见 APIKeyBetaHeader。
func FullClaudeCodeMimicryBetas() []string {
	return []string{
		BetaClaudeCode,
		BetaInterleavedThinking,
		BetaRedactThinking,
		BetaThinkingTokenCount,
		BetaContextManagement,
		BetaPromptCachingScope,
		BetaAdvancedToolUse,
		BetaEffort,
	}
}

// ClaudeCodeTitleBetas 返回 TTY title query 的固定 beta 顺序。
func ClaudeCodeTitleBetas() []string {
	return []string{
		BetaInterleavedThinking,
		BetaRedactThinking,
		BetaThinkingTokenCount,
		BetaContextManagement,
		BetaPromptCachingScope,
		BetaStructuredOutputs,
	}
}

// ClaudeCodeMainBetasForModel 返回 TTY main 请求的模型分支 beta 列表。
func ClaudeCodeMainBetasForModel(modelID string) []string {
	lower := strings.ToLower(modelID)
	if strings.Contains(lower, "haiku") {
		return []string{
			BetaInterleavedThinking,
			BetaRedactThinking,
			BetaThinkingTokenCount,
			BetaContextManagement,
			BetaPromptCachingScope,
			BetaClaudeCode,
			BetaAdvancedToolUse,
		}
	}

	if strings.Contains(lower, "opus-4-8") || strings.Contains(lower, "opus-4.8") {
		return []string{
			BetaClaudeCode,
			BetaInterleavedThinking,
			BetaRedactThinking,
			BetaThinkingTokenCount,
			BetaContextManagement,
			BetaPromptCachingScope,
			BetaMidConversation,
			BetaAdvancedToolUse,
			BetaEffort,
		}
	}
	return FullClaudeCodeMimicryBetas()
}

// ClaudeCodeMainBetaHeaderForModel 返回 TTY main 请求的 anthropic-beta header。
func ClaudeCodeMainBetaHeaderForModel(modelID string) string {
	return strings.Join(ClaudeCodeMainBetasForModel(modelID), ",")
}

// DefaultHeaders 是 Claude Code 客户端默认请求头。
var DefaultHeaders = map[string]string{
	// Keep these in sync with recent Claude CLI traffic to reduce the chance
	// that Claude Code-scoped OAuth credentials are rejected as "non-CLI" usage.
	"User-Agent":                                "claude-cli/" + CLICurrentVersion + " (external, cli)",
	"X-Stainless-Lang":                          "js",
	"X-Stainless-Package-Version":               "0.94.0",
	"X-Stainless-OS":                            "Windows",
	"X-Stainless-Arch":                          "x64",
	"X-Stainless-Runtime":                       "node",
	"X-Stainless-Runtime-Version":               "v26.3.0",
	"X-Stainless-Retry-Count":                   "0",
	"X-Stainless-Timeout":                       "600",
	"X-App":                                     "cli",
	"Anthropic-Dangerous-Direct-Browser-Access": "true",
}

// Model 表示一个 Claude 模型
type Model struct {
	ID          string `json:"id"`
	Type        string `json:"type"`
	DisplayName string `json:"display_name"`
	CreatedAt   string `json:"created_at"`
}

// DefaultModels Claude Code 客户端支持的默认模型列表
var DefaultModels = []Model{
	{
		ID:          "claude-fable-5",
		Type:        "model",
		DisplayName: "Claude Fable 5",
		CreatedAt:   "2026-06-09T00:00:00Z",
	},
	{
		ID:          "claude-opus-4-5-20251101",
		Type:        "model",
		DisplayName: "Claude Opus 4.5",
		CreatedAt:   "2025-11-01T00:00:00Z",
	},
	{
		ID:          "claude-opus-4-6",
		Type:        "model",
		DisplayName: "Claude Opus 4.6",
		CreatedAt:   "2026-02-06T00:00:00Z",
	},
	{
		ID:          "claude-opus-4-7",
		Type:        "model",
		DisplayName: "Claude Opus 4.7",
		CreatedAt:   "2026-04-17T00:00:00Z",
	},
	{
		ID:          "claude-opus-4-8",
		Type:        "model",
		DisplayName: "Claude Opus 4.8",
		CreatedAt:   "2026-05-29T00:00:00Z",
	},
	{
		ID:          "claude-sonnet-4-6",
		Type:        "model",
		DisplayName: "Claude Sonnet 4.6",
		CreatedAt:   "2026-02-18T00:00:00Z",
	},
	{
		ID:          "claude-sonnet-4-5-20250929",
		Type:        "model",
		DisplayName: "Claude Sonnet 4.5",
		CreatedAt:   "2025-09-29T00:00:00Z",
	},
	{
		ID:          "claude-haiku-4-5-20251001",
		Type:        "model",
		DisplayName: "Claude Haiku 4.5",
		CreatedAt:   "2025-10-01T00:00:00Z",
	},
}

// DefaultModelIDs 返回默认模型的 ID 列表
func DefaultModelIDs() []string {
	ids := make([]string, len(DefaultModels))
	for i, m := range DefaultModels {
		ids[i] = m.ID
	}
	return ids
}

// DefaultTestModel 测试时使用的默认模型
const DefaultTestModel = "claude-sonnet-4-5-20250929"

// ModelIDOverrides Claude OAuth 请求需要的模型 ID 映射
var ModelIDOverrides = map[string]string{
	"claude-sonnet-4-5": "claude-sonnet-4-5-20250929",
	"claude-opus-4-5":   "claude-opus-4-5-20251101",
	"claude-haiku-4-5":  "claude-haiku-4-5-20251001",
}

// ModelIDReverseOverrides 用于将上游模型 ID 还原为短名
var ModelIDReverseOverrides = map[string]string{
	"claude-sonnet-4-5-20250929": "claude-sonnet-4-5",
	"claude-opus-4-5-20251101":   "claude-opus-4-5",
	"claude-haiku-4-5-20251001":  "claude-haiku-4-5",
}

// NormalizeModelID 根据 Claude OAuth 规则映射模型
func NormalizeModelID(id string) string {
	if id == "" {
		return id
	}
	if mapped, ok := ModelIDOverrides[id]; ok {
		return mapped
	}
	return id
}

// DenormalizeModelID 将上游模型 ID 转换为短名
func DenormalizeModelID(id string) string {
	if id == "" {
		return id
	}
	if mapped, ok := ModelIDReverseOverrides[id]; ok {
		return mapped
	}
	return id
}
