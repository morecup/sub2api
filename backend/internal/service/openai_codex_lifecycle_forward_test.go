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
)

func TestCodexLifecycleWSForwardFiveModels(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, model := range []string{"gpt-5.5", "gpt-5.6-luna", "gpt-5.6-terra", "gpt-5.6-sol", "gpt-6-astra"} {
		for _, kind := range []string{"turn", "compaction", "prewarm_then_turn"} {
			t.Run(model+"/"+kind, func(t *testing.T) {
				cfg := &config.Config{}
				cfg.Gateway.CodexTelemetry.Mode = "off"
				cfg.Gateway.OpenAIWS.Enabled = true
				cfg.Gateway.OpenAIWS.OAuthEnabled = true
				cfg.Gateway.OpenAIWS.ResponsesWebsocketsV2 = true
				cfg.Gateway.OpenAIWS.MaxConnsPerAccount = 1
				cfg.Gateway.OpenAIWS.MaxIdlePerAccount = 1
				cfg.Gateway.OpenAIWS.PrewarmGenerateEnabled = kind == "prewarm_then_turn"
				completed := []byte(`{"type":"response.completed","response":{"id":"resp_local_fixture","usage":{"input_tokens":3,"output_tokens":1}}}`)
				conn := &openAIWSCaptureConn{events: [][]byte{completed, completed}}
				dialer := &openAIWSCaptureDialer{conn: conn}
				pool := newOpenAIWSConnPool(cfg)
				pool.setClientDialerForTest(dialer)
				svc := &OpenAIGatewayService{cfg: cfg, httpUpstream: &httpUpstreamRecorder{}, cache: &stubGatewayCache{}, openaiWSResolver: NewOpenAIWSProtocolResolver(cfg), toolCorrector: NewCodexToolCorrector(), openaiWSPool: pool}
				t.Cleanup(svc.CloseOpenAIWSPool)
				account := &Account{ID: 817, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true, Concurrency: 1,
					Credentials: map[string]any{"access_token": "local-dummy"}, Extra: map[string]any{"responses_websockets_v2_enabled": true}}
				thread, turn, window := newCodexUUIDV7(), newCodexUUIDV7(), newCodexUUIDV7()
				started := time.Now().Add(-time.Minute).UnixMilli()
				wantKind := kind
				if kind == "prewarm_then_turn" {
					wantKind = "turn"
				}
				md, err := json.Marshal(map[string]any{"request_kind": wantKind, "session_id": thread, "thread_id": thread, "turn_id": turn, "root_turn_id": turn, "turn_started_at_unix_ms": started, "window_number": 2, "context_window_id": window})
				require.NoError(t, err)
				body, err := json.Marshal(map[string]any{"model": model, "stream": false, "prompt_cache_key": thread, "input": "local fixture", "reasoning": map[string]any{"effort": "high"}, "client_metadata": map[string]any{openAIWSTurnMetadataHeader: string(md)}})
				require.NoError(t, err)
				c, _ := gin.CreateTestContext(httptest.NewRecorder())
				c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
				c.Request.Header.Set("User-Agent", codexDesktopUserAgent)
				c.Request.Header.Set(openAIWSTurnMetadataHeader, `{"request_kind":"prewarm","window_number":0}`)
				result, err := svc.Forward(context.Background(), c, account, body)
				require.NoError(t, err)
				require.NotNil(t, result)
				request := requestToJSONString(conn.lastWrite)
				metadata := gjson.Get(request, "client_metadata."+openAIWSTurnMetadataHeader).String()
				require.Equal(t, wantKind, gjson.Get(metadata, "request_kind").String())
				require.Equal(t, turn, gjson.Get(metadata, "turn_id").String())
				require.Equal(t, started, gjson.Get(metadata, "turn_started_at_unix_ms").Int())
				require.EqualValues(t, 2, gjson.Get(metadata, "window_number").Int())
				require.Equal(t, window, gjson.Get(metadata, "context_window_id").String())
				require.Equal(t, "high", gjson.Get(request, "reasoning.effort").String())
				require.Equal(t, dialer.lastHeaders.Get("session-id"), gjson.Get(request, "prompt_cache_key").String())
				if kind == "prewarm_then_turn" {
					require.Len(t, conn.writes, 2)
					prewarm := requestToJSONString(conn.writes[0])
					prewarmMetadata := gjson.Get(prewarm, "client_metadata."+openAIWSTurnMetadataHeader).String()
					require.Equal(t, "prewarm", gjson.Get(prewarmMetadata, "request_kind").String())
					require.Empty(t, gjson.Get(prewarmMetadata, "turn_id").String())
					require.False(t, gjson.Get(prewarm, "client_metadata.root_turn_id").Exists())
					require.Equal(t, turn, gjson.Get(request, "client_metadata.root_turn_id").String())
				}
			})
		}
	}
}

func TestCodexLifecycleHTTPFallbackUsesFrameMetadata(t *testing.T) {
	gin.SetMode(gin.TestMode)
	turn, thread := newCodexUUIDV7(), newCodexUUIDV7()
	metadata := `{"request_kind":"turn","turn_id":"` + turn + `","window_number":1}`
	body, err := json.Marshal(map[string]any{"model": "gpt-6-astra", "input": "fixture", "client_metadata": map[string]string{openAIWSTurnMetadataHeader: metadata}})
	require.NoError(t, err)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	c.Request.Header.Set(openAIWSTurnMetadataHeader, `{"request_kind":"prewarm","window_number":0}`)
	cfg := &config.Config{}
	cfg.Gateway.OpenAICodexRequestCompressionDisabled = true
	svc := &OpenAIGatewayService{cfg: cfg}
	a := &Account{ID: 817, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	req, err := svc.buildUpstreamRequest(context.Background(), c, a, body, "local-dummy", true, thread, true)
	require.NoError(t, err)
	defer req.Body.Close()
	require.Equal(t, turn, gjson.Get(req.Header.Get(openAIWSTurnMetadataHeader), "turn_id").String())
	require.EqualValues(t, 1, gjson.Get(req.Header.Get(openAIWSTurnMetadataHeader), "window_number").Int())
	actual, err := io.ReadAll(req.Body)
	require.NoError(t, err)
	require.Equal(t, turn, gjson.GetBytes(actual, "client_metadata.turn_id").String())
}

func TestCodexLifecycleWSFrameRetainsLongToolHistoryAndSubagent(t *testing.T) {
	thread, turn, root := newCodexUUIDV7(), newCodexUUIDV7(), newCodexUUIDV7()
	headers := make(http.Header)
	applyCodexOAuthWSMimicHeaders(headers, 817, 1, thread, "", "", "")
	md, err := json.Marshal(map[string]any{"turn_id": turn, "root_turn_id": root, "thread_source": "subagent", "agent_name": "/root/worker", "window_number": 2})
	require.NoError(t, err)
	input := []map[string]any{}
	for i := range 400 {
		callID := fmt.Sprintf("call_fixture_%d", i)
		input = append(input, map[string]any{"type": "function_call", "call_id": callID, "name": "read", "arguments": "{}"}, map[string]any{"type": "function_call_output", "call_id": callID, "output": strings.Repeat("fixture", 256)})
	}
	raw, err := json.Marshal(map[string]any{"type": "response.create", "model": "gpt-6-astra", "input": input, "previous_response_id": "resp_fixture", "client_metadata": map[string]string{openAIWSTurnMetadataHeader: string(md)}})
	require.NoError(t, err)
	updated, err := syncCodexWSFrameMetadata(raw, headers, `{"request_kind":"prewarm","window_number":0}`)
	require.NoError(t, err)
	require.Equal(t, gjson.GetBytes(raw, "input").Raw, gjson.GetBytes(updated, "input").Raw)
	require.Equal(t, "resp_fixture", gjson.GetBytes(updated, "previous_response_id").String())
	require.Equal(t, turn, gjson.GetBytes(updated, "client_metadata.turn_id").String())
	require.Equal(t, root, gjson.GetBytes(updated, "client_metadata.root_turn_id").String())
	metadata := gjson.GetBytes(updated, "client_metadata."+openAIWSTurnMetadataHeader).String()
	require.Equal(t, "/root/worker", gjson.Get(metadata, "agent_name").String())
	require.EqualValues(t, 2, gjson.Get(metadata, "window_number").Int())
}

type lifecycleDuplexConn struct{ *stagedPassthroughConn }

func (c *lifecycleDuplexConn) WriteJSON(ctx context.Context, value any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return c.WriteFrame(ctx, coderws.MessageText, raw)
}

func TestCodexLifecycleNativeWSPerFrameWindow(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, mode := range []string{OpenAIWSIngressModeCtxPool, OpenAIWSIngressModePassthrough} {
		t.Run(mode, func(t *testing.T) {
			cfg := passthroughLifecycleConfig()
			cfg.Gateway.CodexTelemetry.Mode = "off"
			cfg.Gateway.OpenAIWS.OAuthEnabled = true
			cfg.Gateway.OpenAIWS.ReadTimeoutSeconds = 3
			cfg.Gateway.OpenAIWS.MaxConnsPerAccount = 1
			cfg.Gateway.OpenAIWS.MaxIdlePerAccount = 1
			upstream := newStagedPassthroughConn()
			dialer := &stagedPassthroughDialer{conn: &lifecycleDuplexConn{upstream}}
			svc := newPassthroughLifecycleService(cfg, upstream)
			svc.openaiWSPassthroughDialer = dialer
			svc.openaiWSPool = newOpenAIWSConnPool(cfg)
			svc.openaiWSPool.setClientDialerForTest(dialer)
			t.Cleanup(svc.CloseOpenAIWSPool)
			account := &Account{ID: 817, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true, Concurrency: 1, Credentials: map[string]any{"access_token": "local-dummy"}, Extra: map[string]any{"openai_oauth_responses_websockets_v2_mode": mode}}
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			defer cancel()
			server, done := startPassthroughLifecycleServer(t, ctx, svc, account)
			defer server.Close()
			headers := make(http.Header)
			headers.Set(openAIWSTurnMetadataHeader, `{"request_kind":"prewarm","window_number":0}`)
			client, _, err := coderws.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http"), &coderws.DialOptions{HTTPHeader: headers})
			require.NoError(t, err)
			defer client.CloseNow()
			thread, turn := newCodexUUIDV7(), newCodexUUIDV7()
			var upstreamThread string
			for n := 0; n < 3; n++ {
				md, err := json.Marshal(map[string]any{"request_kind": "turn", "thread_id": thread, "turn_id": turn, "turn_started_at_unix_ms": 1700000000000, "window_number": n})
				require.NoError(t, err)
				body, err := json.Marshal(map[string]any{"type": "response.create", "model": "gpt-6-astra", "input": "fixture", "prompt_cache_key": thread, "client_metadata": map[string]string{openAIWSTurnMetadataHeader: string(md)}})
				require.NoError(t, err)
				require.NoError(t, client.Write(ctx, coderws.MessageText, body))
				written := requirePassthroughUpstreamWrite(t, upstream, 2*time.Second)
				metadata := gjson.GetBytes(written, "client_metadata."+openAIWSTurnMetadataHeader).String()
				require.Equal(t, turn, gjson.Get(metadata, "turn_id").String())
				require.EqualValues(t, n, gjson.Get(metadata, "window_number").Int())
				if n == 0 {
					upstreamThread = gjson.Get(metadata, "thread_id").String()
				}
				require.Equal(t, upstreamThread, gjson.Get(metadata, "thread_id").String())
				require.NotEqual(t, thread, upstreamThread)
				require.Equal(t, fmt.Sprintf("%s:%d", upstreamThread, n), gjson.Get(metadata, "window_id").String())
				upstream.Send(fmt.Sprintf(`{"type":"response.completed","response":{"id":"resp_fixture_%d","usage":{"input_tokens":1,"output_tokens":1}}}`, n))
				_, err = readPassthroughLifecycleFrame(t, client, 2*time.Second)
				require.NoError(t, err)
			}
			_ = client.Close(coderws.StatusNormalClosure, "fixture complete")
			select {
			case <-done:
			case <-ctx.Done():
				t.Fatal("gateway did not stop after client close")
			}
		})
	}
}
