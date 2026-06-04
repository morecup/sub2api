package handler

import (
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestOpenAISameAccountRetryOverride(t *testing.T) {
	limit := 1
	delay := time.Duration(0)
	account := &service.Account{
		Type:        service.AccountTypeAPIKey,
		Credentials: map[string]any{"pool_mode": true, "pool_mode_retry_count": float64(5)},
	}
	failoverErr := &service.UpstreamFailoverError{
		RetryableOnSameAccount: true,
		SameAccountRetryLimit:  &limit,
		SameAccountRetryDelay:  &delay,
	}

	require.Equal(t, 1, openAISameAccountRetryLimit(account, failoverErr))
	require.Zero(t, openAISameAccountRetryDelay(failoverErr))
}

func TestOpenAISameAccountRetryFallsBackToAccountConfig(t *testing.T) {
	account := &service.Account{
		Type:        service.AccountTypeAPIKey,
		Credentials: map[string]any{"pool_mode": true, "pool_mode_retry_count": float64(5)},
	}
	failoverErr := &service.UpstreamFailoverError{RetryableOnSameAccount: true}

	require.Equal(t, 5, openAISameAccountRetryLimit(account, failoverErr))
	require.Equal(t, sameAccountRetryDelay, openAISameAccountRetryDelay(failoverErr))
}
