package service

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestImagesResponsesOAuthPreservesCapturedHTTPHeaders(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", nil)
	account := testCodexProfileAccount(7315, 5)
	account.Credentials = map[string]any{"access_token": "fixture-token", "chatgpt_account_id": "fixture-account"}
	exporter := newTestCodexTelemetry()
	upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: http.StatusBadRequest, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"error":{"message":"fixture stop"}}`))}}
	svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream, codexTelemetry: exporter}
	svc.codexTelemetryOnce.Do(func() {})
	_, _ = svc.forwardOpenAIImagesOAuth(context.Background(), c, account, &OpenAIImagesRequest{Endpoint: openAIImagesGenerationsEndpoint, Model: "gpt-image-1", Prompt: "fixture", N: 1}, "")
	require.Len(t, upstream.requests, 1)
	require.Equal(t, chatgptCodexURL, upstream.lastReq.URL.String())
	require.Empty(t, upstream.lastReq.Header.Get("OpenAI-Beta"))
	require.Equal(t, codexClientProfileForAccount(account).UserAgent(), upstream.lastReq.Header.Get("User-Agent"))
	require.Equal(t, codexDesktopOriginator, upstream.lastReq.Header.Get("originator"))
	require.Equal(t, "zstd", upstream.lastReq.Header.Get("Content-Encoding"))
}
