package service

import (
	"context"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

func multiProxyIDs(ids []int64) []int64 {
	if len(ids) < 2 {
		return nil
	}
	return append([]int64(nil), ids...)
}

func (s *adminServiceImpl) validateAccountProxyIDs(ctx context.Context, ids []int64) ([]int64, error) {
	if len(ids) > 64 {
		return nil, infraerrors.BadRequest("INVALID_PROXY_POOL", "at most 64 proxies can be selected")
	}
	out := make([]int64, 0, len(ids))
	seen := make(map[int64]bool, len(ids))
	for _, id := range ids {
		if id <= 0 {
			return nil, infraerrors.BadRequest("INVALID_PROXY_POOL", "proxy IDs must be positive")
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		p, err := s.proxyRepo.GetByID(ctx, id)
		if err != nil {
			return nil, err
		}
		if p == nil || !p.IsActive() || p.IsExpired(time.Now()) {
			return nil, infraerrors.BadRequest("INVALID_PROXY_POOL", "selected proxy is inactive or expired")
		}
		out = append(out, id)
	}
	return out, nil
}
