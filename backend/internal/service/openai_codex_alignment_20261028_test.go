package service

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/gin-gonic/gin"
	"github.com/klauspost/compress/zstd"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

type codexOctoberRequestContract struct {
	HTTP, WS, Usage, Models struct {
		Names  []string          `json:"names"`
		Values map[string]string `json:"values"`
	}
}

type codexOctoberModelsUpstream struct {
	*httpUpstreamRecorder
	profile *tlsfingerprint.Profile
}

func (u *codexOctoberModelsUpstream) DoWithTLS(req *http.Request, proxy string, accountID int64, concurrency int, profile *tlsfingerprint.Profile) (*http.Response, error) {
	u.profile = profile
	return u.httpUpstreamRecorder.Do(req, proxy, accountID, concurrency)
}

func TestCodexOctober28NativeModelsTransport(t *testing.T) {
	contract := loadCodexOctoberRequestContract(t)
	upstream := &codexOctoberModelsUpstream{httpUpstreamRecorder: &httpUpstreamRecorder{resp: &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"models":[{"slug":"gpt-6.1-terra"}]}`))}}}
	svc := &OpenAIGatewayService{httpUpstream: upstream, tlsFPProfileService: &TLSFingerprintProfileService{}}
	account := newCodexModelsTestAccount()
	_, err := svc.FetchCodexModelsManifest(context.Background(), account, "ignored", "")
	require.NoError(t, err)
	for name, value := range contract.Models.Values {
		require.Equal(t, value, upstream.lastReq.Header.Get(name), name)
	}
	require.NotNil(t, upstream.profile)
	require.Equal(t, append(contract.Models.Names, "if-none-match"), upstream.profile.HTTP2.RegularHeaderOrder)
	require.Equal(t, tlsfingerprint.CodexDesktopProfile().SignatureAlgorithms, upstream.profile.SignatureAlgorithms)
}

func loadCodexOctoberRequestContract(t *testing.T) codexOctoberRequestContract {
	t.Helper()
	raw, err := os.ReadFile("../../../docs/codex-desktop-capture/2026-10-28_0.165.0-alpha.1.2/request-contract.json")
	require.NoError(t, err)
	var contract codexOctoberRequestContract
	require.NoError(t, json.Unmarshal(raw, &contract))
	return contract
}

func TestCodexOctober28AllModelRequestContract(t *testing.T) {
	contract := loadCodexOctoberRequestContract(t)
	for _, model := range []string{"gpt-6.1-sol", "gpt-6-astra", "gpt-6.1-terra", "gpt-6-sol", "gpt-6-luna", "gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna"} {
		t.Run(model, func(t *testing.T) {
			account := testCodexProfileAccount(10281, 0)
			account.Credentials = map[string]any{"chatgpt_account_id": "fixture-account"}
			svc := &OpenAIGatewayService{cfg: &config.Config{}}
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
			c.Request.Header.Set("originator", "codex_work_desktop")
			body := []byte(`{"model":"` + model + `","stream":true,"input":[],"reasoning":{"effort":"high"}}`)
			req, err := svc.buildUpstreamRequest(context.Background(), c, account, body, "fixture-token", true, "contract", true)
			require.NoError(t, err)
			for name, value := range contract.HTTP.Values {
				require.Equal(t, value, req.Header.Get(name), name)
			}
			compressed, err := io.ReadAll(req.Body)
			require.NoError(t, err)
			decoder, err := zstd.NewReader(nil)
			require.NoError(t, err)
			defer decoder.Close()
			wire, err := decoder.DecodeAll(compressed, nil)
			require.NoError(t, err)
			metadata := req.Header.Get(openAIWSTurnMetadataHeader)
			require.Equal(t, metadata, gjson.GetBytes(wire, "client_metadata.x-codex-turn-metadata").String())
			require.Equal(t, model, gjson.Get(metadata, "model").String())
			require.Equal(t, "new", gjson.Get(metadata, "history_initialization").String())
			require.Equal(t, req.Header.Get("session-id"), gjson.GetBytes(wire, "prompt_cache_key").String())
			ws, _, err := svc.buildOpenAIWSHeaders(context.Background(), c, account, "fixture-token", OpenAIWSProtocolDecision{}, true, "", "", "contract", model, "", "high")
			require.NoError(t, err)
			for name, value := range contract.WS.Values {
				require.Equal(t, value, ws.Get(name), name)
			}
			require.Empty(t, ws.Get(responsesLiteHeader))
			frame, err := syncCodexWSFrameMetadata([]byte(`{"type":"response.create","model":"`+model+`","input":[]}`), ws, "")
			require.NoError(t, err)
			require.Equal(t, "true", gjson.GetBytes(frame, "client_metadata."+responsesLiteWSMetadataKey).String())
			require.NotEmpty(t, gjson.GetBytes(frame, "client_metadata.x-codex-ws-stream-request-start-ms").String())
		})
	}
}

func TestCodexOctober28HTTPFallbackMetadataPreservesContent(t *testing.T) {
	body := []byte(`{"model":"gpt-6.1-terra","input":[{"type":"function_call_output","output":{"integer":9007199254740993,"decimal":1.234567890123456789}}],"tools":[{"type":"function","parameters":{"default":9007199254740993}}],"client_metadata":{"mcp_attribution":"preserve","extra":9007199254740993,"x-codex-ws-stream-request-start-ms":"123","ws_request_header_x_openai_internal_codex_responses_lite":"true"}}`)
	metadata := buildCodexTurnMetadata("019f85a3-654e-7542-8941-95713900af32", "", nil, "")
	updated, modified, err := applyCodexClientMetadataBytes(body, metadata)
	require.NoError(t, err)
	require.True(t, modified)
	for _, path := range []string{"input", "tools", "client_metadata.extra", "client_metadata.mcp_attribution"} {
		require.Equal(t, gjson.GetBytes(body, path).Raw, gjson.GetBytes(updated, path).Raw, path)
	}
	for _, name := range []string{"x-codex-ws-stream-request-start-ms", responsesLiteWSMetadataKey} {
		require.False(t, gjson.GetBytes(updated, "client_metadata."+name).Exists())
	}
	again, modified, err := applyCodexClientMetadataBytes(updated, metadata)
	require.NoError(t, err)
	require.False(t, modified)
	require.Equal(t, updated, again)
}

func TestCodexOctober28WSHandshakeWireOrder(t *testing.T) {
	contract := loadCodexOctoberRequestContract(t)
	lines := []string{"GET /backend-api/codex/responses HTTP/1.1"}
	for i := len(contract.WS.Names) - 1; i >= 0; i-- {
		lines = append(lines, contract.WS.Names[i]+": fixture")
	}
	wire, ok := reorderCodexWebSocketHandshake([]byte(strings.Join(lines, "\r\n") + "\r\n\r\n"))
	require.True(t, ok)
	var names []string
	for _, line := range strings.Split(strings.TrimSuffix(string(wire), "\r\n\r\n"), "\r\n")[1:] {
		name, _, _ := strings.Cut(line, ":")
		names = append(names, strings.ToLower(name))
	}
	require.Equal(t, contract.WS.Names, names)
}

func TestCodexOctober28TimeContextRoleIsolation(t *testing.T) {
	account := testCodexProfileAccount(10282, 0)
	account.codexEgressProfile = &codexEgressProfile{timezone: "America/New_York", locale: "en-US"}
	for _, role := range []string{"developer", "user", "assistant", "system"} {
		for _, text := range []string{codexEgressAppTimeFixture, "Explain " + codexEgressAppTimeFixture, "```xml\n" + codexEgressAppTimeFixture + "\n```"} {
			body, err := json.Marshal(map[string]any{"input": []any{map[string]any{"role": role, "content": text}}})
			require.NoError(t, err)
			updated, err := rewriteCodexEgressTimezone(body, account, time.Date(2026, 10, 20, 1, 0, 0, 0, time.UTC))
			require.NoError(t, err)
			if role == "developer" && text == codexEgressAppTimeFixture {
				require.Contains(t, gjson.GetBytes(updated, "input.0.content").String(), "<current_date>2026-10-19</current_date>")
			} else {
				require.Equal(t, body, updated)
			}
		}
	}
}

func TestCodexOctober28DesktopEndpointHeaders(t *testing.T) {
	contract := loadCodexOctoberRequestContract(t)
	for _, endpoint := range []string{"/backend-api/wham/usage", "/backend-api/codex/models"} {
		t.Run(endpoint, func(t *testing.T) {
			account := testCodexProfileAccount(10283, 0)
			account.Credentials = map[string]any{"access_token": "fixture-token", "chatgpt_account_id": "fixture-account"}
			account.codexEgressProfile = &codexEgressProfile{timezone: "Asia/Tokyo", locale: "ja-JP"}
			upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("{}"))}}
			svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream}
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodGet, endpoint, nil)
			c.Request.Header.Set("user-agent", "Mozilla/5.0 Chrome/154.0.0.0 (Codex Desktop; 26.928.20755)")
			c.Request.Header.Set("sec-ch-ua", `"Chromium";v="154"`)
			c.Request.Header.Set("baggage", "sentry-release=codex%4026.928.20755")
			c.Request.Header.Set("oai-did", "inbound-device")
			c.Request.Header.Set("x-openai-codex-window-type", "electron")
			resp, err := svc.ProxyCodexDesktopEndpoint(context.Background(), c, account, endpoint, nil)
			require.NoError(t, err)
			defer resp.Body.Close()
			header := upstream.lastReq.Header
			client := codexClientProfileForAccount(account)
			require.Equal(t, client.DeviceID, header.Get("oai-did"))
			require.Equal(t, client.BrowserClientHints(), header.Get("sec-ch-ua"))
			require.Contains(t, header.Get("baggage"), "codex%4026.1023.50603")
			if strings.HasSuffix(endpoint, "/usage") {
				for name, value := range contract.Usage.Values {
					if name == "oai-language" {
						value = "ja-JP"
					}
					if name == "accept-language" {
						value = "ja-JP,ja;q=0.9"
					}
					require.Equal(t, value, header.Get(name), name)
				}
				require.Empty(t, header.Get("accept"))
			} else {
				require.Equal(t, client.ElectronUserAgent(), header.Get("user-agent"))
				require.Empty(t, header.Get("x-openai-codex-pricing-chooser"))
			}
		})
	}
}
