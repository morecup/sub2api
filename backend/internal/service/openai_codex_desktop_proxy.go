package service

// 本文件承载最新版 Codex Desktop App 的非 Responses 业务请求透传。
// 这些请求（插件、MCP、analytics、wham usage 等）不应套用 Responses body
// 归一化；这里只做安全的路径约束、账号认证和响应转发。

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

const codexDesktopBackendAPIBaseURL = "https://chatgpt.com/backend-api"

// ProxyCodexDesktopEndpoint forwards one allow-listed Codex Desktop backend
// endpoint through an OAuth account. The caller supplies the already selected
// account and the exact local /backend-api/... path; no request-body
// normalization is performed because plugin/MCP/usage payloads are not
// Responses payloads.
func (s *OpenAIGatewayService) ProxyCodexDesktopEndpoint(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	path string,
	body []byte,
) (*http.Response, error) {
	if s == nil || account == nil {
		return nil, fmt.Errorf("codex desktop proxy account is unavailable")
	}
	if account.Type != AccountTypeOAuth || account.Platform != PlatformOpenAI {
		return nil, fmt.Errorf("codex desktop endpoint requires an OpenAI OAuth account")
	}
	path = strings.TrimSpace(path)
	if !strings.HasPrefix(path, "/backend-api/") {
		return nil, fmt.Errorf("invalid codex desktop endpoint path")
	}
	method := http.MethodGet
	if c != nil && c.Request != nil && c.Request.Method != "" {
		method = c.Request.Method
	}
	targetURL := codexDesktopBackendAPIBaseURL + strings.TrimPrefix(path, "/backend-api")
	if c != nil && c.Request != nil && c.Request.URL != nil && c.Request.URL.RawQuery != "" {
		targetURL += "?" + c.Request.URL.RawQuery
	}

	token, _, err := s.GetAccessToken(ctx, account)
	if err != nil {
		return nil, fmt.Errorf("get codex desktop account token: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, method, targetURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req = req.WithContext(WithHTTPUpstreamProfile(req.Context(), HTTPUpstreamProfileOpenAI))
	req.Host = "chatgpt.com"

	// 只保留 Desktop 业务请求需要的无敏感头；鉴权、账号和 UA 在下面统一覆写。
	if c != nil && c.Request != nil {
		for key, values := range c.Request.Header {
			lower := strings.ToLower(strings.TrimSpace(key))
			switch {
			case lower == "accept", lower == "accept-language", lower == "content-type",
				lower == "oai-language", lower == "oai-product-sku", lower == "mcp-protocol-version",
				lower == "originator", lower == "version", lower == "x-codex-beta-features",
				lower == "x-codex-installation-id", lower == "x-codex-window-id",
				lower == "x-codex-turn-state", lower == "x-codex-turn-metadata",
				lower == "x-oai-attestation", lower == "oai-did",
				lower == "sec-ch-ua", lower == "sec-ch-ua-mobile", lower == "sec-ch-ua-platform",
				lower == "sec-fetch-site", lower == "sec-fetch-mode", lower == "sec-fetch-dest",
				lower == "priority",
				strings.HasPrefix(lower, "sentry-"), lower == "baggage":
				for _, value := range values {
					req.Header.Add(key, value)
				}
			}
		}
	}
	req.Header.Del("authorization")
	req.Header.Del("x-api-key")
	authHeaders, err := s.buildOpenAIAuthenticationHeaders(ctx, account, token)
	if err != nil {
		return nil, fmt.Errorf("build codex desktop authentication: %w", err)
	}
	for key, values := range authHeaders {
		for _, value := range values {
			req.Header.Add(key, value)
		}
	}
	if err := resolveAndSetOpenAIChatGPTAccountHeaders(ctx, s.accountRepo, req.Header, account); err != nil {
		return nil, fmt.Errorf("resolve chatgpt account headers: %w", err)
	}
	// Desktop 0.145 的 Electron fetch wrapper 对 /wham/usage 会带浏览器 UA，
	// 而插件/Responses 请求带 Codex Desktop UA；有入站值时原样保留，空值才
	// 回退到官方 Desktop 画像，避免把两类最新版抓包混成单一旧画像。
	incomingUA := ""
	incomingOriginator := ""
	if c != nil && c.Request != nil {
		incomingUA = strings.TrimSpace(c.Request.Header.Get("user-agent"))
		incomingOriginator = strings.TrimSpace(c.Request.Header.Get("originator"))
	}
	if incomingUA != "" {
		req.Header.Set("user-agent", incomingUA)
	} else {
		req.Header.Set("user-agent", codexDesktopUserAgent)
	}
	if incomingOriginator != "" {
		req.Header.Set("originator", incomingOriginator)
	} else {
		req.Header.Set("originator", codexDesktopOriginator)
	}
	if req.Header.Get("accept") == "" {
		req.Header.Set("accept", "application/json")
	}
	if len(body) > 0 && req.Header.Get("content-type") == "" {
		req.Header.Set("content-type", "application/json")
	}
	account.ApplyHeaderOverrides(req.Header)

	proxyURL := ""
	if account.ProxyID != nil && account.Proxy != nil {
		proxyURL = account.Proxy.URL()
	}
	return s.httpUpstream.Do(req, proxyURL, account.ID, account.Concurrency)
}

// WriteCodexDesktopProxyResponse copies a generic upstream response to Gin.
// Hop-by-hop headers are intentionally excluded; streaming bodies are copied
// without buffering so MCP/SSE endpoints remain usable.
func WriteCodexDesktopProxyResponse(c *gin.Context, resp *http.Response) {
	if c == nil || resp == nil {
		return
	}
	for key, values := range resp.Header {
		lower := strings.ToLower(key)
		if lower == "connection" || lower == "keep-alive" || lower == "transfer-encoding" || lower == "upgrade" {
			continue
		}
		for _, value := range values {
			c.Header(key, value)
		}
	}
	c.Status(resp.StatusCode)
	if resp.Body == nil {
		return
	}
	defer resp.Body.Close()
	_, _ = io.Copy(c.Writer, resp.Body)
}
