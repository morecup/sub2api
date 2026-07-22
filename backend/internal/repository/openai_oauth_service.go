package repository

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/imroc/req/v3"
)

// Codex /oauth/token 实抓画像（2026-07-21/22，见 docs/codex-desktop-capture/
// 2026-07-21_0.145.0-alpha.27/raw/ 下 auth_login_exchange_flows.txt 与
// auth_refresh_flows_desktop.txt）：
//   - authorization_code 交换：form-urlencoded，无 originator/user-agent 头
//   - refresh：JSON body（裸 application/json，仅 client_id/grant_type/refresh_token），
//     originator: Codex Desktop，UA 为不带应用版本段的 Desktop 形态
const (
	openAIOAuthOriginator = "Codex Desktop"
	openAIOAuthUserAgent  = "Codex Desktop/0.145.0-alpha.27 (Windows 10.0.26100; x86_64) unknown"
)

// 请求体字段顺序与实抓 JSON 一致（client_id, grant_type, refresh_token）。
type openAITokenRefreshRequest struct {
	ClientID     string `json:"client_id"`
	GrantType    string `json:"grant_type"`
	RefreshToken string `json:"refresh_token"`
}

// NewOpenAIOAuthClient creates a new OpenAI OAuth client
func NewOpenAIOAuthClient() service.OpenAIOAuthClient {
	return &openaiOAuthService{tokenURL: openai.TokenURL}
}

type openaiOAuthService struct {
	tokenURL string
}

// postOAuthTokenRequest 按实抓画像发送 /oauth/token 请求：JSON body（裸 application/json）、
// accept: */*、originator/user-agent 为 Codex Desktop 形态。result 用于承接成功响应解析。
func (s *openaiOAuthService) postOAuthTokenRequest(ctx context.Context, client *req.Client, payload any, result *openai.TokenResponse) (*req.Response, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	return client.R().
		SetContext(ctx).
		SetHeader("content-type", "application/json").
		SetHeader("accept", "*/*").
		SetHeader("originator", openAIOAuthOriginator).
		SetHeader("user-agent", openAIOAuthUserAgent).
		SetBody(body).
		SetSuccessResult(result).
		Post(s.tokenURL)
}

func (s *openaiOAuthService) ExchangeCode(ctx context.Context, code, codeVerifier, redirectURI, proxyURL, clientID string) (*openai.TokenResponse, error) {
	client, err := createOpenAIReqClient(proxyURL)
	if err != nil {
		return nil, infraerrors.Newf(http.StatusBadGateway, "OPENAI_OAUTH_CLIENT_INIT_FAILED", "create HTTP client: %v", err)
	}

	if redirectURI == "" {
		redirectURI = openai.DefaultRedirectURI
	}
	clientID = strings.TrimSpace(clientID)
	if clientID == "" {
		clientID = openai.ClientID
	}

	// 实抓（codex-rs 0.139 登录回调，见归档 auth_login_exchange_flows.txt）：
	// authorization_code 交换为 form-urlencoded，字段顺序 grant_type, code,
	// redirect_uri, client_id, code_verifier；无 originator/user-agent 头。
	form := "grant_type=" + url.QueryEscape("authorization_code") +
		"&code=" + url.QueryEscape(code) +
		"&redirect_uri=" + url.QueryEscape(redirectURI) +
		"&client_id=" + url.QueryEscape(clientID) +
		"&code_verifier=" + url.QueryEscape(codeVerifier)

	var tokenResp openai.TokenResponse
	resp, err := client.R().
		SetContext(ctx).
		SetHeader("content-type", "application/x-www-form-urlencoded").
		SetHeader("accept", "*/*").
		SetHeader("user-agent", "").
		SetBodyString(form).
		SetSuccessResult(&tokenResp).
		Post(s.tokenURL)

	if err != nil {
		if shouldReturnOpenAINoProxyHint(ctx, proxyURL, err) {
			return nil, newOpenAINoProxyHintError(err)
		}
		return nil, infraerrors.Newf(http.StatusBadGateway, "OPENAI_OAUTH_REQUEST_FAILED", "request failed: %v", err)
	}

	if !resp.IsSuccessState() {
		return nil, infraerrors.Newf(http.StatusBadGateway, "OPENAI_OAUTH_TOKEN_EXCHANGE_FAILED", "token exchange failed: status %d, body: %s", resp.StatusCode, resp.String())
	}

	return &tokenResp, nil
}

func (s *openaiOAuthService) RefreshToken(ctx context.Context, refreshToken, proxyURL string) (*openai.TokenResponse, error) {
	return s.RefreshTokenWithClientID(ctx, refreshToken, proxyURL, "")
}

func (s *openaiOAuthService) RefreshTokenWithClientID(ctx context.Context, refreshToken, proxyURL string, clientID string) (*openai.TokenResponse, error) {
	// 调用方应始终传入正确的 client_id；为兼容旧数据，未指定时默认使用 OpenAI ClientID
	clientID = strings.TrimSpace(clientID)
	if clientID == "" {
		clientID = openai.ClientID
	}
	return s.refreshTokenWithClientID(ctx, refreshToken, proxyURL, clientID)
}

func (s *openaiOAuthService) refreshTokenWithClientID(ctx context.Context, refreshToken, proxyURL, clientID string) (*openai.TokenResponse, error) {
	client, err := createOpenAIReqClient(proxyURL)
	if err != nil {
		return nil, infraerrors.Newf(http.StatusBadGateway, "OPENAI_OAUTH_CLIENT_INIT_FAILED", "create HTTP client: %v", err)
	}

	// 实抓：refresh 请求体仅 client_id/grant_type/refresh_token 三个字段（不含 scope）。
	payload := openAITokenRefreshRequest{
		ClientID:     clientID,
		GrantType:    "refresh_token",
		RefreshToken: refreshToken,
	}

	var tokenResp openai.TokenResponse
	resp, err := s.postOAuthTokenRequest(ctx, client, payload, &tokenResp)

	if err != nil {
		if shouldReturnOpenAINoProxyHint(ctx, proxyURL, err) {
			return nil, newOpenAINoProxyHintError(err)
		}
		return nil, infraerrors.Newf(http.StatusBadGateway, "OPENAI_OAUTH_REQUEST_FAILED", "request failed: %v", err)
	}

	if !resp.IsSuccessState() {
		return nil, infraerrors.Newf(http.StatusBadGateway, "OPENAI_OAUTH_TOKEN_REFRESH_FAILED", "token refresh failed: status %d, body: %s", resp.StatusCode, resp.String())
	}

	return &tokenResp, nil
}

func createOpenAIReqClient(proxyURL string) (*req.Client, error) {
	return getSharedReqClient(reqClientOptions{
		ProxyURL: proxyURL,
		Timeout:  120 * time.Second,
	})
}

func shouldReturnOpenAINoProxyHint(ctx context.Context, proxyURL string, err error) bool {
	if strings.TrimSpace(proxyURL) != "" || err == nil {
		return false
	}
	if ctx != nil && ctx.Err() != nil {
		return false
	}
	return !errors.Is(err, context.Canceled)
}

func newOpenAINoProxyHintError(cause error) error {
	return infraerrors.New(
		http.StatusBadGateway,
		"OPENAI_OAUTH_PROXY_REQUIRED",
		"OpenAI OAuth request failed: no proxy is configured and this server could not reach OpenAI directly. Select a proxy that can access OpenAI, then retry; if the authorization code has expired, regenerate the authorization URL.",
	).WithCause(cause)
}
