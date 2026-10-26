//go:build unit

package service

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// 生图聚合链测试桩：按分组 ID 决定该组是否有账号，并记录实际被尝试的分组。
// ---------------------------------------------------------------------------

type imagePolicyChainAccountRepo struct {
	AccountRepository

	mu sync.Mutex
	// accountsByGroup 记录每个分组可调度的账号；缺失表示该组无可用账号。
	accountsByGroup map[int64][]Account
	visitedGroups   []int64
}

func (r *imagePolicyChainAccountRepo) GetByID(_ context.Context, id int64) (*Account, error) {
	for _, accounts := range r.accountsByGroup {
		for i := range accounts {
			if accounts[i].ID == id {
				account := accounts[i]
				return &account, nil
			}
		}
	}
	return nil, errors.New("account not found")
}

func (r *imagePolicyChainAccountRepo) ListSchedulableByGroupIDAndPlatform(_ context.Context, groupID int64, platform string) ([]Account, error) {
	r.mu.Lock()
	r.visitedGroups = append(r.visitedGroups, groupID)
	r.mu.Unlock()
	out := make([]Account, 0)
	for _, account := range r.accountsByGroup[groupID] {
		if account.Platform == platform {
			out = append(out, account)
		}
	}
	return out, nil
}

func (r *imagePolicyChainAccountRepo) ListSchedulableByGroupID(_ context.Context, groupID int64) ([]Account, error) {
	return r.ListSchedulableByGroupIDAndPlatform(context.Background(), groupID, PlatformOpenAI)
}

func (r *imagePolicyChainAccountRepo) ListSchedulableByPlatform(_ context.Context, platform string) ([]Account, error) {
	out := make([]Account, 0)
	for _, accounts := range r.accountsByGroup {
		for _, account := range accounts {
			if account.Platform == platform {
				out = append(out, account)
			}
		}
	}
	return out, nil
}

func (r *imagePolicyChainAccountRepo) ListSchedulableUngroupedByPlatform(_ context.Context, _ string) ([]Account, error) {
	return nil, nil
}

func (r *imagePolicyChainAccountRepo) ListModelAvailabilityCandidates(ctx context.Context, groupID *int64, _ []string, _ bool) ([]Account, error) {
	if groupID == nil {
		return nil, nil
	}
	return r.ListSchedulableByGroupID(ctx, *groupID)
}

func (r *imagePolicyChainAccountRepo) visits() []int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]int64(nil), r.visitedGroups...)
}

func newImagePolicyChainGroup(id int64, allowImage bool) *Group {
	return &Group{
		ID:                   id,
		Platform:             PlatformOpenAI,
		Status:               StatusActive,
		Hydrated:             true,
		RateMultiplier:       1,
		AllowImageGeneration: allowImage,
	}
}

func newImagePolicyChainAccount(id, groupID int64) Account {
	return Account{
		ID:            id,
		Name:          "image-account",
		Platform:      PlatformOpenAI,
		Type:          AccountTypeAPIKey,
		Status:        StatusActive,
		Schedulable:   true,
		Credentials:   map[string]any{"api_key": "sk-test"},
		AccountGroups: []AccountGroup{{GroupID: groupID}},
		GroupIDs:      []int64{groupID},
	}
}

func newImagePolicyChainService(accounts []Account) (*OpenAIGatewayService, *imagePolicyChainAccountRepo) {
	byGroup := make(map[int64][]Account)
	for _, account := range accounts {
		for _, groupID := range account.GroupIDs {
			byGroup[groupID] = append(byGroup[groupID], account)
		}
	}
	repo := &imagePolicyChainAccountRepo{accountsByGroup: byGroup}
	cfg := &config.Config{}
	cfg.Gateway.Scheduling.LoadBatchEnabled = false
	return &OpenAIGatewayService{
		accountRepo:        repo,
		cache:              &schedulerTestGatewayCache{},
		cfg:                cfg,
		concurrencyService: NewConcurrencyService(schedulerTestConcurrencyCache{}),
	}, repo
}

func newAggregatedImageKey(primary *Group, groups ...*Group) *APIKey {
	primaryID := primary.ID
	entries := make([]*APIKeyGroupEntry, 0, len(groups)+1)
	entries = append(entries, &APIKeyGroupEntry{Group: primary, Priority: 1})
	for i, group := range groups {
		entries = append(entries, &APIKeyGroupEntry{Group: group, Priority: i + 2})
	}
	return &APIKey{ID: 900, GroupID: &primaryID, Group: primary, Groups: entries}
}

func resolveImagePolicyForTest(t *testing.T, key *APIKey, model string) ImageGenerationGroupPolicy {
	t.Helper()
	policy, ok := ResolveImageGenerationGroupPolicy(key, model, nil)
	require.True(t, ok, "聚合链应产出请求级生图分组策略")
	return policy
}

func selectImagesWithPolicy(t *testing.T, svc *OpenAIGatewayService, key *APIKey, policy ImageGenerationGroupPolicy) (*AccountSelectionResult, error) {
	t.Helper()
	// 真实链路中认证中间件先安装请求级分组路由，生图 handler 再叠加生图策略；
	// 两者一起才构成完整上下文。
	ctx := WithAPIKeyGroupFallbackRouting(context.Background(), key)
	ctx = WithImageGenerationGroupPolicy(ctx, policy)
	selection, _, err := svc.SelectAccountWithSchedulerForImages(ctx, key.GroupID, "", "gpt-image-2", nil, OpenAIImagesCapabilityBasic)
	return selection, err
}

// A. [A 禁用生图, B 允许生图且有账号] → 选择 B。
func TestImagePolicy_A_SkipsDisabledPrimaryAndSelectsAllowedFallback(t *testing.T) {
	groupA := newImagePolicyChainGroup(1001, false)
	groupB := newImagePolicyChainGroup(1002, true)
	key := newAggregatedImageKey(groupA, groupB)
	svc, repo := newImagePolicyChainService([]Account{newImagePolicyChainAccount(1, groupB.ID)})

	policy := resolveImagePolicyForTest(t, key, "gpt-image-2")
	require.Len(t, policy.Groups, 1)
	require.Equal(t, groupB.ID, policy.Groups[0].ID)

	selection, err := selectImagesWithPolicy(t, svc, key, policy)
	require.NoError(t, err)
	require.NotNil(t, selection)
	require.NotNil(t, selection.Account)
	require.Equal(t, int64(1), selection.Account.ID)
	require.Equal(t, []int64{groupB.ID}, repo.visits(), "禁用生图的首组不得进入调度")
	require.Equal(t, groupB.ID, *key.GroupID, "命中后组时请求级路由应指向实际分组")
	require.True(t, selection.UsedAPIKeyFallback)
	require.Equal(t, groupB.ID, selection.RoutedGroupID)
}

// B. [A 允许但无账号, B 禁用但有账号, C 允许且有账号] → 跳过 B 选 C。
func TestImagePolicy_B_SkipsDisabledMiddleGroupEvenWithAccounts(t *testing.T) {
	groupA := newImagePolicyChainGroup(1101, true)
	groupB := newImagePolicyChainGroup(1102, false)
	groupC := newImagePolicyChainGroup(1103, true)
	key := newAggregatedImageKey(groupA, groupB, groupC)
	svc, repo := newImagePolicyChainService([]Account{
		newImagePolicyChainAccount(11, groupB.ID),
		newImagePolicyChainAccount(12, groupC.ID),
	})

	policy := resolveImagePolicyForTest(t, key, "gpt-image-2")
	require.Len(t, policy.Groups, 2, "禁用生图的中间组必须被剔除")

	selection, err := selectImagesWithPolicy(t, svc, key, policy)
	require.NoError(t, err)
	require.NotNil(t, selection)
	require.Equal(t, int64(12), selection.Account.ID)
	require.Equal(t, []int64{groupA.ID, groupC.ID}, repo.visits(), "不得借权在禁用生图的 B 组执行")
	require.Equal(t, groupC.ID, *key.GroupID)
}

// C. 全链禁用生图 → 权限拒绝，且不发生上游调用。
func TestImagePolicy_C_AllDisabledKeepsPermissionError(t *testing.T) {
	groupA := newImagePolicyChainGroup(1201, false)
	groupB := newImagePolicyChainGroup(1202, false)
	key := newAggregatedImageKey(groupA, groupB)

	require.False(t, ChainAllowsImageGeneration(key), "全链禁用生图必须被识别")
	policy, ok := ResolveImageGenerationGroupPolicy(key, "gpt-image-2", nil)
	require.True(t, ok)
	require.Empty(t, policy.Groups)

	svc, repo := newImagePolicyChainService([]Account{newImagePolicyChainAccount(21, groupB.ID)})
	selection, err := selectImagesWithPolicy(t, svc, key, policy)
	require.ErrorIs(t, err, ErrImageGenerationNotAllowed)
	require.Nil(t, selection)
	require.Empty(t, repo.visits(), "权限拒绝前不得触达任何分组调度")
	require.Equal(t, groupA.ID, *key.GroupID, "权限拒绝不得改写 API Key 的分组")
}

// D. 存在允许生图的分组但都没有账号 → 容量错误，而不是权限错误。
func TestImagePolicy_D_AllowedGroupsWithoutAccountsReportsCapacity(t *testing.T) {
	groupA := newImagePolicyChainGroup(1301, false)
	groupB := newImagePolicyChainGroup(1302, true)
	groupC := newImagePolicyChainGroup(1303, true)
	key := newAggregatedImageKey(groupA, groupB, groupC)
	svc, repo := newImagePolicyChainService(nil)

	policy := resolveImagePolicyForTest(t, key, "gpt-image-2")
	_, err := selectImagesWithPolicy(t, svc, key, policy)
	require.ErrorIs(t, err, ErrNoAvailableAccounts)
	require.NotErrorIs(t, err, ErrImageGenerationNotAllowed, "不得把容量不足误报为全链无权限")
	require.Equal(t, []int64{groupB.ID, groupC.ID}, repo.visits(), "应依次尝试每个允许生图的分组")
}

// E. 模型只在后组白名单中允许 → 不被首组白名单提前拒绝，最终只在允许的组执行。
func TestImagePolicy_E_ModelOnlyAllowlistedInLaterGroup(t *testing.T) {
	groupA := newImagePolicyChainGroup(1401, true)
	groupA.ModelAllowlist = GroupModelAllowlist{Enabled: true, Models: []string{"gpt-5.4"}}
	groupB := newImagePolicyChainGroup(1402, true)
	groupB.ModelAllowlist = GroupModelAllowlist{Enabled: true, Models: []string{"gpt-image-2"}}
	key := newAggregatedImageKey(groupA, groupB)
	svc, repo := newImagePolicyChainService([]Account{newImagePolicyChainAccount(31, groupB.ID)})

	require.True(t, ChainModelAllowlistAllows(key, "gpt-image-2"), "链上并集准入应放行仅后组允许的模型")

	policy := resolveImagePolicyForTest(t, key, "gpt-image-2")
	require.Len(t, policy.Groups, 1)
	require.Equal(t, groupB.ID, policy.Groups[0].ID)

	selection, err := selectImagesWithPolicy(t, svc, key, policy)
	require.NoError(t, err)
	require.Equal(t, int64(31), selection.Account.ID)
	require.Equal(t, []int64{groupB.ID}, repo.visits(), "首组白名单不允许该模型，不得在其内执行")
}

// F. 后组生图开关开启但白名单禁止目标模型 → 不得在该组转发。
func TestImagePolicy_F_AllowsImageButAllowlistForbidsModel(t *testing.T) {
	groupA := newImagePolicyChainGroup(1501, false)
	groupB := newImagePolicyChainGroup(1502, true)
	groupB.ModelAllowlist = GroupModelAllowlist{Enabled: true, Models: []string{"gpt-5.4"}}
	key := newAggregatedImageKey(groupA, groupB)

	require.False(t, ChainModelAllowlistAllows(key, "gpt-image-2"))
	policy := resolveImagePolicyForTest(t, key, "gpt-image-2")
	require.Empty(t, policy.Groups, "开关允许但白名单禁止的分组不得成为候选")

	svc, repo := newImagePolicyChainService([]Account{newImagePolicyChainAccount(41, groupB.ID)})
	_, err := selectImagesWithPolicy(t, svc, key, policy)
	require.ErrorIs(t, err, ErrImageGenerationNotAllowed)
	require.Empty(t, repo.visits())
}

// H. 普通单组 Key 禁用生图仍拒绝；普通文本 Key 不受影响；旧式单兜底沿用同一能力筛选语义。
func TestImagePolicy_H_SingleGroupAndLegacyFallbackSemantics(t *testing.T) {
	t.Run("single_disabled_group_rejected", func(t *testing.T) {
		group := newImagePolicyChainGroup(1601, false)
		groupID := group.ID
		key := &APIKey{ID: 901, GroupID: &groupID, Group: group}
		require.False(t, ChainAllowsImageGeneration(key))
		_, ok := ResolveImageGenerationGroupPolicy(key, "gpt-image-2", nil)
		require.False(t, ok, "普通密钥必须沿用单分组语义")
	})

	t.Run("single_enabled_group_allowed", func(t *testing.T) {
		group := newImagePolicyChainGroup(1611, true)
		groupID := group.ID
		key := &APIKey{ID: 902, GroupID: &groupID, Group: group}
		require.True(t, ChainAllowsImageGeneration(key))
	})

	t.Run("ungrouped_key_preserves_existing_behavior", func(t *testing.T) {
		key := &APIKey{ID: 903}
		require.True(t, ChainAllowsImageGeneration(key), "未分组密钥保持既有放行语义")
	})

	t.Run("legacy_single_fallback_uses_same_capability_filter", func(t *testing.T) {
		primary := newImagePolicyChainGroup(1621, false)
		fallback := newImagePolicyChainGroup(1622, true)
		primaryID, fallbackID := primary.ID, fallback.ID
		key := &APIKey{ID: 904, GroupID: &primaryID, Group: primary, FallbackGroupID: &fallbackID, FallbackGroup: fallback}

		policy, ok := ResolveImageGenerationGroupPolicy(key, "gpt-image-2", nil)
		require.True(t, ok)
		require.Len(t, policy.Groups, 1)
		require.Equal(t, fallbackID, policy.Groups[0].ID)

		svc, repo := newImagePolicyChainService([]Account{newImagePolicyChainAccount(51, fallbackID)})
		selection, err := selectImagesWithPolicy(t, svc, key, policy)
		require.NoError(t, err)
		require.Equal(t, int64(51), selection.Account.ID)
		require.Equal(t, []int64{fallbackID}, repo.visits())
	})

	t.Run("text_requests_unaffected", func(t *testing.T) {
		group := newImagePolicyChainGroup(1631, false)
		// 文本模型不受生图开关约束：GroupAllowsImageGenerationModel 只拦生图模型。
		require.True(t, GroupAllowsImageGenerationModel(group, "gpt-5.4"))
		require.False(t, GroupAllowsImageGenerationModel(group, "gpt-image-2"))
	})
}

// I. 模型列表可见性：禁用生图的分组不得把生图模型宣称为可调用，但保留文本模型。
func TestImagePolicy_I_ModelListingExcludesDisabledImageModels(t *testing.T) {
	group := newImagePolicyChainGroup(1701, false)
	require.False(t, GroupAllowsImageGenerationModel(group, "gpt-image-2"))
	require.False(t, GroupAllowsImageGenerationModel(group, "gpt-image-2.5-flare"))
	require.True(t, GroupAllowsImageGenerationModel(group, "gpt-5.4"))

	enabled := newImagePolicyChainGroup(1702, true)
	require.True(t, GroupAllowsImageGenerationModel(enabled, "gpt-image-2"))
}

// J1. 认证快照序列化/恢复后主分组与链分组的 AllowImageGeneration 不变。
func TestImagePolicy_J1_AuthSnapshotPreservesAllowImageGeneration(t *testing.T) {
	primary := newImagePolicyChainGroup(1801, false)
	primary.ImagePrice1K = func() *float64 { v := 0.04; return &v }()
	fallback := newImagePolicyChainGroup(1802, true)
	fallback.ImagePrice1K = func() *float64 { v := 0.08; return &v }()

	primarySnapshot := apiKeyAuthGroupSnapshotFromGroup(primary)
	fallbackSnapshot := apiKeyAuthGroupSnapshotFromGroup(fallback)
	require.False(t, primarySnapshot.AllowImageGeneration)
	require.True(t, fallbackSnapshot.AllowImageGeneration)

	payload, err := json.Marshal(APIKeyAuthGroupEntrySnapshot{Priority: 2, Group: fallbackSnapshot})
	require.NoError(t, err)
	var decoded APIKeyAuthGroupEntrySnapshot
	require.NoError(t, json.Unmarshal(payload, &decoded))

	restored := apiKeyAuthGroupFromSnapshot(decoded.Group)
	require.NotNil(t, restored)
	require.True(t, restored.AllowImageGeneration, "链分组快照恢复后生图开关必须保留")
	require.NotNil(t, restored.ImagePrice1K)
	require.InDelta(t, 0.08, *restored.ImagePrice1K, 1e-9)

	restoredPrimary := apiKeyAuthGroupFromSnapshot(primarySnapshot)
	require.NotNil(t, restoredPrimary)
	require.False(t, restoredPrimary.AllowImageGeneration)
}

// J2. 并发请求之间不发生路由组污染。
func TestImagePolicy_J2_ConcurrentRequestsDoNotLeakRoutes(t *testing.T) {
	groupA := newImagePolicyChainGroup(1901, false)
	groupB := newImagePolicyChainGroup(1902, true)
	groupC := newImagePolicyChainGroup(1903, true)
	key := newAggregatedImageKey(groupA, groupB, groupC)
	svc, _ := newImagePolicyChainService([]Account{
		newImagePolicyChainAccount(61, groupB.ID),
		newImagePolicyChainAccount(62, groupC.ID),
	})

	policy := resolveImagePolicyForTest(t, key, "gpt-image-2")
	require.Len(t, policy.Groups, 2)

	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx := WithImageGenerationGroupPolicy(context.Background(), policy)
			_, _, _ = svc.SelectAccountWithSchedulerForImages(ctx, key.GroupID, "", "gpt-image-2", nil, OpenAIImagesCapabilityBasic)
		}()
	}
	wg.Wait()

	// 每条请求都必须落在策略允许的分组上；主分组（禁用生图）永不出现。
	require.NotContains(t, svc.accountRepo.(*imagePolicyChainAccountRepo).visits(), groupA.ID)
	require.Contains(t, policy.GroupIDs(), groupB.ID)
}

// GroupIDs 便于断言策略内容，避免测试直接依赖内部切片顺序。
func (p ImageGenerationGroupPolicy) GroupIDs() []int64 {
	out := make([]int64, 0, len(p.Groups))
	for _, group := range p.Groups {
		out = append(out, group.ID)
	}
	return out
}
