package repository

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestAPIKeyRepository_GetByKeyForAuthLoadsFallbackGroup(t *testing.T) {
	repo, client := newAPIKeyRepoSQLite(t)
	ctx := context.Background()
	user := mustCreateAPIKeyRepoUser(t, ctx, client, "getbykey-auth-fallback-unit@test.com")

	primary, err := client.Group.Create().
		SetName("primary-auth-fallback-unit").
		SetPlatform(service.PlatformOpenAI).
		SetStatus(service.StatusActive).
		SetSubscriptionType(service.SubscriptionTypeStandard).
		SetRateMultiplier(1).
		Save(ctx)
	require.NoError(t, err)
	fallback, err := client.Group.Create().
		SetName("fallback-auth-fallback-unit").
		SetPlatform(service.PlatformOpenAI).
		SetStatus(service.StatusActive).
		SetSubscriptionType(service.SubscriptionTypeStandard).
		SetRateMultiplier(2.5).
		SetRpmLimit(60).
		SetPeakRateEnabled(true).
		SetPeakStart("09:00").
		SetPeakEnd("18:00").
		SetPeakRateMultiplier(1.4).
		Save(ctx)
	require.NoError(t, err)

	key := &service.APIKey{
		UserID:          user.ID,
		Key:             "sk-getbykey-auth-fallback-unit",
		Name:            "Fallback Key Unit",
		GroupID:         &primary.ID,
		FallbackGroupID: &fallback.ID,
		Status:          service.StatusActive,
	}
	require.NoError(t, repo.Create(ctx, key))

	got, err := repo.GetByKeyForAuth(ctx, key.Key)
	require.NoError(t, err)
	require.Equal(t, &primary.ID, got.GroupID)
	require.Equal(t, &fallback.ID, got.FallbackGroupID)
	require.NotNil(t, got.FallbackGroup)
	require.Equal(t, fallback.ID, got.FallbackGroup.ID)
	require.Equal(t, service.PlatformOpenAI, got.FallbackGroup.Platform)
	require.Equal(t, service.StatusActive, got.FallbackGroup.Status)
	require.Equal(t, 2.5, got.FallbackGroup.RateMultiplier)
	require.Equal(t, 60, got.FallbackGroup.RPMLimit)
	require.True(t, got.FallbackGroup.PeakRateEnabled)
	require.Equal(t, "09:00", got.FallbackGroup.PeakStart)
	require.Equal(t, "18:00", got.FallbackGroup.PeakEnd)
	require.Equal(t, 1.4, got.FallbackGroup.PeakRateMultiplier)

	keys, err := repo.ListKeysByGroupID(ctx, fallback.ID)
	require.NoError(t, err)
	require.Equal(t, []string{key.Key}, keys)
}
