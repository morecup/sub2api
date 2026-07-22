package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCodexDesktopResetCreditRequestsUseLatestWebviewProfile(t *testing.T) {
	account := &Account{
		ID:       100,
		Platform: PlatformOpenAI,
		Type:     AccountTypeOAuth,
		Status:   StatusActive,
		Credentials: map[string]any{
			"access_token":       "access-token",
			"chatgpt_account_id": "account-100",
			"chatgpt_cookie":     "session=test-session",
		},
	}

	var getCalls, postCalls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "Bearer access-token", r.Header.Get("authorization"))
		require.Equal(t, "account-100", r.Header.Get("chatgpt-account-id"))
		require.Equal(t, codexDesktopAPILanguage, r.Header.Get("oai-language"))
		require.Equal(t, codexDesktopOriginator, r.Header.Get("originator"))
		require.Equal(t, codexDesktopWebviewUserAgent, r.Header.Get("user-agent"))
		require.Equal(t, codexDesktopWebviewAcceptEncoding, r.Header.Get("accept-encoding"))
		require.Equal(t, codexDesktopWebviewAcceptLanguage, r.Header.Get("accept-language"))
		require.Equal(t, codexDesktopWebviewSecFetchSite, r.Header.Get("sec-fetch-site"))
		require.Equal(t, codexDesktopWebviewSecFetchMode, r.Header.Get("sec-fetch-mode"))
		require.Equal(t, codexDesktopWebviewSecFetchDest, r.Header.Get("sec-fetch-dest"))
		require.Equal(t, codexDesktopWebviewPriority, r.Header.Get("priority"))
		require.Equal(t, codexDesktopWebviewSentryTrace, r.Header.Get("sentry-trace"))
		require.Equal(t, codexDesktopWebviewBaggage, r.Header.Get("baggage"))
		require.Equal(t, "session=test-session", r.Header.Get("cookie"))
		require.Empty(t, r.Header.Get("accept"))
		require.Empty(t, r.Header.Get("openai-beta"))
		require.Empty(t, r.Header.Get("x-openai-attach-auth"))
		require.Empty(t, r.Header.Get("x-openai-attach-integrity-state"))
		require.Empty(t, r.Header.Get("sec-ch-ua"))

		w.Header().Set("content-type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/backend-api/wham/rate-limit-reset-credits":
			getCalls++
			require.Empty(t, r.Header.Get("content-type"))
			_, _ = w.Write([]byte(`{"available_count":1,"credits":[{"id":"credit-1","status":"available"}]}`))
		case r.Method == http.MethodPost && r.URL.Path == "/backend-api/wham/rate-limit-reset-credits/consume":
			postCalls++
			require.Equal(t, "application/json", r.Header.Get("content-type"))
			var body map[string]any
			require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
			require.Equal(t, "credit-1", body["credit_id"])
			require.Regexp(t, `^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`, body["redeem_request_id"])
			_, _ = w.Write([]byte(`{"code":"reset","windows_reset":1}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	svc := NewCodexDesktopAPIService(newQuotaRedirectingFactory(srv), nil)
	credits, err := svc.GetRateLimitResetCredits(context.Background(), account)
	require.NoError(t, err)
	require.Equal(t, []ResetCredit{{CreditID: "credit-1", Raw: map[string]any{"id": "credit-1", "status": "available"}}}, credits)

	result, err := svc.ConsumeRateLimitResetCredit(context.Background(), account, "credit-1", "")
	require.NoError(t, err)
	require.True(t, result.Success)
	require.Equal(t, 1, getCalls)
	require.Equal(t, 1, postCalls)
}
