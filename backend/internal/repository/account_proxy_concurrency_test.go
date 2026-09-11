package repository

import (
	"context"
	"fmt"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
	"sync"
	"testing"
	"time"
)

func TestProxyPoolRoundRobinAndIndependentCapacity(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	cache := NewConcurrencyCache(client, 15, 900).(*concurrencyCache)
	ctx := context.Background()
	pool := []int64{11, 22, 33}
	for i := 0; i < 6; i++ {
		id, err := cache.AcquireAccountProxyPoolSlot(ctx, 7, pool, 2, fmt.Sprint(i))
		require.NoError(t, err)
		require.Equal(t, pool[i%3], id)
	}
	count, err := cache.GetAccountConcurrency(ctx, 7)
	require.NoError(t, err)
	require.Equal(t, 6, count)
	id, err := cache.AcquireAccountProxyPoolSlot(ctx, 7, pool, 2, "full")
	require.NoError(t, err)
	require.Zero(t, id)
	require.NoError(t, cache.ReleaseAccountProxyPoolSlot(ctx, 7, 22, "1"))
	id, err = cache.AcquireAccountProxyPoolSlot(ctx, 7, pool, 2, "replacement")
	require.NoError(t, err)
	require.Equal(t, int64(22), id)
	id, err = cache.AcquireAccountProxyPoolSlot(ctx, 7, pool, 2, "replacement")
	require.NoError(t, err)
	require.Equal(t, int64(22), id)
	count, err = cache.GetAccountConcurrency(ctx, 7)
	require.NoError(t, err)
	require.Equal(t, 6, count)
}

func TestProxyPoolConcurrentInstances(t *testing.T) {
	server := miniredis.RunT(t)
	clients := []*redis.Client{redis.NewClient(&redis.Options{Addr: server.Addr()}), redis.NewClient(&redis.Options{Addr: server.Addr()})}
	for _, c := range clients {
		t.Cleanup(func() { _ = c.Close() })
	}
	caches := []*concurrencyCache{NewConcurrencyCache(clients[0], 15, 900).(*concurrencyCache), NewConcurrencyCache(clients[1], 15, 900).(*concurrencyCache)}
	var wg sync.WaitGroup
	results := make(chan int64, 100)
	errs := make(chan error, 100)
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			p, e := caches[i%2].AcquireAccountProxyPoolSlot(context.Background(), 8, []int64{11, 22, 33}, 4, fmt.Sprint(i))
			results <- p
			errs <- e
		}(i)
	}
	wg.Wait()
	close(results)
	close(errs)
	for e := range errs {
		require.NoError(t, e)
	}
	counts := map[int64]int{}
	for p := range results {
		if p > 0 {
			counts[p]++
		}
	}
	require.Equal(t, map[int64]int{11: 4, 22: 4, 33: 4}, counts)
}

func TestProxyPoolExpiryAndSingleProxyLegacy(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	cache := NewConcurrencyCache(client, 15, 900).(*concurrencyCache)
	ctx := context.Background()
	ok, err := cache.AcquireAccountSlot(ctx, 9, 1, "legacy")
	require.NoError(t, err)
	require.True(t, ok)
	ok, err = cache.AcquireAccountSlot(ctx, 9, 1, "legacy-full")
	require.NoError(t, err)
	require.False(t, ok)
	p, err := cache.AcquireAccountProxyPoolSlot(ctx, 10, []int64{11, 22}, 1, "expired")
	require.NoError(t, err)
	require.Equal(t, int64(11), p)
	server.FastForward(16 * time.Minute)
	p, err = cache.AcquireAccountProxyPoolSlot(ctx, 10, []int64{11, 22}, 1, "new")
	require.NoError(t, err)
	require.Equal(t, int64(11), p)
	require.NoError(t, cache.ReleaseAccountProxyPoolSlot(ctx, 10, 11, "expired"))
	require.Equal(t, int64(1), client.ZCard(ctx, accountProxySlotKey(10, 11)).Val())
}

func TestProxyPoolPinnedTurnDoesNotMoveToFreeProxy(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	cache := NewConcurrencyCache(client, 15, 900).(*concurrencyCache)
	ctx := context.Background()
	id, err := cache.AcquireAccountProxyPoolSlot(ctx, 7, []int64{11, 22}, 1, "busy", 22)
	require.NoError(t, err)
	require.Equal(t, int64(22), id)
	id, err = cache.AcquireAccountProxyPoolSlot(ctx, 7, []int64{11, 22}, 1, "next-turn", 22)
	require.NoError(t, err)
	require.Zero(t, id)
	counts, err := cache.GetAccountProxyConcurrencyBatch(ctx, map[int64][]int64{7: {11, 22}})
	require.NoError(t, err)
	require.Equal(t, map[int64]int{11: 0, 22: 1}, counts[7])
	require.NoError(t, cache.ReleaseAccountProxyPoolSlot(ctx, 7, 22, "busy"))
	id, err = cache.AcquireAccountProxyPoolSlot(ctx, 7, []int64{11, 22}, 1, "next-turn", 22)
	require.NoError(t, err)
	require.Equal(t, int64(22), id)
}
