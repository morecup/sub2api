package service

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
)

// normalizeOpenAIResponsesLiteTools applies the Responses Lite request
// contract: reasoning must cover all turns, and private namespace declarations
// use the input.additional_tools carrier. Other top-level tools must belong to
// the small set accepted by the Lite endpoint; rejecting unsupported hosted
// tools is intentional because silently dropping them would change behavior.
func normalizeOpenAIResponsesLiteTools(reqBody map[string]any) (bool, error) {
	if reqBody == nil {
		return false, nil
	}
	if rawReasoning, exists := reqBody["reasoning"]; exists && rawReasoning != nil {
		if _, ok := rawReasoning.(map[string]any); !ok {
			return false, fmt.Errorf("responses Lite requires reasoning to be an object")
		}
	}
	rawTools, exists := reqBody["tools"]
	if !exists || rawTools == nil {
		changed, err := ensureOpenAIResponsesLiteReasoningContext(reqBody)
		if err != nil {
			return false, err
		}
		return changed || stripOpenAIResponsesLiteImageDetails(reqBody), nil
	}
	tools, ok := rawTools.([]any)
	if !ok {
		return false, fmt.Errorf("responses Lite requires tools to be an array")
	}

	topLevelTools := make([]any, 0, len(tools))
	namespaceTools := make([]any, 0, len(tools))
	for index, rawTool := range tools {
		if customTool, ok := rawTool.(string); ok {
			if strings.TrimSpace(customTool) == "" {
				return false, fmt.Errorf("responses Lite custom tool at index %d must not be empty", index)
			}
			topLevelTools = append(topLevelTools, rawTool)
			continue
		}
		tool, ok := rawTool.(map[string]any)
		if !ok {
			return false, fmt.Errorf("responses Lite tool at index %d must be an object", index)
		}
		toolType := strings.TrimSpace(firstNonEmptyString(tool["type"]))
		switch toolType {
		case "function", "custom", "tool_search":
			topLevelTools = append(topLevelTools, rawTool)
		case "namespace":
			namespaceTools = append(namespaceTools, rawTool)
		case "":
			return false, fmt.Errorf("responses Lite tool at index %d is missing type", index)
		default:
			return false, fmt.Errorf("responses Lite does not support top-level tool type %q at index %d", toolType, index)
		}
	}
	if len(namespaceTools) == 0 {
		changed, err := ensureOpenAIResponsesLiteReasoningContext(reqBody)
		if err != nil {
			return false, err
		}
		return changed || stripOpenAIResponsesLiteImageDetails(reqBody), nil
	}

	input, err := appendOpenAIResponsesLiteAdditionalTools(reqBody["input"], namespaceTools)
	if err != nil {
		return false, err
	}
	if _, err := ensureOpenAIResponsesLiteReasoningContext(reqBody); err != nil {
		return false, err
	}
	reqBody["input"] = input
	if len(topLevelTools) == 0 {
		delete(reqBody, "tools")
	} else {
		reqBody["tools"] = topLevelTools
	}
	_ = stripOpenAIResponsesLiteImageDetails(reqBody)
	return true, nil
}

// stripOpenAIResponsesLiteImageDetails mirrors codex-rs client_common.rs:
// Lite requests omit detail from input images in message content and in
// function/custom tool output content items. Other detail fields are preserved.
func stripOpenAIResponsesLiteImageDetails(reqBody map[string]any) bool {
	if reqBody == nil {
		return false
	}
	input, ok := reqBody["input"].([]any)
	if !ok {
		return false
	}
	modified := false
	for _, rawItem := range input {
		item, ok := rawItem.(map[string]any)
		if !ok {
			continue
		}
		switch strings.TrimSpace(firstNonEmptyString(item["type"])) {
		case "message":
			modified = stripOpenAIResponsesLiteImageDetailsFromContent(item["content"]) || modified
		case "function_call_output", "custom_tool_call_output":
			modified = stripOpenAIResponsesLiteImageDetailsFromContent(item["output"]) || modified
		}
	}
	return modified
}

func stripOpenAIResponsesLiteImageDetailsFromContent(value any) bool {
	content, ok := value.([]any)
	if !ok {
		return false
	}
	modified := false
	for _, rawItem := range content {
		item, ok := rawItem.(map[string]any)
		if !ok || strings.TrimSpace(firstNonEmptyString(item["type"])) != "input_image" {
			continue
		}
		if _, exists := item["detail"]; exists {
			delete(item, "detail")
			modified = true
		}
	}
	return modified
}

func ensureOpenAIResponsesLiteReasoningContext(reqBody map[string]any) (bool, error) {
	rawReasoning, exists := reqBody["reasoning"]
	if !exists || rawReasoning == nil {
		reqBody["reasoning"] = map[string]any{"context": "all_turns"}
		return true, nil
	}
	reasoning, ok := rawReasoning.(map[string]any)
	if !ok {
		return false, fmt.Errorf("responses Lite requires reasoning to be an object")
	}
	if context, ok := reasoning["context"].(string); ok && context == "all_turns" {
		return false, nil
	}
	reasoning["context"] = "all_turns"
	return true, nil
}

func appendOpenAIResponsesLiteAdditionalTools(input any, namespaceTools []any) ([]any, error) {
	var items []any
	switch typed := input.(type) {
	case nil:
		items = make([]any, 0, 1)
	case string:
		items = []any{map[string]any{
			"type":    "message",
			"role":    "user",
			"content": typed,
		}}
	case []any:
		items = typed
	default:
		return nil, fmt.Errorf("responses Lite namespace tools require input to be a string or array")
	}

	var target map[string]any
	var targetTools []any
	var allAdditionalTools []any
	for _, rawItem := range items {
		item, ok := rawItem.(map[string]any)
		if !ok || strings.TrimSpace(firstNonEmptyString(item["type"])) != "additional_tools" {
			continue
		}
		rawAdditionalTools, exists := item["tools"]
		additionalTools := []any(nil)
		toolsOK := true
		if exists && rawAdditionalTools != nil {
			additionalTools, toolsOK = rawAdditionalTools.([]any)
		}
		if !toolsOK {
			return nil, fmt.Errorf("responses Lite input.additional_tools tools must be an array")
		}
		if target == nil {
			target = item
			targetTools = additionalTools
		}
		allAdditionalTools = append(allAdditionalTools, additionalTools...)
	}

	merged, err := mergeOpenAIResponsesLiteAdditionalTools(allAdditionalTools, namespaceTools)
	if err != nil {
		return nil, err
	}
	newTools := merged[len(allAdditionalTools):]
	if target != nil {
		if len(newTools) > 0 {
			target["tools"] = append(append([]any(nil), targetTools...), newTools...)
		}
		return items, nil
	}

	items = append(items, map[string]any{
		"type":  "additional_tools",
		"role":  "developer",
		"tools": newTools,
	})
	return items, nil
}

func mergeOpenAIResponsesLiteAdditionalTools(existing []any, moved []any) ([]any, error) {
	merged := append([]any(nil), existing...)
	seen := make(map[string]any, len(existing)+len(moved))
	for _, rawTool := range existing {
		if identity := openAIResponsesLiteToolIdentity(rawTool); identity != "" {
			if previous, exists := seen[identity]; exists && !reflect.DeepEqual(previous, rawTool) {
				return nil, fmt.Errorf("responses Lite additional_tools contains conflicting definitions for %s", openAIResponsesLiteToolIdentityForError(rawTool))
			}
			seen[identity] = rawTool
		}
	}
	for _, rawTool := range moved {
		identity := openAIResponsesLiteToolIdentity(rawTool)
		if identity != "" {
			if previous, exists := seen[identity]; exists {
				if reflect.DeepEqual(previous, rawTool) {
					continue
				}
				return nil, fmt.Errorf("responses Lite additional_tools conflicts with migrated %s", openAIResponsesLiteToolIdentityForError(rawTool))
			}
			seen[identity] = rawTool
		}
		merged = append(merged, rawTool)
	}
	return merged, nil
}

func openAIResponsesLiteToolIdentity(rawTool any) string {
	tool, ok := rawTool.(map[string]any)
	if !ok {
		return ""
	}
	toolType := strings.TrimSpace(firstNonEmptyString(tool["type"]))
	name := strings.TrimSpace(firstNonEmptyString(tool["name"]))
	if toolType == "" || name == "" {
		return ""
	}
	return toolType + "\x00" + name
}

func openAIResponsesLiteToolIdentityForError(rawTool any) string {
	tool, _ := rawTool.(map[string]any)
	return fmt.Sprintf("tool type %q name %q", strings.TrimSpace(firstNonEmptyString(tool["type"])), strings.TrimSpace(firstNonEmptyString(tool["name"])))
}

// sinkOpenAIResponsesLiteRequestBody 把 responses lite 模型的 instructions/tools 下沉进
// input 数组，对齐 codex-rs build_responses_request（core/src/client.rs:843-864）：
//   - input[0] = {"type":"additional_tools","role":"developer","tools":[...]}
//     （tools 为空时上游仍发送该载体，client.rs:844-848 无空值分支）
//   - input[1] = {"type":"message","role":"developer","content":[{"type":"input_text","text":...}]}
//     （instructions 非空时；已按 lite 形态携带 instructions 的输入不重复生成）
//
// 顶层 instructions/tools 省略（上游序列化跳过空 instructions 与 None tools），
// parallel_tool_calls 强制 false（client.rs:897 `&& !use_responses_lite`），
// reasoning.context=all_turns（client.rs:818-820）。
// 真实 lite 客户端的输入已是该形态（载体后跟随着 instructions developer message），
// 此时只归一载体位置与顶层字段，不再注入合成路径补填的默认 instructions，避免重复。
func sinkOpenAIResponsesLiteRequestBody(reqBody map[string]any) bool {
	if reqBody == nil {
		return false
	}
	input, _ := reqBody["input"].([]any)

	// 摘下已有 additional_tools 载体并收集其 tools（保持相对顺序）。
	// 首个载体后紧跟 developer message 时，说明入站已按 lite 形态下沉 instructions。
	var carrierTools []any
	hadCarrier := false
	instructionsAlreadySunk := false
	rest := make([]any, 0, len(input))
	for index, rawItem := range input {
		item, ok := rawItem.(map[string]any)
		if !ok || strings.TrimSpace(firstNonEmptyString(item["type"])) != "additional_tools" {
			rest = append(rest, rawItem)
			continue
		}
		if !hadCarrier {
			hadCarrier = true
			if next := index + 1; next < len(input) {
				if nextItem, ok := input[next].(map[string]any); ok &&
					strings.TrimSpace(firstNonEmptyString(nextItem["type"])) == "message" &&
					strings.TrimSpace(firstNonEmptyString(nextItem["role"])) == "developer" {
					instructionsAlreadySunk = true
				}
			}
		}
		if tools, ok := item["tools"].([]any); ok {
			carrierTools = append(carrierTools, tools...)
		}
	}

	// 顶层 tools 在前、已有载体 tools 在后，按 type+name 去重（保留先出现者）。
	topTools, _ := reqBody["tools"].([]any)
	merged := make([]any, 0, len(topTools)+len(carrierTools))
	merged = append(merged, topTools...)
	merged = append(merged, carrierTools...)
	seen := make(map[string]bool, len(merged))
	deduped := merged[:0]
	for _, tool := range merged {
		if identity := openAIResponsesLiteToolIdentity(tool); identity != "" {
			if seen[identity] {
				continue
			}
			seen[identity] = true
		}
		deduped = append(deduped, tool)
	}

	newInput := make([]any, 0, len(rest)+2)
	newInput = append(newInput, map[string]any{
		"type":  "additional_tools",
		"role":  "developer",
		"tools": deduped,
	})
	if !instructionsAlreadySunk {
		if instructions, ok := reqBody["instructions"].(string); ok && strings.TrimSpace(instructions) != "" {
			newInput = append(newInput, map[string]any{
				"type": "message",
				"role": "developer",
				"content": []any{map[string]any{
					"type": "input_text",
					"text": instructions,
				}},
			})
		}
	}
	newInput = append(newInput, rest...)
	reqBody["input"] = newInput

	// 顶层 instructions/tools 省略；parallel_tool_calls 与 reasoning.context 按 lite 合约钉死。
	delete(reqBody, "instructions")
	delete(reqBody, "tools")
	reqBody["parallel_tool_calls"] = false
	// reasoning 非对象时无法写入 context：保留原值交给上游 400，不在此处放大改动面。
	_, _ = ensureOpenAIResponsesLiteReasoningContext(reqBody)
	_ = stripOpenAIResponsesLiteImageDetails(reqBody)
	return true
}

// sinkOpenAIResponsesLiteProbeBody 对 lite 探测模型的探针 body 执行 responses lite
// 下沉，保持探针侧 lite 头与 body 契约一致（否则 lite 头 + 普通 body 会被上游 400）。
// 非 lite 模型或解析失败时原样返回；已有 reasoning 的 effort/summary 由 sink 保留，
// 仅补齐 context=all_turns。
func sinkOpenAIResponsesLiteProbeBody(body []byte, model string) []byte {
	if !isCodexResponsesLiteModel(model) {
		return body
	}
	var reqBody map[string]any
	if err := json.Unmarshal(body, &reqBody); err != nil {
		return body
	}
	if !sinkOpenAIResponsesLiteRequestBody(reqBody) {
		return body
	}
	rebuilt, err := marshalOpenAIUpstreamJSON(reqBody)
	if err != nil {
		return body
	}
	return rebuilt
}

func normalizeOpenAIResponsesLiteToolsPayload(body []byte) ([]byte, bool, error) {
	var requestBody map[string]any
	if err := json.Unmarshal(body, &requestBody); err != nil {
		return body, false, fmt.Errorf("decode responses Lite request body: %w", err)
	}
	changed, err := normalizeOpenAIResponsesLiteTools(requestBody)
	if err != nil || !changed {
		return body, false, err
	}
	rebuilt, err := marshalOpenAIUpstreamJSON(requestBody)
	if err != nil {
		return body, false, fmt.Errorf("encode responses Lite request body: %w", err)
	}
	return rebuilt, true, nil
}
