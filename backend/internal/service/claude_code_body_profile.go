package service

import (
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/claude"
	"github.com/tidwall/gjson"
)

type claudeCodeBodyProfile string

const (
	claudeCodeBodyProfileGeneric    claudeCodeBodyProfile = "generic_anthropic_messages"
	claudeCodeBodyProfileMainTTY    claudeCodeBodyProfile = "cc_main_tty"
	claudeCodeBodyProfileMainSDK    claudeCodeBodyProfile = "cc_main_sdk"
	claudeCodeBodyProfileTitle      claudeCodeBodyProfile = "cc_title_structured"
	claudeCodeBodyProfileSideQuery  claudeCodeBodyProfile = "cc_side_query"
	claudeCodeBodyProfileStructured claudeCodeBodyProfile = "cc_structured_one_shot"
	claudeCodeBodyProfileForkedMain claudeCodeBodyProfile = "cc_forked_main_derived"
	claudeCodeBodyProfileProbe      claudeCodeBodyProfile = "cc_probe_or_maintenance"
)

type claudeCodeOfficialProfile string

const (
	claudeCodeOfficialProfileUnknown  claudeCodeOfficialProfile = ""
	claudeCodeOfficialProfileCLITitle claudeCodeOfficialProfile = "cli-title"
	claudeCodeOfficialProfileCLIMain  claudeCodeOfficialProfile = "cli-main"
)

type claudeCodeSystemProfile string

const (
	claudeCodeSystemProfileUnknown            claudeCodeSystemProfile = ""
	claudeCodeSystemProfileCLITitle           claudeCodeSystemProfile = "cli-title"
	claudeCodeSystemProfileCLIMainDefault     claudeCodeSystemProfile = "cli-main-default"
	claudeCodeSystemProfileCLIMainAppend      claudeCodeSystemProfile = "cli-main-append-system"
	claudeCodeSystemProfileCLIMainReplace     claudeCodeSystemProfile = "cli-main-replace-system"
	claudeCodeSystemProfileCLIMainBare        claudeCodeSystemProfile = "cli-main-bare"
	claudeCodeSystemProfileCLIMainSafe        claudeCodeSystemProfile = "cli-main-safe"
	claudeCodeSystemProfileCLIAgentSubrequest claudeCodeSystemProfile = "cli-agent-subrequest"
)

const (
	claudeCodeReminderDeferredTools = "deferred_tools"
	claudeCodeReminderAgentTypes    = "agent_types"
	claudeCodeReminderSkills        = "skills"
	claudeCodeReminderContext       = "context"
	claudeCodeReminderUnknown       = "unknown"
)

type claudeCodeBodyClassification struct {
	Profile              claudeCodeBodyProfile
	OfficialProfile      claudeCodeOfficialProfile
	SystemProfile        claudeCodeSystemProfile
	HasBilling           bool
	BillingEntryPoint    string
	HasIdentity          bool
	HasStructuredOutput  bool
	HasTitleSchema       bool
	HasContextManagement bool
	HasEffort            bool
	HasGlobalSystemCache bool
	HasThinking          bool
	ThinkingType         string
	HasToolsField        bool
	ToolsCount           int
	HasToolSearch        bool
	LeadingReminderTypes []string
	HasToolChoice        bool
	HasStopSequences     bool
	MaxTokens            int64
	BodyBetaTokens       []string
}

func (c claudeCodeBodyClassification) isClaudeCodeFamily() bool {
	return c.HasBilling
}

func (c claudeCodeBodyClassification) isMainProfile() bool {
	return c.Profile == claudeCodeBodyProfileMainTTY ||
		c.Profile == claudeCodeBodyProfileMainSDK ||
		c.Profile == claudeCodeBodyProfileForkedMain
}

func classifyClaudeMessagesBody(body []byte) claudeCodeBodyClassification {
	c := claudeCodeBodyClassification{
		Profile:        claudeCodeBodyProfileGeneric,
		BodyBetaTokens: claudeCodeBodyBetaTokensFromBody(body),
	}
	if len(body) == 0 {
		return c
	}

	c.HasStructuredOutput = gjson.GetBytes(body, "output_config.format").Exists() ||
		gjson.GetBytes(body, "output_format").Exists()
	c.HasTitleSchema = claudeCodeBodyHasTitleSchema(body)
	c.HasContextManagement = gjson.GetBytes(body, "context_management").Exists()
	c.HasEffort = gjson.GetBytes(body, "output_config.effort").Exists()
	c.HasGlobalSystemCache = claudeCodeBodyHasGlobalSystemCache(body)
	c.HasThinking = gjson.GetBytes(body, "thinking").Exists()
	c.ThinkingType = strings.ToLower(strings.TrimSpace(gjson.GetBytes(body, "thinking.type").String()))
	c.HasToolChoice = gjson.GetBytes(body, "tool_choice").Exists()
	c.HasStopSequences = gjson.GetBytes(body, "stop_sequences").Exists()
	c.MaxTokens = gjson.GetBytes(body, "max_tokens").Int()

	if tools := gjson.GetBytes(body, "tools"); tools.Exists() {
		c.HasToolsField = true
		if tools.IsArray() {
			c.ToolsCount = len(tools.Array())
			c.HasToolSearch = claudeCodeBodyHasToolNamed(tools, "ToolSearch")
		}
	}
	c.LeadingReminderTypes = claudeCodeBodyLeadingReminderTypes(body)

	c.HasBilling, c.BillingEntryPoint = claudeCodeBillingFromSystem0(body)
	c.HasIdentity = claudeCodeBodyHasIdentityBlock(body)
	if !c.HasBilling {
		if c.MaxTokens == 1 {
			c.Profile = claudeCodeBodyProfileProbe
		}
		return c
	}

	entrypoint := strings.ToLower(c.BillingEntryPoint)
	switch {
	case c.MaxTokens == 1:
		c.Profile = claudeCodeBodyProfileProbe
	case c.HasStructuredOutput && c.HasTitleSchema:
		c.Profile = claudeCodeBodyProfileTitle
	case c.HasStructuredOutput && c.ThinkingType == "disabled" && (!c.HasToolsField || c.ToolsCount == 0):
		c.Profile = claudeCodeBodyProfileStructured
	case strings.Contains(entrypoint, "sdk"):
		c.Profile = claudeCodeBodyProfileMainSDK
	case c.HasContextManagement || c.HasGlobalSystemCache || c.HasEffort || c.MaxTokens >= 32000:
		c.Profile = claudeCodeBodyProfileMainTTY
	case c.HasStopSequences || c.HasToolChoice || (!c.HasIdentity && c.MaxTokens > 0):
		c.Profile = claudeCodeBodyProfileSideQuery
	case c.HasIdentity:
		c.Profile = claudeCodeBodyProfileMainTTY
	default:
		c.Profile = claudeCodeBodyProfileSideQuery
	}
	c.OfficialProfile = classifyClaudeCodeOfficialProfile(body, c)
	c.SystemProfile = classifyClaudeCodeSystemProfile(body, c)

	return c
}

func classifyClaudeCodeOfficialProfile(body []byte, c claudeCodeBodyClassification) claudeCodeOfficialProfile {
	if !c.isClaudeCodeFamily() {
		return claudeCodeOfficialProfileUnknown
	}
	entrypoint := strings.ToLower(strings.TrimSpace(c.BillingEntryPoint))
	if entrypoint != "cli" {
		return claudeCodeOfficialProfileUnknown
	}
	if claudeCodeBodyLooksLikeCLITitle(body, c) {
		return claudeCodeOfficialProfileCLITitle
	}
	if c.Profile != claudeCodeBodyProfileMainTTY ||
		!c.HasIdentity ||
		!c.HasToolsField ||
		c.ThinkingType != "adaptive" {
		return claudeCodeOfficialProfileUnknown
	}
	if !claudeCodeCLIMainHasRecognizedReminderShape(c) {
		return claudeCodeOfficialProfileUnknown
	}
	return claudeCodeOfficialProfileCLIMain
}

func claudeCodeBodyLooksLikeCLITitle(body []byte, c claudeCodeBodyClassification) bool {
	if c.Profile != claudeCodeBodyProfileTitle ||
		!c.HasTitleSchema ||
		c.ToolsCount != 0 {
		return false
	}
	if c.HasThinking && c.ThinkingType != "disabled" {
		return false
	}
	if strings.TrimSpace(gjson.GetBytes(body, "output_config.format.type").String()) != "json_schema" {
		return false
	}
	if !claudeCodeTitleSchemaRequiresTitle(body) {
		return false
	}
	if strings.TrimSpace(claudeCodeSystemTextAt(body, 1)) != claudeCodeSystemPrompt {
		return false
	}
	system2 := strings.TrimSpace(claudeCodeSystemTextAt(body, 2))
	if !strings.HasPrefix(system2, "Generate a concise, sentence-case title") {
		return false
	}
	return claudeCodeFirstUserTextHasSession(body)
}

func claudeCodeTitleSchemaRequiresTitle(body []byte) bool {
	required := gjson.GetBytes(body, "output_config.format.schema.required")
	if !required.IsArray() {
		return false
	}
	found := false
	required.ForEach(func(_, item gjson.Result) bool {
		if item.Type == gjson.String && item.String() == "title" {
			found = true
			return false
		}
		return true
	})
	return found
}

func claudeCodeFirstUserTextHasSession(body []byte) bool {
	text := claudeCodeFirstUserText(body)
	return strings.Contains(text, "<session>") && strings.Contains(text, "</session>")
}

func claudeCodeFirstUserText(body []byte) string {
	messages := gjson.GetBytes(body, "messages")
	if !messages.IsArray() {
		return ""
	}
	text := ""
	messages.ForEach(func(_, msg gjson.Result) bool {
		if msg.Get("role").String() != "user" {
			return true
		}
		content := msg.Get("content")
		if content.Type == gjson.String {
			text = content.String()
			return false
		}
		if !content.IsArray() {
			return false
		}
		content.ForEach(func(_, block gjson.Result) bool {
			if block.Get("type").String() == "text" {
				text = block.Get("text").String()
				return false
			}
			return true
		})
		return false
	})
	return text
}

func claudeCodeCLIMainHasRecognizedReminderShape(c claudeCodeBodyClassification) bool {
	if c.HasToolSearch {
		if claudeCodeReminderPrefixMatches(c.LeadingReminderTypes,
			[]string{claudeCodeReminderDeferredTools, claudeCodeReminderAgentTypes, claudeCodeReminderSkills, claudeCodeReminderContext},
		) {
			return true
		}
		return false
	}
	if claudeCodeReminderPrefixMatches(c.LeadingReminderTypes,
		[]string{claudeCodeReminderAgentTypes, claudeCodeReminderSkills, claudeCodeReminderContext},
	) {
		return true
	}
	if claudeCodeReminderPrefixMatches(c.LeadingReminderTypes,
		[]string{claudeCodeReminderSkills, claudeCodeReminderContext},
	) {
		return true
	}
	return claudeCodeReminderPrefixMatches(c.LeadingReminderTypes, []string{claudeCodeReminderContext})
}

func claudeCodeCLIMainHasStandardRendererReminderShape(c claudeCodeBodyClassification) bool {
	if c.HasToolSearch {
		return claudeCodeReminderPrefixMatches(c.LeadingReminderTypes,
			[]string{claudeCodeReminderDeferredTools, claudeCodeReminderAgentTypes, claudeCodeReminderSkills, claudeCodeReminderContext},
		)
	}
	return claudeCodeReminderPrefixMatches(c.LeadingReminderTypes,
		[]string{claudeCodeReminderAgentTypes, claudeCodeReminderSkills, claudeCodeReminderContext},
	)
}

func classifyClaudeCodeSystemProfile(body []byte, c claudeCodeBodyClassification) claudeCodeSystemProfile {
	if !c.isClaudeCodeFamily() {
		return claudeCodeSystemProfileUnknown
	}
	if c.Profile == claudeCodeBodyProfileTitle {
		return claudeCodeSystemProfileCLITitle
	}
	entrypoint := strings.ToLower(strings.TrimSpace(c.BillingEntryPoint))
	if entrypoint != "cli" {
		return claudeCodeSystemProfileUnknown
	}

	system0 := claudeCodeSystemTextAt(body, 0)
	system2 := strings.TrimSpace(claudeCodeSystemTextAt(body, 2))
	switch {
	case strings.Contains(system0, "cc_is_subagent=true") ||
		strings.HasPrefix(system2, "You are an agent for Claude Code"):
		return claudeCodeSystemProfileCLIAgentSubrequest
	case strings.HasPrefix(system2, "CWD:"):
		return claudeCodeSystemProfileCLIMainBare
	case strings.HasPrefix(system2, "Generate a concise, sentence-case title"):
		return claudeCodeSystemProfileCLITitle
	case strings.HasPrefix(system2, "You are an interactive agent that helps users with software engineering tasks."):
		if claudeCodeMainSystemHasAppendBlock(system2) {
			return claudeCodeSystemProfileCLIMainAppend
		}
		if !strings.Contains(system2, "# auto memory") && strings.Contains(system2, "# Environment") {
			return claudeCodeSystemProfileCLIMainSafe
		}
		return claudeCodeSystemProfileCLIMainDefault
	case strings.Contains(system2, "gitStatus:") || strings.Contains(system2, "CUSTOM_"):
		return claudeCodeSystemProfileCLIMainReplace
	case c.OfficialProfile == claudeCodeOfficialProfileCLIMain:
		return claudeCodeSystemProfileCLIMainDefault
	default:
		return claudeCodeSystemProfileUnknown
	}
}

func claudeCodeSystemTextAt(body []byte, index int) string {
	system := gjson.GetBytes(body, "system")
	if !system.IsArray() {
		return ""
	}
	items := system.Array()
	if index < 0 || index >= len(items) {
		return ""
	}
	text := items[index].Get("text")
	if !text.Exists() || text.Type != gjson.String {
		return ""
	}
	return text.String()
}

func claudeCodeMainSystemHasAppendBlock(text string) bool {
	anchor := "not an exhaustive survey"
	anchorIndex := strings.Index(text, anchor)
	if anchorIndex < 0 {
		return false
	}
	afterAnchor := text[anchorIndex+len(anchor):]
	gitIndex := strings.Index(afterAnchor, "\ngitStatus:")
	if gitIndex < 0 {
		return false
	}
	return strings.TrimSpace(afterAnchor[:gitIndex]) != ""
}

func claudeCodeReminderPrefixMatches(got, want []string) bool {
	if len(got) < len(want) {
		return false
	}
	for i := range want {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func claudeCodeBodyHasToolNamed(tools gjson.Result, name string) bool {
	found := false
	tools.ForEach(func(_, item gjson.Result) bool {
		if item.Get("name").String() == name {
			found = true
			return false
		}
		return true
	})
	return found
}

func claudeCodeBodyLeadingReminderTypes(body []byte) []string {
	var out []string
	messages := gjson.GetBytes(body, "messages")
	if !messages.IsArray() {
		return out
	}
	foundUser := false
	messages.ForEach(func(_, msg gjson.Result) bool {
		if foundUser || msg.Get("role").String() != "user" {
			return true
		}
		foundUser = true
		content := msg.Get("content")
		if content.Type == gjson.String {
			text := content.String()
			if isClaudeCodeMetaUserText(text) {
				out = append(out, claudeCodeReminderType(text))
			}
			return false
		}
		if !content.IsArray() {
			return false
		}
		content.ForEach(func(_, block gjson.Result) bool {
			if block.Get("type").String() != "text" {
				return false
			}
			text := block.Get("text").String()
			if !isClaudeCodeMetaUserText(text) {
				return false
			}
			out = append(out, claudeCodeReminderType(text))
			return true
		})
		return false
	})
	return out
}

func claudeCodeReminderType(text string) string {
	switch {
	case strings.Contains(text, "The following deferred tools are now available via ToolSearch"):
		return claudeCodeReminderDeferredTools
	case strings.Contains(text, "Available agent types for the Agent tool:"):
		return claudeCodeReminderAgentTypes
	case strings.Contains(text, "The following skills are available for use with the Skill tool:"):
		return claudeCodeReminderSkills
	case strings.Contains(text, "As you answer the user's questions, you can use the following context:"):
		return claudeCodeReminderContext
	default:
		return claudeCodeReminderUnknown
	}
}

func claudeCodeBillingFromSystem0(body []byte) (bool, string) {
	system := gjson.GetBytes(body, "system")
	if !system.IsArray() {
		return false, ""
	}
	items := system.Array()
	if len(items) == 0 {
		return false, ""
	}
	text := items[0].Get("text")
	if !text.Exists() || text.Type != gjson.String {
		return false, ""
	}
	entrypoint, _, ok := parseClaudeCodeBillingAttributionText(text.String())
	return ok, entrypoint
}

func claudeCodeBodyHasIdentityBlock(body []byte) bool {
	system := gjson.GetBytes(body, "system")
	if !system.IsArray() {
		return false
	}
	items := system.Array()
	if len(items) < 2 {
		return false
	}
	text := items[1].Get("text")
	if !text.Exists() || text.Type != gjson.String {
		return false
	}
	trimmed := strings.TrimSpace(text.String())
	if hasClaudeCodePrefix(trimmed) {
		return true
	}
	for _, prompt := range claudeCodeSystemPrompts {
		if strings.HasPrefix(trimmed, prompt) {
			return true
		}
	}
	return false
}

func claudeCodeBodyHasTitleSchema(body []byte) bool {
	for _, path := range []string{
		"output_config.format.schema.properties.title",
		"output_config.format.properties.title",
		"output_format.schema.properties.title",
		"output_format.properties.title",
	} {
		if gjson.GetBytes(body, path).Exists() {
			return true
		}
	}
	return false
}

func claudeCodeBodyBetaTokensFromBody(body []byte) []string {
	var out []string
	add := func(token string) {
		token = strings.TrimSpace(token)
		if token == "" {
			return
		}
		for _, existing := range out {
			if existing == token {
				return
			}
		}
		out = append(out, token)
	}

	for _, field := range []string{"betas", "anthropic_beta"} {
		value := gjson.GetBytes(body, field)
		switch {
		case value.Type == gjson.String:
			for _, part := range strings.Split(value.String(), ",") {
				add(part)
			}
		case value.IsArray():
			value.ForEach(func(_, item gjson.Result) bool {
				if item.Type == gjson.String {
					for _, part := range strings.Split(item.String(), ",") {
						add(part)
					}
				}
				return true
			})
		}
	}
	return out
}

func claudeCodeBodyProfileBetaTokens(modelID string, c claudeCodeBodyClassification) []string {
	tokens := make([]string, 0, 10)
	add := func(token string) {
		token = strings.TrimSpace(token)
		if token == "" {
			return
		}
		for _, existing := range tokens {
			if existing == token {
				return
			}
		}
		tokens = append(tokens, token)
	}

	if c.isMainProfile() {
		mainBetas := claude.ClaudeCodeMainBetasForModel(modelID)
		if c.OfficialProfile == claudeCodeOfficialProfileCLIMain && !c.HasToolSearch {
			mainBetas = claude.ClaudeCodeMainToolSearchOffBetasForModel(modelID)
		}
		for _, token := range mainBetas {
			add(token)
		}
	} else if c.OfficialProfile == claudeCodeOfficialProfileCLITitle {
		for _, token := range claude.ClaudeCodeTitleBetasForModel(modelID) {
			add(token)
		}
	} else {
		for _, token := range c.BodyBetaTokens {
			add(token)
		}
		if c.isClaudeCodeFamily() {
			add(claude.BetaClaudeCode)
		}
		if c.HasThinking && c.ThinkingType != "disabled" {
			add(claude.BetaInterleavedThinking)
			add(claude.BetaRedactThinking)
			add(claude.BetaThinkingTokenCount)
		}
		if c.HasGlobalSystemCache {
			add(claude.BetaPromptCachingScope)
		}
		if c.ToolsCount > 0 || c.HasToolChoice {
			add(claude.BetaAdvancedToolUse)
		}
	}

	if c.HasContextManagement {
		add(claude.BetaContextManagement)
	}
	if c.HasEffort {
		add(claude.BetaEffort)
	}
	if c.HasStructuredOutput {
		add(claude.BetaStructuredOutputs)
	}

	return tokens
}
