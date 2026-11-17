package service

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAPIKeyAuthSnapshotGroupForceOpenAIFastRoundtrip(t *testing.T) {
	groupID := int64(50)
	apiKey := &APIKey{
		ID: 82, UserID: 40, GroupID: &groupID, Key: "sk-fast-roundtrip", Status: StatusActive,
		User: &User{ID: 40, Status: StatusActive},
		Group: &Group{
			ID: groupID, Name: "fast-roundtrip", Platform: PlatformOpenAI, Status: StatusActive,
			Hydrated: true, ForceOpenAIFast: true, FreeOpenAIFast: true,
		},
	}
	svc := &APIKeyService{}

	payload, err := json.Marshal(&APIKeyAuthCacheEntry{Snapshot: svc.snapshotFromAPIKey(context.Background(), apiKey)})
	require.NoError(t, err)
	var cached APIKeyAuthCacheEntry
	require.NoError(t, json.Unmarshal(payload, &cached))

	materialized, used, err := svc.applyAuthCacheEntry(apiKey.Key, &cached)
	require.NoError(t, err)
	require.True(t, used)
	require.NotNil(t, materialized.Group)
	require.True(t, materialized.Group.Hydrated)
	require.True(t, materialized.Group.ForceOpenAIFast)
	require.True(t, materialized.Group.FreeOpenAIFast)
	require.Equal(t, apiKeyAuthSnapshotVersion, cached.Snapshot.Version)
}

func TestAPIKeyAuthSnapshotGroupForceOpenAIUltrafastRoundtrip(t *testing.T) {
	groupID := int64(51)
	apiKey := &APIKey{
		ID: 83, UserID: 40, GroupID: &groupID, Key: "sk-ultrafast-roundtrip", Status: StatusActive,
		User: &User{ID: 40, Status: StatusActive},
		Group: &Group{
			ID: groupID, Name: "ultrafast-roundtrip", Platform: PlatformOpenAI, Status: StatusActive,
			Hydrated: true, ForceOpenAIUltrafast: true,
		},
	}
	svc := &APIKeyService{}

	payload, err := json.Marshal(&APIKeyAuthCacheEntry{Snapshot: svc.snapshotFromAPIKey(context.Background(), apiKey)})
	require.NoError(t, err)
	require.Contains(t, string(payload), `"force_openai_ultrafast":true`)
	var cached APIKeyAuthCacheEntry
	require.NoError(t, json.Unmarshal(payload, &cached))

	materialized, used, err := svc.applyAuthCacheEntry(apiKey.Key, &cached)
	require.NoError(t, err)
	require.True(t, used)
	require.NotNil(t, materialized.Group)
	require.True(t, materialized.Group.ForceOpenAIUltrafast)
	require.False(t, materialized.Group.ForceOpenAIFast)
}

func TestAPIKeyAuthSnapshotGroupChainUltrafastRoundtrip(t *testing.T) {
	for _, primaryUltrafast := range []bool{false, true} {
		t.Run(map[bool]string{false: "fallback_ultrafast", true: "primary_ultrafast"}[primaryUltrafast], func(t *testing.T) {
			svc := &APIKeyService{}
			apiKey := profitAuthTestAPIKey()
			apiKey.Group.ForceOpenAIUltrafast = primaryUltrafast
			apiKey.Group.ForceOpenAIFast = true
			fallbackID := int64(51)
			fallback := &Group{
				ID: fallbackID, Platform: PlatformOpenAI, Status: StatusActive,
				SubscriptionType: SubscriptionTypeStandard, Hydrated: true,
				ForceOpenAIUltrafast: !primaryUltrafast, RequirePrivacySet: true,
			}
			apiKey.FallbackGroupID = &fallbackID
			apiKey.FallbackGroup = fallback
			apiKey.Groups = []*APIKeyGroupEntry{
				{Group: apiKey.Group, Priority: 1},
				{Group: fallback, Priority: 2},
			}
			payload, err := json.Marshal(&APIKeyAuthCacheEntry{Snapshot: svc.snapshotFromAPIKey(context.Background(), apiKey)})
			require.NoError(t, err)
			var cached APIKeyAuthCacheEntry
			require.NoError(t, json.Unmarshal(payload, &cached))
			materialized, used, err := svc.applyAuthCacheEntry(apiKey.Key, &cached)
			require.NoError(t, err)
			require.True(t, used)
			require.Equal(t, primaryUltrafast, materialized.Group.ForceOpenAIUltrafast)
			require.True(t, materialized.Group.ForceOpenAIFast)
			require.NotNil(t, materialized.FallbackGroup)
			require.Equal(t, !primaryUltrafast, materialized.FallbackGroup.ForceOpenAIUltrafast)
			require.True(t, materialized.FallbackGroup.RequirePrivacySet)
			chain := materialized.GroupChain()
			require.Len(t, chain, 2)
			require.Equal(t, primaryUltrafast, chain[0].Group.ForceOpenAIUltrafast)
			require.Equal(t, !primaryUltrafast, chain[1].Group.ForceOpenAIUltrafast)
			require.True(t, chain[1].Group.RequirePrivacySet)
		})
	}
}
