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

type claudeCodeBodyClassification struct {
	Profile              claudeCodeBodyProfile
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
		}
	}

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

	return c
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
		for _, token := range claude.ClaudeCodeMainBetasForModel(modelID) {
			add(token)
		}
	} else if c.Profile == claudeCodeBodyProfileTitle {
		for _, token := range claude.ClaudeCodeTitleBetas() {
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
