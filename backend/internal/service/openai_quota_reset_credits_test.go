package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseOpenAIRateLimitResetCreditDetails_PreservesAvailableCreditOrder(t *testing.T) {
	body := []byte(`{
		"availableCount":"2",
		"credits":[
			{"reset_type":"codex_rate_limits","status":"redeemed","expires_at":"2026-07-01T04:05:06Z"},
			{"reset_type":"codex_rate_limits","status":"available","expires_at":"2026-07-04T04:05:06Z"},
			{"resetType":"codex_rate_limits","status":"available","expiresAt":"2026-07-03T04:05:06Z"},
			{"reset_type":"other","status":"available","expires_at":"2026-07-02T04:05:06Z"}
		]
	}`)

	details, err := parseOpenAIRateLimitResetCreditDetails(body)
	require.NoError(t, err)
	require.NotNil(t, details.AvailableCount)
	require.Equal(t, 2, *details.AvailableCount)
	require.Equal(t, []OpenAIRateLimitResetCreditDetail{
		{ExpiresAt: "2026-07-04T04:05:06Z"},
		{ExpiresAt: "2026-07-03T04:05:06Z"},
	}, details.Credits)
}

func TestParseOpenAIRateLimitResetCreditDetailsReadsApplicableCount(t *testing.T) {
	details, err := parseOpenAIRateLimitResetCreditDetails([]byte(`{
		"available_count":4,
		"applicable_available_count":1
	}`))
	require.NoError(t, err)
	require.NotNil(t, details.AvailableCount)
	require.Equal(t, 4, *details.AvailableCount)
	require.NotNil(t, details.ApplicableAvailableCount)
	require.Equal(t, 1, *details.ApplicableAvailableCount)
}

func TestQueryUsageResetCreditCountPrecedence(t *testing.T) {
	tests := []struct {
		name        string
		usageBody   string
		detailBody  string
		wantCount   int
		wantCredits int
		wantNil     bool
	}{
		{
			name:       "detail count creates missing usage credits",
			usageBody:  `{}`,
			detailBody: `{"available_count":3,"credits":[{"expires_at":"2026-07-03T04:05:06Z"}]}`,
			wantCount:  3, wantCredits: 1,
		},
		{
			name:       "explicit detail zero overrides usage and records",
			usageBody:  `{"rate_limit_reset_credits":{"available_count":4}}`,
			detailBody: `{"available_count":0,"credits":[{"expires_at":"2026-07-03T04:05:06Z"}]}`,
			wantCount:  0, wantCredits: 1,
		},
		{
			name:       "available records override usage when detail count is absent",
			usageBody:  `{"rate_limit_reset_credits":{"available_count":7}}`,
			detailBody: `{"credits":[{"expires_at":"2026-07-03T04:05:06Z"},{"expiresAt":"2026-07-04T04:05:06Z"}]}`,
			wantCount:  2, wantCredits: 2,
		},
		{
			name:       "empty detail list overrides usage with zero",
			usageBody:  `{"rate_limit_reset_credits":{"available_count":7}}`,
			detailBody: `{"credits":[]}`,
			wantCount:  0,
		},
		{
			name:       "fully filtered list overrides usage with zero",
			usageBody:  `{"rate_limit_reset_credits":{"available_count":7}}`,
			detailBody: `{"credits":[{"reset_type":"codex_rate_limits","status":"redeemed","expires_at":"2026-07-03T04:05:06Z"},{"reset_type":"other","status":"available","expires_at":"2026-07-04T04:05:06Z"}]}`,
			wantCount:  0,
		},
		{
			name:       "available records without expiry still count",
			usageBody:  `{"rate_limit_reset_credits":{"available_count":7}}`,
			detailBody: `{"credits":[{"status":"available"},{"status":"available","expires_at":"2026-07-04T04:05:06Z"}]}`,
			wantCount:  2, wantCredits: 1,
		},
		{
			name:        "shape without count or list preserves usage details",
			usageBody:   `{"rate_limit_reset_credits":{"available_count":5,"credits":[{"expires_at":"usage-expiry"}]}}`,
			detailBody:  `{}`,
			wantCount:   5,
			wantCredits: 1,
		},
		{
			name:        "valid detail count survives malformed authoritative list",
			usageBody:   `{"rate_limit_reset_credits":{"available_count":7,"credits":[{"expires_at":"usage-expiry"}]}}`,
			detailBody:  `{"available_count":2,"credits":"malformed"}`,
			wantCount:   2,
			wantCredits: 1,
		},
		{
			name:       "valid detail count creates quota despite malformed authoritative list",
			usageBody:  `{}`,
			detailBody: `{"available_count":2,"credits":"malformed"}`,
			wantCount:  2,
		},
		{
			name:       "negative detail count without list preserves usage",
			usageBody:  `{"rate_limit_reset_credits":{"available_count":4}}`,
			detailBody: `{"available_count":-1}`,
			wantCount:  4,
		},
		{
			name:       "negative detail count falls back to available records",
			usageBody:  `{"rate_limit_reset_credits":{"available_count":4}}`,
			detailBody: `{"available_count":-1,"credits":[{"status":"available","expires_at":"2026-07-04T04:05:06Z"}]}`,
			wantCount:  1, wantCredits: 1,
		},
		{
			name:       "empty object preserves missing usage credits",
			usageBody:  `{}`,
			detailBody: `{}`,
			wantNil:    true,
		},
		{
			name:       "object rate limit reached type does not fail the quota query",
			usageBody:  `{"rate_limit_reached_type":{"type":"primary_window","metered_feature":"codex"}}`,
			detailBody: `{}`,
			wantNil:    true,
		},
		{
			name:       "null body preserves missing usage credits",
			usageBody:  `{}`,
			detailBody: `null`,
			wantNil:    true,
		},
		{
			name:       "empty body preserves missing usage credits",
			usageBody:  `{}`,
			detailBody: ``,
			wantNil:    true,
		},
		{
			name:       "null object record is not counted",
			usageBody:  `{"rate_limit_reset_credits":{"available_count":7}}`,
			detailBody: `{"credits":[null]}`,
			wantCount:  0,
		},
		{
			name:       "null top level record is not counted",
			usageBody:  `{"rate_limit_reset_credits":{"available_count":7}}`,
			detailBody: `[null]`,
			wantCount:  0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			account := &Account{
				ID:       100,
				Platform: PlatformOpenAI,
				Type:     AccountTypeOAuth,
				Status:   StatusActive,
				Credentials: map[string]any{
					"chatgpt_account_id": "org-parent123",
				},
			}
			repo := &stubQuotaAccountRepo{accounts: map[int64]*Account{100: account}}
			tokenCache := &stubQuotaTokenCache{tokens: map[string]string{
				OpenAITokenCacheKey(account): "fake-token",
			}}
			tokenProvider := NewOpenAITokenProvider(repo, tokenCache, nil)

			var detailCalls int
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("content-type", "application/json")
				switch r.URL.Path {
				case "/backend-api/wham/usage":
					_, _ = w.Write([]byte(tt.usageBody))
				case "/backend-api/wham/rate-limit-reset-credits":
					detailCalls++
					_, _ = w.Write([]byte(tt.detailBody))
				default:
					http.NotFound(w, r)
				}
			}))
			defer srv.Close()

			svc := NewOpenAIQuotaService(repo, nil, tokenProvider, newQuotaRedirectingFactory(srv))
			usage, err := svc.QueryUsage(context.Background(), 100)
			require.NoError(t, err)
			require.NotNil(t, usage)
			require.Equal(t, 1, detailCalls)
			if tt.wantNil {
				require.Nil(t, usage.RateLimitResetCredits)
				return
			}
			require.NotNil(t, usage.RateLimitResetCredits)
			require.Equal(t, tt.wantCount, usage.RateLimitResetCredits.AvailableCount)
			require.Len(t, usage.RateLimitResetCredits.Credits, tt.wantCredits)
		})
	}
}

func TestQueryUsageKeepsAggregationWithDesktopRequestProfile(t *testing.T) {
	account := &Account{
		ID:       100,
		Platform: PlatformOpenAI,
		Type:     AccountTypeOAuth,
		Status:   StatusActive,
		Credentials: map[string]any{
			"chatgpt_account_id": "account-100",
			"chatgpt_cookie":     "session=test-session",
		},
	}
	repo := &stubQuotaAccountRepo{accounts: map[int64]*Account{account.ID: account}}
	tokenCache := &stubQuotaTokenCache{tokens: map[string]string{
		OpenAITokenCacheKey(account): "access-token",
	}}
	tokenProvider := NewOpenAITokenProvider(repo, tokenCache, nil)
	var listCalls int

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodGet, r.Method)
		require.Equal(t, "Bearer access-token", r.Header.Get("authorization"))
		require.Equal(t, "account-100", r.Header.Get("chatgpt-account-id"))
		require.Empty(t, r.Header.Get("accept"))
		require.Equal(t, openaiQuotaCodexLanguageTag, r.Header.Get("oai-language"))
		require.Equal(t, codexDesktopOriginator, r.Header.Get("originator"))
		require.Equal(t, codexDesktopWebviewUserAgent, r.Header.Get("user-agent"))
		require.Equal(t, codexDesktopWebviewAcceptEncoding, r.Header.Get("accept-encoding"))
		require.Equal(t, codexDesktopWebviewAcceptLanguage, r.Header.Get("accept-language"))
		require.Equal(t, codexDesktopWebviewSecFetchSite, r.Header.Get("sec-fetch-site"))
		require.Equal(t, codexDesktopWebviewSecFetchMode, r.Header.Get("sec-fetch-mode"))
		require.Equal(t, codexDesktopWebviewSecFetchDest, r.Header.Get("sec-fetch-dest"))
		require.Equal(t, codexDesktopWebviewPriority, r.Header.Get("priority"))
		require.Equal(t, "session=test-session", r.Header.Get("cookie"))
		require.Empty(t, r.Header.Get("openai-beta"))
		require.Empty(t, r.Header.Get("x-openai-attach-auth"))
		require.Empty(t, r.Header.Get("x-openai-attach-integrity-state"))
		require.Empty(t, r.Header.Get("sec-ch-ua"))
		switch r.URL.Path {
		case "/backend-api/wham/usage":
			w.Header().Set("content-type", "application/json")
			_, _ = w.Write([]byte(`{"rate_limit_reset_credits":{"available_count":3}}`))
		case "/backend-api/wham/rate-limit-reset-credits":
			listCalls++
			w.Header().Set("content-type", "application/json")
			_, _ = w.Write([]byte(`{"available_count":2,"credits":[{"status":"available","expires_at":"2026-07-25T00:00:00Z"},{"status":"available","expires_at":"2026-07-26T00:00:00Z"}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	svc := NewOpenAIQuotaService(repo, nil, tokenProvider, newQuotaRedirectingFactory(srv))
	usage, err := svc.QueryUsage(context.Background(), account.ID)
	require.NoError(t, err)
	require.NotNil(t, usage.RateLimitResetCredits)
	require.Equal(t, 2, usage.RateLimitResetCredits.AvailableCount)
	require.Len(t, usage.RateLimitResetCredits.Credits, 2)
	require.Equal(t, 1, listCalls)
}

func TestOpenAIQuotaUsageDecodesLatestCodexDesktopShape(t *testing.T) {
	var usage OpenAIQuotaUsage
	err := json.Unmarshal([]byte(`{
		"user_id":"user-1",
		"account_id":"user-1",
		"email":"user@example.com",
		"plan_type":"free",
		"rate_limit":{"allowed":true,"limit_reached":false,"primary_window":{"used_percent":4,"limit_window_seconds":2592000,"reset_after_seconds":2576406,"reset_at":1787241505},"secondary_window":null},
		"code_review_rate_limit":null,
		"additional_rate_limits":null,
		"credits":{"has_credits":false,"unlimited":false,"overage_limit_reached":false,"balance":null,"approx_local_messages":null,"approx_cloud_messages":null},
		"spend_control":{"reached":false,"individual_limit":null},
		"rate_limit_reached_type":null,
		"promo":null,
		"rate_limit_reset_credits":{"available_count":2,"applicable_available_count":1}
	}`), &usage)
	require.NoError(t, err)
	require.NotNil(t, usage.RateLimit)
	require.NotNil(t, usage.RateLimit.PrimaryWindow)
	require.Equal(t, int64(2592000), usage.RateLimit.PrimaryWindow.LimitWindowSeconds)
	require.NotNil(t, usage.Credits)
	require.NotNil(t, usage.SpendControl)
	require.NotNil(t, usage.RateLimitResetCredits)
	require.Equal(t, 2, usage.RateLimitResetCredits.AvailableCount)
	require.NotNil(t, usage.RateLimitResetCredits.ApplicableAvailableCount)
	require.Equal(t, 1, *usage.RateLimitResetCredits.ApplicableAvailableCount)
}

func TestOpenAIQuotaUsageDecodesObjectRateLimitReachedType(t *testing.T) {
	var usage OpenAIQuotaUsage
	err := json.Unmarshal([]byte(`{
		"rate_limit_reached_type": {
			"type": "primary_window",
			"metered_feature": "codex"
		}
	}`), &usage)
	require.NoError(t, err)

	reachedType, ok := usage.RateLimitReachedType.(map[string]any)
	require.True(t, ok)
	require.Equal(t, "primary_window", reachedType["type"])
	require.Equal(t, "codex", reachedType["metered_feature"])
}

func TestResetCreditMatchesDesktopAutomaticRequest(t *testing.T) {
	account := &Account{
		ID:       100,
		Platform: PlatformOpenAI,
		Type:     AccountTypeOAuth,
		Status:   StatusActive,
		Credentials: map[string]any{
			"chatgpt_account_id": "account-100",
		},
	}
	repo := &stubQuotaAccountRepo{accounts: map[int64]*Account{account.ID: account}}
	tokenCache := &stubQuotaTokenCache{tokens: map[string]string{
		OpenAITokenCacheKey(account): "access-token",
	}}
	tokenProvider := NewOpenAITokenProvider(repo, tokenCache, nil)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodPost, r.Method)
		require.Equal(t, "/backend-api/wham/rate-limit-reset-credits/consume", r.URL.Path)
		require.Equal(t, "Bearer access-token", r.Header.Get("authorization"))
		require.Equal(t, "account-100", r.Header.Get("chatgpt-account-id"))
		require.Empty(t, r.Header.Get("accept"))
		require.Equal(t, openaiQuotaCodexLanguageTag, r.Header.Get("oai-language"))
		require.Equal(t, codexDesktopOriginator, r.Header.Get("originator"))
		require.Equal(t, codexDesktopWebviewUserAgent, r.Header.Get("user-agent"))
		require.Equal(t, codexDesktopWebviewAcceptEncoding, r.Header.Get("accept-encoding"))
		require.Equal(t, codexDesktopWebviewAcceptLanguage, r.Header.Get("accept-language"))
		require.Equal(t, codexDesktopWebviewPriority, r.Header.Get("priority"))
		require.Empty(t, r.Header.Get("openai-beta"))
		require.Empty(t, r.Header.Get("x-openai-attach-auth"))
		require.Empty(t, r.Header.Get("x-openai-attach-integrity-state"))
		require.Empty(t, r.Header.Get("sec-ch-ua"))

		var body map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		require.Regexp(t, `^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`, body["redeem_request_id"])
		_, hasCreditID := body["credit_id"]
		require.False(t, hasCreditID, "automatic mode must omit credit_id")

		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{"code":"reset","windows_reset":2}`))
	}))
	defer srv.Close()

	svc := NewOpenAIQuotaService(repo, nil, tokenProvider, newQuotaRedirectingFactory(srv))
	result, err := svc.ResetCredit(context.Background(), account.ID)
	require.NoError(t, err)
	require.Equal(t, "reset", result.Code)
	require.Equal(t, 2, result.WindowsReset)
}
