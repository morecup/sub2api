package handler

import (
	"context"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

func openAISameAccountRetryLimit(account *service.Account, failoverErr *service.UpstreamFailoverError) int {
	if failoverErr != nil && failoverErr.SameAccountRetryLimit != nil {
		if *failoverErr.SameAccountRetryLimit < 0 {
			return 0
		}
		return *failoverErr.SameAccountRetryLimit
	}
	if account == nil {
		return 0
	}
	return account.GetPoolModeRetryCount()
}

func openAISameAccountRetryDelay(failoverErr *service.UpstreamFailoverError) time.Duration {
	if failoverErr != nil && failoverErr.SameAccountRetryDelay != nil {
		if *failoverErr.SameAccountRetryDelay < 0 {
			return 0
		}
		return *failoverErr.SameAccountRetryDelay
	}
	return sameAccountRetryDelay
}

func waitOpenAISameAccountRetry(ctx context.Context, delay time.Duration) bool {
	if delay <= 0 {
		return ctx.Err() == nil
	}
	select {
	case <-ctx.Done():
		return false
	case <-time.After(delay):
		return true
	}
}
