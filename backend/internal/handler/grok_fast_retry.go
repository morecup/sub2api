package handler

import (
	"github.com/gin-gonic/gin"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

// forwardWithGrokFastRetries keeps the selected account and its concurrency
// slot while retrying known Grok capacity/connection responses with no delay.
// The account selected for the single cross-account follow-up is attempted once.
func forwardWithGrokFastRetries(
	c *gin.Context,
	account *service.Account,
	state *service.OpenAIOAuth429FailoverState,
	forward func() (*service.OpenAIForwardResult, error),
) (*service.OpenAIForwardResult, error) {
	if account == nil || account.Platform != service.PlatformGrok {
		return forward()
	}

	maxAttempts := 1 + service.GrokFastSameAccountRetryCount
	phase := "primary"
	if state.GrokFollowupPending() {
		maxAttempts = 1
		phase = "account_followup"
	}
	defer service.SetOpsUpstreamRetryMetadata(c, 0, "")

	var result *service.OpenAIForwardResult
	var err error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		service.SetOpsUpstreamRetryMetadata(c, state.NextGrokUpstreamAttempt(), phase)
		result, err = forward()
		if err == nil || !service.IsGrokFastTransientFailoverError(err) || attempt+1 == maxAttempts {
			return result, err
		}
	}

	return result, err
}
