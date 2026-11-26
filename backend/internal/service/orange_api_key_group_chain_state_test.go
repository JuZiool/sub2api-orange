// Orange 特有：分组链成员状态与读/写契约（v0.2.25-8 契约 v2）的单元测试。
//
// 锁定三条修复口径（缺陷：密钥列表显示 5 个分组，编辑弹窗只剩 3 个）：
//
//	D1 详情读路径必须用 userRepo 重载 owner（含 allowed_groups），否则专属分组被误判成无权限；
//	D2 读视图（详情 + 列表）返回链上**全部**成员 + state，不再静默丢弃（停用/跨平台/无权限/已删都要看得见）；
//	D3 GET 纯读（0 次写库）；PUT 任何「会移除成员」的保存都必须先拿到 409 确认（旧客户端同样受保护），
//	   而把读视图里的成员 1:1 提交回来必须能落库（候选位不再因 disabled / scope_mismatch /
//	   no_permission / 已删除被 400 拒绝，见契约 C2）。
package service

import (
	"context"
	"errors"
	"testing"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

// orangeChainStateTestGroup 构造标准分组（启用、非专属、Anthropic 平台）。
func orangeChainStateTestGroup(id int64, name string) *Group {
	group := orangePolicyTestGroup(id, PlatformAnthropic)
	group.Name = name
	return group
}

// orangeChainStateTestExclusiveGroup 构造专属分组（is_exclusive=true，需授权才可绑定）。
func orangeChainStateTestExclusiveGroup(id int64, name string) *Group {
	group := orangeChainStateTestGroup(id, name)
	group.IsExclusive = true
	return group
}

func orangeChainStateTestInt64Ptr(value int64) *int64 { return &value }

// ---- 假仓储（只实现被测路径用到的方法，其余交给嵌入接口兜底） ----

type orangeChainStateTestAPIKeyRepo struct {
	APIKeyRepository // 未实现的方法不会被测试触发，调用即 panic，便于快速定位
	keys             map[int64]*APIKey
	members          map[int64][]OrangeGroupChainMember
	modes            map[int64]string
	groups           map[int64]*Group

	replaceCalls           int
	replaceAndPrimaryCalls int
	setModeCalls           int
	lastPrimaryID          int64
	lastTail               []int64
	lastMode               string
}

func newOrangeChainStateTestAPIKeyRepo(groups map[int64]*Group) *orangeChainStateTestAPIKeyRepo {
	return &orangeChainStateTestAPIKeyRepo{
		keys:    make(map[int64]*APIKey),
		members: make(map[int64][]OrangeGroupChainMember),
		modes:   make(map[int64]string),
		groups:  groups,
	}
}

// GetByID 刻意模仿真实仓储：WithUser() 但不预载 allowed_groups（缺陷 D1 的现场）。
func (r *orangeChainStateTestAPIKeyRepo) GetByID(_ context.Context, id int64) (*APIKey, error) {
	key, ok := r.keys[id]
	if !ok || key == nil {
		return nil, ErrAPIKeyNotFound
	}
	cloned := *key
	cloned.Group = cloneOrangeGroup(key.Group)
	if key.User != nil {
		userCopy := *key.User
		userCopy.AllowedGroups = append([]int64(nil), key.User.AllowedGroups...)
		cloned.User = &userCopy
	}
	return &cloned, nil
}

func (r *orangeChainStateTestAPIKeyRepo) VerifyOwnership(_ context.Context, userID int64, apiKeyIDs []int64) ([]int64, error) {
	owned := make([]int64, 0, len(apiKeyIDs))
	for _, id := range apiKeyIDs {
		if key, ok := r.keys[id]; ok && key != nil && key.UserID == userID {
			owned = append(owned, id)
		}
	}
	return owned, nil
}

func (r *orangeChainStateTestAPIKeyRepo) OrangeLoadGroupChains(_ context.Context, apiKeyIDs []int64) (map[int64][]OrangeGroupChainMember, error) {
	out := make(map[int64][]OrangeGroupChainMember, len(apiKeyIDs))
	for _, id := range apiKeyIDs {
		out[id] = append([]OrangeGroupChainMember(nil), r.members[id]...)
	}
	return out, nil
}

func (r *orangeChainStateTestAPIKeyRepo) OrangeLoadGroupChainModes(_ context.Context, apiKeyIDs []int64) (map[int64]string, error) {
	out := make(map[int64]string, len(apiKeyIDs))
	for _, id := range apiKeyIDs {
		out[id] = r.modes[id]
	}
	return out, nil
}

func (r *orangeChainStateTestAPIKeyRepo) OrangeListAPIKeyIDsByChainGroup(context.Context, int64) ([]int64, error) {
	return nil, nil
}

func (r *orangeChainStateTestAPIKeyRepo) OrangeReplaceGroupChain(_ context.Context, apiKeyID int64, groupIDs []int64) error {
	r.replaceCalls++
	r.members[apiKeyID] = orangeChainStateTestMembers(groupIDs)
	r.modes[apiKeyID] = ""
	r.lastTail = append([]int64(nil), groupIDs...)
	r.lastMode = ""
	return nil
}

func (r *orangeChainStateTestAPIKeyRepo) OrangeReplaceGroupChainAndPrimary(_ context.Context, apiKeyID, primaryGroupID int64, tail []int64, mode string) error {
	r.replaceAndPrimaryCalls++
	if key, ok := r.keys[apiKeyID]; ok && key != nil {
		groupID := primaryGroupID
		key.GroupID = &groupID
		key.Group = cloneOrangeGroup(r.groups[primaryGroupID])
	}
	r.members[apiKeyID] = orangeChainStateTestMembers(tail)
	r.modes[apiKeyID] = mode
	r.lastPrimaryID = primaryGroupID
	r.lastTail = append([]int64(nil), tail...)
	r.lastMode = mode
	return nil
}

func (r *orangeChainStateTestAPIKeyRepo) OrangeSetGroupChainMode(_ context.Context, apiKeyID int64, mode string) error {
	r.setModeCalls++
	r.modes[apiKeyID] = mode
	return nil
}

// writeCalls 汇总所有写库方法的调用次数（D3 断言用）。
func (r *orangeChainStateTestAPIKeyRepo) writeCalls() int {
	return r.replaceCalls + r.replaceAndPrimaryCalls + r.setModeCalls
}

func orangeChainStateTestMembers(groupIDs []int64) []OrangeGroupChainMember {
	members := make([]OrangeGroupChainMember, 0, len(groupIDs))
	for i, groupID := range groupIDs {
		members = append(members, OrangeGroupChainMember{GroupID: groupID, Priority: i + 1})
	}
	return members
}

type orangeChainStateTestGroupRepo struct {
	GroupRepository
	groups map[int64]*Group
}

func (r *orangeChainStateTestGroupRepo) GetByID(_ context.Context, id int64) (*Group, error) {
	group, ok := r.groups[id]
	if !ok || group == nil {
		return nil, ErrGroupNotFound
	}
	return cloneOrangeGroup(group), nil
}

func (r *orangeChainStateTestGroupRepo) GetByIDLite(ctx context.Context, id int64) (*Group, error) {
	return r.GetByID(ctx, id)
}

type orangeChainStateTestUserRepo struct {
	UserRepository
	users map[int64]*User
	err   error
}

func (r *orangeChainStateTestUserRepo) GetByID(_ context.Context, id int64) (*User, error) {
	if r.err != nil {
		return nil, r.err
	}
	user, ok := r.users[id]
	if !ok || user == nil {
		return nil, ErrUserNotFound
	}
	cloned := *user
	cloned.AllowedGroups = append([]int64(nil), user.AllowedGroups...)
	return &cloned, nil
}

// orangeChainStateTestFixture 是一条“列表显示 5 个分组”的现场：
// 链首 2（公开）+ 候选 15（专属，已授权）、16（专属，未授权）、17（停用）、18（已删除）。
func orangeChainStateTestFixture() (*orangeChainStateTestAPIKeyRepo, *orangeChainStateTestGroupRepo, *orangeChainStateTestUserRepo) {
	head := orangeChainStateTestGroup(2, "主分组")
	granted := orangeChainStateTestExclusiveGroup(15, "自用生图")
	ungranted := orangeChainStateTestExclusiveGroup(16, "自用圆模")
	disabled := orangeChainStateTestGroup(17, "临时停用")
	disabled.Status = StatusDisabled
	// 18 只存在于链里（分组已被删除），不在 groups 表里。
	groups := map[int64]*Group{2: head, 15: granted, 16: ungranted, 17: disabled}

	apiKeyRepo := newOrangeChainStateTestAPIKeyRepo(groups)
	apiKeyRepo.keys[29] = &APIKey{
		ID: 29, UserID: 7, Key: "sk-orange-state-test",
		GroupID: orangeChainStateTestInt64Ptr(2), Group: cloneOrangeGroup(head),
		// 真实 apiKeyRepo.GetByID 不预载 allowed_groups：这里刻意保持为空，用于证明 D1 修复。
		User: &User{ID: 7},
	}
	apiKeyRepo.members[29] = []OrangeGroupChainMember{
		{GroupID: 15, Priority: 1},
		{GroupID: 16, Priority: 2},
		{GroupID: 17, Priority: 3},
		{GroupID: 18, Priority: 4},
	}
	apiKeyRepo.modes[29] = OrangeGroupChainModeAggregate

	groupRepo := &orangeChainStateTestGroupRepo{groups: groups}
	userRepo := &orangeChainStateTestUserRepo{users: map[int64]*User{
		7: {ID: 7, Status: StatusActive, AllowedGroups: []int64{15}},
	}}
	return apiKeyRepo, groupRepo, userRepo
}

// orangeChainStateTestService 组装被测服务（字段同包可直接赋值，无需 wire）。
func orangeChainStateTestService(apiKeyRepo *orangeChainStateTestAPIKeyRepo, groupRepo *orangeChainStateTestGroupRepo, userRepo *orangeChainStateTestUserRepo) *APIKeyService {
	return &APIKeyService{apiKeyRepo: apiKeyRepo, userRepo: userRepo, groupRepo: groupRepo}
}

func orangeChainMemberStates(members []OrangeAPIKeyGroupChainMemberView) []OrangeChainMemberState {
	states := make([]OrangeChainMemberState, 0, len(members))
	for _, member := range members {
		states = append(states, member.State)
	}
	return states
}

func orangeChainMemberIDs(members []OrangeAPIKeyGroupChainMemberView) []int64 {
	ids := make([]int64, 0, len(members))
	for _, member := range members {
		ids = append(ids, member.GroupID)
	}
	return ids
}

// ---- 状态判定（唯一入口） ----

func TestOrangeChainMemberStateOf(t *testing.T) {
	head := orangeChainStateTestGroup(2, "主分组")
	// 链首自身：不做 scope 比较 → available。
	require.Equal(t, OrangeChainMemberStateAvailable,
		orangeChainMemberStateOf(head, head, &User{ID: 7}))

	// 读不到的分组 → missing（优先级最高，命中即返回）。
	require.Equal(t, OrangeChainMemberStateMissing, orangeChainMemberStateOf(head, nil, &User{ID: 7}))

	// 停用 → disabled；即使同时“无权限”也必须先报 disabled。
	disabled := orangeChainStateTestExclusiveGroup(16, "停用且未授权")
	disabled.Status = StatusDisabled
	require.Equal(t, OrangeChainMemberStateDisabled,
		orangeChainMemberStateOf(head, disabled, &User{ID: 7}))

	// 上下文不完整（缺平台）→ disabled。
	notHydrated := orangeChainStateTestGroup(19, "上下文不完整")
	notHydrated.Platform = ""
	require.Equal(t, OrangeChainMemberStateDisabled,
		orangeChainMemberStateOf(head, notHydrated, &User{ID: 7}))

	// 专属未授权 → no_permission；授权后 → available。
	exclusive := orangeChainStateTestExclusiveGroup(15, "自用生图")
	require.Equal(t, OrangeChainMemberStateNoPermission,
		orangeChainMemberStateOf(head, exclusive, &User{ID: 7}))
	require.Equal(t, OrangeChainMemberStateAvailable,
		orangeChainMemberStateOf(head, exclusive, &User{ID: 7, AllowedGroups: []int64{15}}))

	// 平台不一致 → scope_mismatch。
	otherPlatform := orangeChainStateTestGroup(20, "别的平台")
	otherPlatform.Platform = PlatformOpenAI
	require.Equal(t, OrangeChainMemberStateScopeMismatch,
		orangeChainMemberStateOf(head, otherPlatform, &User{ID: 7}))

	// 订阅类型不一致 → scope_mismatch。
	otherSubscription := orangeChainStateTestGroup(21, "订阅分组")
	otherSubscription.SubscriptionType = SubscriptionTypeSubscription
	require.Equal(t, OrangeChainMemberStateScopeMismatch,
		orangeChainMemberStateOf(head, otherSubscription, &User{ID: 7}))

	// 没有用户快照时不判权限（与 orangeGroupBindableForUser 同口径，避免误判越权）。
	require.Equal(t, OrangeChainMemberStateAvailable,
		orangeChainMemberStateOf(head, exclusive, nil))
}

// ---- D2：读视图返回全部成员 + state ----

func TestOrangeChainFullViewKeepsAllMembersWithState(t *testing.T) {
	apiKeyRepo, groupRepo, userRepo := orangeChainStateTestFixture()
	svc := orangeChainStateTestService(apiKeyRepo, groupRepo, userRepo)
	t.Cleanup(func() { InvalidateOrangeGroupChainCache(29) })

	apiKey, err := apiKeyRepo.GetByID(context.Background(), 29)
	require.NoError(t, err)
	// 视图构造前必须先重载 owner（生产路径由 OrangeGetAPIKeyGroupChain 负责）。
	_, err = svc.orangeLoadChainOwner(context.Background(), apiKey)
	require.NoError(t, err)

	view := svc.OrangeBuildAPIKeyGroupChainViewFull(context.Background(), apiKey)

	require.Equal(t, int64(29), view.APIKeyID)
	require.Equal(t, int64(2), view.PrimaryGroupID)
	require.Equal(t, OrangeGroupChainModeAggregate, view.Mode)
	// 5 个成员一个都不能少：链首 + 4 个候选（专属已授权/专属未授权/停用/已删除）。
	require.Equal(t, []int64{2, 15, 16, 17, 18}, orangeChainMemberIDs(view.Members))
	require.Equal(t, []OrangeChainMemberState{
		OrangeChainMemberStateAvailable,
		OrangeChainMemberStateAvailable,
		OrangeChainMemberStateNoPermission,
		OrangeChainMemberStateDisabled,
		OrangeChainMemberStateMissing,
	}, orangeChainMemberStates(view.Members))
	// missing 成员没有分组详情，但仍占一行（只有 group_id）。
	require.Nil(t, view.Members[4].Group)
	// 纯读：视图不带自愈结果。
	require.Empty(t, view.AutoPrunedGroupIDs)
}

// 兼容视图（网关/旧调用方）保持只返回可用成员的旧行为。
func TestOrangeChainCompatViewKeepsOnlyAvailableMembers(t *testing.T) {
	apiKeyRepo, groupRepo, userRepo := orangeChainStateTestFixture()
	svc := orangeChainStateTestService(apiKeyRepo, groupRepo, userRepo)
	t.Cleanup(func() { InvalidateOrangeGroupChainCache(29) })

	apiKey, err := apiKeyRepo.GetByID(context.Background(), 29)
	require.NoError(t, err)
	_, err = svc.orangeLoadChainOwner(context.Background(), apiKey)
	require.NoError(t, err)

	view := svc.OrangeBuildAPIKeyGroupChainView(context.Background(), apiKey)
	require.Equal(t, []int64{2, 15}, orangeChainMemberIDs(view.Members))
}

// ---- D1 + D3：GET 纯读，并重载带 allowed_groups 的 owner ----

func TestOrangeGetGroupChainIsPureReadAndReloadsOwner(t *testing.T) {
	apiKeyRepo, groupRepo, userRepo := orangeChainStateTestFixture()
	svc := orangeChainStateTestService(apiKeyRepo, groupRepo, userRepo)
	t.Cleanup(func() { InvalidateOrangeGroupChainCache(29) })

	view, err := svc.OrangeGetAPIKeyGroupChain(context.Background(), 7, 29)
	require.NoError(t, err)

	// 15 是已授权的专属分组：只有 owner 被 userRepo 重载（含 allowed_groups）才会判成 available。
	require.Equal(t, []int64{2, 15, 16, 17, 18}, orangeChainMemberIDs(view.Members))
	require.Equal(t, OrangeChainMemberStateAvailable, view.Members[1].State)
	require.Equal(t, OrangeChainMemberStateNoPermission, view.Members[2].State)

	// D3：GET 绝不再写库。
	require.Zero(t, apiKeyRepo.writeCalls(), "GET must not write the chain")
	require.Empty(t, view.AutoPrunedGroupIDs)
}

// ---- 列表口径与详情完全一致（契约 B） ----

func TestOrangeAttachGroupChainsReturnsAllMembers(t *testing.T) {
	apiKeyRepo, groupRepo, userRepo := orangeChainStateTestFixture()
	svc := orangeChainStateTestService(apiKeyRepo, groupRepo, userRepo)
	t.Cleanup(func() { InvalidateOrangeGroupChainCache(29) })

	keys := []APIKey{{
		ID: 29, UserID: 7, Key: "sk-orange-state-test",
		GroupID: orangeChainStateTestInt64Ptr(2), Group: cloneOrangeGroup(groupRepo.groups[2]),
		// 列表里的 key.User 不含 allowed_groups：必须被 userRepo 重载结果覆盖（不再优先用它）。
		User: &User{ID: 7},
	}}
	svc.OrangeAttachGroupChains(context.Background(), keys)

	// 契约 B：列表 groups[] 同样返回**全部**成员，包括 state=missing 的已删除分组（只有 group_id）。
	require.Len(t, keys[0].Groups, 5)
	ids := make([]int64, 0, len(keys[0].Groups))
	states := make([]OrangeChainMemberState, 0, len(keys[0].Groups))
	for _, entry := range keys[0].Groups {
		ids = append(ids, entry.GroupID)
		states = append(states, entry.State)
	}
	require.Equal(t, []int64{2, 15, 16, 17, 18}, ids)
	require.Equal(t, []OrangeChainMemberState{
		OrangeChainMemberStateAvailable,
		OrangeChainMemberStateAvailable,
		OrangeChainMemberStateNoPermission,
		OrangeChainMemberStateDisabled,
		OrangeChainMemberStateMissing,
	}, states)
	// missing 成员没有分组详情，但仍占一行（优先用 GroupID 表达"这一行是哪个分组"）。
	require.Nil(t, keys[0].Groups[4].Group)
	require.Equal(t, int64(18), keys[0].Groups[4].GroupID)
	require.Equal(t, OrangeGroupChainModeAggregate, keys[0].GroupChainMode)
	require.Zero(t, apiKeyRepo.writeCalls())

	// 「列表与详情成员集合完全一致」：group_id / priority / state 必须逐项相等。
	view, err := svc.OrangeGetAPIKeyGroupChain(context.Background(), 7, 29)
	require.NoError(t, err)
	require.Len(t, view.Members, len(keys[0].Groups))
	for i, member := range view.Members {
		require.Equal(t, member.GroupID, keys[0].Groups[i].GroupID, "member %d group id", i)
		require.Equal(t, member.Priority, keys[0].Groups[i].Priority, "member %d priority", i)
		require.Equal(t, member.State, keys[0].Groups[i].State, "member %d state", i)
	}
}

// 链首分组本身已被删除（api_keys.group_id 仍指向它）：列表同样要保留那一行（state=missing），
// 否则列表会比链详情少一行，前端会以为"已经被删掉了"。
func TestOrangeAttachGroupChainsKeepsMissingHead(t *testing.T) {
	member := orangeChainStateTestGroup(15, "自用生图")
	groups := map[int64]*Group{15: member}
	apiKeyRepo := newOrangeChainStateTestAPIKeyRepo(groups)
	apiKeyRepo.keys[31] = &APIKey{ID: 31, UserID: 7, Key: "sk-orange-missing-head",
		GroupID: orangeChainStateTestInt64Ptr(2), Group: nil, User: &User{ID: 7}}
	apiKeyRepo.members[31] = []OrangeGroupChainMember{{GroupID: 15, Priority: 1}}
	apiKeyRepo.modes[31] = OrangeGroupChainModeFallback
	groupRepo := &orangeChainStateTestGroupRepo{groups: groups}
	userRepo := &orangeChainStateTestUserRepo{users: map[int64]*User{
		7: {ID: 7, Status: StatusActive},
	}}
	svc := orangeChainStateTestService(apiKeyRepo, groupRepo, userRepo)
	t.Cleanup(func() { InvalidateOrangeGroupChainCache(31) })

	keys := []APIKey{{ID: 31, UserID: 7, Key: "sk-orange-missing-head",
		GroupID: orangeChainStateTestInt64Ptr(2), Group: nil, User: &User{ID: 7}}}
	svc.OrangeAttachGroupChains(context.Background(), keys)

	require.Len(t, keys[0].Groups, 2)
	require.Equal(t, int64(2), keys[0].Groups[0].GroupID)
	require.Equal(t, OrangeChainMemberStateMissing, keys[0].Groups[0].State)
	require.Nil(t, keys[0].Groups[0].Group)
	require.Equal(t, int64(15), keys[0].Groups[1].GroupID)
	require.Equal(t, OrangeChainMemberStateAvailable, keys[0].Groups[1].State)

	// 与链详情逐项一致。
	view, err := svc.OrangeGetAPIKeyGroupChain(context.Background(), 7, 31)
	require.NoError(t, err)
	require.Equal(t, []int64{2, 15}, orangeChainMemberIDs(view.Members))
	require.Equal(t, []OrangeChainMemberState{
		OrangeChainMemberStateMissing,
		OrangeChainMemberStateAvailable,
	}, orangeChainMemberStates(view.Members))
}

// ---- C1/C3：PUT 的删除保护（旧客户端同样受保护） ----

// 不提供 expected_group_ids 的调用方（旧客户端）：removed 用**服务端当前集合**计算，
// 因此「静默全量替换」一律拒绝（契约 C3），确认后才能落库。
func TestOrangeSetGroupChainWithoutExpectedIdsStillBlocksPrune(t *testing.T) {
	apiKeyRepo, groupRepo, userRepo := orangeChainStateTestFixture()
	svc := orangeChainStateTestService(apiKeyRepo, groupRepo, userRepo)
	t.Cleanup(func() { InvalidateOrangeGroupChainCache(29) })

	_, err := svc.OrangeSetAPIKeyGroupChain(context.Background(), 7, 29, OrangeSetGroupChainInput{
		GroupIDs: []int64{2, 15},
		Mode:     OrangeGroupChainModeFallback,
	})
	require.Error(t, err)
	require.ErrorIs(t, err, ErrOrangeGroupChainPruneConfirm)
	require.True(t, infraerrors.IsConflict(err))

	var pruneErr *OrangeGroupChainPruneError
	require.ErrorAs(t, err, &pruneErr)
	require.Len(t, pruneErr.Removed, 3)
	require.Contains(t, pruneErr.Message(), "保存将移除 3 个分组")
	require.Contains(t, pruneErr.Message(), "自用圆模、临时停用、#18")
	require.Zero(t, apiKeyRepo.writeCalls(), "prune confirmation must not write the chain")

	// 用户确认后，同一个请求体可以落库。
	view, err := svc.OrangeSetAPIKeyGroupChain(context.Background(), 7, 29, OrangeSetGroupChainInput{
		GroupIDs:     []int64{2, 15},
		Mode:         OrangeGroupChainModeFallback,
		ConfirmPrune: true,
	})
	require.NoError(t, err)
	require.Equal(t, 1, apiKeyRepo.replaceAndPrimaryCalls)
	require.Equal(t, []int64{15}, apiKeyRepo.lastTail)
	require.Equal(t, []int64{2, 15}, orangeChainMemberIDs(view.Members))
}

// 409 必须带结构化清单（契约 C1/M2）：Removed 与 metadata.pending_removals 的 JSON 一一对应。
func TestOrangeSetGroupChainPruneErrorCarriesStructuredRemovals(t *testing.T) {
	apiKeyRepo, groupRepo, userRepo := orangeChainStateTestFixture()
	svc := orangeChainStateTestService(apiKeyRepo, groupRepo, userRepo)
	t.Cleanup(func() { InvalidateOrangeGroupChainCache(29) })

	_, err := svc.OrangeSetAPIKeyGroupChain(context.Background(), 7, 29, OrangeSetGroupChainInput{
		GroupIDs:         []int64{2, 15},
		Mode:             OrangeGroupChainModeFallback,
		ExpectedGroupIDs: []int64{2, 15, 16, 17, 18},
	})
	var pruneErr *OrangeGroupChainPruneError
	require.ErrorAs(t, err, &pruneErr)
	require.Equal(t, []OrangePrunedChainGroup{
		{ID: 16, Name: "自用圆模"},
		{ID: 17, Name: "临时停用"},
		{ID: 18, Name: "#18"},
	}, pruneErr.Removed)
	// handler 直接把这段 JSON 放进 metadata.pending_removals。
	require.JSONEq(t,
		`[{"group_id":16,"name":"自用圆模"},{"group_id":17,"name":"临时停用"},{"group_id":18,"name":"#18"}]`,
		pruneErr.PendingRemovalsJSON())
	// 409 语义（reason/code）与文案经 infraerrors 取值也必须一致（handler/前端据此判定）。
	require.Equal(t, OrangeGroupChainPruneReason, infraerrors.Reason(err))
	require.Equal(t, pruneErr.Message(), infraerrors.Message(err))
	require.Zero(t, apiKeyRepo.writeCalls())
}

func TestOrangeSetGroupChainPruneRequiresConfirm(t *testing.T) {
	apiKeyRepo, groupRepo, userRepo := orangeChainStateTestFixture()
	svc := orangeChainStateTestService(apiKeyRepo, groupRepo, userRepo)
	t.Cleanup(func() { InvalidateOrangeGroupChainCache(29) })

	// 新客户端带了它看到的完整集合 [2,15,16,17,18]，本次只提交 [2,15] → 会移除 16/17/18。
	_, err := svc.OrangeSetAPIKeyGroupChain(context.Background(), 7, 29, OrangeSetGroupChainInput{
		GroupIDs:         []int64{2, 15},
		Mode:             OrangeGroupChainModeFallback,
		ExpectedGroupIDs: []int64{2, 15, 16, 17, 18},
	})
	require.Error(t, err)
	require.ErrorIs(t, err, ErrOrangeGroupChainPruneConfirm)
	require.True(t, infraerrors.IsConflict(err))
	require.Equal(t, "ORANGE_GROUP_CHAIN_PRUNE_CONFIRM", infraerrors.Reason(err))
	require.Contains(t, infraerrors.Message(err), "保存将移除 3 个分组")
	require.Contains(t, infraerrors.Message(err), "自用圆模、临时停用、#18")
	require.Contains(t, infraerrors.Message(err), "确认请重试")

	// 409 时不能写库。
	require.Zero(t, apiKeyRepo.writeCalls(), "prune confirmation must not write the chain")
}

func TestOrangeSetGroupChainConfirmPruneWritesChain(t *testing.T) {
	apiKeyRepo, groupRepo, userRepo := orangeChainStateTestFixture()
	svc := orangeChainStateTestService(apiKeyRepo, groupRepo, userRepo)
	t.Cleanup(func() { InvalidateOrangeGroupChainCache(29) })

	view, err := svc.OrangeSetAPIKeyGroupChain(context.Background(), 7, 29, OrangeSetGroupChainInput{
		GroupIDs:         []int64{2, 15},
		Mode:             OrangeGroupChainModeFallback,
		ExpectedGroupIDs: []int64{2, 15, 16, 17, 18},
		ConfirmPrune:     true,
	})
	require.NoError(t, err)
	require.Equal(t, 1, apiKeyRepo.replaceAndPrimaryCalls)
	require.Equal(t, []int64{15}, apiKeyRepo.lastTail)
	require.Equal(t, []int64{2, 15}, orangeChainMemberIDs(view.Members))
}

// 清空链（group_ids=[]）时主分组本身不会被删除，因此不计入「将被移除」的分组；
// 但候选确实会被删掉，仍需要用户确认。
func TestOrangeSetGroupChainClearKeepsPrimaryButStillNeedsConfirm(t *testing.T) {
	apiKeyRepo, groupRepo, userRepo := orangeChainStateTestFixture()
	svc := orangeChainStateTestService(apiKeyRepo, groupRepo, userRepo)
	t.Cleanup(func() { InvalidateOrangeGroupChainCache(29) })

	_, err := svc.OrangeSetAPIKeyGroupChain(context.Background(), 7, 29, OrangeSetGroupChainInput{
		GroupIDs:         nil,
		ExpectedGroupIDs: []int64{2, 15, 16, 17, 18},
	})
	require.ErrorIs(t, err, ErrOrangeGroupChainPruneConfirm)
	// 主分组 2 保留，因此只报 4 个被移除的分组。
	require.Contains(t, infraerrors.Message(err), "保存将移除 4 个分组")
	require.Zero(t, apiKeyRepo.writeCalls())

	_, err = svc.OrangeSetAPIKeyGroupChain(context.Background(), 7, 29, OrangeSetGroupChainInput{
		GroupIDs:         nil,
		ExpectedGroupIDs: []int64{2, 15, 16, 17, 18},
		ConfirmPrune:     true,
	})
	require.NoError(t, err)
	require.Equal(t, 1, apiKeyRepo.replaceCalls)
	require.Zero(t, apiKeyRepo.replaceAndPrimaryCalls)
}

// ---- C2：写入校验放宽（只有链首强校验，B1 回归） ----

// orangeChainWriteTestFixture 是「候选里有停用 / 跨平台 / 已删除分组」的现场：
// 链首 2（可用）+ 候选 15（可用）、17（停用）、19（跨平台）、18（已删除，不在 groups 表里）。
func orangeChainWriteTestFixture() (*orangeChainStateTestAPIKeyRepo, *orangeChainStateTestGroupRepo, *orangeChainStateTestUserRepo) {
	head := orangeChainStateTestGroup(2, "主分组")
	ok := orangeChainStateTestGroup(15, "自用生图")
	disabled := orangeChainStateTestGroup(17, "临时停用")
	disabled.Status = StatusDisabled
	crossPlatform := orangeChainStateTestGroup(19, "别的平台")
	crossPlatform.Platform = PlatformOpenAI
	groups := map[int64]*Group{2: head, 15: ok, 17: disabled, 19: crossPlatform}

	apiKeyRepo := newOrangeChainStateTestAPIKeyRepo(groups)
	apiKeyRepo.keys[29] = &APIKey{
		ID: 29, UserID: 7, Key: "sk-orange-write-test",
		GroupID: orangeChainStateTestInt64Ptr(2), Group: cloneOrangeGroup(head),
		User: &User{ID: 7},
	}
	apiKeyRepo.members[29] = []OrangeGroupChainMember{
		{GroupID: 15, Priority: 1},
		{GroupID: 17, Priority: 2},
		{GroupID: 19, Priority: 3},
		{GroupID: 18, Priority: 4},
	}
	apiKeyRepo.modes[29] = OrangeGroupChainModeAggregate
	groupRepo := &orangeChainStateTestGroupRepo{groups: groups}
	userRepo := &orangeChainStateTestUserRepo{users: map[int64]*User{
		7: {ID: 7, Status: StatusActive},
	}}
	return apiKeyRepo, groupRepo, userRepo
}

// B1 回归：读视图返回的成员 1:1 提交回来必须 200（改造前这里必然 400 ORANGE_GROUP_CHAIN_INVALID：
// 停用 / 跨平台 / 已删除成员会被逐个拒绝），且这些成员的 state 在重读视图里保持原样。
//
// 本用例取代了旧口径的 TestOrangeSetGroupChainRejectsDisabledGroup（"候选停用即 400"）：
// 契约 C2 起只有链首强校验，候选停用只影响 state，不再阻断保存。
func TestOrangeSetGroupChainAcceptsIncompatibleCandidates(t *testing.T) {
	apiKeyRepo, groupRepo, userRepo := orangeChainWriteTestFixture()
	svc := orangeChainStateTestService(apiKeyRepo, groupRepo, userRepo)
	t.Cleanup(func() { InvalidateOrangeGroupChainCache(29) })

	before, err := svc.OrangeGetAPIKeyGroupChain(context.Background(), 7, 29)
	require.NoError(t, err)
	require.Equal(t, []int64{2, 15, 17, 19, 18}, orangeChainMemberIDs(before.Members))
	require.Equal(t, []OrangeChainMemberState{
		OrangeChainMemberStateAvailable,
		OrangeChainMemberStateAvailable,
		OrangeChainMemberStateDisabled,
		OrangeChainMemberStateScopeMismatch,
		OrangeChainMemberStateMissing,
	}, orangeChainMemberStates(before.Members))

	// 前端把编辑弹窗里的 5 行原样提交（既不删也不改顺序）。
	view, err := svc.OrangeSetAPIKeyGroupChain(context.Background(), 7, 29, OrangeSetGroupChainInput{
		GroupIDs:         orangeChainMemberIDs(before.Members),
		Mode:             OrangeGroupChainModeAggregate,
		ExpectedGroupIDs: orangeChainMemberIDs(before.Members),
	})
	require.NoError(t, err)
	require.Equal(t, 1, apiKeyRepo.replaceAndPrimaryCalls)
	require.Equal(t, int64(2), apiKeyRepo.lastPrimaryID)
	require.Equal(t, []int64{15, 17, 19, 18}, apiKeyRepo.lastTail)

	// 重读视图：停用 / 跨平台 / 已删除成员一个都不能少，state 也不能被改写。
	require.Equal(t, []int64{2, 15, 17, 19, 18}, orangeChainMemberIDs(view.Members))
	require.Equal(t, []OrangeChainMemberState{
		OrangeChainMemberStateAvailable,
		OrangeChainMemberStateAvailable,
		OrangeChainMemberStateDisabled,
		OrangeChainMemberStateScopeMismatch,
		OrangeChainMemberStateMissing,
	}, orangeChainMemberStates(view.Members))
}

// 已删除分组的候选 ID：只要它当前仍在链上就允许写回（用户配置不丢，契约 C2）。
func TestOrangeSetGroupChainKeepsDeletedCandidate(t *testing.T) {
	apiKeyRepo, groupRepo, userRepo := orangeChainStateTestFixture()
	svc := orangeChainStateTestService(apiKeyRepo, groupRepo, userRepo)
	t.Cleanup(func() { InvalidateOrangeGroupChainCache(29) })

	// 只保留链首 2 与已删除的候选 18（15/16/17 会被移除，需要确认）。
	view, err := svc.OrangeSetAPIKeyGroupChain(context.Background(), 7, 29, OrangeSetGroupChainInput{
		GroupIDs:     []int64{2, 18},
		Mode:         OrangeGroupChainModeFallback,
		ConfirmPrune: true,
	})
	require.NoError(t, err)
	require.Equal(t, []int64{18}, apiKeyRepo.lastTail)
	require.Equal(t, []int64{2, 18}, orangeChainMemberIDs(view.Members))
	// 18 在分组表里已不存在：视图里只有 ID，state=missing。
	require.Nil(t, view.Members[1].Group)
	require.Equal(t, OrangeChainMemberStateMissing, view.Members[1].State)
}

// 停用分组作为**候选**：PUT 200 且成员被保留（旧口径是 400 ORANGE_GROUP_CHAIN_INVALID）。
// 这里带 ConfirmPrune 只是因为本次提交顺手移除了 15/16/18 三个候选，与"停用"本身无关。
func TestOrangeSetGroupChainKeepsDisabledCandidate(t *testing.T) {
	apiKeyRepo, groupRepo, userRepo := orangeChainStateTestFixture()
	svc := orangeChainStateTestService(apiKeyRepo, groupRepo, userRepo)
	t.Cleanup(func() { InvalidateOrangeGroupChainCache(29) })

	view, err := svc.OrangeSetAPIKeyGroupChain(context.Background(), 7, 29, OrangeSetGroupChainInput{
		GroupIDs:     []int64{2, 17},
		Mode:         OrangeGroupChainModeFallback,
		ConfirmPrune: true,
	})
	require.NoError(t, err)
	require.Equal(t, 1, apiKeyRepo.replaceAndPrimaryCalls)
	require.Equal(t, []int64{17}, apiKeyRepo.lastTail, "停用分组的候选必须被保留")
	require.Equal(t, []int64{2, 17}, orangeChainMemberIDs(view.Members))
	require.Equal(t, OrangeChainMemberStateDisabled, view.Members[1].State)
}

// 链首仍是停用分组 → 400（强校验只放宽候选，链首保持原行为）。
// 注意顺序：本次提交同时会移除成员，因此先要过 409 删除保护，确认后才走到链首校验。
func TestOrangeSetGroupChainRejectsDisabledHead(t *testing.T) {
	apiKeyRepo, groupRepo, userRepo := orangeChainWriteTestFixture()
	svc := orangeChainStateTestService(apiKeyRepo, groupRepo, userRepo)
	t.Cleanup(func() { InvalidateOrangeGroupChainCache(29) })

	_, err := svc.OrangeSetAPIKeyGroupChain(context.Background(), 7, 29, OrangeSetGroupChainInput{
		GroupIDs:     []int64{17, 15},
		Mode:         OrangeGroupChainModeFallback,
		ConfirmPrune: true,
	})
	require.ErrorIs(t, err, ErrOrangeGroupChainInvalid)
	require.True(t, infraerrors.IsBadRequest(err))
	require.Zero(t, apiKeyRepo.writeCalls())
}

// 候选里出现「既不存在、也不在当前链上」的分组 ID → 404 GROUP_NOT_FOUND。
func TestOrangeSetGroupChainRejectsUnknownCandidate(t *testing.T) {
	apiKeyRepo, groupRepo, userRepo := orangeChainStateTestFixture()
	svc := orangeChainStateTestService(apiKeyRepo, groupRepo, userRepo)
	t.Cleanup(func() { InvalidateOrangeGroupChainCache(29) })

	_, err := svc.OrangeSetAPIKeyGroupChain(context.Background(), 7, 29, OrangeSetGroupChainInput{
		GroupIDs:     []int64{2, 99},
		Mode:         OrangeGroupChainModeFallback,
		ConfirmPrune: true,
	})
	require.ErrorIs(t, err, ErrGroupNotFound)
	require.True(t, infraerrors.IsNotFound(err))
	require.Zero(t, apiKeyRepo.writeCalls())
}

// ---- 契约 E：写链成功但视图重建失败时不得回 5xx ----

// orangeChainFailingUserRepo：前 failFrom-1 次 GetByID 成功，之后失败（模拟写后 owner 读不到）。
type orangeChainFailingUserRepo struct {
	UserRepository
	inner    *orangeChainStateTestUserRepo
	failFrom int
	calls    int
}

func (r *orangeChainFailingUserRepo) GetByID(ctx context.Context, id int64) (*User, error) {
	r.calls++
	if r.failFrom > 0 && r.calls >= r.failFrom {
		return nil, errors.New("user repo unavailable")
	}
	return r.inner.GetByID(ctx, id)
}

func TestOrangeSetGroupChainDegradesViewWhenOwnerReloadFails(t *testing.T) {
	apiKeyRepo, groupRepo, userRepo := orangeChainStateTestFixture()
	// 第 1 次 GetByID 是写前校验用户绑定权限（必须成功），第 2 次是写后重载 owner（模拟失败）。
	failing := &orangeChainFailingUserRepo{inner: userRepo, failFrom: 2}
	svc := &APIKeyService{apiKeyRepo: apiKeyRepo, userRepo: failing, groupRepo: groupRepo}
	t.Cleanup(func() { InvalidateOrangeGroupChainCache(29) })

	view, err := svc.OrangeSetAPIKeyGroupChain(context.Background(), 7, 29, OrangeSetGroupChainInput{
		GroupIDs:         []int64{2, 15, 16, 17, 18},
		Mode:             OrangeGroupChainModeAggregate,
		ExpectedGroupIDs: []int64{2, 15, 16, 17, 18},
	})
	require.NoError(t, err, "链已写库，视图重建失败不能再回 5xx")
	require.Equal(t, 1, apiKeyRepo.replaceAndPrimaryCalls)
	require.Equal(t, 2, failing.calls)

	// 降级视图：成员集合与顺序仍完整，但 state 为空（拿不到 owner 时不判权限）。
	require.Equal(t, int64(29), view.APIKeyID)
	require.Equal(t, int64(2), view.PrimaryGroupID)
	require.Equal(t, OrangeGroupChainModeAggregate, view.Mode)
	require.Equal(t, []int64{2, 15, 16, 17, 18}, orangeChainMemberIDs(view.Members))
	for i, member := range view.Members {
		require.Empty(t, string(member.State), "member %d must not carry a state when degraded", i)
	}
}

// ---- 显式 heal：唯一的读后写库入口 ----

func TestOrangeHealGroupChainPrunesOnlyNoPermissionAndMissing(t *testing.T) {
	apiKeyRepo, groupRepo, userRepo := orangeChainStateTestFixture()
	svc := orangeChainStateTestService(apiKeyRepo, groupRepo, userRepo)
	t.Cleanup(func() { InvalidateOrangeGroupChainCache(29) })

	view, err := svc.OrangeHealAPIKeyGroupChain(context.Background(), 7, 29)
	require.NoError(t, err)

	// 只修剪 no_permission(16) 与 missing(18)；停用(17) 必须保留（既有单测语义）。
	require.Equal(t, 1, apiKeyRepo.replaceAndPrimaryCalls)
	require.Equal(t, int64(2), apiKeyRepo.lastPrimaryID)
	require.Equal(t, []int64{15, 17}, apiKeyRepo.lastTail)
	require.Equal(t, OrangeGroupChainModeAggregate, apiKeyRepo.lastMode)

	require.Equal(t, []int64{16, 18}, view.AutoPrunedGroupIDs)
	require.Len(t, view.AutoPrunedGroups, 2)
	require.Equal(t, "自用圆模", view.AutoPrunedGroups[0].Name)
	require.Equal(t, "#18", view.AutoPrunedGroups[1].Name)
	require.Zero(t, view.AutoPromotedGroupID)

	// 返回的是写库之后的完整视图：[2, 15, 17]，17 仍是 disabled。
	require.Equal(t, []int64{2, 15, 17}, orangeChainMemberIDs(view.Members))
	require.Equal(t, []OrangeChainMemberState{
		OrangeChainMemberStateAvailable,
		OrangeChainMemberStateAvailable,
		OrangeChainMemberStateDisabled,
	}, orangeChainMemberStates(view.Members))

	// 幂等：再 heal 一次没有可修剪项，不得再写库。
	second, err := svc.OrangeHealAPIKeyGroupChain(context.Background(), 7, 29)
	require.NoError(t, err)
	require.Equal(t, 1, apiKeyRepo.replaceAndPrimaryCalls)
	require.Empty(t, second.AutoPrunedGroupIDs)
}

// 链首失去权限时 heal 会提升第一个可用成员，并把旧链首记入 auto_pruned。
func TestOrangeHealGroupChainPromotesEntitledMember(t *testing.T) {
	head := orangeChainStateTestExclusiveGroup(2, "主分组已撤权")
	member := orangeChainStateTestGroup(15, "自用生图")
	groups := map[int64]*Group{2: head, 15: member}

	apiKeyRepo := newOrangeChainStateTestAPIKeyRepo(groups)
	apiKeyRepo.keys[29] = &APIKey{ID: 29, UserID: 7, Key: "sk-orange-state-test",
		GroupID: orangeChainStateTestInt64Ptr(2), Group: cloneOrangeGroup(head), User: &User{ID: 7}}
	apiKeyRepo.members[29] = []OrangeGroupChainMember{{GroupID: 15, Priority: 1}}
	apiKeyRepo.modes[29] = OrangeGroupChainModeFallback

	groupRepo := &orangeChainStateTestGroupRepo{groups: groups}
	userRepo := &orangeChainStateTestUserRepo{users: map[int64]*User{
		7: {ID: 7, Status: StatusActive, AllowedGroups: []int64{15}},
	}}
	svc := orangeChainStateTestService(apiKeyRepo, groupRepo, userRepo)
	t.Cleanup(func() { InvalidateOrangeGroupChainCache(29) })

	view, err := svc.OrangeHealAPIKeyGroupChain(context.Background(), 7, 29)
	require.NoError(t, err)
	require.Equal(t, int64(15), apiKeyRepo.lastPrimaryID)
	require.Empty(t, apiKeyRepo.lastTail)
	require.Equal(t, int64(15), view.PrimaryGroupID)
	require.Equal(t, []int64{2}, view.AutoPrunedGroupIDs)
	require.Equal(t, int64(15), view.AutoPromotedGroupID)
	require.Equal(t, []int64{15}, orangeChainMemberIDs(view.Members))
}
