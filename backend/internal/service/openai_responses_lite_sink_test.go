package service

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// lite 模型名单：codex-rs models-manager/models.json @ c5eb33aed（2026-07-22 核实）。
func TestIsCodexResponsesLiteModel(t *testing.T) {
	tests := []struct {
		model string
		want  bool
	}{
		{"gpt-5.6-sol", true},
		{"gpt-5.6-terra", true},
		{"gpt-5.6-luna", true},
		{"gpt-5.5", false},
		{"gpt-5.4", false},
		{"gpt-5.4-mini", false},
		{"gpt-5.2", false},
		{"codex-auto-review", false},
		// 大小写/空白归一后仍精确匹配。
		{" GPT-5.6-TERRA ", true},
		{"gpt-5.6-Luna", true},
		{"GPT-5.5", false},
		// 不做前缀模糊匹配。
		{"gpt-5.6", false},
		{"gpt-5.6-terra-x", false},
		{"gpt-5.6-terra-high", false},
		{"", false},
		{"   ", false},
	}
	for _, tt := range tests {
		require.Equal(t, tt.want, isCodexResponsesLiteModel(tt.model), "model=%q", tt.model)
	}
}

// lite 合成路径：instructions/tools 下沉进 input，对齐上游 build_responses_request
// （client.rs:843-864）与 0.145 terra 实抓：input[0]=additional_tools、
// input[1]=developer message，顶层 instructions/tools 省略，parallel_tool_calls=false，
// reasoning.context=all_turns。
func TestApplyCodexOAuthTransform_ResponsesLiteSink(t *testing.T) {
	reqBody := map[string]any{
		"model":        "gpt-5.6-terra",
		"instructions": "test instructions",
		"tools": []any{
			map[string]any{"type": "function", "name": "shell"},
			map[string]any{"type": "custom", "name": "exec"},
		},
		"input": []any{
			map[string]any{"type": "message", "role": "user", "content": "hello"},
		},
	}

	applyCodexOAuthTransform(reqBody, false, false)

	require.NotContains(t, reqBody, "instructions")
	require.NotContains(t, reqBody, "tools")
	require.Equal(t, false, reqBody["parallel_tool_calls"])
	require.Equal(t, "all_turns", reqBody["reasoning"].(map[string]any)["context"])

	input := reqBody["input"].([]any)
	require.Len(t, input, 3)

	carrier := input[0].(map[string]any)
	require.Equal(t, "additional_tools", carrier["type"])
	require.Equal(t, "developer", carrier["role"])
	require.NotContains(t, carrier, "id")
	carrierTools := carrier["tools"].([]any)
	require.Len(t, carrierTools, 2)
	require.Equal(t, "shell", carrierTools[0].(map[string]any)["name"])
	require.Equal(t, "exec", carrierTools[1].(map[string]any)["name"])

	sunk := input[1].(map[string]any)
	require.Equal(t, "message", sunk["type"])
	require.Equal(t, "developer", sunk["role"])
	require.NotContains(t, sunk, "id")
	require.Equal(t, []any{map[string]any{"type": "input_text", "text": "test instructions"}}, sunk["content"])

	require.Equal(t, "user", input[2].(map[string]any)["role"])
}

func TestApplyCodexOAuthTransform_ResponsesLiteSinkStripsImageDetails(t *testing.T) {
	reqBody := map[string]any{
		"model": "gpt-5.6-terra",
		"input": []any{
			map[string]any{
				"type":    "message",
				"role":    "user",
				"content": []any{map[string]any{"type": "input_image", "image_url": "data:image/png;base64,a", "detail": "high"}},
			},
			map[string]any{
				"type":    "function_call_output",
				"call_id": "call_image_output",
				"output":  []any{map[string]any{"type": "input_image", "image_url": "https://example.com/a.png", "detail": "low"}},
			},
		},
	}

	applyCodexOAuthTransform(reqBody, false, false)

	input := reqBody["input"].([]any)
	require.NotContains(t, input[1].(map[string]any)["content"].([]any)[0], "detail")
	require.NotContains(t, input[3].(map[string]any)["output"].([]any)[0], "detail")
}

// tools 为空时上游仍发送 additional_tools 载体（client.rs:844-848 无空值分支）。
func TestApplyCodexOAuthTransform_ResponsesLiteSinkEmptyTools(t *testing.T) {
	reqBody := map[string]any{
		"model":        "gpt-5.6-luna",
		"instructions": "base",
		"input":        []any{map[string]any{"type": "message", "role": "user", "content": "hi"}},
	}

	applyCodexOAuthTransform(reqBody, false, false)

	input := reqBody["input"].([]any)
	require.Len(t, input, 3)
	carrier := input[0].(map[string]any)
	require.Equal(t, "additional_tools", carrier["type"])
	require.Equal(t, "developer", carrier["role"])
	tools, ok := carrier["tools"].([]any)
	require.True(t, ok, "additional_tools.tools 必须是数组（空数组也要发送）")
	require.Empty(t, tools)
}

// 已是 lite 形态的入站（真实 lite 客户端）：载体与 instructions 不重复生成，
// 合成路径补填的默认 instructions 不再注入，整体保持幂等。
func TestApplyCodexOAuthTransform_ResponsesLiteSinkIdempotent(t *testing.T) {
	clientInstructions := "client base instructions"
	reqBody := map[string]any{
		"model": "gpt-5.6-terra",
		"input": []any{
			map[string]any{"type": "additional_tools", "role": "developer", "tools": []any{
				map[string]any{"type": "custom", "name": "exec"},
			}},
			map[string]any{"type": "message", "role": "developer", "content": []any{
				map[string]any{"type": "input_text", "text": clientInstructions},
			}},
			map[string]any{"type": "message", "role": "user", "content": "hello"},
		},
	}

	applyCodexOAuthTransform(reqBody, false, false)

	require.NotContains(t, reqBody, "instructions")
	require.NotContains(t, reqBody, "tools")
	input := reqBody["input"].([]any)
	require.Len(t, input, 3)
	require.Equal(t, "additional_tools", input[0].(map[string]any)["type"])
	require.Len(t, input[0].(map[string]any)["tools"].([]any), 1)

	developerMessages := 0
	for _, item := range input {
		m := item.(map[string]any)
		if m["type"] == "message" && m["role"] == "developer" {
			developerMessages++
			require.Equal(t, clientInstructions, m["content"].([]any)[0].(map[string]any)["text"])
		}
	}
	require.Equal(t, 1, developerMessages, "不得重复注入默认 instructions")
}

// 顶层 tools 与已有载体（前置归一化迁移的 namespace）合并进同一载体并移到 input[0]，
// 顶层 instructions 正常下沉。
func TestApplyCodexOAuthTransform_ResponsesLiteSinkMergesExistingCarrier(t *testing.T) {
	reqBody := map[string]any{
		"model":        "gpt-5.6-terra",
		"instructions": "test",
		"tools": []any{
			map[string]any{"type": "function", "name": "shell"},
		},
		"input": []any{
			map[string]any{"type": "message", "role": "user", "content": "hello"},
			map[string]any{"type": "additional_tools", "role": "developer", "tools": []any{
				map[string]any{"type": "namespace", "name": "collaboration"},
			}},
		},
	}

	applyCodexOAuthTransform(reqBody, false, false)

	input := reqBody["input"].([]any)
	require.Len(t, input, 3)
	carrier := input[0].(map[string]any)
	require.Equal(t, "additional_tools", carrier["type"])
	tools := carrier["tools"].([]any)
	require.Len(t, tools, 2)
	require.Equal(t, "shell", tools[0].(map[string]any)["name"])
	require.Equal(t, "collaboration", tools[1].(map[string]any)["name"])
	require.Equal(t, "message", input[1].(map[string]any)["type"])
	require.Equal(t, "developer", input[1].(map[string]any)["role"])
	require.Equal(t, "user", input[2].(map[string]any)["role"])
}

// compact 与 turn 共用上游 build_responses_request：同样下沉，并保留 compact 既有的
// store/stream 剥离行为。
func TestApplyCodexOAuthTransform_ResponsesLiteSinkCompact(t *testing.T) {
	reqBody := map[string]any{
		"model":        "gpt-5.6-sol",
		"instructions": "compact instructions",
		"store":        true,
		"stream":       true,
		"tools":        []any{map[string]any{"type": "function", "name": "shell"}},
		"input":        []any{map[string]any{"type": "message", "role": "user", "content": "compact me"}},
	}

	applyCodexOAuthTransform(reqBody, false, true)

	require.NotContains(t, reqBody, "store")
	require.NotContains(t, reqBody, "stream")
	require.NotContains(t, reqBody, "instructions")
	require.NotContains(t, reqBody, "tools")
	require.Equal(t, false, reqBody["parallel_tool_calls"])
	require.Equal(t, "all_turns", reqBody["reasoning"].(map[string]any)["context"])
	input := reqBody["input"].([]any)
	require.Equal(t, "additional_tools", input[0].(map[string]any)["type"])
	require.Equal(t, "developer", input[1].(map[string]any)["role"])
	require.Equal(t, "compact instructions", input[1].(map[string]any)["content"].([]any)[0].(map[string]any)["text"])
}

// 桥接路径（SkipResponsesLiteSink）保持非 lite 形态：顶层 instructions/tools 原样保留。
func TestApplyCodexOAuthTransform_ResponsesLiteSinkSkippedForBridge(t *testing.T) {
	reqBody := map[string]any{
		"model":        "gpt-5.6-terra",
		"instructions": "bridge instructions",
		"tools":        []any{map[string]any{"type": "function", "name": "shell"}},
		"input":        []any{map[string]any{"type": "message", "role": "user", "content": "hi"}},
	}

	applyCodexOAuthTransformWithOptions(reqBody, codexOAuthTransformOptions{SkipResponsesLiteSink: true})

	require.Equal(t, "bridge instructions", reqBody["instructions"])
	require.Len(t, reqBody["tools"].([]any), 1)
	require.NotContains(t, reqBody, "parallel_tool_calls")
	input := reqBody["input"].([]any)
	require.Len(t, input, 1)
	require.Equal(t, "user", input[0].(map[string]any)["role"])
}

// 非 lite 回归红线：变换输出与现状一致——instructions/tools 留在顶层，
// 不生成 additional_tools，不强制 parallel_tool_calls，不注入 reasoning.context。
func TestApplyCodexOAuthTransform_NonLiteOutputUnchanged(t *testing.T) {
	reqBody := map[string]any{
		"model":        "gpt-5.5",
		"instructions": "keep me",
		"tools": []any{
			map[string]any{"type": "function", "name": "shell"},
			map[string]any{"type": "namespace", "name": "collaboration"},
		},
		"input": []any{
			map[string]any{"type": "message", "role": "user", "content": []any{
				map[string]any{"type": "input_image", "image_url": "https://example.com/non-lite.png", "detail": "high"},
			}},
		},
	}

	applyCodexOAuthTransform(reqBody, false, false)

	require.Equal(t, "keep me", reqBody["instructions"])
	require.Len(t, reqBody["tools"].([]any), 2)
	require.NotContains(t, reqBody, "parallel_tool_calls")
	require.NotContains(t, reqBody, "reasoning")
	input := reqBody["input"].([]any)
	require.Len(t, input, 1)
	require.Equal(t, "user", input[0].(map[string]any)["role"])
	require.Equal(t, "high", input[0].(map[string]any)["content"].([]any)[0].(map[string]any)["detail"])
}

// lite 头按条件发送：true 时设置，false 时不发送。
func TestApplyCodexOAuthMimicHeadersResponsesLiteConditional(t *testing.T) {
	newReq := func() *http.Request {
		req := httptest.NewRequest(http.MethodPost, "https://chatgpt.com/backend-api/codex/responses", strings.NewReader(`{}`))
		req.Header.Set("authorization", "Bearer token")
		return req
	}

	liteReq := newReq()
	applyCodexOAuthMimicHeaders(liteReq, 1, 0, "seed", codexDesktopOriginator, false, true)
	require.Equal(t, "true", liteReq.Header.Get("x-openai-internal-codex-responses-lite"))

	nonLiteReq := newReq()
	applyCodexOAuthMimicHeaders(nonLiteReq, 1, 0, "seed", codexDesktopOriginator, false, false)
	require.Empty(t, nonLiteReq.Header.Get("x-openai-internal-codex-responses-lite"))

	// compact 同样按条件发送。
	compactReq := newReq()
	applyCodexOAuthMimicHeaders(compactReq, 1, 0, "seed", codexDesktopOriginator, true, true)
	require.Equal(t, "true", compactReq.Header.Get("x-openai-internal-codex-responses-lite"))
}

// 403 instructions 检查：lite（透传入站带 lite 头）豁免；非 lite 仍拦截。
func TestDetectOpenAIPassthroughInstructionsRejectReasonResponsesLiteExempt(t *testing.T) {
	body := []byte(`{"model":"gpt-5.1-codex-max","input":[{"type":"message","role":"user","content":"hi"}]}`)

	// 非 lite：缺 instructions 仍按原逻辑拦截。
	require.Equal(t, "instructions_missing", detectOpenAIPassthroughInstructionsRejectReason("gpt-5.1-codex-max", body, false))
	// lite：豁免（lite 请求顶层本就没有 instructions）。
	require.Empty(t, detectOpenAIPassthroughInstructionsRejectReason("gpt-5.1-codex-max", body, true))
	// 非 codex 模型本来不检查，两种模式都放行。
	require.Empty(t, detectOpenAIPassthroughInstructionsRejectReason("gpt-5.5", body, false))
	require.Empty(t, detectOpenAIPassthroughInstructionsRejectReason("gpt-5.5", body, true))
}

// 探针 body 下沉：lite 探测模型的 instructions/tools 下沉、reasoning 补 context 且
// 保留已有 effort/summary；非 lite 探测模型 body 逐字节不动（与 lite 头条件发送一致）。
func TestSinkOpenAIResponsesLiteProbeBody(t *testing.T) {
	payload := createOpenAITestPayload("gpt-5.6-terra", true)
	body, err := json.Marshal(payload)
	require.NoError(t, err)

	out := sinkOpenAIResponsesLiteProbeBody(body, "gpt-5.6-terra")
	require.False(t, gjson.GetBytes(out, "instructions").Exists())
	require.Equal(t, "additional_tools", gjson.GetBytes(out, "input.0.type").String())
	require.Equal(t, "developer", gjson.GetBytes(out, "input.0.role").String())
	require.True(t, gjson.GetBytes(out, "input.0.tools").IsArray())
	require.Equal(t, "message", gjson.GetBytes(out, "input.1.type").String())
	require.Equal(t, "developer", gjson.GetBytes(out, "input.1.role").String())
	require.Equal(t, openai.DefaultInstructions, gjson.GetBytes(out, "input.1.content.0.text").String())
	require.Equal(t, "user", gjson.GetBytes(out, "input.2.role").String())
	require.Equal(t, "all_turns", gjson.GetBytes(out, "reasoning.context").String())
	require.False(t, gjson.GetBytes(out, "parallel_tool_calls").Bool())

	// 已有 reasoning effort/summary 保留，只补 context。
	custom := []byte(`{"model":"gpt-5.6-luna","instructions":"i","input":[],"reasoning":{"effort":"high","summary":"detailed"}}`)
	out = sinkOpenAIResponsesLiteProbeBody(custom, "gpt-5.6-luna")
	require.Equal(t, "high", gjson.GetBytes(out, "reasoning.effort").String())
	require.Equal(t, "detailed", gjson.GetBytes(out, "reasoning.summary").String())
	require.Equal(t, "all_turns", gjson.GetBytes(out, "reasoning.context").String())

	// 非 lite 探测模型：逐字节不动。
	body55, err := json.Marshal(createOpenAITestPayload("gpt-5.5", true))
	require.NoError(t, err)
	require.Equal(t, body55, sinkOpenAIResponsesLiteProbeBody(body55, "gpt-5.5"))
}
