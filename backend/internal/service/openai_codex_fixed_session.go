package service

import (
	"strings"

	"github.com/google/uuid"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// shouldSkipFailoverForCodexFixed 用于 Codex OAuth 流量：
// 当上游返回 429/403/5xx 时不切换到其他账号、不重试，直接把原始响应透传给客户端。
// 配合 ratelimit_service.HandleUpstreamError 的入口跳过逻辑，OAuth Codex 账号
// 在 429/403/5xx 时本地状态保持 active，调度器仍可以选到它。
//
// 401 不在此列：token 失效要走原有刷新逻辑，不能绕过。
func shouldSkipFailoverForCodexFixed(account *Account, statusCode int) bool {
	if account == nil || account.Type != AccountTypeOAuth || account.Platform != PlatformOpenAI {
		return false
	}
	if statusCode == 429 || statusCode == 403 {
		return true
	}
	return statusCode >= 500 && statusCode < 600
}

// 实验：上游 ChatGPT internal API 的 plan 限额检查只看 input 数组最后一项的 type。
// 若最后一项是 function_call_output，上游识别为"工具调用循环延续帧"，跳过限额；
// 否则按"用户新一轮"计费。codex CLI 在工具调用循环里天然如此，
// 但其他客户端（curl / 自写 agent / cline / cursor 等）不会主动凑成这种结构。
//
// appendBypassToolFrameIfNeeded 在最后一项不是 function_call_output 时，向 input
// 末尾追加一对 fake function_call + function_call_output（用 uuid 作 call_id），
// 让上游把请求识别为"延续帧"而非"新一轮"。
const codexBypassStubToolName = "noop"

func appendBypassToolFrameIfNeeded(body []byte) []byte {
	if len(body) == 0 {
		return body
	}
	if isCodexRemoteCompactionV2Body(body) {
		return body
	}
	input := gjson.GetBytes(body, "input")
	if !input.IsArray() {
		return body
	}
	items := input.Array()
	if len(items) == 0 {
		return body
	}
	last := items[len(items)-1]
	if last.Get("type").String() == "function_call_output" {
		return body
	}

	// 1) 确保 tools 里有 stub function（已存在则不动）
	bodyOut, ok := ensureCodexBypassStubTool(body)
	if !ok {
		return body
	}

	// 2) 在 input 末尾追加 fc + fc_output 配对
	callID := "call_" + uuid.NewString()
	fcID := "fc_" + uuid.NewString()
	fc := map[string]any{
		"type":      "function_call",
		"id":        fcID,
		"call_id":   callID,
		"name":      codexBypassStubToolName,
		"arguments": "{}",
	}
	fcOut := map[string]any{
		"type":    "function_call_output",
		"call_id": callID,
		"output":  "",
	}
	if next, err := sjson.SetBytes(bodyOut, "input.-1", fc); err == nil {
		bodyOut = next
	} else {
		return body
	}
	if next, err := sjson.SetBytes(bodyOut, "input.-1", fcOut); err == nil {
		bodyOut = next
	} else {
		return body
	}
	return bodyOut
}

// ensureCodexBypassStubTool 确保 tools 数组里包含 codexBypassStubToolName 这个
// function 工具；若已存在则原样返回。返回 (newBody, true) 表示成功。
func ensureCodexBypassStubTool(body []byte) ([]byte, bool) {
	tools := gjson.GetBytes(body, "tools")
	if tools.IsArray() {
		for _, t := range tools.Array() {
			if t.Get("type").String() == "function" && t.Get("name").String() == codexBypassStubToolName {
				return body, true
			}
		}
	}
	stub := map[string]any{
		"type":        "function",
		"name":        codexBypassStubToolName,
		"description": "No-op placeholder",
		"parameters": map[string]any{
			"type":                 "object",
			"properties":           map[string]any{},
			"additionalProperties": false,
		},
		"strict": true,
	}
	if !tools.Exists() {
		next, err := sjson.SetBytes(body, "tools", []any{stub})
		return next, err == nil
	}
	next, err := sjson.SetBytes(body, "tools.-1", stub)
	return next, err == nil
}

// applyCodexBypassToolFrame 在 OAuth Codex 上游请求构建处调用：
// 仅当 input 最后一项不是 function_call_output 时改写，让上游识别为延续帧。
// 仅作用于 Responses API 格式的请求（有 input 数组）；Chat Completions 格式无 input 字段，自动跳过。
func applyCodexBypassToolFrame(body []byte) []byte {
	return appendBypassToolFrameIfNeeded(body)
}

func isCodexRemoteCompactionV2Body(body []byte) bool {
	if len(body) == 0 {
		return false
	}
	input := gjson.GetBytes(body, "input")
	if !input.IsArray() {
		return false
	}
	items := input.Array()
	return len(items) > 0 && items[len(items)-1].Get("type").String() == "compaction_trigger"
}

func isCodexRemoteCompactionV2RequestBody(reqBody map[string]any) bool {
	if reqBody == nil {
		return false
	}
	input, ok := reqBody["input"].([]any)
	if !ok {
		return false
	}
	return isCodexRemoteCompactionV2Input(input)
}

func isCodexRemoteCompactionV2Input(input []any) bool {
	if len(input) == 0 {
		return false
	}
	last, ok := input[len(input)-1].(map[string]any)
	if !ok {
		return false
	}
	return strings.TrimSpace(firstNonEmptyString(last["type"])) == "compaction_trigger"
}
