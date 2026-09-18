//go:build unit

package service

import (
	"context"
	"net/http"
	"testing"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

func TestGrokQuotaServiceProbesBypassSchedulingGate(t *testing.T) {
	future := time.Now().Add(time.Hour)
	past := time.Now().Add(-time.Hour)
	for _, state := range []struct {
		name   string
		mutate func(*Account)
	}{
		{"manual_pause", func(a *Account) { a.Schedulable = false }},
		{"rate_limited", func(a *Account) { a.RateLimitResetAt = &future }},
		{"overloaded", func(a *Account) { a.OverloadUntil = &future }},
		{"cooldown", func(a *Account) { a.TempUnschedulableUntil = &future }},
		{"disabled", func(a *Account) { a.Status = StatusDisabled }},
		{"error", func(a *Account) { a.Status = StatusError }},
		{"auto_paused", func(a *Account) { a.AutoPauseOnExpired = true; a.ExpiresAt = &past }},
	} {
		for _, path := range []string{"billing", "active_probe", "forced_usage_refresh"} {
			t.Run(state.name+"/"+path, func(t *testing.T) {
				account := healthyGrokQuotaOAuthAccount(701)
				state.mutate(account)
				before := *account
				repo := &grokQuotaAccountRepo{mockAccountRepoForPlatform: &mockAccountRepoForPlatform{
					accountsByID: map[int64]*Account{account.ID: account},
				}}
				upstream := &grokHybridUpstream{weeklyUsagePercent: f64p(25)}
				provider := NewGrokTokenProvider(repo, nil)
				svc := NewGrokQuotaService(repo, nil, provider, upstream, nil)
				ctx := context.Background()

				// Model traffic remains blocked even with a valid OAuth token.
				_, err := provider.GetAccessToken(ctx, account)
				require.ErrorIs(t, err, errOAuthRefreshAccountStateChanged)

				wantRequests := 2
				switch path {
				case "billing":
					result, err := svc.ProbeBilling(ctx, account.ID)
					require.NoError(t, err)
					require.NotNil(t, result.Billing)
					require.Equal(t, 25.0, *result.Billing.UsagePercent)
				case "active_probe":
					result, err := svc.ProbeUsage(ctx, account.ID)
					require.NoError(t, err)
					require.True(t, result.HeadersObserved)
					wantRequests = 1
				case "forced_usage_refresh":
					usageService := &AccountUsageService{
						grokQuotaFetcher: NewGrokQuotaFetcher(), grokQuotaService: svc, cache: NewUsageCache(),
					}
					usage, err := usageService.getGrokUsage(ctx, account, true)
					require.NoError(t, err)
					require.NotNil(t, usage.GrokBilling)
					require.Equal(t, 25.0, *usage.GrokBilling.UsagePercent)
				}
				requests, _ := upstream.snapshot()
				require.Len(t, requests, wantRequests)
				for _, req := range requests {
					require.Equal(t, "Bearer access-token", req.Header.Get("Authorization"))
					require.Equal(t, grokCLIVersion, req.Header.Get("X-Grok-Client-Version"))
					if path != "active_probe" {
						require.Equal(t, http.MethodGet, req.Method)
						require.Equal(t, "/v1/billing", req.URL.Path)
					}
				}
				require.Equal(t, before.Status, account.Status)
				require.Equal(t, before.Schedulable, account.Schedulable)
				require.Equal(t, before.RateLimitResetAt, account.RateLimitResetAt)
				require.Equal(t, before.OverloadUntil, account.OverloadUntil)
				require.Equal(t, before.TempUnschedulableUntil, account.TempUnschedulableUntil)
				_, err = provider.GetAccessToken(ctx, account)
				require.ErrorIs(t, err, errOAuthRefreshAccountStateChanged)
			})
		}
	}
}

func TestGrokQuotaServiceProbeStillRejectsInvalidCredentials(t *testing.T) {
	for _, tc := range []struct {
		name    string
		mutate  func(*Account)
		message string
	}{
		{"missing_refresh_token", func(a *Account) { delete(a.Credentials, "refresh_token") }, "refresh token is missing"},
		{"expired", func(a *Account) { a.Credentials["expires_at"] = time.Now().Add(-time.Hour).Format(time.RFC3339) }, "refresh is not configured"},
		{"missing_proxy", func(a *Account) { id := int64(8); a.ProxyID = &id }, "configured proxy is missing"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			account := healthyGrokQuotaOAuthAccount(702)
			account.Schedulable = false
			tc.mutate(account)
			repo := &grokQuotaAccountRepo{mockAccountRepoForPlatform: &mockAccountRepoForPlatform{
				accountsByID: map[int64]*Account{account.ID: account},
			}}
			upstream := &grokHybridUpstream{}
			svc := NewGrokQuotaService(repo, nil, NewGrokTokenProvider(repo, nil), upstream, nil)
			_, err := svc.ProbeBilling(context.Background(), account.ID)
			require.Equal(t, "GROK_QUOTA_TOKEN_UNAVAILABLE", infraerrors.Reason(err))
			require.ErrorContains(t, err, tc.message)
			requests, _ := upstream.snapshot()
			require.Empty(t, requests)
		})
	}
}

func TestGrokQuotaServiceProbeRefreshesExpiredTokenWhileCoolingDown(t *testing.T) {
	account := healthyGrokQuotaOAuthAccount(703)
	future := time.Now().Add(time.Hour)
	account.TempUnschedulableUntil = &future
	account.Credentials["expires_at"] = time.Now().Add(-time.Hour).Format(time.RFC3339)
	repo := &grokQuotaAccountRepo{mockAccountRepoForPlatform: &mockAccountRepoForPlatform{
		accountsByID: map[int64]*Account{account.ID: account},
	}}
	refreshRepo := &refreshAPIAccountRepo{account: account}
	executor := &refreshAPIExecutorStub{needsRefresh: true, credentials: map[string]any{
		"access_token": "refreshed-token", "refresh_token": "rotated-refresh",
		"expires_at": time.Now().Add(2 * time.Hour).Format(time.RFC3339),
	}}
	cache := &grokTokenCacheForProviderTest{lockResult: true}
	provider := NewGrokTokenProvider(repo, cache)
	provider.SetRefreshAPI(NewOAuthRefreshAPI(refreshRepo, cache), executor)
	upstream := &grokHybridUpstream{}
	svc := NewGrokQuotaService(repo, nil, provider, upstream, nil)
	_, err := svc.ProbeBilling(context.Background(), account.ID)
	require.NoError(t, err)
	require.Equal(t, 1, executor.refreshCalls)
	require.Equal(t, 1, refreshRepo.successCASCalls)
	require.Equal(t, 1, cache.releaseCalls)
	requests, _ := upstream.snapshot()
	require.Len(t, requests, 2)
	for _, req := range requests {
		require.Equal(t, "Bearer refreshed-token", req.Header.Get("Authorization"))
	}
	require.Equal(t, &future, account.TempUnschedulableUntil)
}

func TestGrokTokenProviderManualTestRejectsProxyChangeDuringRefresh(t *testing.T) {
	account := healthyGrokQuotaOAuthAccount(704)
	account.Credentials["expires_at"] = time.Now().Add(-time.Hour).Format(time.RFC3339)
	proxyID := int64(1)
	account.ProxyID = &proxyID
	account.Proxy = &Proxy{ID: proxyID, Protocol: "http", Host: "original.example", Port: 8080}
	repo := &refreshAPIAccountRepo{account: account}
	executor := &refreshAPIExecutorStub{needsRefresh: true, credentials: map[string]any{
		"access_token": "rotated-token", "refresh_token": "rotated-refresh",
		"expires_at": time.Now().Add(time.Hour).Format(time.RFC3339),
	}, onRefresh: func() {
		current := *account
		newProxyID := int64(2)
		current.ProxyID = &newProxyID
		current.Proxy = &Proxy{ID: newProxyID, Protocol: "http", Host: "replacement.example", Port: 8080}
		current.Credentials = map[string]any{
			"access_token": "new-account-token", "refresh_token": "new-account-refresh",
			"expires_at": time.Now().Add(time.Hour).Format(time.RFC3339),
		}
		repo.account = &current
	}}
	provider := NewGrokTokenProvider(repo, nil)
	provider.SetRefreshAPI(NewOAuthRefreshAPI(repo, nil), executor)
	token, err := provider.GetAccessTokenForManualTest(context.Background(), account)
	require.ErrorIs(t, err, errOAuthRefreshAccountStateChanged)
	require.Empty(t, token)
	require.Zero(t, repo.updateCredentialsCalls, "a concurrent proxy change must not be overwritten")
}
