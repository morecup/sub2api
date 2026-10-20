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

const codexCapacity429TestMessage = "Codex is at capacity, please try again later"
const codexCapacity429TestBody = `{"detail":"Codex is at capacity, please try again later"}`

func TestCodexCapacity429Classification(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		want   bool
	}{
		{"detail", 429, codexCapacity429TestBody, true},
		{"error_message", 429, `{"error":{"message":"Codex is at capacity, please try again later"}}`, true},
		{"response_error", 429, `{"response":{"error":{"message":"Codex is at capacity, please try again later"}}}`, true},
		{"plain_text", 429, codexCapacity429TestMessage, true},
		{"case_and_punctuation", 429, `{"message":" CODEX is at capacity, please try again later. "}`, true},
		{"wrong_status", 503, codexCapacity429TestBody, false},
		{"quota", 429, `{"error":{"type":"usage_limit_reached","message":"You have hit your usage limit","resets_in_seconds":120}}`, false},
		{"echoed_capacity", 429, `{"error":{"message":"You have hit your usage limit"},"echo":"Codex is at capacity, please try again later"}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, isOpenAI429RetryWithoutCooldown(tc.status, nil, []byte(tc.body)))
		})
	}
}

type codexCapacity429Repo struct {
	errorPolicyRepoStub
	rateLimitCalls int
}

func (r *codexCapacity429Repo) SetRateLimited(context.Context, int64, time.Time) error {
	r.rateLimitCalls++
	return nil
}

func TestCodexCapacity429NeverCoolsDownAccount(t *testing.T) {
	for _, accountType := range []string{AccountTypeOAuth, AccountTypeAPIKey} {
		for _, retryAfter := range []string{"", "0.02", "300"} {
			t.Run(accountType+"/"+retryAfter, func(t *testing.T) {
				repo := &codexCapacity429Repo{}
				rateLimits := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
				gateway := &OpenAIGatewayService{rateLimitService: rateLimits}
				rateLimits.SetAccountRuntimeBlocker(gateway)
				account := &Account{
					ID: 1958, Platform: PlatformOpenAI, Type: accountType,
					Status: StatusActive, Schedulable: true,
					Credentials: map[string]any{
						"temp_unschedulable_enabled": true,
						"temp_unschedulable_rules": []any{map[string]any{
							"error_code": float64(429), "keywords": []any{"at capacity"},
							"duration_minutes": float64(30),
						}},
					},
				}
				body := []byte(codexCapacity429TestBody)
				headers := http.Header{}
				if retryAfter != "" {
					headers.Set("Retry-After", retryAfter)
				}
				// Exhausting an older retry window must not turn this capacity
				// response into a persistent or in-memory account cooldown.
				gateway.openaiOAuth429RetryStartedAt.Store(account.ID, time.Now().Add(-3*time.Minute))
				for attempt := 0; attempt < 3; attempt++ {
					ctx := context.Background()
					require.False(t, rateLimits.HandleUpstreamError(ctx, account, 429, headers, body, "gpt-5.6-sol"))
					rateLimits.handle429(ctx, account, headers, body)
					gateway.markOpenAIOAuth429RateLimited(ctx, account, headers, body)
					failure := gateway.failoverOpenAIUpstreamHTTPError(ctx, nil, account,
						&http.Response{StatusCode: 429, Header: headers}, body, codexCapacity429TestMessage, "gpt-5.6-sol")
					require.True(t, failure.IsOpenAI429RetryWithoutCooldown())
					require.True(t, failure.RetryableOnSameAccount)
					require.True(t, failure.RequestScopedTransient)
					require.True(t, failure.SkipRetryExhaustedTempUnschedule)
					require.False(t, failure.ShouldReportAccountScheduleFailure())
					require.Equal(t, 1, failure.EffectiveSameAccountRetryLimit(0))
					require.Equal(t, http.StatusTooManyRequests, failure.ClientStatusCode)
					require.Equal(t, codexCapacity429TestMessage, failure.ClientMessage)
					wantDelay := 100 * time.Millisecond
					if retryAfter == "0.02" {
						wantDelay = 20 * time.Millisecond
					} else if retryAfter == "300" {
						wantDelay = openAIOAuth429MaxRetryDelay
					}
					require.Equal(t, wantDelay, failure.SameAccountRetryDelay)
					require.False(t, gateway.isOpenAIAccountRuntimeBlocked(account))
					exhausted := *failure
					exhausted.RetryableOnSameAccount = false
					_, _, countsAgainstHealth := classifyOpenAIAPIKeyHealthFailure(&exhausted)
					require.False(t, countsAgainstHealth, "exhaustion must not trigger the API-key health breaker")
				}
				require.Zero(t, repo.rateLimitCalls)
				require.Zero(t, repo.tempCalls)
				require.Zero(t, repo.setErrCalls)
				require.Empty(t, repo.modelRateLimitCalls)
				require.Zero(t, gateway.openaiOAuth429WindowCount.Load())
			})
		}
	}
}

func TestCodexCapacity429DoesNotTreatQuotaHeadersAsTheCapacityCause(t *testing.T) {
	repo := &codexCapacity429Repo{}
	rateLimits := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	account := &Account{ID: 1958, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	headers := http.Header{
		"X-Codex-Primary-Used-Percent":        {"100"},
		"X-Codex-Primary-Reset-After-Seconds": {"120"},
		"X-Codex-Primary-Window-Minutes":      {"300"},
	}
	body := []byte(codexCapacity429TestBody)
	require.True(t, isOpenAI429RetryWithoutCooldown(429, headers, body))
	disposition, reset := classifyOpenAIOAuth429(headers, body)
	require.Equal(t, openAIOAuth429Transient, disposition)
	require.Nil(t, reset)
	rateLimits.handle429(context.Background(), account, headers, body)
	require.Zero(t, repo.rateLimitCalls)
}
