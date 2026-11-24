// Orange 特有：分组链「链成员权限」的单元测试。
//
// 背景：分组链（兜底分组 / 聚合密钥）是创建时写入 orange_api_key_group_chain 的快照，
// 分组之后被改成专属、或撤销 user_allowed_groups 授权都不会回写该表。上游鉴权中间件
// 只校验链首（api_keys.group_id），因此链成员必须在请求时再做一次"用户是否仍拥有该分组"
// 的判定，否则请求会被路由到用户已经没有权限的专属分组（越权 + 错误计费）。
//
// 本文件锁定以下口径：
//   - 专属未授权成员：不进入请求级路由链、不进入链模型策略（目录/白名单/生图门控）；
//   - 专属已授权 / 公开成员：行为与修复前一致（仍可路由）；
//   - restrict_public_groups 用户：未授权的公开成员同样被剔除；
//   - 链首失去权限：不静默换组，退回"普通 Key"由上游返回 403 GROUP_NOT_ALLOWED；
//   - 订阅型成员：沿用既有约定（由订阅服务校验），本文件不改变其行为。
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

// 链首失去权限：不得静默换到链上其它分组，必须保持"普通 Key"退到上游 403 语义。
func TestOrangeChainHeadWithoutGrantStaysPlainKey(t *testing.T) {
	head := orangeEntitlementTestExclusiveGroup(11)
	publicTail := orangePolicyTestGroup(13, PlatformAnthropic)

	apiKey := orangePolicyTestAPIKey(head, publicTail)
	apiKey.User = orangeEntitlementTestUser()

	if got, want := orangeChainGroupIDs(OrangeChainGroups(apiKey)), []int64{13}; !orangeIDsEqual(got, want) {
		t.Fatalf("model policy must ignore unauthorized head, got %v want %v", got, want)
	}
	if got := orangeEntitlementRouteTo(apiKey); got != nil {
		t.Fatalf("head without grant must not install group-chain routing (上游 403 兜底), got %v", got)
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

// 链详情接口必须把"无权限的成员"留在链上并标注可用性：用户要能看到"我配过它、现在没权限"。
func TestOrangeBuildAPIKeyGroupChainViewMarksUnavailableMember(t *testing.T) {
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
	if len(view.Members) != 2 {
		t.Fatalf("view must keep unavailable member for explanation, got %d members", len(view.Members))
	}
	if !view.Members[0].Available {
		t.Fatalf("head member should stay available, got %+v", view.Members[0])
	}
	if view.Members[1].Available {
		t.Fatalf("exclusive member without grant must be marked unavailable, got %+v", view.Members[1])
	}
	if view.Members[1].UnavailableReason != OrangeGroupChainUnavailableReasonNotEntitled {
		t.Fatalf("unexpected reason %q", view.Members[1].UnavailableReason)
	}

	// 授权恢复后同一接口必须立刻回到"可用"（请求时会重新用该分组）。
	apiKey.User.AllowedGroups = []int64{exclusive.ID}
	view = (&APIKeyService{}).OrangeBuildAPIKeyGroupChainView(context.Background(), apiKey)
	if len(view.Members) != 2 || !view.Members[1].Available || view.Members[1].UnavailableReason != "" {
		t.Fatalf("granted member must become available again, got %+v", view.Members)
	}
}
