package service

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"time"
)

// Multi-proxy selection must be performed by the same distributed operation
// that reserves its capacity. Never fall back to direct traffic on cache errors.
func (s *ConcurrencyService) acquireAccountProxyPoolSlot(ctx context.Context, accountID int64, limit int, ids []int64, pinnedProxyIDs ...int64) (*AcquireResult, error) {
	cache, ok := s.cache.(accountProxyConcurrencyCache)
	if !ok {
		return nil, fmt.Errorf("account proxy pool concurrency is unavailable")
	}
	seen := make(map[int64]bool, len(ids))
	for _, id := range ids {
		if id <= 0 || seen[id] {
			return nil, fmt.Errorf("invalid or duplicate proxy ID %d", id)
		}
		seen[id] = true
	}
	requestID := generateRequestID()
	proxyID, err := cache.AcquireAccountProxyPoolSlot(ctx, accountID, ids, limit, requestID, pinnedProxyIDs...)
	if err != nil {
		return nil, err
	}
	if proxyID == 0 {
		return &AcquireResult{}, nil
	}
	var once sync.Once
	return &AcquireResult{Acquired: true, ProxyID: proxyID, ReleaseFunc: func() {
		once.Do(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := cache.ReleaseAccountProxyPoolSlot(ctx, accountID, proxyID, requestID); err != nil {
				// The lease expires even if Redis is unavailable during cleanup.
				slog.Warn("failed to release proxy slot", "account_id", accountID, "proxy_id", proxyID, "error", err)
			}
		})
	}}, nil
}

// WithProxyRoute copies the account snapshot. Shared scheduler snapshots must
// never be mutated by a request selecting a different outbound proxy.
func (a *Account) WithProxyRoute(proxyID int64) (*Account, error) {
	if proxyID == 0 {
		return a, nil
	}
	for _, p := range a.Proxies {
		if p != nil && p.ID == proxyID && p.IsActive() && !p.IsExpired(time.Now()) {
			copy := *a
			copy.ProxyID = &p.ID
			copy.Proxy = p
			copy.SelectedProxyID = proxyID
			return &copy, nil
		}
	}
	return nil, fmt.Errorf("selected proxy %d is unavailable for account %d", proxyID, a.ID)
}

func (r *AcquireResult) RouteAccount(a *Account) (*Account, error) {
	if r == nil || !r.Acquired || r.ProxyID == 0 {
		return a, nil
	}
	account, err := a.WithProxyRoute(r.ProxyID)
	if err != nil && r.ReleaseFunc != nil {
		r.ReleaseFunc()
	}
	return account, err
}

// AcquireAccountRoute updates only the caller's local pointer after Redis has
// reserved the selected proxy. The release closure owns that exact reservation.
func (s *ConcurrencyService) AcquireAccountRoute(ctx context.Context, account **Account, limit int) (*AcquireResult, error) {
	if account == nil || *account == nil {
		return nil, fmt.Errorf("missing account for proxy routing")
	}
	a := *account
	var result *AcquireResult
	var err error
	if len(a.ProxyIDs) > 1 {
		ids := make([]int64, 0, len(a.ProxyIDs))
		now := time.Now()
		for _, id := range a.ProxyIDs {
			for _, p := range a.Proxies {
				if p != nil && p.ID == id && p.IsActive() && !p.IsExpired(now) {
					ids = append(ids, id)
					break
				}
			}
		}
		if len(ids) == 0 {
			return nil, fmt.Errorf("account %d has no available proxy", a.ID)
		}
		result, err = s.acquireAccountProxyPoolSlot(ctx, a.ID, limit, ids, a.SelectedProxyID)
	} else {
		result, err = s.AcquireAccountSlot(ctx, a.ID, limit)
	}
	if err != nil || result == nil || !result.Acquired {
		return result, err
	}
	routed, err := result.RouteAccount(a)
	if err != nil {
		return nil, err
	}
	*account = routed
	return result, nil
}

// TotalConcurrency is used only for aggregate scheduling and wait capacity.
// Each proxy's admission limit remains the configured Concurrency value.
func (a *Account) TotalConcurrency() int {
	if a == nil {
		return 0
	}
	if len(a.ProxyIDs) > 1 && a.Concurrency > 0 {
		return a.Concurrency * len(a.ProxyIDs)
	}
	return a.Concurrency
}

func (a *Account) TotalLoadFactor() int {
	if a == nil {
		return 1
	}
	n := len(a.ProxyIDs)
	if n < 2 {
		n = 1
	}
	return a.EffectiveLoadFactor() * n
}

// ProxyPoolUsage exposes only names and counts, never proxy credentials.
type ProxyPoolUsage struct {
	ProxyID            int64  `json:"proxy_id"`
	ProxyName          string `json:"proxy_name"`
	CurrentConcurrency int    `json:"current_concurrency"`
	MaxConcurrency     int    `json:"max_concurrency"`
}

func (s *ConcurrencyService) GetProxyPoolUsage(ctx context.Context, accounts []Account) (map[int64][]ProxyPoolUsage, error) {
	out := make(map[int64][]ProxyPoolUsage)
	pools := make(map[int64][]int64)
	for _, a := range accounts {
		if len(a.ProxyIDs) > 1 {
			pools[a.ID] = a.ProxyIDs
		}
	}
	if len(pools) == 0 {
		return out, nil
	}
	cache, ok := s.cache.(interface {
		GetAccountProxyConcurrencyBatch(context.Context, map[int64][]int64) (map[int64]map[int64]int, error)
	})
	if !ok {
		return out, fmt.Errorf("proxy pool metrics unavailable")
	}
	counts, err := cache.GetAccountProxyConcurrencyBatch(ctx, pools)
	if err != nil {
		return out, err
	}
	for _, a := range accounts {
		if len(a.ProxyIDs) < 2 {
			continue
		}
		for _, id := range a.ProxyIDs {
			name := fmt.Sprintf("#%d", id)
			for _, p := range a.Proxies {
				if p != nil && p.ID == id {
					name = p.Name
					break
				}
			}
			out[a.ID] = append(out[a.ID], ProxyPoolUsage{ProxyID: id, ProxyName: name, CurrentConcurrency: counts[a.ID][id], MaxConcurrency: a.Concurrency})
		}
	}
	return out, nil
}

// A stale scheduler snapshot must not reserve a legacy slot and then hydrate
// into a multi-proxy account, or silently apply a different per-proxy limit.
func validateProxyReservation(selected, hydrated *Account) error {
	if len(selected.ProxyIDs) < 2 && len(hydrated.ProxyIDs) < 2 {
		return nil
	}
	if !slices.Equal(selected.ProxyIDs, hydrated.ProxyIDs) || selected.Concurrency != hydrated.Concurrency || selected.SelectedProxyID == 0 {
		return fmt.Errorf("account %d proxy configuration changed during admission", selected.ID)
	}
	return nil
}
