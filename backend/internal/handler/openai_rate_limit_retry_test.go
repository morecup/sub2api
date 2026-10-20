package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestOpenAIRateLimitRetry(t *testing.T) {
	for _, outcome := range []string{"success", "429", "different_failure", "stop_failure", "canceled", "output_started", "quota_429", "delayed_success", "delayed_429", "canceled_during_wait", "canceled_on_replay", "output_started_on_replay"} {
		t.Run(outcome, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil).WithContext(ctx)
			account := &service.Account{ID: 42, Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth}
			limited := &service.UpstreamFailoverError{
				StatusCode:                     http.StatusTooManyRequests,
				ResponseBody:                   []byte(`{"detail":"Rate limit exceeded"}`),
				Reason:                         service.GatewayFailureReason("openai_429_retry_without_cooldown"),
				RetryableOnSameAccount:         true,
				SuppressAccountScheduleFailure: true,
				ClientStatusCode:               http.StatusTooManyRequests,
				ClientMessage:                  "Rate limit exceeded",
			}
			if outcome == "quota_429" {
				limited.Reason = ""
			}
			if outcome == "delayed_success" || outcome == "delayed_429" {
				limited.SameAccountRetryDelay = 20 * time.Millisecond
			}
			if outcome == "canceled_during_wait" {
				limited.SameAccountRetryDelay = time.Second
			}
			attempts := 0
			var firstAttemptAt time.Time
			wantResult := &service.OpenAIForwardResult{}
			result, err := forwardWithAccountFastRetries(c, account, &service.OpenAIOAuth429FailoverState{}, func() (*service.OpenAIForwardResult, error) {
				attempts++
				if attempts == 1 {
					firstAttemptAt = time.Now()
					if outcome == "canceled" {
						cancel()
					}
					if outcome == "output_started" {
						_, _ = c.Writer.WriteString("data: semantic output\n\n")
					}
					if outcome == "canceled_during_wait" {
						timer := time.AfterFunc(5*time.Millisecond, cancel)
						t.Cleanup(func() { timer.Stop() })
					}
					return nil, limited
				}
				require.Equal(t, "same_account_rate_limit", c.GetString(service.OpsUpstreamRetryPhaseKey))
				if limited.SameAccountRetryDelay > 0 {
					require.GreaterOrEqual(t, time.Since(firstAttemptAt), limited.SameAccountRetryDelay)
				}
				if outcome == "success" || outcome == "delayed_success" {
					return wantResult, nil
				}
				if outcome == "different_failure" {
					return nil, &service.UpstreamFailoverError{StatusCode: http.StatusBadGateway, RetryableOnSameAccount: true}
				}
				if outcome == "stop_failure" {
					return nil, &service.UpstreamFailoverError{StatusCode: http.StatusServiceUnavailable, NextAccountAction: service.NextAccountStop}
				}
				if outcome == "canceled_on_replay" {
					cancel()
				}
				if outcome == "output_started_on_replay" {
					_, _ = c.Writer.WriteString("data: semantic output\n\n")
				}
				return nil, limited
			})
			require.Empty(t, c.GetString(service.OpsUpstreamRetryPhaseKey))
			if outcome == "canceled" || outcome == "canceled_during_wait" || outcome == "output_started" || outcome == "quota_429" {
				require.Equal(t, 1, attempts)
			} else {
				require.Equal(t, 2, attempts)
			}
			if outcome == "success" || outcome == "delayed_success" {
				require.NoError(t, err)
				require.Same(t, wantResult, result)
				return
			}
			var next *service.UpstreamFailoverError
			require.ErrorAs(t, err, &next)
			require.True(t, limited.ShouldRetryNextAccount(), "the original error must not be mutated")
			if outcome == "quota_429" {
				require.Same(t, limited, next)
				return
			}
			wantNextAccount := outcome == "429" || outcome == "delayed_429" || outcome == "different_failure"
			require.Equal(t, wantNextAccount, next.ShouldRetryNextAccount())
			require.False(t, next.RetryableOnSameAccount, "the outer loop must not retry this account again")
			if outcome == "429" || outcome == "delayed_429" {
				require.False(t, next.ShouldReportAccountScheduleFailure())
				(&OpenAIGatewayHandler{}).handleFailoverExhausted(c, next, false)
				require.Equal(t, http.StatusTooManyRequests, recorder.Code)
				require.Contains(t, recorder.Body.String(), "Rate limit exceeded")
			}
		})
	}
}
