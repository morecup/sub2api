package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const codexEgressEnvironmentFixture = "<environment_context>\n  <cwd>D:\\work & examples</cwd>\n  <shell>powershell</shell>\n  <current_date>2026-10-20</current_date>\n  <timezone>Asia/Shanghai</timezone>\n</environment_context>"
const codexEgressAppTimeFixture = "<codex_apps_client_time_context>\n<timezone>Asia/Shanghai</timezone>\n<current_date>2026-10-20</current_date>\n</codex_apps_client_time_context>"

func codexEgressRequestFixture(t *testing.T) []byte {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"model": "gpt-5.1", "stream": false, "instructions": "test",
		"input": []any{map[string]any{
			"role": "user", "content": []any{
				map[string]any{"type": "input_text", "text": "# AGENTS.md\nKeep user instructions intact."},
				map[string]any{"type": "input_text", "text": codexEgressEnvironmentFixture},
				map[string]any{"type": "input_text", "text": "Explain <timezone>Asia/Shanghai</timezone>."},
			},
		}, map[string]any{"role": "developer", "content": []any{
			map[string]any{"type": "input_text", "text": codexEgressAppTimeFixture},
			map[string]any{"type": "input_text", "text": "Example: " + codexEgressAppTimeFixture},
		}}},
	})
	require.NoError(t, err)
	return body
}

func TestCodexEgressRequestEnvironmentAndDate(t *testing.T) {
	account := testCodexProfileAccount(910, 0)
	account.codexEgressProfile = &codexEgressProfile{timezone: "America/New_York", locale: "en-US"}
	now := time.Date(2026, 10, 20, 1, 0, 0, 0, time.UTC)
	body := codexEgressRequestFixture(t)
	// A surgical patch must not round a large JSON number or rewrite other fields.
	body, err := sjson.SetRawBytes(body, "metadata", []byte(`{"large":9007199254740993,"text":"<timezone>Asia/Shanghai</timezone>"}`))
	require.NoError(t, err)
	original := string(body)
	result, err := rewriteCodexEgressTimezone(body, account, now)
	require.NoError(t, err)
	text := gjson.GetBytes(result, "input.0.content.1.text").String()
	require.Contains(t, text, "<timezone>America/New_York</timezone>")
	require.Contains(t, text, "<current_date>2026-10-19</current_date>")
	require.Contains(t, gjson.GetBytes(result, "input.1.content.0.text").String(), "<timezone>America/New_York</timezone>")
	require.Contains(t, gjson.GetBytes(result, "input.1.content.0.text").String(), "<current_date>2026-10-19</current_date>")
	require.Contains(t, text, "<cwd>D:\\work & examples</cwd>")
	for _, path := range []string{"input.0.content.0", "input.0.content.2", "input.1.content.1", "metadata", "instructions"} {
		require.Equal(t, gjson.GetBytes(body, path).Raw, gjson.GetBytes(result, path).Raw, path)
	}
	require.Equal(t, original, string(body), "account retries must retain the original client bytes")
	again, err := rewriteCodexEgressTimezone(result, account, now)
	require.NoError(t, err)
	require.Equal(t, result, again)
	require.False(t, gjson.GetBytes(result, "timezone").Exists(), "do not add unsupported top-level fields to Responses")
}

func TestCodexEgressRequestShapesAndContentIsolation(t *testing.T) {
	account := testCodexProfileAccount(911, 0)
	account.codexEgressProfile = &codexEgressProfile{timezone: "Asia/Tokyo", locale: "ja-JP"}
	block := "<environment_context><timezone>Asia/Shanghai</timezone></environment_context>"
	for _, tc := range []struct {
		name, path string
		input      any
		wantChange bool
	}{
		{"cli", "input.0.content.0.text", []any{map[string]any{"role": "user", "content": []any{map[string]any{"type": "input_text", "text": block}}}}, true},
		{"string_input", "input", block, true},
		{"single_message", "input.content", map[string]any{"type": "message", "role": "user", "content": block}, true},
		{"whitespace", "input", " \r\n" + block + "\r\n ", true},
		{"quoted_example", "input", "Explain this example: " + block, false},
		{"code_fence", "input", "```xml\n" + block + "\n```", false},
		{"unclosed", "input", "<environment_context><timezone>Asia/Shanghai</timezone>", false},
		{"missing_timezone", "input", "<environment_context><current_date>2026-10-20</current_date></environment_context>", false},
		{"assistant", "input.content", map[string]any{"role": "assistant", "content": block}, false},
		{"tool_result", "input.output", map[string]any{"type": "function_call_output", "output": block}, false},
		{"tool_arguments", "input.arguments", map[string]any{"type": "function_call", "arguments": block}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body, err := json.Marshal(map[string]any{"input": tc.input})
			require.NoError(t, err)
			result, err := rewriteCodexEgressTimezone(body, account, time.Now())
			require.NoError(t, err)
			if tc.wantChange {
				require.Equal(t, strings.ReplaceAll(gjson.GetBytes(body, tc.path).String(), "Asia/Shanghai", "Asia/Tokyo"), gjson.GetBytes(result, tc.path).String())
			} else {
				require.Equal(t, body, result)
			}
		})
	}
	for _, account := range []*Account{
		nil,
		testCodexProfileAccount(912, 0),
		{Platform: PlatformOpenAI, Type: AccountTypeOAuth, codexEgressProfile: &codexEgressProfile{}},
		{Platform: PlatformOpenAI, Type: AccountTypeOAuth, codexEgressProfile: &codexEgressProfile{timezone: "invalid/zone"}},
		{Platform: PlatformOpenAI, Type: AccountTypeAPIKey, codexEgressProfile: &codexEgressProfile{timezone: "Asia/Tokyo"}},
	} {
		body := codexEgressRequestFixture(t)
		result, err := rewriteCodexEgressTimezone(body, account, time.Now())
		require.NoError(t, err)
		require.Equal(t, body, result, "no resolved OAuth egress means no body override")
	}
}

func codexEgressRequestAccount(t *testing.T, id int64, country, zone string, probe *codexEgressProbeStub) *Account {
	t.Helper()
	account := testCodexProfileAccount(id, 0)
	account.Credentials = map[string]any{"access_token": "fixture-token", "chatgpt_account_id": "fixture-account"}
	account.Concurrency = 1
	account.ProxyID = &id
	account.Proxy = &Proxy{ID: id, Protocol: "http", Host: "proxy.invalid", Port: 8080, Username: country}
	probe.exits[account.Proxy.URL()] = &ProxyExitInfo{IP: "203.0.113.7", CountryCode: country, Timezone: zone}
	return account
}

func assertCodexEgressRequestEnvironment(t *testing.T, body []byte, zone string) {
	t.Helper()
	var texts []string
	for _, item := range gjson.GetBytes(body, "input").Array() {
		for _, part := range item.Get("content").Array() {
			texts = append(texts, part.Get("text").String())
		}
	}
	text := strings.Join(texts, "\n")
	require.Contains(t, text, "<timezone>"+zone+"</timezone>")
	require.Contains(t, text, "# AGENTS.md\nKeep user instructions intact.")
	require.Contains(t, text, "Explain <timezone>Asia/Shanghai</timezone>.")
	require.Contains(t, text, strings.ReplaceAll(codexEgressAppTimeFixture[:strings.Index(codexEgressAppTimeFixture, "<current_date>")], "Asia/Shanghai", zone))
	require.Contains(t, text, "Example: "+codexEgressAppTimeFixture)
}

func TestCodexEgressRequestHTTPForwardAndAccountSwitch(t *testing.T) {
	for _, mode := range []string{"http", "passthrough", "compact"} {
		t.Run(mode, func(t *testing.T) {
			probe := &codexEgressProbeStub{exits: make(map[string]*ProxyExitInfo)}
			us := codexEgressRequestAccount(t, 913, "US", "America/New_York", probe)
			jp := codexEgressRequestAccount(t, 914, "JP", "Asia/Tokyo", probe)
			upstream := &httpUpstreamRecorder{}
			svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream, toolCorrector: NewCodexToolCorrector(), codexEgressProfiles: NewCodexEgressProfileService(probe, nil)}
			body := codexEgressRequestFixture(t)
			original := string(body)
			for _, account := range []*Account{us, jp} {
				account.Extra["openai_passthrough"] = mode == "passthrough"
				path := "/v1/responses"
				if mode == "compact" {
					path += "/compact"
				}
				c, _ := gin.CreateTestContext(httptest.NewRecorder())
				c.Request = httptest.NewRequest(http.MethodPost, path, nil)
				c.Request.Header.Set("User-Agent", "codex_cli_rs/0.162.1")
				upstream.resp = &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_egress\",\"model\":\"gpt-5.1\",\"output\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}\n\ndata: [DONE]\n\n"))}
				if mode == "compact" {
					upstream.resp.Header.Set("Content-Type", "application/json")
					upstream.resp.Body = io.NopCloser(strings.NewReader(`{"id":"cmp_egress","object":"response.compaction","output":[],"usage":{"input_tokens":1,"output_tokens":1}}`))
				}
				_, err := svc.Forward(context.Background(), c, account, body)
				require.NoError(t, err)
				assertCodexEgressRequestEnvironment(t, upstream.lastBody, probe.exits[account.Proxy.URL()].Timezone)
				require.Equal(t, account.Proxy.URL(), upstream.lastProxyURL)
				require.True(t, strings.HasSuffix(upstream.lastReq.URL.Path, path[len("/v1"):]))
				require.Equal(t, original, string(body))
				require.Nil(t, account.codexEgressProfile, "do not mutate the scheduler's account")
			}
		})
	}
}

func codexEgressRequestWSConfig() *config.Config {
	cfg := passthroughLifecycleConfig()
	cfg.Gateway.OpenAIWS.OAuthEnabled = true
	cfg.Gateway.OpenAIWS.MaxConnsPerAccount = 1
	cfg.Gateway.OpenAIWS.MinIdlePerAccount = 0
	cfg.Gateway.OpenAIWS.MaxIdlePerAccount = 1
	return cfg
}

func TestCodexEgressRequestHTTPToWS(t *testing.T) {
	probe := &codexEgressProbeStub{exits: make(map[string]*ProxyExitInfo)}
	account := codexEgressRequestAccount(t, 915, "JP", "Asia/Tokyo", probe)
	account.Extra["responses_websockets_v2_enabled"] = true
	account.Extra["openai_oauth_responses_websockets_v2_mode"] = OpenAIWSIngressModeCtxPool
	cfg := codexEgressRequestWSConfig()
	upstream := &openAIWSCaptureConn{events: [][]byte{[]byte(`{"type":"response.completed","response":{"id":"resp_egress_ws","model":"gpt-5.1","usage":{"input_tokens":1,"output_tokens":1}}}`)}}
	pool := newOpenAIWSConnPool(cfg)
	defer pool.Close()
	pool.setClientDialerForTest(&openAIWSCaptureDialer{conn: upstream})
	svc := &OpenAIGatewayService{cfg: cfg, httpUpstream: &httpUpstreamRecorder{}, toolCorrector: NewCodexToolCorrector(), openaiWSPool: pool, openaiWSResolver: NewOpenAIWSProtocolResolver(cfg), codexEgressProfiles: NewCodexEgressProfileService(probe, nil)}
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	c.Request.Header.Set("User-Agent", "codex_cli_rs/0.162.1")
	result, err := svc.Forward(context.Background(), c, account, codexEgressRequestFixture(t))
	require.NoError(t, err)
	require.True(t, result.OpenAIWSMode)
	upstream.mu.Lock()
	wire, err := json.Marshal(upstream.lastWrite)
	upstream.mu.Unlock()
	require.NoError(t, err)
	assertCodexEgressRequestEnvironment(t, wire, "Asia/Tokyo")
}

type codexEgressRequestWSConn struct{ *stagedPassthroughConn }

func (c *codexEgressRequestWSConn) WriteJSON(ctx context.Context, value any) error {
	wire, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return c.WriteFrame(ctx, coderws.MessageText, wire)
}

func TestCodexEgressRequestWSIngressEveryTurn(t *testing.T) {
	for _, mode := range []string{OpenAIWSIngressModeCtxPool, OpenAIWSIngressModePassthrough} {
		t.Run(mode, func(t *testing.T) {
			probe := &codexEgressProbeStub{exits: make(map[string]*ProxyExitInfo)}
			account := codexEgressRequestAccount(t, 916, "US", "America/New_York", probe)
			account.Extra["openai_oauth_responses_websockets_v2_mode"] = mode
			cfg := codexEgressRequestWSConfig()
			upstream := &codexEgressRequestWSConn{newStagedPassthroughConn()}
			defer upstream.Close()
			dialer := &stagedPassthroughDialer{conn: upstream}
			pool := newOpenAIWSConnPool(cfg)
			defer pool.Close()
			pool.setClientDialerForTest(dialer)
			svc := &OpenAIGatewayService{cfg: cfg, httpUpstream: &httpUpstreamRecorder{}, toolCorrector: NewCodexToolCorrector(), openaiWSPool: pool, openaiWSPassthroughDialer: dialer, openaiWSResolver: NewOpenAIWSProtocolResolver(cfg), codexEgressProfiles: NewCodexEgressProfileService(probe, nil)}
			done := make(chan error, 1)
			turnDone := make(chan struct{}, 2)
			hooks := &OpenAIWSIngressHooks{AfterTurn: func(_ int, _ *OpenAIForwardResult, _ error) { turnDone <- struct{}{} }}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				conn, err := coderws.Accept(w, r, nil)
				if err != nil {
					done <- err
					return
				}
				defer conn.CloseNow()
				_, first, err := conn.Read(r.Context())
				if err != nil {
					done <- err
					return
				}
				c, _ := gin.CreateTestContext(httptest.NewRecorder())
				c.Request = r
				done <- svc.ProxyResponsesWebSocketFromClient(r.Context(), c, conn, account, "fixture-token", first, hooks)
			}))
			defer server.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			client, _, err := coderws.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http"), nil)
			require.NoError(t, err)
			defer client.CloseNow()
			for turn := 1; turn <= 2; turn++ {
				body, err := sjson.SetBytes(codexEgressRequestFixture(t), "type", "response.create")
				require.NoError(t, err)
				require.NoError(t, client.Write(ctx, coderws.MessageText, body))
				select {
				case wire := <-upstream.writes:
					assertCodexEgressRequestEnvironment(t, wire, "America/New_York")
				case <-ctx.Done():
					t.Fatal("upstream did not receive the request")
				}
				upstream.Send(fmt.Sprintf(`{"type":"response.completed","response":{"id":"resp_egress_%d","model":"gpt-5.1","usage":{"input_tokens":1,"output_tokens":1}}}`, turn))
				_, event, err := client.Read(ctx)
				require.NoError(t, err)
				require.Equal(t, "response.completed", gjson.GetBytes(event, "type").String())
				select {
				case <-turnDone:
				case <-ctx.Done():
					t.Fatal("turn did not complete")
				}
			}
			_ = client.Close(coderws.StatusNormalClosure, "done")
			select {
			case err := <-done:
				if err != nil {
					require.Contains(t, err.Error(), "StatusNormalClosure")
				}
			case <-ctx.Done():
				t.Fatal("websocket forwarding did not stop")
			}
			require.Equal(t, []string{account.Proxy.URL()}, probe.calls)
		})
	}
}
