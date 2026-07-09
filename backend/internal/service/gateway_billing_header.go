package service

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// ccVersionInBillingRe matches the semver part of cc_version (X.Y.Z), preserving
// the trailing message-derived suffix (e.g. ".c02") if present.
var (
	ccVersionInBillingRe             = regexp.MustCompile(`cc_version=\d+\.\d+\.\d+`)
	claudeCodeBillingWorkloadValueRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)
)

type claudeCodeBillingAttributionOptions struct {
	Entrypoint string
	Workload   string
	IsSubagent bool
	OmitCCH    bool
}

// syncBillingHeaderVersion rewrites cc_version in x-anthropic-billing-header
// system text blocks to match the version extracted from userAgent.
// Only touches system array blocks whose text starts with "x-anthropic-billing-header".
func syncBillingHeaderVersion(body []byte, userAgent string) []byte {
	version := ExtractCLIVersion(userAgent)
	if version == "" {
		return body
	}

	systemResult := gjson.GetBytes(body, "system")
	if !systemResult.Exists() || !systemResult.IsArray() {
		return body
	}

	replacement := "cc_version=" + version
	idx := 0
	systemResult.ForEach(func(_, item gjson.Result) bool {
		text := item.Get("text")
		if text.Exists() && text.Type == gjson.String &&
			strings.HasPrefix(text.String(), "x-anthropic-billing-header") {
			newText := ccVersionInBillingRe.ReplaceAllString(text.String(), replacement)
			if newText != text.String() {
				if updated, err := sjson.SetBytes(body, fmt.Sprintf("system.%d.text", idx), newText); err == nil {
					body = updated
				}
			}
		}
		idx++
		return true
	})

	return body
}

func parseClaudeCodeBillingAttributionText(text string) (entrypoint string, hasCCH bool, ok bool) {
	trimmed := strings.TrimSpace(text)
	if !strings.HasPrefix(trimmed, claudeCodeBillingHeaderPrefix) {
		return "", false, false
	}
	if !strings.Contains(trimmed, "cc_version=") || !strings.Contains(trimmed, claudeCodeEntrypointMarker) {
		return "", false, false
	}

	rest := trimmed[strings.Index(trimmed, claudeCodeEntrypointMarker)+len(claudeCodeEntrypointMarker):]
	end := strings.IndexAny(rest, "; \t\r\n")
	if end >= 0 {
		rest = rest[:end]
	}
	entrypoint = strings.TrimSpace(rest)
	return entrypoint, strings.Contains(trimmed, "cch="), entrypoint != ""
}

func extractClaudeCodeBillingAttributionOptions(text string) claudeCodeBillingAttributionOptions {
	opts := claudeCodeBillingAttributionOptions{}
	trimmed := strings.TrimSpace(text)
	if !strings.HasPrefix(trimmed, claudeCodeBillingHeaderPrefix) {
		return opts
	}
	for _, part := range strings.Split(trimmed, ";") {
		field := strings.TrimSpace(part)
		if strings.HasPrefix(field, "cc_workload=") {
			workload := strings.TrimPrefix(field, "cc_workload=")
			if claudeCodeBillingWorkloadValueRe.MatchString(workload) {
				opts.Workload = workload
			}
			continue
		}
		if field == "cc_is_subagent=true" {
			opts.IsSubagent = true
		}
	}
	return opts
}

func normalizeClaudeCodeBillingEntrypoint(_ string) string {
	return "cli"
}

func refreshClaudeCodeBillingAttribution(body []byte, cliVersion string) []byte {
	return refreshClaudeCodeBillingAttributionWithOptions(body, cliVersion, false)
}

func refreshClaudeCodeBillingAttributionWithoutCCH(body []byte, cliVersion string) []byte {
	return refreshClaudeCodeBillingAttributionWithOptions(body, cliVersion, true)
}

func refreshClaudeCodeBillingAttributionWithOptions(body []byte, cliVersion string, omitCCH bool) []byte {
	cliVersion = strings.TrimSpace(cliVersion)
	if cliVersion == "" {
		return body
	}

	system := gjson.GetBytes(body, "system")
	if !system.IsArray() {
		return body
	}
	items := system.Array()
	if len(items) == 0 {
		return body
	}
	text := items[0].Get("text")
	if !text.Exists() || text.Type != gjson.String {
		return body
	}
	current := text.String()
	entrypoint, _, ok := parseClaudeCodeBillingAttributionText(current)
	if !ok {
		return body
	}

	opts := extractClaudeCodeBillingAttributionOptions(current)
	opts.Entrypoint = normalizeClaudeCodeBillingEntrypoint(entrypoint)
	opts.OmitCCH = omitCCH
	nextText, err := buildBillingAttributionTextWithOptions(
		body,
		cliVersion,
		opts,
	)
	if err != nil {
		return body
	}
	nextBlock, err := marshalAnthropicSystemTextBlock(nextText, false)
	if err != nil {
		return body
	}
	if updated, ok := setJSONRawBytes(body, "system.0", nextBlock); ok {
		return updated
	}
	return body
}
