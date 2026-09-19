package service

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

type codexTicketProxyRepo struct {
	ProxyRepository
	proxies map[int64]*Proxy
	err     error
}

func (r *codexTicketProxyRepo) GetByID(_ context.Context, id int64) (*Proxy, error) {
	if r.err != nil {
		return nil, r.err
	}
	return r.proxies[id], nil
}

func activeCodexTicketProxy(id int64, host string) *Proxy {
	return &Proxy{ID: id, Name: host, Protocol: "http", Host: host, Port: 8080, Status: StatusActive}
}

func TestOpenAICodexTicketProxyURL_SelectionIsExact(t *testing.T) {
	now := time.Now()
	expiredAt := now.Add(-time.Minute)
	accountProxy := activeCodexTicketProxy(7, "account.example")
	selectedProxy := activeCodexTicketProxy(8, "selected.example")
	inactiveProxy := activeCodexTicketProxy(9, "inactive.example")
	inactiveProxy.Status = StatusDisabled
	expiredProxy := activeCodexTicketProxy(10, "expired.example")
	expiredProxy.ExpiresAt = &expiredAt
	repo := &codexTicketProxyRepo{proxies: map[int64]*Proxy{
		7: accountProxy, 8: selectedProxy, 9: inactiveProxy, 10: expiredProxy,
	}}
	svc := ticketTestService(t, config.OpenAICodexTicketConfig{HarvestProxyURL: "http://global.example:8080"}, nil)
	svc.proxyRepo = repo

	for _, tc := range []struct {
		name    string
		choice  any
		proxyID *int64
		proxy   *Proxy
		wantURL string
		ready   bool
	}{
		{name: "missing_uses_global", wantURL: "http://global.example:8080", ready: true},
		{name: "explicit_global", choice: "global", wantURL: "http://global.example:8080", ready: true},
		{name: "account_loaded", choice: "account", proxyID: codexTicketInt64Pointer(7), proxy: accountProxy, wantURL: "http://account.example:8080", ready: true},
		{name: "account_resolved", choice: "account", proxyID: codexTicketInt64Pointer(7), wantURL: "http://account.example:8080", ready: true},
		{name: "account_without_proxy_is_direct", choice: "account", wantURL: "", ready: true},
		{name: "selected_other_proxy", choice: "8", wantURL: "http://selected.example:8080", ready: true},
		{name: "selected_missing_does_not_fallback", choice: "999", ready: false},
		{name: "selected_inactive_does_not_fallback", choice: "9", ready: false},
		{name: "selected_expired_does_not_fallback", choice: "10", ready: false},
		{name: "invalid_choice_does_not_fallback", choice: "invalid", ready: false},
		{name: "numeric_choice_from_json", choice: float64(8), wantURL: "http://selected.example:8080", ready: true},
		{name: "invalid_non_string_does_not_fallback", choice: true, ready: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			account := ticketTestAccount(41)
			account.ProxyID = tc.proxyID
			account.Proxy = tc.proxy
			if tc.choice != nil {
				account.Extra = map[string]any{OpenAICodexTicketProxyExtraKey: tc.choice}
			}
			got, ready := svc.openAICodexTicketProxyURL(context.Background(), account)
			require.Equal(t, tc.ready, ready)
			require.Equal(t, tc.wantURL, got)
		})
	}

	repo.err = errors.New("lookup failed")
	account := ticketTestAccount(41)
	account.Extra = map[string]any{OpenAICodexTicketProxyExtraKey: "8"}
	got, ready := svc.openAICodexTicketProxyURL(context.Background(), account)
	require.False(t, ready)
	require.Empty(t, got)
}

func TestOpenAICodexTicketProbe_AccountDirectAndUnavailableSelection(t *testing.T) {
	model := openAICodexTicketDefaultModel
	response := codexTicketResponse()
	upstream := &httpUpstreamRecorder{responses: []*http.Response{response}}
	svc := ticketTestService(t, config.OpenAICodexTicketConfig{
		Enabled: true, HarvestProxyURL: "http://global.example:8080", Models: []string{model},
	}, upstream)
	svc.proxyRepo = &codexTicketProxyRepo{proxies: map[int64]*Proxy{}}
	account := ticketTestAccount(41)
	account.Extra = map[string]any{OpenAICodexTicketProxyExtraKey: "account"}
	svc.probeOnceOpenAICodexTicket(context.Background(), account, model)
	require.Len(t, upstream.requests, 1)
	require.Empty(t, upstream.lastProxyURL)
	require.NotNil(t, svc.lookupOpenAICodexTicket(account, model))

	missing := ticketTestAccount(42)
	missing.Extra = map[string]any{OpenAICodexTicketProxyExtraKey: "999"}
	svc.probeOnceOpenAICodexTicket(context.Background(), missing, model)
	require.Len(t, upstream.requests, 1)
}

func codexTicketInt64Pointer(value int64) *int64 { return &value }
