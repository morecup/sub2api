package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type completionBindingCache struct {
	GatewayCache
	accountID   int64
	hasDeadline bool
	value       any
}

func (c *completionBindingCache) SetSessionAccountID(ctx context.Context, _ int64, _ string, id int64, _ time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	_, c.hasDeadline = ctx.Deadline()
	c.value = ctx.Value(completionBindingContextKey{})
	c.accountID = id
	return nil
}
func (c *completionBindingCache) GetSessionAccountID(context.Context, int64, string) (int64, error) {
	return c.accountID, nil
}

type completionBindingContextKey struct{}

func TestResponseAccountBindingSurvivesCompletedRequestCancellation(t *testing.T) {
	cache := &completionBindingCache{}
	ctx, cancel := context.WithCancel(context.WithValue(context.Background(), completionBindingContextKey{}, "trace-context"))
	cancel()
	store := NewOpenAIWSStateStore(cache)
	require.NoError(t, store.BindResponseAccount(ctx, 1, "resp_completed", 9, time.Hour))
	require.True(t, cache.hasDeadline)
	require.Equal(t, "trace-context", cache.value)
	// A fresh process-local store must recover the mapping from shared cache.
	restarted := NewOpenAIWSStateStore(cache)
	id, err := restarted.GetResponseAccount(context.Background(), 1, "resp_completed")
	require.NoError(t, err)
	require.EqualValues(t, 9, id)
}
