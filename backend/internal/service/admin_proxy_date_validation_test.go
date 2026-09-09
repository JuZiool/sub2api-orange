//go:build unit

package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestAdminProxyRejectsOutOfRangeExpiry(t *testing.T) {
	for _, year := range []int{-1, 10000} {
		date := time.Date(year, 1, 1, 0, 0, 0, 0, time.UTC)
		repo := &updatingProxyRepoStub{
			proxyRepoStub: &proxyRepoStub{},
			proxy: &Proxy{
				ID:             9,
				Protocol:       "http",
				Host:           "old.example",
				Port:           8080,
				Status:         StatusActive,
				FallbackMode:   FallbackModeNone,
				ExpiryWarnDays: 7,
			},
		}
		svc := &adminServiceImpl{proxyRepo: repo}

		_, err := svc.CreateProxy(context.Background(), &CreateProxyInput{
			Name:      "p",
			Protocol:  "http",
			Host:      "new.example",
			Port:      8080,
			ExpiresAt: &date,
		})
		require.Error(t, err, "create must reject out-of-range expiry year %d", year)

		_, err = svc.UpdateProxy(context.Background(), 9, &UpdateProxyInput{
			ExpiresAt:      &date,
			FallbackMode:   FallbackModeNone,
			ExpiryWarnDays: 7,
		})
		require.Error(t, err, "update must reject out-of-range expiry year %d", year)
		require.Equal(t, 0, repo.updateCalls, "invalid dates must never reach persistence")
	}
}
