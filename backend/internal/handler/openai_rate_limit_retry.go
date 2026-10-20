package handler

import (
	"errors"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// forwardWithAccountFastRetries keeps the selected account and concurrency slot
// for a bare OpenAI 429 replay. Returning to the scheduler could select another
// account, so this retry is completed before the outer failover loop resumes.
func forwardWithAccountFastRetries(
	c *gin.Context,
	account *service.Account,
	state *service.OpenAIOAuth429FailoverState,
	forward func() (*service.OpenAIForwardResult, error),
) (*service.OpenAIForwardResult, error) {
	if account == nil || account.Platform != service.PlatformOpenAI {
		return forwardWithGrokFastRetries(c, account, state, forward)
	}
	writtenBefore := service.OpenAICompactKeepaliveAdjustedWrittenSize(c)
	result, err := forward()
	var failoverErr *service.UpstreamFailoverError
	if !errors.As(err, &failoverErr) || !failoverErr.IsOpenAI429RetryWithoutCooldown() {
		return result, err
	}
	if openAIRequestAllowsFailoverReplay(c) && openAIForwardMayFailover(c, writtenBefore, failoverErr) {
		if sleepWithContext(c.Request.Context(), failoverErr.SameAccountRetryDelay) &&
			openAIRequestAllowsFailoverReplay(c) && openAIForwardMayFailover(c, writtenBefore, failoverErr) {
			logger.FromContext(c.Request.Context()).Warn("openai.rate_limit_same_account_retry",
				zap.Int64("account_id", account.ID),
				zap.Int("retry_count", 1),
				zap.Duration("retry_delay", failoverErr.SameAccountRetryDelay),
			)
			previousPhase, _ := c.Get(service.OpsUpstreamRetryPhaseKey)
			c.Set(service.OpsUpstreamRetryPhaseKey, "same_account_rate_limit")
			defer c.Set(service.OpsUpstreamRetryPhaseKey, previousPhase)
			result, err = forward()
		}
	}
	// The in-slot replay consumes this account's retry budget. Let the outer
	// failover loop exclude it and select the next candidate by priority; preserve
	// explicit stop decisions from a different failure on the replay.
	if errors.As(err, &failoverErr) {
		next := *failoverErr
		next.RetryableOnSameAccount = false
		if next.IsOpenAI429RetryWithoutCooldown() {
			next.NextAccountAction = service.NextAccountRetry
		}
		if !openAIRequestAllowsFailoverReplay(c) || !openAIForwardMayFailover(c, writtenBefore, failoverErr) {
			next.NextAccountAction = service.NextAccountStop
		}
		return result, &next
	}
	return result, err
}
