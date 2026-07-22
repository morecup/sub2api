//go:build unit

package service

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestNormalizeOpenAIResponsesLiteTools_MovesNamespacesAndKeepsSupportedTools(t *testing.T) {
	reqBody := map[string]any{
		"model": "gpt-5.6-terra",
		"tools": []any{
			map[string]any{"type": "function", "name": "shell"},
			map[string]any{"type": "custom", "name": "exec"},
			map[string]any{"type": "tool_search"},
			map[string]any{"type": "namespace", "name": "collaboration", "tools": []any{
				map[string]any{"type": "function", "name": "spawn_agent"},
			}},
		},
		"input": []any{
			map[string]any{"type": "message", "role": "user", "content": "hello"},
			map[string]any{"type": "additional_tools", "role": "developer", "tools": []any{
				map[string]any{"type": "namespace", "name": "image_gen"},
				map[string]any{"type": "namespace", "name": "collaboration", "tools": []any{
					map[string]any{"type": "function", "name": "spawn_agent"},
				}},
			}},
		},
		"tool_choice": map[string]any{"type": "namespace", "name": "collaboration"},
	}

	changed, err := normalizeOpenAIResponsesLiteTools(reqBody)

	require.NoError(t, err)
	require.True(t, changed)
	tools := reqBody["tools"].([]any)
	require.Len(t, tools, 3)
	require.Equal(t, "function", tools[0].(map[string]any)["type"])
	require.Equal(t, "custom", tools[1].(map[string]any)["type"])
	require.Equal(t, "tool_search", tools[2].(map[string]any)["type"])
	input := reqBody["input"].([]any)
	require.Len(t, input, 2)
	additional := input[1].(map[string]any)["tools"].([]any)
	require.Len(t, additional, 2)
	require.Equal(t, "image_gen", additional[0].(map[string]any)["name"])
	require.Equal(t, "collaboration", additional[1].(map[string]any)["name"], "existing namespace must not be duplicated")
	require.Equal(t, map[string]any{"type": "namespace", "name": "collaboration"}, reqBody["tool_choice"])
}

func TestNormalizeOpenAIResponsesLiteTools_RejectsConflictingAdditionalTool(t *testing.T) {
	reqBody := map[string]any{
		"tools": []any{map[string]any{
			"type":  "namespace",
			"name":  "collaboration",
			"tools": []any{map[string]any{"type": "function", "name": "spawn_agent"}},
		}},
		"input": []any{map[string]any{
			"type": "additional_tools",
			"tools": []any{map[string]any{
				"type":  "namespace",
				"name":  "collaboration",
				"tools": []any{map[string]any{"type": "function", "name": "send_message"}},
			}},
		}},
	}

	changed, err := normalizeOpenAIResponsesLiteTools(reqBody)

	require.ErrorContains(t, err, `conflicts with migrated tool type "namespace" name "collaboration"`)
	require.False(t, changed)
	require.Len(t, reqBody["tools"], 1, "conflicts must not partially remove top-level tools")
}

func TestNormalizeOpenAIResponsesLiteTools_DeduplicatesAcrossAdditionalToolItems(t *testing.T) {
	namespace := map[string]any{
		"type":  "namespace",
		"name":  "collaboration",
		"tools": []any{map[string]any{"type": "function", "name": "spawn_agent"}},
	}
	reqBody := map[string]any{
		"tools": []any{namespace},
		"input": []any{
			map[string]any{
				"type":  "additional_tools",
				"tools": []any{map[string]any{"type": "custom", "name": "exec"}},
			},
			map[string]any{
				"type":  "additional_tools",
				"tools": []any{namespace},
			},
		},
	}

	changed, err := normalizeOpenAIResponsesLiteTools(reqBody)

	require.NoError(t, err)
	require.True(t, changed)
	require.NotContains(t, reqBody, "tools")
	input := reqBody["input"].([]any)
	require.Len(t, input[0].(map[string]any)["tools"], 1)
	require.Len(t, input[1].(map[string]any)["tools"], 1)
}

func TestNormalizeOpenAIResponsesLiteTools_ConvertsStringInput(t *testing.T) {
	reqBody := map[string]any{
		"input": "hello",
		"tools": []any{map[string]any{
			"type": "namespace",
			"name": "collaboration",
		}},
	}

	changed, err := normalizeOpenAIResponsesLiteTools(reqBody)

	require.NoError(t, err)
	require.True(t, changed)
	require.NotContains(t, reqBody, "tools")
	input := reqBody["input"].([]any)
	require.Len(t, input, 2)
	require.Equal(t, "message", input[0].(map[string]any)["type"])
	require.Equal(t, "hello", input[0].(map[string]any)["content"])
	require.Equal(t, "additional_tools", input[1].(map[string]any)["type"])
}

func TestNormalizeOpenAIResponsesLiteTools_KeepsSupportedTopLevelTools(t *testing.T) {
	reqBody := map[string]any{
		"reasoning": map[string]any{"context": "all_turns"},
		"tools": []any{
			map[string]any{"type": "function", "name": "shell"},
			map[string]any{"type": "custom", "name": "exec"},
			map[string]any{"type": "tool_search"},
			"custom shorthand",
		},
	}

	changed, err := normalizeOpenAIResponsesLiteTools(reqBody)

	require.NoError(t, err)
	require.False(t, changed)
	require.Len(t, reqBody["tools"], 4)
}

func TestNormalizeOpenAIResponsesLiteTools_EnsuresReasoningContext(t *testing.T) {
	tests := []struct {
		name      string
		reasoning any
	}{
		{name: "missing"},
		{name: "missing context", reasoning: map[string]any{"effort": "high"}},
		{name: "wrong context", reasoning: map[string]any{"effort": "medium", "context": "current_turn"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reqBody := map[string]any{"input": "hello"}
			if tt.reasoning != nil {
				reqBody["reasoning"] = tt.reasoning
			}

			changed, err := normalizeOpenAIResponsesLiteTools(reqBody)

			require.NoError(t, err)
			require.True(t, changed)
			reasoning := reqBody["reasoning"].(map[string]any)
			require.Equal(t, "all_turns", reasoning["context"])
			if tt.name != "missing" {
				require.Equal(t, tt.reasoning.(map[string]any)["effort"], reasoning["effort"])
			}
		})
	}
}

func TestNormalizeOpenAIResponsesLiteTools_RejectsNonObjectReasoning(t *testing.T) {
	reqBody := map[string]any{"reasoning": "high"}

	changed, err := normalizeOpenAIResponsesLiteTools(reqBody)

	require.ErrorContains(t, err, "reasoning to be an object")
	require.False(t, changed)
	require.Equal(t, "high", reqBody["reasoning"])
}

func TestNormalizeOpenAIResponsesLiteTools_RejectsUnsupportedTools(t *testing.T) {
	tests := []struct {
		name string
		tool map[string]any
		want string
	}{
		{name: "hosted web search", tool: map[string]any{"type": "web_search"}, want: `top-level tool type "web_search"`},
		{name: "hosted image generation", tool: map[string]any{"type": "image_generation"}, want: `top-level tool type "image_generation"`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reqBody := map[string]any{"tools": []any{tt.tool}}
			changed, err := normalizeOpenAIResponsesLiteTools(reqBody)
			require.ErrorContains(t, err, tt.want)
			require.False(t, changed)
			require.Len(t, reqBody["tools"], 1, "validation errors must not partially mutate tools")
		})
	}
}

func TestNormalizeOpenAIResponsesLiteToolsPayload_PreservesResponseCreateShape(t *testing.T) {
	body := []byte(`{
		"type":"response.create",
		"model":"gpt-5.6-terra",
		"client_metadata":{"ws_request_header_x_openai_internal_codex_responses_lite":"true"},
		"input":[{"type":"message","role":"user","content":"hello"}],
		"tools":[{"type":"namespace","name":"collaboration","tools":[{"type":"function","name":"spawn_agent"}]}],
		"tool_choice":{"type":"namespace","name":"collaboration"}
	}`)

	updated, changed, err := normalizeOpenAIResponsesLiteToolsPayload(body)

	require.NoError(t, err)
	require.True(t, changed)
	require.Equal(t, "response.create", gjson.GetBytes(updated, "type").String())
	require.False(t, gjson.GetBytes(updated, "tools").Exists())
	require.Equal(t, "collaboration", gjson.GetBytes(updated, `input.#(type=="additional_tools").tools.0.name`).String())
	require.Equal(t, "namespace", gjson.GetBytes(updated, "tool_choice.type").String())
}

func TestNormalizeOpenAIResponsesLiteTools_StripsImageDetailsOnlyFromSupportedContent(t *testing.T) {
	reqBody := map[string]any{
		"input": []any{
			map[string]any{
				"type": "message",
				"content": []any{
					map[string]any{"type": "input_image", "image_url": "data:image/png;base64,a", "detail": "high"},
					map[string]any{"type": "input_text", "text": "keep", "detail": "metadata"},
				},
			},
			map[string]any{
				"type":   "function_call_output",
				"output": []any{map[string]any{"type": "input_image", "image_url": "https://example.com/a.png", "detail": "low"}},
			},
			map[string]any{
				"type":   "custom_tool_call_output",
				"output": []any{map[string]any{"type": "input_image", "image_url": "https://example.com/b.png", "detail": "auto"}},
			},
		},
	}

	changed, err := normalizeOpenAIResponsesLiteTools(reqBody)

	require.NoError(t, err)
	require.True(t, changed)
	require.Equal(t, "all_turns", reqBody["reasoning"].(map[string]any)["context"])
	require.NotContains(t, reqBody["input"].([]any)[0].(map[string]any)["content"].([]any)[0], "detail")
	require.Equal(t, "metadata", reqBody["input"].([]any)[0].(map[string]any)["content"].([]any)[1].(map[string]any)["detail"])
	require.NotContains(t, reqBody["input"].([]any)[1].(map[string]any)["output"].([]any)[0], "detail")
	require.NotContains(t, reqBody["input"].([]any)[2].(map[string]any)["output"].([]any)[0], "detail")
}

func TestApplyCodexOAuthTransform_PreservesLiteNamespaceToolChoice(t *testing.T) {
	reqBody := map[string]any{
		"model": "gpt-5.6-terra",
		"input": []any{map[string]any{
			"type": "additional_tools",
			"tools": []any{map[string]any{
				"type": "namespace",
				"name": "collaboration",
			}},
		}},
		"tool_choice": map[string]any{"type": "namespace", "name": "collaboration"},
	}

	applyCodexOAuthTransform(reqBody, true, false)

	require.Equal(t, map[string]any{"type": "namespace", "name": "collaboration"}, reqBody["tool_choice"])
}

func TestOpenAIGatewayServiceForward_NormalizesResponsesLiteToolsForOAuth(t *testing.T) {
	gin.SetMode(gin.TestMode)

	for _, passthrough := range []bool{false, true} {
		name := "managed"
		if passthrough {
			name = "passthrough"
		}
		t.Run(name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(nil))
			c.Request.Header.Set("User-Agent", "codex_cli_rs/0.144.1")
			c.Request.Header.Set(responsesLiteHeader, "true")
			upstream := &httpUpstreamRecorder{resp: &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
				Body: io.NopCloser(strings.NewReader(
					"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_lite\",\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}\n\n" +
						"data: [DONE]\n\n",
				)),
			}}
			svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream}
			account := &Account{
				ID: 501, Name: "responses-lite", Platform: PlatformOpenAI, Type: AccountTypeOAuth,
				Concurrency: 1, Status: StatusActive, Schedulable: true, RateMultiplier: f64p(1),
				Credentials: map[string]any{"access_token": "oauth-token", "chatgpt_account_id": "chatgpt-account"},
				Extra:       map[string]any{"openai_passthrough": passthrough},
			}
			body := []byte(`{
				"model":"gpt-5.6-terra","stream":true,"instructions":"test",
				"reasoning":{"effort":"high","context":"current_turn"},
				"tools":[
					{"type":"function","name":"shell","parameters":{"type":"object"}},
					{"type":"custom","name":"exec"},
					{"type":"tool_search"},
					{"type":"namespace","name":"collaboration","tools":[{"type":"function","name":"spawn_agent","parameters":{"type":"object"}}]}
				],
				"input":[{"type":"message","role":"user","content":"hello"}],
				"tool_choice":{"type":"namespace","name":"collaboration"}
			}`)

			result, err := svc.Forward(context.Background(), c, account, body)

			require.NoError(t, err)
			require.NotNil(t, result)
			// lite 头：managed 按出站 lite 模型发送；passthrough 按入站保留（入站已带）。
			require.Equal(t, "true", upstream.lastReq.Header.Get(responsesLiteHeader))
			require.Equal(t, "high", gjson.GetBytes(upstream.lastBody, "reasoning.effort").String())
			require.Equal(t, "all_turns", gjson.GetBytes(upstream.lastBody, "reasoning.context").String())
			require.Equal(t, "namespace", gjson.GetBytes(upstream.lastBody, "tool_choice.type").String())
			require.Equal(t, "collaboration", gjson.GetBytes(upstream.lastBody, "tool_choice.name").String())
			if passthrough {
				// 透传不下沉：顶层 tools 保留 function/custom/tool_search，namespace
				// 已在前置 Lite 归一化中迁移到 input.additional_tools。
				require.False(t, gjson.GetBytes(upstream.lastBody, `tools.#(type=="namespace")`).Exists())
				require.Equal(t, "shell", gjson.GetBytes(upstream.lastBody, `tools.#(type=="function").name`).String())
				require.Equal(t, "exec", gjson.GetBytes(upstream.lastBody, `tools.#(type=="custom").name`).String())
				require.True(t, gjson.GetBytes(upstream.lastBody, `tools.#(type=="tool_search")`).Exists())
				require.Equal(t, "collaboration", gjson.GetBytes(upstream.lastBody, `input.#(type=="additional_tools").tools.0.name`).String())
			} else {
				// managed（合成）路径：lite 模型按上游 build_responses_request 下沉——
				// 顶层 instructions/tools 省略，全部工具进入 input[0] 的 additional_tools
				// （顶层 tools 在前、namespace 迁移在后），instructions 成为其后的 developer message。
				require.False(t, gjson.GetBytes(upstream.lastBody, "tools").Exists())
				require.False(t, gjson.GetBytes(upstream.lastBody, "instructions").Exists())
				require.False(t, gjson.GetBytes(upstream.lastBody, "parallel_tool_calls").Bool())
				carrier := gjson.GetBytes(upstream.lastBody, `input.#(type=="additional_tools")`)
				require.True(t, carrier.Exists())
				require.Equal(t, "developer", carrier.Get("role").String())
				require.Equal(t, "shell", carrier.Get(`tools.#(type=="function").name`).String())
				require.Equal(t, "exec", carrier.Get(`tools.#(type=="custom").name`).String())
				require.True(t, carrier.Get(`tools.#(type=="tool_search")`).Exists())
				require.Equal(t, "collaboration", carrier.Get(`tools.#(type=="namespace").name`).String())
				sunkInstructions := gjson.GetBytes(upstream.lastBody, "input.1")
				require.Equal(t, "message", sunkInstructions.Get("type").String())
				require.Equal(t, "developer", sunkInstructions.Get("role").String())
				require.Equal(t, "input_text", sunkInstructions.Get("content.0.type").String())
				require.Equal(t, "test", sunkInstructions.Get("content.0.text").String())
			}
		})
	}
}

// Lite 归一化是 lite-only：合成路径由 lite 出站模型触发 sink（无需入站标记），
// 透传路径必须由入站 lite 头触发。入站不带 lite 头时：managed（lite 模型 terra）
// 仍由 sink 注入 reasoning.context=all_turns、下沉 instructions 并发送 lite 头；
// passthrough 的 body 不做任何 lite 归一化（无 reasoning.context、顶层
// instructions 保留），头也不发。
func TestOpenAIGatewayServiceForward_ResponsesLiteNormalizationIsLiteOnly(t *testing.T) {
	gin.SetMode(gin.TestMode)

	for _, passthrough := range []bool{false, true} {
		name := "managed"
		if passthrough {
			name = "passthrough"
		}
		t.Run(name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(nil))
			c.Request.Header.Set("User-Agent", "codex_cli_rs/0.144.1")
			// 入站故意不带 responses-lite 头。
			require.Empty(t, c.GetHeader(responsesLiteHeader))
			upstream := &httpUpstreamRecorder{resp: &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
				Body: io.NopCloser(strings.NewReader(
					"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_lite_forced\",\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}\n\n" +
						"data: [DONE]\n\n",
				)),
			}}
			svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream}
			account := &Account{
				ID: 502, Name: "responses-lite-forced", Platform: PlatformOpenAI, Type: AccountTypeOAuth,
				Concurrency: 1, Status: StatusActive, Schedulable: true, RateMultiplier: f64p(1),
				Credentials: map[string]any{"access_token": "oauth-token", "chatgpt_account_id": "chatgpt-account"},
				Extra:       map[string]any{"openai_passthrough": passthrough},
			}
			// body 不带 reasoning 字段。
			body := []byte(`{
				"model":"gpt-5.6-terra","stream":true,"instructions":"test",
				"input":[{"type":"message","role":"user","content":"hello"}]
			}`)

			result, err := svc.Forward(context.Background(), c, account, body)

			require.NoError(t, err)
			require.NotNil(t, result)
			if passthrough {
				// 透传无入站标记：不做任何 lite 归一化——无 lite 头、无 reasoning，
				// 顶层 instructions 与 input 原样透传。
				require.Empty(t, upstream.lastReq.Header.Get(responsesLiteHeader))
				require.False(t, gjson.GetBytes(upstream.lastBody, "reasoning").Exists())
				require.Equal(t, "test", gjson.GetBytes(upstream.lastBody, "instructions").String())
				require.Equal(t, "user", gjson.GetBytes(upstream.lastBody, "input.0.role").String())
				require.False(t, gjson.GetBytes(upstream.lastBody, `input.#(type=="additional_tools")`).Exists())
			} else {
				// managed 按出站 lite 模型触发 sink：发送 lite 头，reasoning.context=all_turns，
				// instructions 下沉为 developer message，顶层 instructions 省略。
				require.Equal(t, "true", upstream.lastReq.Header.Get(responsesLiteHeader))
				require.Equal(t, "all_turns", gjson.GetBytes(upstream.lastBody, "reasoning.context").String())
				require.False(t, gjson.GetBytes(upstream.lastBody, "instructions").Exists())
				require.Equal(t, "additional_tools", gjson.GetBytes(upstream.lastBody, "input.0.type").String())
				require.Equal(t, "developer", gjson.GetBytes(upstream.lastBody, "input.1.role").String())
				require.Equal(t, "test", gjson.GetBytes(upstream.lastBody, "input.1.content.0.text").String())
			}
		})
	}
}
