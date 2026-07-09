package service

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/Wei-Shaw/sub2api/internal/pkg/claude"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/tidwall/gjson"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

var claudeOAuthAllowedForwardBodyFields = map[string]struct{}{
	"model":                 {},
	"messages":              {},
	"system":                {},
	"tools":                 {},
	"tool_choice":           {},
	"metadata":              {},
	"max_tokens":            {},
	"thinking":              {},
	"temperature":           {},
	"context_management":    {},
	"context_hint":          {},
	"output_config":         {},
	"output_format":         {},
	"stream":                {},
	"stop_sequences":        {},
	"speed":                 {},
	"diagnostics":           {},
	"fallbacks":             {},
	"fallback_credit_token": {},
}

const defaultClaudeCodeTitleRemoteSessionID = "00000000-0000-4000-8000-000000000201"

func filterClaudeOAuthForwardBodyFields(body []byte) ([]byte, bool) {
	if len(body) == 0 {
		return body, false
	}
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return body, false
	}

	root := gjson.ParseBytes(body)
	var buf bytes.Buffer
	buf.WriteByte('{')
	changed := false
	first := true
	root.ForEach(func(key, value gjson.Result) bool {
		field := key.String()
		if _, ok := claudeOAuthAllowedForwardBodyFields[field]; !ok {
			changed = true
			return true
		}
		if !first {
			buf.WriteByte(',')
		}
		keyJSON, err := json.Marshal(field)
		if err != nil {
			changed = true
			return true
		}
		buf.Write(keyJSON)
		buf.WriteByte(':')
		buf.WriteString(value.Raw)
		first = false
		return true
	})
	if !changed {
		return body, false
	}
	buf.WriteByte('}')
	return buf.Bytes(), true
}

func stripAnthropicBetaBodyFields(body []byte) ([]byte, bool) {
	out := body
	modified := false
	if gjson.GetBytes(out, "betas").Exists() {
		if next, ok := deleteJSONPathBytes(out, "betas"); ok {
			out = next
			modified = true
		}
	}
	if gjson.GetBytes(out, "anthropic_beta").Exists() {
		if next, ok := deleteJSONPathBytes(out, "anthropic_beta"); ok {
			out = next
			modified = true
		}
	}
	return out, modified
}

func claudeCodeMimicFingerprint(fp *Fingerprint) *Fingerprint {
	if fp == nil {
		return nil
	}
	clone := *fp
	clone.UserAgent = claude.DefaultHeaders["User-Agent"]
	clone.StainlessLang = claude.DefaultHeaders["X-Stainless-Lang"]
	clone.StainlessPackageVersion = claude.DefaultHeaders["X-Stainless-Package-Version"]
	clone.StainlessOS = claude.DefaultHeaders["X-Stainless-OS"]
	clone.StainlessArch = claude.DefaultHeaders["X-Stainless-Arch"]
	clone.StainlessRuntime = claude.DefaultHeaders["X-Stainless-Runtime"]
	clone.StainlessRuntimeVersion = claude.DefaultHeaders["X-Stainless-Runtime-Version"]
	return &clone
}

func claudeCodeVersionForFingerprint(fp *Fingerprint) string {
	if fp != nil {
		if version := ExtractCLIVersion(fp.UserAgent); version != "" {
			return version
		}
	}
	return claude.CLICurrentVersion
}

func forceRewriteSystemForNonClaudeCodeWithPromptBlocks(body []byte, system any, expansionPrompt string, blocksConfig string) ([]byte, error) {
	out := rewriteSystemForNonClaudeCodeWithPromptBlocks(body, system, expansionPrompt, blocksConfig)
	if !classifyClaudeMessagesBody(out).isClaudeCodeFamily() {
		return body, fmt.Errorf("force Claude Code system rewrite failed")
	}
	return out, nil
}

func buildClaudeCodeDynamicSystemPrompt(body []byte) string {
	cwd, err := os.Getwd()
	if err != nil || strings.TrimSpace(cwd) == "" {
		cwd = "."
	}
	modelID := strings.TrimSpace(gjson.GetBytes(body, "model").String())
	if modelID == "" {
		modelID = "claude-sonnet-4-6"
	}

	lines := []string{
		"# Text output (does not apply to tool calls)",
		"Assume users can't see most tool calls or thinking — only your text output. Before your first tool call, state in one sentence what you're about to do. While working, give short updates at key moments: when you find something, when you change direction, or when you hit a blocker. Brief is good — silent is not. One sentence per update is almost always enough.",
		"",
		"Don't narrate your internal deliberation. User-facing text should be relevant communication to the user, not a running commentary on your thought process. State results and decisions directly, and focus user-facing text on relevant updates for the user.",
		"",
		"When you do write updates, write so the reader can pick up cold: complete sentences, no unexplained jargon or shorthand from earlier in the session. But keep it tight — a clear sentence is better than a clear paragraph.",
		"",
		"End-of-turn summary: one or two sentences. What changed and what's next. Nothing else.",
		"",
		"Match responses to the task: a simple question gets a direct answer, not headers and sections.",
		"",
		"In code: default to writing no comments. Never write multi-paragraph docstrings or multi-line comment blocks — one short line max. Don't create planning, decision, or analysis documents unless the user asks for them — work from conversation context, not intermediate files.",
		"",
		"# Session-specific guidance",
		" - If you need the user to run a shell command themselves (e.g., an interactive login like `gcloud auth login`), suggest they type `! <command>` in the prompt — the `!` prefix runs the command in this session so its output lands directly in the conversation.",
		" - Use the Agent tool with specialized agents when the task at hand matches the agent's description. Subagents are valuable for parallelizing independent queries or for protecting the main context window from excessive results, but they should not be used excessively when not needed. Importantly, avoid duplicating work that subagents are already doing - if you delegate research to a subagent, do not also perform the same searches yourself.",
		" - For broad codebase exploration or research that'll take more than 3 queries, spawn Agent with subagent_type=Explore. Otherwise use the Glob or Grep directly.",
		" - When the user types `/<skill-name>`, invoke it via Skill. Only use skills listed in the user-invocable skills section — don't guess.",
		"",
		"# auto memory",
		"",
		fmt.Sprintf("You have a persistent, file-based memory system at `%s`. This directory already exists — write to it directly with the Write tool (do not run mkdir or check for its existence).", claudeCodeMemoryDir(cwd)),
		"",
		"You should build up this memory system over time so that future conversations can have a complete picture of who the user is, how they'd like to collaborate with you, what behaviors to avoid or repeat, and the context behind the work the user gives you.",
		"",
		"If the user explicitly asks you to remember something, save it immediately as whichever type fits best. If they ask you to forget something, find and remove the relevant entry.",
		"",
		"## What NOT to save in memory",
		"",
		"- Code patterns, conventions, architecture, file paths, or project structure — these can be derived by reading the current project state.",
		"- Git history, recent changes, or who-changed-what — `git log` / `git blame` are authoritative.",
		"- Debugging solutions or fix recipes — the fix is in the code; the commit message has the context.",
		"- Anything already documented in CLAUDE.md files.",
		"- Ephemeral task details: in-progress work, temporary state, current conversation context.",
		"",
		"## When to access memories",
		"- When memories seem relevant, or the user references prior-conversation work.",
		"- You MUST access memory when the user explicitly asks you to check, recall, or remember.",
		"- If the user says to *ignore* or *not use* memory: Do not apply remembered facts, cite, compare against, or mention memory content.",
		"",
		"# Environment",
		"You have been invoked in the following environment: ",
		fmt.Sprintf(" - Primary working directory: %s", cwd),
		fmt.Sprintf(" - Platform: %s", claudeCodeTTYPlatform()),
		" - Shell: bash",
		" - OS Version: Windows 11 Enterprise LTSC 2024 10.0.26100",
		fmt.Sprintf(" - You are powered by the model named %s. The exact model ID is %s.", claudeCodeModelDisplayName(modelID), modelID),
		" - Assistant knowledge cutoff is August 2025.",
		" - The most recent Claude models are Fable 5 and the Claude 4.X family. Model IDs — Fable 5: 'claude-fable-5', Opus 4.8: 'claude-opus-4-8', Sonnet 4.6: 'claude-sonnet-4-6', Haiku 4.5: 'claude-haiku-4-5-20251001'. When building AI applications, default to the latest and most capable Claude models.",
		" - Claude Code is available as a CLI in the terminal, desktop app (Mac/Windows), web app (claude.ai/code), and IDE extensions (VS Code, JetBrains).",
		" - Fast mode for Claude Code uses Claude Opus with faster output (it does not downgrade to a smaller model). It can be toggled with /fast and is available on Opus 4.8/4.7/4.6.",
		"",
		"# Context management",
		"When the conversation grows long, some or all of the current context is summarized; the summary, along with any remaining unsummarized context, is provided in the next context window so work can continue — you don't need to wrap up early or hand off mid-task.",
		"",
		"When you have enough information to act, act. Do not re-derive facts already established in the conversation, re-litigate a decision the user has already made, or narrate options you will not pursue. If you are weighing a choice, give a recommendation, not an exhaustive survey",
	}
	return strings.Join(lines, "\n")
}

func claudeCodeTTYPlatform() string {
	if runtime.GOOS == "windows" {
		return "win32"
	}
	return runtime.GOOS
}

func claudeCodeModelDisplayName(modelID string) string {
	switch claude.NormalizeModelID(modelID) {
	case "claude-opus-4-8":
		return "Opus 4.8"
	case "claude-opus-4-7":
		return "Opus 4.7"
	case "claude-opus-4-6":
		return "Opus 4.6"
	case "claude-haiku-4-5-20251001":
		return "Haiku 4.5"
	default:
		return "Sonnet 4.6"
	}
}

func claudeCodeMemoryDir(cwd string) string {
	base := strings.TrimSpace(os.Getenv("CLAUDE_CONFIG_DIR"))
	if base == "" {
		if appData := strings.TrimSpace(os.Getenv("APPDATA")); appData != "" {
			base = filepath.Join(appData, "Claude")
		} else if home := strings.TrimSpace(os.Getenv("USERPROFILE")); home != "" {
			base = filepath.Join(home, ".claude")
		} else {
			base = ".claude"
		}
	}
	return filepath.Join(base, "projects", claudeCodeProjectKey(cwd), "memory")
}

func claudeCodeProjectKey(cwd string) string {
	volume := filepath.VolumeName(cwd)
	rest := strings.TrimPrefix(cwd, volume)
	parts := strings.FieldsFunc(rest, func(r rune) bool {
		return r == '\\' || r == '/'
	})
	var out []string
	if volume != "" {
		out = append(out, strings.TrimSuffix(volume, ":"))
	}
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	if len(out) == 0 {
		return "default"
	}
	return strings.Join(out, "-")
}

func claudeCodeBodyHasGlobalSystemCache(body []byte) bool {
	system := gjson.GetBytes(body, "system")
	if !system.IsArray() {
		return false
	}
	found := false
	system.ForEach(func(_, item gjson.Result) bool {
		if item.Get("cache_control.type").String() == "ephemeral" &&
			item.Get("cache_control.scope").String() == "global" {
			found = true
			return false
		}
		return true
	})
	return found
}

func claudeCodeBodyDrivenBetaTokens(modelID string, body []byte) []string {
	return claudeCodeBodyProfileBetaTokens(modelID, classifyClaudeMessagesBody(body))
}

func claudeCodeUserAgentForEntrypoint(entrypoint string) string {
	entrypoint = normalizeClaudeCodeBillingEntrypoint(entrypoint)
	return fmt.Sprintf("claude-cli/%s (external, %s)", claude.CLICurrentVersion, entrypoint)
}

func refineClaudeCodeMessagesProfileForHTTPRequest(profile claudeCodeBodyClassification, c *gin.Context, clientHeaders http.Header) claudeCodeBodyClassification {
	if profile.OfficialProfile != claudeCodeOfficialProfileCLITitle {
		return profile
	}
	if !claudeCodeHTTPRequestLooksLikeMessagesBeta(c) || !claudeCodeClientHeadersLookLikeCLI(clientHeaders) {
		profile.OfficialProfile = claudeCodeOfficialProfileUnknown
	}
	return profile
}

func refineClaudeCodeMessagesProfileForClientHeaders(profile claudeCodeBodyClassification, clientHeaders http.Header) claudeCodeBodyClassification {
	if profile.OfficialProfile != claudeCodeOfficialProfileCLITitle {
		return profile
	}
	if !claudeCodeClientHeadersLookLikeCLI(clientHeaders) {
		profile.OfficialProfile = claudeCodeOfficialProfileUnknown
	}
	return profile
}

func claudeCodeHTTPRequestLooksLikeMessagesBeta(c *gin.Context) bool {
	if c == nil || c.Request == nil || c.Request.URL == nil {
		return true
	}
	if c.Request.URL.Path != "/v1/messages" {
		return false
	}
	return strings.EqualFold(c.Request.URL.Query().Get("beta"), "true")
}

func claudeCodeClientHeadersLookLikeCLI(headers http.Header) bool {
	if headers == nil {
		return false
	}
	ua := strings.ToLower(strings.TrimSpace(getHeaderRaw(headers, "User-Agent")))
	return strings.HasPrefix(ua, "claude-cli/") && strings.Contains(ua, "(external, cli)")
}

func applyClaudeCodeFamilyHeaders(req *http.Request, profile claudeCodeBodyClassification, body []byte, clientHeaders http.Header) {
	if req == nil || !profile.isClaudeCodeFamily() {
		return
	}
	setHeaderRaw(req.Header, "User-Agent", claudeCodeUserAgentForEntrypoint(profile.BillingEntryPoint))
	applyClaudeCodePlatformHeaders(req, body, clientHeaders)
	if profile.OfficialProfile == claudeCodeOfficialProfileCLITitle {
		applyClaudeCodeTitleRemoteSessionHeader(req, clientHeaders)
	}
	ensureClaudeCodeRequestIDHeader(req)
}

func applyClaudeCodePlatformHeaders(req *http.Request, body []byte, clientHeaders http.Header) {
	if req == nil {
		return
	}
	setHeaderRaw(req.Header, "X-Stainless-OS", resolveClaudeCodeStainlessOS(body, clientHeaders))
	if getHeaderRaw(req.Header, "X-Stainless-Arch") == "" {
		setHeaderRaw(req.Header, "X-Stainless-Arch", claude.DefaultHeaders["X-Stainless-Arch"])
	}
}

func applyClaudeCodeTitleRemoteSessionHeader(req *http.Request, clientHeaders http.Header) {
	if req == nil {
		return
	}
	remoteSessionID := ""
	if clientHeaders != nil {
		remoteSessionID = strings.TrimSpace(getHeaderRaw(clientHeaders, "x-claude-remote-session-id"))
	}
	if remoteSessionID == "" {
		remoteSessionID = defaultClaudeCodeTitleRemoteSessionID
	}
	setHeaderRaw(req.Header, "x-claude-remote-session-id", remoteSessionID)
}

func resolveClaudeCodeStainlessOS(body []byte, clientHeaders http.Header) string {
	if clientHeaders != nil {
		if osName := normalizeClaudeCodeStainlessOS(getHeaderRaw(clientHeaders, "X-Stainless-OS")); osName != "" {
			return osName
		}
	}
	if osName := detectClaudeCodeStainlessOSFromBody(body); osName != "" {
		return osName
	}
	if fallback := strings.TrimSpace(claude.DefaultHeaders["X-Stainless-OS"]); fallback != "" {
		return fallback
	}
	return "Linux"
}

func detectClaudeCodeStainlessOSFromBody(body []byte) string {
	system2 := strings.ToLower(claudeCodeSystemTextAt(body, 2))
	switch {
	case strings.Contains(system2, "platform: win32"),
		strings.Contains(system2, "platform: windows"),
		strings.Contains(system2, "os version: windows"):
		return "Windows"
	case strings.Contains(system2, "platform: linux"),
		strings.Contains(system2, "os version: linux"),
		strings.Contains(system2, "os version: ubuntu"),
		strings.Contains(system2, "os version: debian"):
		return "Linux"
	default:
		return ""
	}
}

func normalizeClaudeCodeStainlessOS(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "windows", "win32":
		return "Windows"
	case "linux":
		return "Linux"
	default:
		return ""
	}
}

func ensureClaudeCodeRequestIDHeader(req *http.Request) {
	if req == nil {
		return
	}
	// Real Claude CLI 每个请求都会生成一个新的 UUID 放在 x-client-request-id。
	// 上游会以此作为会话/请求指纹的一部分，缺失或重复都可能触发第三方判定。
	if getHeaderRaw(req.Header, "x-client-request-id") == "" {
		setHeaderRaw(req.Header, "x-client-request-id", uuid.NewString())
	}
}

func syncClaudeCodeSessionIDHeader(req *http.Request, body []byte, force bool) {
	if req == nil {
		return
	}
	if !force && getHeaderRaw(req.Header, "X-Claude-Code-Session-Id") == "" {
		return
	}
	uid := gjson.GetBytes(body, "metadata.user_id").String()
	if uid == "" {
		return
	}
	parsed := ParseMetadataUserID(uid)
	if parsed == nil || parsed.SessionID == "" {
		return
	}
	setHeaderRaw(req.Header, "X-Claude-Code-Session-Id", parsed.SessionID)
}

func claudeErrorTypeForStatus(status int) string {
	switch status {
	case http.StatusBadRequest:
		return "invalid_request_error"
	case http.StatusUnauthorized:
		return "authentication_error"
	case http.StatusForbidden:
		return "permission_error"
	case http.StatusNotFound:
		return "not_found_error"
	case http.StatusTooManyRequests:
		return "rate_limit_error"
	default:
		return "api_error"
	}
}

func (s *GatewayService) countTokensApplicationError(c *gin.Context, err error) bool {
	var appErr *infraerrors.ApplicationError
	if !errors.As(err, &appErr) {
		return false
	}
	status := int(appErr.Code)
	if status <= 0 {
		status = http.StatusInternalServerError
	}
	message := strings.TrimSpace(appErr.Message)
	if message == "" {
		message = "Request failed"
	}
	s.countTokensError(c, status, claudeErrorTypeForStatus(status), message)
	return true
}
