package service

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/stretchr/testify/require"
)

type countingPrivacyGroupRepo struct {
	GroupRepository
	group *Group
	calls atomic.Int32
}

func (r *countingPrivacyGroupRepo) GetByIDLite(context.Context, int64) (*Group, error) {
	r.calls.Add(1)
	return r.group, nil
}

func (r *countingPrivacyGroupRepo) GetByID(ctx context.Context, id int64) (*Group, error) {
	return r.GetByIDLite(ctx, id)
}

// Direct requests schedule on the authenticated group, so the privacy gate
// comes from the auth snapshot in ctx instead of a group-table read per pick.
func TestOpenAIGatewayService_PrivacyGateReadsAuthGroupFromContext(t *testing.T) {
	groupID := int64(39101)
	for _, advanced := range []string{"false", "true"} {
		t.Run(map[string]string{"false": "legacy", "true": "advanced"}[advanced], func(t *testing.T) {
			accounts := []Account{
				{ID: 39111, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true, Concurrency: 1, Priority: 0, GroupIDs: []int64{groupID}, Credentials: map[string]any{"plan_type": "team"}},
				{ID: 39112, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true, Concurrency: 1, Priority: 5, GroupIDs: []int64{groupID}, Credentials: map[string]any{"plan_type": "team"}, Extra: map[string]any{"privacy_mode": PrivacyModeTrainingOff}},
			}
			repo := &guardianAffinityAccountRepo{schedulerGroupAwareOpenAIAccountRepo: schedulerGroupAwareOpenAIAccountRepo{schedulerTestOpenAIAccountRepo{accounts: accounts}}}
			// The repository disagrees with ctx; reading it would admit 39111.
			groupRepo := &countingPrivacyGroupRepo{group: &Group{ID: groupID, Name: "stale", RequirePrivacySet: false}}
			svc := &OpenAIGatewayService{
				accountRepo:        repo,
				cache:              &schedulerTestGatewayCache{},
				cfg:                &config.Config{},
				rateLimitService:   newOpenAIAdvancedSchedulerRateLimitService(advanced),
				concurrencyService: NewConcurrencyService(schedulerTestConcurrencyCache{acquireResults: map[int64]bool{39111: true, 39112: true}}),
				schedulerSnapshot:  &SchedulerSnapshotService{accountRepo: repo, groupRepo: groupRepo},
			}
			ctx := context.WithValue(context.Background(), ctxkey.Group, &Group{
				ID: groupID, Name: "privacy", Platform: PlatformOpenAI, Status: StatusActive,
				Hydrated: true, RequirePrivacySet: true,
			})

			selection, _, err := svc.SelectAccountWithScheduler(ctx, &groupID, "", "", "gpt-5.1", nil, OpenAIUpstreamTransportAny, false)
			require.NoError(t, err)
			require.NotNil(t, selection)
			require.Equal(t, int64(39112), selection.Account.ID)
			require.Zero(t, groupRepo.calls.Load(), "privacy gate must not read the group table for the auth group")
			if selection.ReleaseFunc != nil {
				selection.ReleaseFunc()
			}
		})
	}
}

func TestAPIKeyAuthSnapshotRequirePrivacySetRoundtrip(t *testing.T) {
	svc := &APIKeyService{}
	apiKey := profitAuthTestAPIKey()
	apiKey.Group.RequirePrivacySet = true

	payload, err := json.Marshal(&APIKeyAuthCacheEntry{Snapshot: svc.snapshotFromAPIKey(context.Background(), apiKey)})
	require.NoError(t, err)
	var restored APIKeyAuthCacheEntry
	require.NoError(t, json.Unmarshal(payload, &restored))
	materialized, used, err := svc.applyAuthCacheEntry(apiKey.Key, &restored)
	require.NoError(t, err)
	require.True(t, used)
	require.True(t, materialized.Group.RequirePrivacySet)
}

// Orange 聚合链与官方 privacy 配置共用 v29 认证快照，主组和后组须各自保真。
func TestAPIKeyAuthSnapshotGroupChainRequirePrivacySetRoundtrip(t *testing.T) {
	for _, tc := range []struct {
		name            string
		primaryPrivacy  bool
		fallbackPrivacy bool
	}{
		{name: "primary_requires_privacy", primaryPrivacy: true},
		{name: "fallback_requires_privacy", fallbackPrivacy: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := &APIKeyService{}
			apiKey := profitAuthTestAPIKey()
			apiKey.Group.RequirePrivacySet = tc.primaryPrivacy
			fallback := &Group{
				ID: 51, Name: "privacy-fallback", Platform: PlatformOpenAI,
				Status: StatusActive, Hydrated: true, SubscriptionType: SubscriptionTypeStandard,
				RequirePrivacySet: tc.fallbackPrivacy,
			}
			apiKey.Groups = []*APIKeyGroupEntry{
				{Group: apiKey.Group, Priority: 1},
				{Group: fallback, Priority: 2},
			}
			payload, err := json.Marshal(&APIKeyAuthCacheEntry{Snapshot: svc.snapshotFromAPIKey(context.Background(), apiKey)})
			require.NoError(t, err)
			var cached APIKeyAuthCacheEntry
			require.NoError(t, json.Unmarshal(payload, &cached))
			require.NotNil(t, cached.Snapshot)
			require.Equal(t, 29, cached.Snapshot.Version)
			require.Equal(t, apiKeyAuthSnapshotVersion, cached.Snapshot.Version)
			materialized, used, err := svc.applyAuthCacheEntry(apiKey.Key, &cached)
			require.NoError(t, err)
			require.True(t, used)
			require.NotNil(t, materialized.Group)
			require.Equal(t, tc.primaryPrivacy, materialized.Group.RequirePrivacySet)
			chain := materialized.GroupChain()
			require.Len(t, chain, 2)
			require.Equal(t, apiKey.Group.ID, chain[0].Group.ID)
			require.Equal(t, fallback.ID, chain[1].Group.ID)
			require.Equal(t, tc.primaryPrivacy, chain[0].Group.RequirePrivacySet)
			require.Equal(t, tc.fallbackPrivacy, chain[1].Group.RequirePrivacySet)
		})
	}
}
