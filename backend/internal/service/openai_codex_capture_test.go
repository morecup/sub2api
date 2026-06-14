package service

import (
	"bytes"
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// TestCaptureCodexWireThroughMitm 用真实生产构造函数构建上游请求，经本地 mitmdump 代理发送，
// 以便用 mitmproxy 抓取 sub2api 实际出站请求(头线序 + zstd 体)。默认跳过；
// 运行：CODEX_CAPTURE=1 + mitmdump 监听 127.0.0.1:38080（addon 已短路，不会真正出网）。
func TestCaptureCodexWireThroughMitm(t *testing.T) {
	if os.Getenv("CODEX_CAPTURE") != "1" {
		t.Skip("set CODEX_CAPTURE=1 (with mitmdump on :38080) to run the wire capture")
	}
	gin.SetMode(gin.TestMode)

	proxyURL, err := url.Parse("http://127.0.0.1:38080")
	require.NoError(t, err)
	client := &http.Client{Transport: &http.Transport{
		Proxy:              http.ProxyURL(proxyURL),
		TLSClientConfig:    &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // 抓包用，接受 mitm 证书
		ForceAttemptHTTP2:  true,                                  // 对齐生产 OpenAI h2 协议模式（保证 h2 头线序）
		DisableCompression: true,                                  // 对齐生产：抑制 Go 自动 accept-encoding 协商
	}}

	svc := &OpenAIGatewayService{cfg: &config.Config{}}
	account := &Account{
		Platform:    PlatformOpenAI,
		Type:        AccountTypeOAuth,
		Credentials: map[string]any{"access_token": "fake", "chatgpt_account_id": "acct-capture"},
	}
	body := []byte(`{"model":"gpt-5.1-codex","instructions":"local","input":[{"type":"message","role":"user","content":"hi"}]}`)

	newCtx := func(path string) *gin.Context {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Request = httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
		c.Request.Header.Set("Content-Type", "application/json")
		return c
	}
	send := func(label string, req *http.Request, buildErr error) {
		require.NoError(t, buildErr)
		req.Header.Set("x-capture-label", label)
		resp, err := client.Do(req)
		require.NoError(t, err)
		_ = resp.Body.Close()
	}

	cNative := newCtx("/v1/responses")
	rNative, eNative := svc.buildUpstreamRequest(cNative.Request.Context(), cNative, account, body, "fake-token", true, "cap-seed", true)
	send("native", rNative, eNative)

	cPass := newCtx("/v1/responses")
	rPass, ePass := svc.buildUpstreamRequestOpenAIPassthrough(cPass.Request.Context(), cPass, account, body, "fake-token")
	send("passthrough", rPass, ePass)

	cCompact := newCtx("/v1/responses/compact")
	rCompact, eCompact := svc.buildUpstreamRequest(cCompact.Request.Context(), cCompact, account, body, "fake-token", true, "cap-seed", true)
	send("compact", rCompact, eCompact)
}
