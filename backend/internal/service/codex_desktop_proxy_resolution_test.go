package service

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"testing"

	"github.com/imroc/req/v3"
	"github.com/stretchr/testify/require"
)

type codexProxyLookupStub struct {
	ProxyRepository
	err error
}

func (r *codexProxyLookupStub) GetByID(context.Context, int64) (*Proxy, error) { return nil, r.err }

func TestCodexDesktopConfiguredProxyFailsClosed(t *testing.T) {
	proxyID := int64(7)
	a := &Account{ID: 1, ProxyID: &proxyID, Credentials: map[string]any{"access_token": "test-token", "chatgpt_account_id": "test-account"}}
	for _, repo := range []ProxyRepository{nil, &codexProxyLookupStub{}, &codexProxyLookupStub{err: errors.New("lookup failed")}} {
		calls := 0
		svc := NewCodexDesktopAPIService(func(string) (*req.Client, error) { calls++; return nil, errors.New("must not dial") }, repo)
		_, err := svc.GetRateLimitResetCredits(context.Background(), a)
		require.ErrorContains(t, err, "configured proxy")
		require.Zero(t, calls)
	}
	svc := NewCodexDesktopAPIService(nil, nil)
	url, err := svc.resolveProxyURL(context.Background(), &Account{})
	require.NoError(t, err)
	require.Empty(t, url)
}

func TestCodexQuotaAndWSConfiguredProxyFailsClosed(t *testing.T) {
	proxyID := int64(7)
	account := &Account{ID: 817, Platform: PlatformOpenAI, Type: AccountTypeOAuth, ProxyID: &proxyID, Credentials: map[string]any{"chatgpt_account_id": "local-fixture"}}
	repo := &stubQuotaAccountRepo{accounts: map[int64]*Account{817: account}}
	cache := &stubQuotaTokenCache{tokens: map[string]string{OpenAITokenCacheKey(account): "local-dummy"}}
	for _, proxies := range []ProxyRepository{nil, &codexProxyLookupStub{}, &codexProxyLookupStub{err: errors.New("private connection failure")}} {
		calls := 0
		svc := NewOpenAIQuotaService(repo, proxies, NewOpenAITokenProvider(repo, cache, nil), func(string) (*req.Client, error) { calls++; return nil, errors.New("must not dial") })
		_, err := svc.QueryUsage(context.Background(), 817)
		require.ErrorContains(t, err, "configured OpenAI proxy")
		require.NotContains(t, err.Error(), "private connection")
		require.Zero(t, calls)
	}
	_, _, err := (&OpenAIGatewayService{}).buildOpenAIWSHeaders(context.Background(), nil, account, "local-dummy", OpenAIWSProtocolDecision{}, true, "", "", "fixture")
	require.ErrorContains(t, err, "configured OpenAI proxy")
}

func TestCodexQuotaCookieJarIsolatedByAccountAndProxy(t *testing.T) {
	svc := NewOpenAIQuotaService(nil, nil, nil, func(string) (*req.Client, error) { return req.C(), nil })
	first, err := svc.desktopClient(1, "org-one", "http://same-proxy")
	require.NoError(t, err)
	u, err := url.Parse("https://chatgpt.com/backend-api/wham/usage")
	require.NoError(t, err)
	first.GetClient().Jar.SetCookies(u, []*http.Cookie{{Name: "fixture", Value: "account-one", Path: "/"}})
	again, err := svc.desktopClient(1, "org-one", "http://same-proxy")
	require.NoError(t, err)
	require.Same(t, first, again)
	cookies, err := again.GetCookies(u.String())
	require.NoError(t, err)
	require.Len(t, cookies, 1)
	for _, tc := range []struct {
		accountID  int64
		org, proxy string
	}{{2, "org-two", "http://same-proxy"}, {1, "org-replaced", "http://same-proxy"}, {1, "org-one", "http://different-proxy"}} {
		other, err := svc.desktopClient(tc.accountID, tc.org, tc.proxy)
		require.NoError(t, err)
		cookies, err := other.GetCookies(u.String())
		require.NoError(t, err)
		require.Empty(t, cookies)
	}
}
