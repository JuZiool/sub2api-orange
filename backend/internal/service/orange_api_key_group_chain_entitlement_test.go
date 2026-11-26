// Orange 特有：分组链「链成员权限」与「权限自愈」的单元测试。
//
// 背景：分组链（兜底分组 / 聚合密钥）是写链时的快照，分组之后被改成「专属」或撤销
// user_allowed_groups 授权都不会回写 orange_api_key_group_chain。上游鉴权中间件只校验链首
// （api_keys.group_id），因此 Orange 侧必须补两层能力：
//
//  1. 请求时：链成员按用户权限过滤（不进路由链 / 不进模型目录 / 不放行白名单 / 不放行生图）；
//     链首失去权限时请求内自愈（提升第一个可用成员），避免整条 Key 直接 403；
//  2. 显式 heal 接口（v0.2.25-8 起 GET 是纯读，不再写库）：把失去权限/已删除的成员从链里剔除并写库；
//     读视图本身返回全部成员 + state，界面据此置灰提示（不再"直接不显示"）。
//
// 本文件锁定上述口径（含"不能误伤仍可用的分组"）。
package service

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
)

// orangeEntitlementTestUser 构造没有任何专属分组授权的普通用户。
func orangeEntitlementTestUser() *User {
	return &User{ID: 7, Status: StatusActive}
}

// orangeEntitlementTestExclusiveGroup 构造专属（is_exclusive=true）标准分组。
func orangeEntitlementTestExclusiveGroup(id int64) *Group {
	group := orangePolicyTestGroup(id, PlatformAnthropic)
	group.IsExclusive = true
	return group
}

// orangeEntitlementRouteTo 安装请求级路由链（与鉴权中间件的调用顺序一致），
// 返回请求级路由链上各分组的 ID（未安装链时返回 nil）。
func orangeEntitlementRouteTo(apiKey *APIKey) []int64 {
	headCopy := *apiKey.Group
	ctx := context.WithValue(context.Background(), ctxkey.Group, &headCopy)
	ctx = WithOrangeKeyGroupRouting(ctx, apiKey)
	routing, ok := orangeKeyRoutingFromContext(ctx)
	if !ok || routing == nil {
		return nil
	}
	ids := make([]int64, 0, len(routing.chain))
	for _, entry := range routing.chain {
		ids = append(ids, entry.Group.ID)
	}
	return ids
}

func orangeChainGroupIDs(groups []*Group) []int64 {
	ids := make([]int64, 0, len(groups))
	for _, group := range groups {
		ids = append(ids, group.ID)
	}
	return ids
}

func orangeIDsEqual(got, want []int64) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

// 链成员被改成专属且未授权给该用户：请求级路由链与链模型策略都必须剔除它。
func TestOrangeChainSkipsExclusiveMemberWithoutGrant(t *testing.T) {
	head := orangePolicyTestGroup(11, PlatformAnthropic)
	exclusive := orangeEntitlementTestExclusiveGroup(12)
	tail := orangePolicyTestGroup(13, PlatformAnthropic)

	apiKey := orangePolicyTestAPIKey(head, exclusive, tail)
	apiKey.User = orangeEntitlementTestUser()

	if got, want := orangeChainGroupIDs(OrangeChainGroups(apiKey)), []int64{11, 13}; !orangeIDsEqual(got, want) {
		t.Fatalf("OrangeChainGroups should skip exclusive member, got %v want %v", got, want)
	}
	if got, want := orangeEntitlementRouteTo(apiKey), []int64{11, 13}; !orangeIDsEqual(got, want) {
		t.Fatalf("request routing chain should skip exclusive member, got %v want %v", got, want)
	}

	// failover 推进同样只能看到链上仍有权限的分组。
	headCopy := *apiKey.Group
	ctx := WithOrangeKeyGroupRouting(context.WithValue(context.Background(), ctxkey.Group, &headCopy), apiKey)
	next, ok := OrangeChainNextGroupAfterFailure(ctx, apiKey, map[int64]struct{}{})
	if !ok || next == nil || next.ID != tail.ID {
		t.Fatalf("failover should advance to entitled group %d, got %+v (ok=%v)", tail.ID, next, ok)
	}
}

// 专属成员仍在 user_allowed_groups 中：授权未被撤销，行为必须与修复前一致。
func TestOrangeChainKeepsGrantedExclusiveMember(t *testing.T) {
	head := orangePolicyTestGroup(11, PlatformAnthropic)
	exclusive := orangeEntitlementTestExclusiveGroup(12)

	apiKey := orangePolicyTestAPIKey(head, exclusive)
	apiKey.User = orangeEntitlementTestUser()
	apiKey.User.AllowedGroups = []int64{12}

	if got, want := orangeChainGroupIDs(OrangeChainGroups(apiKey)), []int64{11, 12}; !orangeIDsEqual(got, want) {
		t.Fatalf("granted exclusive member must stay usable, got %v want %v", got, want)
	}
	if got, want := orangeEntitlementRouteTo(apiKey), []int64{11, 12}; !orangeIDsEqual(got, want) {
		t.Fatalf("granted exclusive member must stay routable, got %v want %v", got, want)
	}
}

// restrict_public_groups 用户：未授权的公开成员同样越权，必须剔除。
func TestOrangeChainSkipsPublicMemberForRestrictedUser(t *testing.T) {
	head := orangePolicyTestGroup(11, PlatformAnthropic)
	publicTail := orangePolicyTestGroup(13, PlatformAnthropic)

	apiKey := orangePolicyTestAPIKey(head, publicTail)
	apiKey.User = orangeEntitlementTestUser()
	apiKey.User.RestrictPublicGroups = true
	apiKey.User.AllowedGroups = []int64{11}

	if got, want := orangeChainGroupIDs(OrangeChainGroups(apiKey)), []int64{11}; !orangeIDsEqual(got, want) {
		t.Fatalf("restricted user must not see unauthorized public member, got %v want %v", got, want)
	}
	if got := orangeEntitlementRouteTo(apiKey); got != nil {
		t.Fatalf("restricted chain must collapse to plain key, got %v", got)
	}
}

// 链首失去权限：请求内自愈把第一个仍可用成员提升为主分组（不写库），避免整条 Key 直接 403。
func TestOrangeChainHeadWithoutGrantHealsToEntitledMember(t *testing.T) {
	head := orangeEntitlementTestExclusiveGroup(11)
	tail := orangePolicyTestGroup(13, PlatformAnthropic)

	apiKey := orangePolicyTestAPIKey(head, tail)
	apiKey.User = orangeEntitlementTestUser()

	// 建链时已按权限过滤候选；链首仍原样留在 Groups[0]，由 heal 负责提升。
	if healed := healOrangeKeyChainHead(context.Background(), apiKey); !healed {
		t.Fatal("head without grant should be healed to the first entitled member")
	}
	if apiKey.Group == nil || apiKey.Group.ID != tail.ID {
		t.Fatalf("healed head should be %d, got %+v", tail.ID, apiKey.Group)
	}
	if apiKey.GroupID == nil || *apiKey.GroupID != tail.ID {
		t.Fatalf("healed head id should be %d, got %v", tail.ID, apiKey.GroupID)
	}
	if len(apiKey.Groups) != 1 || apiKey.Groups[0].Group.ID != tail.ID {
		t.Fatalf("chain should be rebuilt from healed group, got %+v", apiKey.Groups)
	}
}

// 所有分组都失去权限：无从提升，保持原样（请求侧仍按 403 处理）。
func TestOrangeChainHeadWithoutGrantAndNoEntitledMember(t *testing.T) {
	head := orangeEntitlementTestExclusiveGroup(11)

	apiKey := orangePolicyTestAPIKey(head)
	apiKey.User = orangeEntitlementTestUser()

	if healed := healOrangeKeyChainHead(context.Background(), apiKey); healed {
		t.Fatal("nothing to heal when every chain member lost entitlement")
	}
	if apiKey.Group == nil || apiKey.Group.ID != head.ID {
		t.Fatalf("head must stay unchanged, got %+v", apiKey.Group)
	}
}

// 订阅型链成员：沿用既有约定（订阅可用性由订阅服务在鉴权阶段校验），本次不改其行为。
func TestOrangeChainSubscriptionMembersUnchanged(t *testing.T) {
	head := orangePolicyTestGroup(21, PlatformAnthropic)
	head.SubscriptionType = SubscriptionTypeSubscription
	member := orangePolicyTestGroup(22, PlatformAnthropic)
	member.SubscriptionType = SubscriptionTypeSubscription

	apiKey := orangePolicyTestAPIKey(head, member)
	apiKey.User = orangeEntitlementTestUser()

	if got, want := orangeChainGroupIDs(OrangeChainGroups(apiKey)), []int64{21, 22}; !orangeIDsEqual(got, want) {
		t.Fatalf("subscription chain members keep existing behaviour, got %v want %v", got, want)
	}
	if got, want := orangeEntitlementRouteTo(apiKey), []int64{21, 22}; !orangeIDsEqual(got, want) {
		t.Fatalf("subscription chain routing keeps existing behaviour, got %v want %v", got, want)
	}
}

// 没有用户快照（鉴权快照未加载 User 的极端情况）时不过滤，避免把正常链路判成越权。
func TestOrangeChainWithoutUserSnapshotKeepsMembers(t *testing.T) {
	head := orangePolicyTestGroup(11, PlatformAnthropic)
	exclusive := orangeEntitlementTestExclusiveGroup(12)

	apiKey := orangePolicyTestAPIKey(head, exclusive)
	if got, want := orangeChainGroupIDs(OrangeChainGroups(apiKey)), []int64{11, 12}; !orangeIDsEqual(got, want) {
		t.Fatalf("missing user snapshot must not filter chain, got %v want %v", got, want)
	}
}

// 兼容视图（网关/旧调用方用的 OrangeBuildAPIKeyGroupChainView）仍只返回可用成员；
// v0.2.25-8 的链详情/列表一律用 OrangeBuildAPIKeyGroupChainViewFull（返回全部成员 + state，
// 见 orange_api_key_group_chain_state_test.go）。授权恢复后同一接口必须立刻重新可见。
func TestOrangeBuildAPIKeyGroupChainViewHidesUnavailableMember(t *testing.T) {
	head := orangePolicyTestGroup(11, PlatformAnthropic)
	exclusive := orangeEntitlementTestExclusiveGroup(12)

	apiKey := orangePolicyTestAPIKey(head, exclusive)
	apiKey.User = orangeEntitlementTestUser()

	// 直接播种进程内链缓存（等价于库里已有一条候选记录）。
	orangeGroupChainCache.mu.Lock()
	orangeGroupChainCache.entries[apiKey.ID] = orangeGroupChainCacheEntry{
		expiresAt: time.Now().Add(time.Minute),
		members:   []OrangeGroupChainMember{{GroupID: exclusive.ID, Priority: 1}},
		groups:    []*Group{exclusive},
		mode:      OrangeGroupChainModeFallback,
	}
	orangeGroupChainCache.mu.Unlock()
	t.Cleanup(func() { InvalidateOrangeGroupChainCache(apiKey.ID) })

	view := (&APIKeyService{}).OrangeBuildAPIKeyGroupChainView(context.Background(), apiKey)
	if len(view.Members) != 1 || view.Members[0].GroupID != head.ID {
		t.Fatalf("view must hide member without entitlement, got %+v", view.Members)
	}

	// 授权恢复后同一接口必须立刻回到"可见"（请求也会重新用该分组）。
	apiKey.User.AllowedGroups = []int64{exclusive.ID}
	view = (&APIKeyService{}).OrangeBuildAPIKeyGroupChainView(context.Background(), apiKey)
	if len(view.Members) != 2 || view.Members[1].GroupID != exclusive.ID {
		t.Fatalf("granted member must become visible again, got %+v", view.Members)
	}
}

// ---- 权限自愈方案（纯函数） ----

func TestOrangeChainSelfHealPlanPrunesMemberAndKeepsHead(t *testing.T) {
	head := orangePolicyTestGroup(11, PlatformAnthropic)
	gone := orangeEntitlementTestExclusiveGroup(12)
	tail := orangePolicyTestGroup(13, PlatformAnthropic)
	user := orangeEntitlementTestUser()

	plan, changed := orangeChainSelfHealPlan(user, head, []*Group{gone, tail}, OrangeGroupChainModeAggregate)
	if !changed {
		t.Fatal("plan should report a change when a member lost entitlement")
	}
	if plan.Primary == nil || plan.Primary.ID != head.ID {
		t.Fatalf("head must stay primary, got %+v", plan.Primary)
	}
	if !orangeIDsEqual(plan.Tail, []int64{tail.ID}) {
		t.Fatalf("tail should drop the pruned member, got %v", plan.Tail)
	}
	if got, want := orangeChainGroupIDs(plan.Pruned), []int64{gone.ID}; !orangeIDsEqual(got, want) {
		t.Fatalf("pruned ids mismatch, got %v want %v", got, want)
	}
	if plan.Mode != OrangeGroupChainModeAggregate {
		t.Fatalf("user intent (aggregate) must be preserved, got %q", plan.Mode)
	}
}

func TestOrangeChainSelfHealPlanPromotesFirstEntitledMember(t *testing.T) {
	head := orangeEntitlementTestExclusiveGroup(11)
	second := orangePolicyTestGroup(13, PlatformAnthropic)
	third := orangePolicyTestGroup(14, PlatformAnthropic)
	user := orangeEntitlementTestUser()

	plan, changed := orangeChainSelfHealPlan(user, head, []*Group{second, third}, OrangeGroupChainModeAggregate)
	if !changed {
		t.Fatal("plan should report a change when head lost entitlement")
	}
	if plan.Primary == nil || plan.Primary.ID != second.ID {
		t.Fatalf("first entitled member should be promoted, got %+v", plan.Primary)
	}
	if !orangeIDsEqual(plan.Tail, []int64{third.ID}) {
		t.Fatalf("tail mismatch, got %v", plan.Tail)
	}
	if got, want := orangeChainGroupIDs(plan.Pruned), []int64{head.ID}; !orangeIDsEqual(got, want) {
		t.Fatalf("pruned ids mismatch, got %v want %v", got, want)
	}
	if plan.Mode != OrangeGroupChainModeAggregate {
		t.Fatalf("user intent (aggregate) must be preserved, got %q", plan.Mode)
	}
}

func TestOrangeChainSelfHealPlanClearsChainWhenTooFewRemain(t *testing.T) {
	head := orangePolicyTestGroup(11, PlatformAnthropic)
	gone := orangeEntitlementTestExclusiveGroup(12)
	user := orangeEntitlementTestUser()

	plan, changed := orangeChainSelfHealPlan(user, head, []*Group{gone}, OrangeGroupChainModeFallback)
	if !changed {
		t.Fatal("plan should report a change")
	}
	if plan.Primary == nil || plan.Primary.ID != head.ID {
		t.Fatalf("single remaining group must stay primary, got %+v", plan.Primary)
	}
	if len(plan.Tail) != 0 {
		t.Fatalf("chain should be cleared when fewer than two groups remain, got %v", plan.Tail)
	}
	if plan.Mode != "" {
		t.Fatalf("cleared chain must not persist a mode, got %q", plan.Mode)
	}
}

func TestOrangeChainSelfHealPlanNoChangeCases(t *testing.T) {
	head := orangePolicyTestGroup(11, PlatformAnthropic)
	tail := orangePolicyTestGroup(13, PlatformAnthropic)
	user := orangeEntitlementTestUser()

	if _, changed := orangeChainSelfHealPlan(user, head, []*Group{tail}, OrangeGroupChainModeFallback); changed {
		t.Fatal("nothing lost entitlement: plan must not report a change")
	}

	// 全部失去权限：无从提升，保持原样（交给用户自己改主分组）。
	goneHead := orangeEntitlementTestExclusiveGroup(21)
	goneTail := orangeEntitlementTestExclusiveGroup(22)
	if _, changed := orangeChainSelfHealPlan(user, goneHead, []*Group{goneTail}, OrangeGroupChainModeFallback); changed {
		t.Fatal("every member lost entitlement: plan must not report a change")
	}
}

func TestOrangeChainSelfHealPlanKeepsInactiveEntitledMemberInTail(t *testing.T) {
	head := orangePolicyTestGroup(11, PlatformAnthropic)
	gone := orangeEntitlementTestExclusiveGroup(12)
	paused := orangePolicyTestGroup(13, PlatformAnthropic)
	paused.Status = StatusDisabled
	user := orangeEntitlementTestUser()

	plan, changed := orangeChainSelfHealPlan(user, head, []*Group{gone, paused}, OrangeGroupChainModeAggregate)
	if !changed {
		t.Fatal("plan should report a change")
	}
	// 临时停用的分组仍有绑定权限：不能因为"暂时停用"就永久删掉用户的配置。
	if !orangeIDsEqual(plan.Tail, []int64{paused.ID}) {
		t.Fatalf("inactive-but-entitled member must stay in tail, got %v", plan.Tail)
	}
}
