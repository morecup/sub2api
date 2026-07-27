package service

import (
	"net/http"

	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
)

// resolveUpstreamTLSProfile returns the client fingerprint to present to the
// upstream for this account, or nil to keep the stock Go handshake.
//
// Grok accounts resolve to the official Grok Build CLI profile (Rust/rustls +
// reqwest/h2), which is what makes xAI's TLS and HTTP/2 fingerprint clustering
// see a plausible client instead of a Go one. Codex/OpenAI accounts have no
// captured profile and resolve to nil, so their transport is unchanged.
func (s *OpenAIGatewayService) resolveUpstreamTLSProfile(account *Account) *tlsfingerprint.Profile {
	if s == nil || s.tlsFPProfileService == nil {
		return nil
	}
	return s.tlsFPProfileService.ResolveTLSProfile(account)
}

// doUpstreamRequest sends an upstream request through the shared client pool,
// applying the account's TLS/HTTP2 client fingerprint when one is resolved.
//
// This is the single upstream egress helper for the OpenAI-compatible gateway
// (Codex and Grok): it keeps profile resolution in one place so no call site
// can silently fall back to an unfingerprinted transport.
func (s *OpenAIGatewayService) doUpstreamRequest(req *http.Request, proxyURL string, account *Account) (*http.Response, error) {
	concurrency := 0
	accountID := int64(0)
	if account != nil {
		accountID = account.ID
		concurrency = account.Concurrency
	}
	return s.httpUpstream.DoWithTLS(req, proxyURL, accountID, concurrency, s.resolveUpstreamTLSProfile(account))
}
