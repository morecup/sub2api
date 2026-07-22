package service

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/tidwall/gjson"
)

const (
	openAICodexToolFrameOn5hExhaustedKey = "codex_tool_frame_on_5h_exhausted"
	openAICodexToolFrame429NoCooldownKey = "codex_tool_frame_429_no_cooldown"
	openAICodexToolFrameForceAfter5hKey  = "codex_tool_frame_force_after_5h"
	openAICodexToolFrameNever429Key      = "codex_tool_frame_never_429"
	codexToolFrameStubToolName           = "noop"
)

func shouldUseCodexToolFrameByQuota(account *Account, now time.Time) bool {
	if account == nil || !account.IsOpenAIOAuth() {
		return false
	}
	if !resolveAccountExtraBool(account.Extra, openAICodexToolFrameOn5hExhaustedKey) {
		return false
	}
	utilization5h, ok := resolveOpenAIQuotaUtilization(account.Extra, "5h", now)
	if !ok || utilization5h < 1 {
		return false
	}
	if shouldForceCodexToolFrameAfter5h(account, now) {
		return true
	}
	if utilization7d, ok := resolveOpenAIQuotaUtilization(account.Extra, "7d", now); ok && utilization7d >= 1 {
		return false
	}
	return true
}

func shouldForceCodexToolFrameAfter5h(account *Account, now time.Time) bool {
	if !isCodexToolFrameForceAfter5hEnabled(account) {
		return false
	}
	utilization5h, ok := resolveOpenAIQuotaUtilization(account.Extra, "5h", now)
	return ok && utilization5h >= 1
}

func isCodexToolFrameForceAfter5hEnabled(account *Account) bool {
	if account == nil || !account.IsOpenAIOAuth() {
		return false
	}
	if !resolveAccountExtraBool(account.Extra, openAICodexToolFrameOn5hExhaustedKey) ||
		!resolveAccountExtraBool(account.Extra, openAICodexToolFrameForceAfter5hKey) {
		return false
	}
	return true
}

func shouldRetryCodexToolFrameFrom429(account *Account, headers http.Header) bool {
	if account == nil || !account.IsOpenAIOAuth() {
		return false
	}
	if !resolveAccountExtraBool(account.Extra, openAICodexToolFrameOn5hExhaustedKey) {
		return false
	}
	if resolveAccountExtraBool(account.Extra, openAICodexToolFrameForceAfter5hKey) {
		return codexRateLimitHeadersIndicate5hExhausted(headers)
	}
	return codexRateLimitHeadersIndicate5hExhausted7dAvailable(headers)
}

func shouldRetryCodexToolFrameFromUsageLimit(account *Account, headers http.Header, now time.Time) bool {
	if account == nil || !account.IsOpenAIOAuth() {
		return false
	}
	if !resolveAccountExtraBool(account.Extra, openAICodexToolFrameOn5hExhaustedKey) {
		return false
	}
	if resolveAccountExtraBool(account.Extra, openAICodexToolFrameForceAfter5hKey) {
		if codexRateLimitHeadersIndicate5hExhausted(headers) {
			return true
		}
		return shouldUseCodexToolFrameByQuota(account, now)
	}
	if codexRateLimitHeadersIndicate5hExhausted7dAvailable(headers) {
		return true
	}
	return shouldUseCodexToolFrameByQuota(account, now)
}

func codexRateLimitHeadersIndicate5hExhausted(headers http.Header) bool {
	snapshot := ParseCodexRateLimitHeaders(headers)
	if snapshot == nil {
		return false
	}
	normalized := snapshot.Normalize()
	if normalized == nil || normalized.Used5hPercent == nil {
		return false
	}
	return *normalized.Used5hPercent >= 100
}

func codexRateLimitHeadersIndicate5hExhausted7dAvailable(headers http.Header) bool {
	snapshot := ParseCodexRateLimitHeaders(headers)
	if snapshot == nil {
		return false
	}
	return codexSnapshotIndicates5hExhausted7dAvailable(snapshot)
}

func codexSnapshotIndicates5hExhausted7dAvailable(snapshot *OpenAICodexUsageSnapshot) bool {
	normalized := snapshot.Normalize()
	if normalized == nil {
		return false
	}
	if normalized.Used5hPercent == nil || *normalized.Used5hPercent < 100 {
		return false
	}
	if normalized.Used7dPercent != nil && *normalized.Used7dPercent >= 100 {
		return false
	}
	return true
}

func (s *OpenAIGatewayService) persistCodexUsageSnapshotForRetry(ctx context.Context, account *Account, headers http.Header) {
	if s == nil || account == nil || account.ID <= 0 || headers == nil {
		return
	}
	snapshot := ParseCodexRateLimitHeaders(headers)
	if snapshot == nil {
		return
	}
	updates := buildCodexUsageExtraUpdates(snapshot, time.Now())
	if len(updates) == 0 {
		return
	}
	if account.Extra == nil {
		account.Extra = make(map[string]any, len(updates))
	}
	for key, value := range updates {
		account.Extra[key] = value
	}
	if s.accountRepo == nil {
		return
	}
	_ = s.accountRepo.UpdateExtra(ctx, account.ID, updates)
}

func appendCodexToolFrameIfNeeded(body []byte) ([]byte, bool) {
	if len(body) == 0 || isCodexRemoteCompactionV2Body(body) {
		return body, false
	}
	var reqBody map[string]any
	if err := json.Unmarshal(body, &reqBody); err != nil {
		return body, false
	}
	input, ok := reqBody["input"].([]any)
	if !ok || len(input) == 0 {
		return body, false
	}
	if item, ok := input[len(input)-1].(map[string]any); ok && strings.TrimSpace(firstNonEmptyString(item["type"])) == "function_call_output" {
		return body, false
	}

	ensureCodexToolFrameStubTool(reqBody)

	callID := "call_" + uuid.NewString()
	fcID := "fc_" + uuid.NewString()
	fc := map[string]any{
		"type":      "function_call",
		"id":        fcID,
		"call_id":   callID,
		"name":      codexToolFrameStubToolName,
		"arguments": "{}",
	}
	fcOut := map[string]any{
		"type":    "function_call_output",
		"call_id": callID,
		"output":  "",
	}
	reqBody["input"] = append(input, fc, fcOut)
	bodyOut, err := marshalOpenAIUpstreamJSON(reqBody)
	if err != nil {
		return body, false
	}
	return bodyOut, true
}

func ensureCodexToolFrameStubTool(reqBody map[string]any) {
	if reqBody == nil {
		return
	}
	if tools, ok := reqBody["tools"].([]any); ok {
		for _, tool := range tools {
			toolMap, ok := tool.(map[string]any)
			if !ok {
				continue
			}
			if strings.TrimSpace(firstNonEmptyString(toolMap["type"])) == "function" &&
				strings.TrimSpace(firstNonEmptyString(toolMap["name"])) == codexToolFrameStubToolName {
				return
			}
		}
	}
	stub := map[string]any{
		"type":        "function",
		"name":        codexToolFrameStubToolName,
		"description": "No-op placeholder",
		"parameters": map[string]any{
			"type":                 "object",
			"properties":           map[string]any{},
			"additionalProperties": false,
		},
		"strict": true,
	}
	tools, ok := reqBody["tools"].([]any)
	if !ok {
		reqBody["tools"] = []any{stub}
		return
	}
	reqBody["tools"] = append(tools, stub)
}

func isCodexRemoteCompactionV2Body(body []byte) bool {
	input := gjson.GetBytes(body, "input")
	if !input.IsArray() {
		return false
	}
	items := input.Array()
	return len(items) > 0 && strings.TrimSpace(items[len(items)-1].Get("type").String()) == "compaction_trigger"
}
