package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type routingTestCache struct {
	*proxyLegacyCache
	ids                        []int64
	selected, pinned, released int64
	err                        error
}

func (c *routingTestCache) AcquireAccountProxyPoolSlot(_ context.Context, _ int64, ids []int64, _ int, _ string, pinned ...int64) (int64, error) {
	c.ids = append([]int64(nil), ids...)
	if len(pinned) > 0 {
		c.pinned = pinned[0]
	}
	return c.selected, c.err
}
func (c *routingTestCache) ReleaseAccountProxyPoolSlot(_ context.Context, _ int64, id int64, _ string) error {
	c.released = id
	return nil
}
func proxyRoutingAccount() *Account {
	first := int64(11)
	return &Account{ID: 7, ProxyID: &first, Concurrency: 2, ProxyIDs: []int64{11, 22}, Proxies: []*Proxy{{ID: 11, Status: StatusActive}, {ID: 22, Status: StatusActive}}}
}
func TestProxyPoolRoutingAndReleaseMatch(t *testing.T) {
	cache := &routingTestCache{proxyLegacyCache: &proxyLegacyCache{}, selected: 22}
	svc := NewConcurrencyService(cache)
	original := proxyRoutingAccount()
	account := original
	result, err := svc.AcquireAccountRoute(context.Background(), &account, 2)
	require.NoError(t, err)
	require.True(t, result.Acquired)
	require.Equal(t, int64(22), *account.ProxyID)
	require.Equal(t, int64(22), account.Proxy.ID)
	require.Equal(t, int64(11), *original.ProxyID)
	require.Zero(t, original.SelectedProxyID)
	result.ReleaseFunc()
	require.Equal(t, int64(22), cache.released)
	// A second WebSocket turn reserves the same outbound proxy.
	_, err = svc.AcquireAccountRoute(context.Background(), &account, 2)
	require.NoError(t, err)
	require.Equal(t, int64(22), cache.pinned)
	require.Equal(t, 4, account.TotalConcurrency())
}
func TestProxyPoolUnavailableNeverFallsBackToDirect(t *testing.T) {
	cache := &routingTestCache{proxyLegacyCache: &proxyLegacyCache{acquireResult: true}, err: errors.New("redis down")}
	account := proxyRoutingAccount()
	original := account
	_, err := NewConcurrencyService(cache).AcquireAccountRoute(context.Background(), &account, 2)
	require.ErrorContains(t, err, "redis down")
	require.Same(t, original, account)
	account.Proxies = nil
	_, err = NewConcurrencyService(cache).AcquireAccountRoute(context.Background(), &account, 2)
	require.ErrorContains(t, err, "no available proxy")
}
func TestProxyPoolSingleProxyKeepsLegacyAcquisition(t *testing.T) {
	cache := &proxyLegacyCache{acquireResult: true}
	account := proxyRoutingAccount()
	account.ProxyIDs = []int64{11}
	original := account
	result, err := NewConcurrencyService(cache).AcquireAccountRoute(context.Background(), &account, 2)
	require.NoError(t, err)
	require.True(t, result.Acquired)
	require.Same(t, original, account)
	result.ReleaseFunc()
	require.Equal(t, []int64{7}, cache.releasedAccountIDs)
}
func TestProxyPoolSkipsInactiveAndExpiredProxies(t *testing.T) {
	cache := &routingTestCache{proxyLegacyCache: &proxyLegacyCache{}, selected: 22}
	account := proxyRoutingAccount()
	expired := time.Now().Add(-time.Minute)
	account.Proxies[0].ExpiresAt = &expired
	result, err := NewConcurrencyService(cache).AcquireAccountRoute(context.Background(), &account, 2)
	require.NoError(t, err)
	require.True(t, result.Acquired)
	require.Equal(t, []int64{22}, cache.ids)
}
func TestProxyPoolRejectsUnavailableSelectedRouteAndReleases(t *testing.T) {
	cache := &routingTestCache{proxyLegacyCache: &proxyLegacyCache{}, selected: 99}
	account := proxyRoutingAccount()
	_, err := NewConcurrencyService(cache).AcquireAccountRoute(context.Background(), &account, 2)
	require.Error(t, err)
	require.Equal(t, int64(99), cache.released)
}

type proxyLegacyCache struct {
	ConcurrencyCache
	acquireResult      bool
	releasedAccountIDs []int64
}

func (c *proxyLegacyCache) AcquireAccountSlot(context.Context, int64, int, string) (bool, error) {
	return c.acquireResult, nil
}
func (c *proxyLegacyCache) ReleaseAccountSlot(_ context.Context, id int64, _ string) error {
	c.releasedAccountIDs = append(c.releasedAccountIDs, id)
	return nil
}

func TestProxyPoolRejectsStaleLegacyReservation(t *testing.T) {
	current := proxyRoutingAccount()
	stale := *current
	stale.ProxyIDs = nil
	require.Error(t, validateProxyReservation(&stale, current))
	current.SelectedProxyID = 22
	hydrated, err := current.WithProxyRoute(22)
	require.NoError(t, err)
	require.NoError(t, validateProxyReservation(current, hydrated))
	hydrated.Concurrency++
	require.Error(t, validateProxyReservation(current, hydrated))
}
