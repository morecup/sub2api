package repository

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestGatewayCacheGrokTurnIndexIsAtomicSharedAndPrivate(t *testing.T) {
	t.Parallel()

	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })

	first, ok := NewGatewayCache(client).(service.GrokTurnIndexStore)
	require.True(t, ok)
	second, ok := NewGatewayCache(client).(service.GrokTurnIndexStore)
	require.True(t, ok)

	const conversationID = "019fa403-private-conversation"
	ctx := context.Background()
	ttl := 12 * time.Hour

	got, err := first.ObserveGrokTurnIndex(ctx, 81, conversationID, 4, ttl)
	require.NoError(t, err)
	require.Equal(t, 4, got)

	got, err = second.ObserveGrokTurnIndex(ctx, 81, conversationID, 2, ttl)
	require.NoError(t, err)
	require.Equal(t, 4, got, "a second process must observe the shared maximum")

	// Retrying the same body-derived turn is a max operation, not an increment.
	got, err = second.ObserveGrokTurnIndex(ctx, 81, conversationID, 4, ttl)
	require.NoError(t, err)
	require.Equal(t, 4, got)

	keys := server.Keys()
	require.Len(t, keys, 1)
	require.True(t, strings.HasPrefix(keys[0], grokTurnIndexPrefix))
	require.NotContains(t, keys[0], conversationID)
	require.Equal(t, ttl, server.TTL(keys[0]))
}

func TestGatewayCacheGrokTurnIndexConcurrentMax(t *testing.T) {
	t.Parallel()

	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	store := NewGatewayCache(client).(service.GrokTurnIndexStore)

	var wait sync.WaitGroup
	for turn := 1; turn <= 32; turn++ {
		turn := turn
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, _ = store.ObserveGrokTurnIndex(context.Background(), 82, "conv-concurrent", turn, time.Hour)
		}()
	}
	wait.Wait()

	got, err := store.ObserveGrokTurnIndex(context.Background(), 82, "conv-concurrent", 1, time.Hour)
	require.NoError(t, err)
	require.Equal(t, 32, got)
}

func TestGatewayCacheGrokCompactionCatalogRoundTripAcrossInstances(t *testing.T) {
	t.Parallel()

	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	first := NewGatewayCache(client).(service.GrokCompactionCatalogStore)
	second := NewGatewayCache(client).(service.GrokCompactionCatalogStore)

	ctx := context.Background()
	_, found, err := first.GetGrokCompactionCatalog(ctx, 91)
	require.NoError(t, err)
	require.False(t, found)

	want := service.GrokCompactionCatalog{Models: map[string]service.GrokCompactionModelConfig{
		"grok-4.5": {
			ContextWindow:    500000,
			ThresholdPercent: 80,
			CompactionAt:     service.GrokCompactionAtConfig{Enabled: true, Value: 400000},
			CompactionsRemaining: service.GrokCompactionsRemainingConfig{
				Enabled: true,
				Dynamic: true,
			},
		},
	}}
	ttl := 6 * time.Hour
	require.NoError(t, first.SetGrokCompactionCatalog(ctx, 91, want, ttl))

	got, found, err := second.GetGrokCompactionCatalog(ctx, 91)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, want, got)
	require.Equal(t, ttl, server.TTL(grokCompactionCatalogKey(91)))

	// An empty but loaded catalog must survive distinctly from a cache miss.
	empty := service.GrokCompactionCatalog{Models: map[string]service.GrokCompactionModelConfig{}}
	require.NoError(t, first.SetGrokCompactionCatalog(ctx, 92, empty, ttl))
	got, found, err = second.GetGrokCompactionCatalog(ctx, 92)
	require.NoError(t, err)
	require.True(t, found)
	require.Empty(t, got.Models)
}
