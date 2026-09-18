package service

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
)

// resolveUpstreamTLSProfile returns the client fingerprint to present to the
// upstream for this account, or nil to keep the stock Go handshake.
//
// Grok accounts resolve to Grok Build CLI; OpenAI OAuth accounts resolve to
// Codex Desktop. Both official clients use Rust/rustls + reqwest/h2, with
// distinct provider and header-order details. OpenAI API Key accounts remain
// on the stock transport because they are not Desktop subscription traffic.
func (s *OpenAIGatewayService) resolveUpstreamTLSProfile(account *Account) *tlsfingerprint.Profile {
	if s == nil || s.tlsFPProfileService == nil {
		return nil
	}
	return s.tlsFPProfileService.ResolveTLSProfile(account)
}

// openAIWSTransportScope keeps TLS tickets and cached dial transports isolated
// per upstream account. It deliberately contains no credential material.
func openAIWSTransportScope(account *Account) string {
	if account == nil || account.ID <= 0 {
		return ""
	}
	return "openai-account:" + strconv.FormatInt(account.ID, 10)
}

// doUpstreamRequest sends an upstream request through the shared client pool,
// applying the account's TLS/HTTP2 client fingerprint when one is resolved.
//
// This is the single upstream egress helper for the OpenAI-compatible gateway
// (Codex and Grok): it keeps profile resolution in one place so no call site
// can silently fall back to an unfingerprinted transport.
func (s *OpenAIGatewayService) doUpstreamRequest(req *http.Request, proxyURL string, account *Account) (*http.Response, error) {
	if account != nil && account.IsOpenAIOAuth() && account.ProxyID != nil && strings.TrimSpace(proxyURL) == "" {
		return nil, errors.New("configured OpenAI proxy is unavailable")
	}
	concurrency := 0
	accountID := int64(0)
	if account != nil {
		accountID = account.ID
		concurrency = account.Concurrency
	}
	profile := s.resolveUpstreamTLSProfile(account)
	cookies := s.codexCookies.prepare(req, account, proxyURL)
	started := time.Now()
	resp, err := s.httpUpstream.DoWithTLS(req, proxyURL, accountID, concurrency, profile)
	s.observeCodexHTTPRequest(req, resp, account, proxyURL, profile, time.Since(started), err)
	if cookies != nil && resp != nil {
		cookieURL := req.URL
		if resp.Request != nil {
			cookieURL = resp.Request.URL
		}
		if cookieURL != nil && cookieURL.Scheme == "https" && strings.EqualFold(cookieURL.Hostname(), "chatgpt.com") {
			cookies.receive(cookieURL, resp.Cookies())
		}
	}
	return resp, err
}
