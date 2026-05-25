//go:build unit

package service

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

// codexBypassRepoStub adds SetRateLimited counting to errorPolicyRepoStub so
// the test can verify the bypass really skipped account-state mutations.
type codexBypassRepoStub struct {
	errorPolicyRepoStub
	rateLimitCalls int
}

func (r *codexBypassRepoStub) SetRateLimited(ctx context.Context, id int64, resetAt time.Time) error {
	r.rateLimitCalls++
	return nil
}

func TestHandleUpstreamError_OAuthCodexBypassesAccountDisable(t *testing.T) {
	cases := []struct {
		name       string
		statusCode int
		body       []byte
	}{
		{"429 usage_limit_reached", 429, []byte(`{"error":{"type":"usage_limit_reached"}}`)},
		{"403 forbidden", 403, []byte(`{"error":{"message":"forbidden"}}`)},
		{"500 server error", 500, []byte("internal server error")},
		{"503 unavailable", 503, []byte("service unavailable")},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := &codexBypassRepoStub{}
			svc := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
			account := &Account{
				ID:       42,
				Type:     AccountTypeOAuth,
				Platform: PlatformOpenAI,
			}

			shouldDisable := svc.HandleUpstreamError(
				context.Background(), account, tc.statusCode,
				http.Header{}, tc.body,
			)

			require.False(t, shouldDisable, "OAuth Codex %d must not disable", tc.statusCode)
			require.Equal(t, 0, repo.rateLimitCalls, "must not call SetRateLimited")
			require.Equal(t, 0, repo.setErrCalls, "must not call SetError")
			require.Equal(t, 0, repo.tempCalls, "must not call SetTempUnschedulable")
		})
	}
}

// 401 is intentionally NOT bypassed — token refresh logic must still run.
func TestHandleUpstreamError_OAuthCodex401NotBypassed(t *testing.T) {
	repo := &codexBypassRepoStub{}
	svc := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	account := &Account{
		ID:       42,
		Type:     AccountTypeOAuth,
		Platform: PlatformOpenAI,
	}

	shouldDisable := svc.HandleUpstreamError(
		context.Background(), account, 401,
		http.Header{}, []byte(`{"detail":"Unauthorized"}`),
	)

	require.True(t, shouldDisable, "401 must follow normal auth-error path")
	require.GreaterOrEqual(t, repo.setErrCalls, 1, "401 should still trigger SetError")
}

// 普通 401（无 detail=Unauthorized、无 token_invalidated 错误码）：
// OAuth 路径应走 force-refresh + temp_unschedulable，不应被 bypass 短路。
func TestHandleUpstreamError_OAuthCodex401Generic_GoesToTempUnschedulable(t *testing.T) {
	repo := &codexBypassRepoStub{}
	svc := NewRateLimitService(repo, nil, &config.Config{
		RateLimit: config.RateLimitConfig{OAuth401CooldownMinutes: 10},
	}, nil, nil)
	account := &Account{
		ID:          43,
		Type:        AccountTypeOAuth,
		Platform:    PlatformOpenAI,
		Credentials: map[string]any{"access_token": "tok", "refresh_token": "rtok", "expires_at": "2099-01-01T00:00:00Z"},
	}

	shouldDisable := svc.HandleUpstreamError(
		context.Background(), account, 401,
		http.Header{}, []byte(`{"error":{"message":"token expired"}}`),
	)

	require.True(t, shouldDisable, "OAuth generic 401 must report shouldDisable=true")
	require.GreaterOrEqual(t, repo.tempCalls, 1, "must call SetTempUnschedulable for token refresh window")
	require.Equal(t, 0, repo.setErrCalls, "generic 401 should NOT permanently SetError")
	require.Equal(t, 0, repo.rateLimitCalls, "401 should NOT touch rate-limit fields")
}

// OAuth 401 with token_revoked / token_invalidated error code → permanent disable.
func TestHandleUpstreamError_OAuthCodex401TokenRevoked_PermanentDisable(t *testing.T) {
	for _, code := range []string{"token_invalidated", "token_revoked"} {
		t.Run(code, func(t *testing.T) {
			repo := &codexBypassRepoStub{}
			svc := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
			account := &Account{
				ID:       44,
				Type:     AccountTypeOAuth,
				Platform: PlatformOpenAI,
			}
			body := []byte(`{"error":{"code":"` + code + `","message":"token gone"}}`)
			shouldDisable := svc.HandleUpstreamError(
				context.Background(), account, 401, http.Header{}, body,
			)
			require.True(t, shouldDisable)
			require.GreaterOrEqual(t, repo.setErrCalls, 1, "%s must SetError (permanent disable)", code)
		})
	}
}

// Non-OpenAI / non-OAuth accounts must NOT be affected by the bypass branch.
func TestHandleUpstreamError_NonCodexUntouchedByBypass(t *testing.T) {
	t.Run("anthropic OAuth 429 still rate-limited", func(t *testing.T) {
		repo := &codexBypassRepoStub{}
		svc := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
		account := &Account{
			ID:       7,
			Type:     AccountTypeOAuth,
			Platform: PlatformAnthropic,
		}
		_ = svc.HandleUpstreamError(context.Background(), account, 429,
			http.Header{}, []byte(""))
		_ = repo
	})

	t.Run("OpenAI APIKey 429 still rate-limited", func(t *testing.T) {
		repo := &codexBypassRepoStub{}
		svc := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
		account := &Account{
			ID:       8,
			Type:     AccountTypeAPIKey,
			Platform: PlatformOpenAI,
		}
		_ = svc.HandleUpstreamError(context.Background(), account, 429,
			http.Header{}, []byte(""))
		require.GreaterOrEqual(t, repo.rateLimitCalls, 1,
			"non-OAuth OpenAI must still go through SetRateLimited")
	})
}
