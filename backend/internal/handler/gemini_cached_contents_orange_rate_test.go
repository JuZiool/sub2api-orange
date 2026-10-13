//go:build unit

package handler

import (
	"net/http"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestGeminiCacheCreateReservesModelRateAndBillsFailedInput(t *testing.T) {
	env := newCachedBillingEnv(t, 150, 900)
	env.group.ModelRateMultipliers = []service.ModelRateMultiplierRule{{Model: "gemini-2.5-pro", Multiplier: 2}}
	env.upstream.createBody = cachedBillingCreateBody(time.Now().Add(24 * time.Hour))
	rec := env.create()
	require.NotEqual(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, 1, env.upstream.methodCalls(http.MethodDelete))
	require.Empty(t, env.repo.records)
	env.waitForUsage(t, 1)
	log := env.usage.first()
	require.Equal(t, 2.0, log.RateMultiplier)
	require.InDelta(t, cachedBillingTokens*cachedBillingInputPrice*2, log.ActualCost, 1e-9)
}

func TestGeminiCachePatchModelRateRejectsBeforeUpstream(t *testing.T) {
	env := newCachedBillingEnv(t, 150, 900)
	env.group.ModelRateMultipliers = []service.ModelRateMultiplierRule{{Model: "gemini-2.5-pro", Multiplier: 2}}
	env.seed(time.Now().Add(time.Hour))
	rec := env.patch()
	require.NotEqual(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Zero(t, env.upstream.methodCalls(http.MethodPatch))
	require.Empty(t, env.usage.totals())
}

func TestGeminiCachePatchFreezesRequestedAliasRate(t *testing.T) {
	env := newCachedBillingEnv(t, 80, 900)
	env.group.ModelRateMultipliers = []service.ModelRateMultiplierRule{{Model: "gemini-alias", Multiplier: 0.5}}
	env.seed(time.Now().Add(time.Hour))
	env.repo.records["abc"].ChannelUsage = service.ChannelUsageFields{OriginalModel: "gemini-alias"}
	env.upstream.beforePost = func(int) {
		env.group.ModelRateMultipliers[0].Multiplier = 9
	}
	rec := env.patch()
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	env.waitForUsage(t, 1)
	log := env.usage.first()
	require.Equal(t, 0.5, log.RateMultiplier)
	require.InDelta(t, cachedBillingTokens*cachedBillingStoragePrice*23*0.5, log.ActualCost, 0.01)
}
