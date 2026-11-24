// Orange 特有：API Key 分组优先级链（密钥级兜底分组 + 聚合分组）。
//
// 语义：一条有序链表达一切能力 ——
//   - 无链（长度 1，只有链首）：普通 Key，行为与上游完全一致；
//   - 长度 2：密钥级兜底分组（主分组无可用账号时切到兜底分组）；
//   - 长度 3..8：聚合分组，按 priority 依次尝试。
//
// 关键约定：
//  1. 链首恒等于 api_keys.group_id（上游既有列、既有语义），本表只存链首之后的候选；
//  2. 切组通过"请求内指针替换"实现：apiKey.Group / apiKey.GroupID / ctxkey.Group 指向
//     实际命中的分组，因此下游限流、计费、用量归属自动跟随，无需改动计费代码；
//  3. GroupID 必须分配**新指针**，禁止原地写 —— 认证缓存快照会把 GroupID 指针直接共享给
//     请求级 APIKey（api_key_auth_cache_impl.go snapshotToAPIKey），原地写会污染 L1/L2 缓存；
//  4. 只有"分组内无可用账号"（ErrNoAvailableAccounts / ErrNoAvailableCompactAccounts）才推进，
//     上游 429/5xx/超时属账号级失败，不换分组；强制平台路由（ctxkey.ForcePlatform）不推进。
package service

import (
	"context"
	"errors"
	"log/slog"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
)

// Orange 特有分组链的参数。
const (
	// orangeChainMaxCandidates 是链首之后的候选项上限（整链最多 8 组）。
	orangeChainMaxCandidates = 8
	// maxOrangeChainAttempts 是单请求内分组链推进的上限，防止脏数据造成死循环。
	maxOrangeChainAttempts = 16
)

// Orange 特有分组链的模式取值（对外 DTO 的 mode 字段）。
const (
	OrangeGroupChainModeNone      = "none"
	OrangeGroupChainModeFallback  = "fallback"
	OrangeGroupChainModeAggregate = "aggregate"
)

// OrangeGroupChainMember 是 orange_api_key_group_chain 表的一行，即"链首之后的候选"。
// Priority 从 1 起，数值越小越先尝试。
type OrangeGroupChainMember struct {
	GroupID  int64
	Priority int
}

// APIKeyGroupEntry 是分组优先级链上的一项（含链首）。
type APIKeyGroupEntry struct {
	Group    *Group
	Priority int
}

// orangeKeyGroupRouting 是请求内的分组路由链。普通 Key 长度 1，兜底 Key 长度 2，聚合 Key 更长。
type orangeKeyGroupRouting struct {
	primaryGroupID int64
	chain          []APIKeyGroupEntry
	apiKey         *APIKey
}

type orangeKeyGroupRoutingContextKey struct{}

// GroupChain 返回该 Key 的分组优先级链（含链首，按 priority 升序）。
// 链上 Group 为空/无效的项会被跳过；未配置链时返回 nil。
func (k *APIKey) GroupChain() []*APIKeyGroupEntry {
	if k == nil || len(k.Groups) == 0 {
		return nil
	}
	chain := make([]*APIKeyGroupEntry, 0, len(k.Groups))
	for _, entry := range k.Groups {
		if entry == nil || entry.Group == nil || entry.Group.ID <= 0 {
			continue
		}
		chain = append(chain, entry)
	}
	if len(chain) == 0 {
		return nil
	}
	return chain
}

// HasGroupChain 报告该 Key 是否配置了分组链（兜底或聚合）。
func (k *APIKey) HasGroupChain() bool {
	return len(k.GroupChain()) >= 2
}

// IsAggregated 报告该 Key 是否为聚合密钥（链上 ≥3 组）。
func (k *APIKey) IsAggregated() bool {
	return len(k.GroupChain()) >= 3
}

// OrangeGroupChainMode 返回该 Key 的分组链模式：none / fallback / aggregate。
// 装载链时写入的持久化模式优先（聚合密钥可能只配了 2 个分组），否则按链长推断。
func (k *APIKey) OrangeGroupChainMode() string {
	if k != nil && (k.GroupChainMode == OrangeGroupChainModeFallback || k.GroupChainMode == OrangeGroupChainModeAggregate) {
		return k.GroupChainMode
	}
	switch len(k.GroupChain()) {
	case 0, 1:
		return OrangeGroupChainModeNone
	case 2:
		return OrangeGroupChainModeFallback
	default:
		return OrangeGroupChainModeAggregate
	}
}

// orangeChainMemberUsableForUser 报告某个分组作为"分组链成员"在本请求内是否可用：
// 分组已停用、上下文不完整，或**用户已不再拥有该分组的绑定权限**时返回 false。
//
// 权限口径与上游单分组校验（middleware.validateAPIKeyGroupAllowed → user.CanBindGroup）
// 完全一致：专属分组必须在 user_allowed_groups 中；开启 restrict_public_groups 的用户，
// 公开分组也必须落在其中。分组链写在 orange_api_key_group_chain 表里是创建时的快照，
// 分组之后被改成专属 / 授权被撤销都不会回写该表，因此必须每请求判定一次。
//
// 链首不靠这里兜底：链首失去权限时由上游鉴权中间件返回 403 GROUP_NOT_ALLOWED
// （与普通 Key 语义一致），静默换组会掩盖"管理员收回了该分组"这件事。
func orangeChainMemberUsableForUser(user *User, group *Group) bool {
	if group == nil || group.ID <= 0 {
		return false
	}
	if !group.IsActive() || !IsGroupContextValid(group) {
		return false
	}
	return orangeGroupBindableForUser(user, group)
}

// orangeUsableKeyChain 按 priority 顺序构建"本次请求可用"的链：过滤空项、软删（GetByID 已剔除）、
// 停用、上下文不完整、**用户已无绑定权限**、重复分组，以及与链首不同平台/不同订阅类型的成员。
// 过滤而不是整链作废，这样单个候选被停用/删除/收回授权不会让整条链失效。
func orangeUsableKeyChain(apiKey *APIKey) []APIKeyGroupEntry {
	entries := apiKey.GroupChain()
	if len(entries) < 2 {
		return nil
	}
	chain := make([]APIKeyGroupEntry, 0, len(entries))
	seen := make(map[int64]struct{}, len(entries))
	skipped := 0
	for _, entry := range entries {
		group := entry.Group
		if _, ok := seen[group.ID]; ok {
			continue
		}
		if !orangeChainMemberUsableForUser(apiKey.User, group) {
			skipped++
			continue
		}
		if len(chain) > 0 {
			head := chain[0].Group
			if group.Platform != head.Platform || group.SubscriptionType != head.SubscriptionType {
				continue
			}
		}
		seen[group.ID] = struct{}{}
		chain = append(chain, APIKeyGroupEntry{Group: group, Priority: entry.Priority})
	}
	if skipped > 0 {
		slog.Debug("orange_key_group_chain_member_skipped",
			"api_key_id", apiKey.ID,
			"skipped_count", skipped,
			"remaining_count", len(chain),
			"reason", "member_not_entitled_or_unavailable")
	}
	if len(chain) < 2 {
		return nil
	}
	return chain
}

// WithOrangeKeyGroupRouting 在鉴权通过后安装请求级分组路由链。
//
// 不满足条件时原样返回 ctx（退化为普通 Key，不报错）：链首必须就是当前主分组且可用，
// 其余成员必须可用、同平台、同订阅类型（跨平台链会在错误的网关上被调度；跨订阅类型会
// 绕过订阅限额校验）。
func WithOrangeKeyGroupRouting(ctx context.Context, apiKey *APIKey) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if apiKey == nil || apiKey.GroupID == nil || *apiKey.GroupID <= 0 {
		return ctx
	}
	chain := orangeUsableKeyChain(apiKey)
	if len(chain) < 2 {
		return ctx
	}
	head := chain[0]
	if head.Group.ID != *apiKey.GroupID {
		// 链首与主分组不一致（例如主分组刚被改动但缓存尚未刷新），本请求按普通 Key 处理。
		return ctx
	}
	for _, entry := range chain[1:] {
		if entry.Group.Platform != head.Group.Platform {
			return ctx
		}
		if entry.Group.SubscriptionType != head.Group.SubscriptionType {
			return ctx
		}
	}
	return context.WithValue(ctx, orangeKeyGroupRoutingContextKey{}, &orangeKeyGroupRouting{
		primaryGroupID: head.Group.ID,
		chain:          chain,
		apiKey:         apiKey,
	})
}

// orangeKeyRoutingFromContext 读取请求内的分组路由链。
func orangeKeyRoutingFromContext(ctx context.Context) (*orangeKeyGroupRouting, bool) {
	if ctx == nil {
		return nil, false
	}
	routing, ok := ctx.Value(orangeKeyGroupRoutingContextKey{}).(*orangeKeyGroupRouting)
	if !ok || routing == nil {
		return nil, false
	}
	return routing, true
}

func orangeChainGroupByID(chain []APIKeyGroupEntry, groupID int64) *Group {
	for i := range chain {
		if chain[i].Group != nil && chain[i].Group.ID == groupID {
			return chain[i].Group
		}
	}
	return nil
}

// orangeChainNextGroup 返回 groupID 在链上的下一个可用分组。
func orangeChainNextGroup(chain []APIKeyGroupEntry, groupID int64) *Group {
	for i := range chain {
		if chain[i].Group == nil || chain[i].Group.ID != groupID {
			continue
		}
		for j := i + 1; j < len(chain); j++ {
			if chain[j].Group != nil && chain[j].Group.ID > 0 && chain[j].Group.ID != groupID {
				return chain[j].Group
			}
		}
		return nil
	}
	return nil
}

// orangeClearUserGroupRPMOverride 清掉认证快照里按主分组算出的 (user, group) RPM 覆盖，
// 避免主分组的覆盖值泄漏到实际命中的分组。
func orangeClearUserGroupRPMOverride(apiKey *APIKey) {
	if apiKey == nil || apiKey.User == nil {
		return
	}
	if apiKey.User.UserGroupRPMOverride == nil && apiKey.User.UserGroupRPMOverrideGroupID == 0 {
		return
	}
	userCopy := *apiKey.User
	userCopy.UserGroupRPMOverride = nil
	userCopy.UserGroupRPMOverrideGroupID = 0
	apiKey.User = &userCopy
}

// activateOrangeKeyRouting 把请求级 apiKey / ctxkey.Group 切到实际命中的分组。
func activateOrangeKeyRouting(ctx context.Context, routedGroupID int64) bool {
	if ctx == nil || routedGroupID <= 0 {
		return false
	}
	routing, ok := orangeKeyRoutingFromContext(ctx)
	if !ok || routing.apiKey == nil || len(routing.chain) == 0 {
		return false
	}
	routed := orangeChainGroupByID(routing.chain, routedGroupID)
	if routed == nil {
		return false
	}
	if contextGroup, ok := ctx.Value(ctxkey.Group).(*Group); ok && contextGroup != nil {
		// 原地覆盖：handler 已持有同一指针，只有这样下游读 ctxkey.Group 才能看到路由后的分组。
		// 该对象来自认证快照的请求内物化（每请求新建）或本请求的链克隆，不会被跨请求共享。
		*contextGroup = *routed
	}
	// 必须分配新指针：认证快照里的 GroupID 指针是共享的，原地写会污染 L1/L2 缓存。
	groupID := routed.ID
	routing.apiKey.GroupID = &groupID
	routing.apiKey.Group = routed
	orangeClearUserGroupRPMOverride(routing.apiKey)
	return true
}

// healOrangeKeyChainHead 在链首分组失效（删除/停用/上下文不完整）时，把本请求的主分组
// 换成链上第一个可用成员。只改请求内指针，不写库；下次请求会再次自愈（幂等）。
func healOrangeKeyChainHead(ctx context.Context, apiKey *APIKey) bool {
	if apiKey == nil || len(apiKey.Groups) == 0 {
		return false
	}
	// 强制平台路由（/antigravity 等）在中间件阶段就确定了平台，换组会把请求送进别的平台，
	// 因此这里不自愈，保持上游既有的 403 行为。
	if ctx != nil {
		if forcePlatform, _ := ctx.Value(ctxkey.ForcePlatform).(string); forcePlatform != "" {
			return false
		}
	}
	head := apiKey.Group
	if head != nil && head.ID > 0 && head.IsActive() && IsGroupContextValid(head) {
		return false
	}

	healedIndex := -1
	var healed *Group
	for i, entry := range apiKey.Groups {
		if entry == nil || entry.Group == nil || entry.Group.ID <= 0 {
			continue
		}
		if head != nil && entry.Group.ID == head.ID {
			continue
		}
		group := entry.Group
		if !group.IsActive() || !IsGroupContextValid(group) {
			continue
		}
		if !orangeGroupBindableForUser(apiKey.User, group) {
			continue
		}
		healedIndex = i
		healed = group
		break
	}
	if healed == nil || healedIndex < 0 {
		return false
	}

	deadGroupID := int64(0)
	if head != nil {
		deadGroupID = head.ID
	}
	groupID := healed.ID
	apiKey.GroupID = &groupID
	apiKey.Group = healed
	apiKey.Groups = orangeRebuildChainFrom(apiKey.Groups, healedIndex)
	orangeClearUserGroupRPMOverride(apiKey)
	slog.Info("orange_key_group_chain_head_healed",
		"api_key_id", apiKey.ID,
		"dead_group_id", deadGroupID,
		"healed_group_id", healed.ID)
	return true
}

// orangeRebuildChainFrom 用第 index 项作为新的链首重建链（丢弃它之前的失效项），并重排序号。
func orangeRebuildChainFrom(entries []*APIKeyGroupEntry, index int) []*APIKeyGroupEntry {
	if index < 0 || index >= len(entries) {
		return entries
	}
	rebuilt := make([]*APIKeyGroupEntry, 0, len(entries))
	rebuilt = append(rebuilt, &APIKeyGroupEntry{Group: entries[index].Group, Priority: 1})
	for i := index + 1; i < len(entries); i++ {
		if entries[i] == nil || entries[i].Group == nil || entries[i].Group.ID <= 0 {
			continue
		}
		rebuilt = append(rebuilt, &APIKeyGroupEntry{Group: entries[i].Group, Priority: len(rebuilt) + 1})
	}
	return rebuilt
}

// orangeGroupBindableForUser 报告用户是否还能使用该分组（自愈时的权限兜底）。
func orangeGroupBindableForUser(user *User, group *Group) bool {
	if user == nil || group == nil {
		return true
	}
	if group.IsSubscriptionType() {
		// 订阅类分组的可用性由订阅服务在鉴权阶段单独校验。
		return true
	}
	return user.CanBindGroup(group.ID, group.IsExclusive)
}

// orangeNextChainGroup 判定当前分组选号失败后是否应推进到链上的下一个分组。
//
// 只认"分组内无可用账号"两类错误；强制平台路由、链已走完、当前分组不在链上都不推进。
func orangeNextChainGroup(ctx context.Context, groupID *int64, selectionErr error) (*Group, context.Context, bool) {
	if ctx == nil || groupID == nil || *groupID <= 0 {
		return nil, ctx, false
	}
	if !errors.Is(selectionErr, ErrNoAvailableAccounts) && !errors.Is(selectionErr, ErrNoAvailableCompactAccounts) {
		return nil, ctx, false
	}
	if forcePlatform, _ := ctx.Value(ctxkey.ForcePlatform).(string); forcePlatform != "" {
		return nil, ctx, false
	}
	routing, ok := orangeKeyRoutingFromContext(ctx)
	if !ok || len(routing.chain) < 2 {
		return nil, ctx, false
	}
	// groupID 可能已经是链上的中间分组（前一次推进的结果），按当前位置取下一项；
	// 走到链尾时 orangeChainNextGroup 返回 nil，推进自然终止。
	if orangeChainGroupByID(routing.chain, *groupID) == nil {
		return nil, ctx, false
	}
	next := orangeChainNextGroup(routing.chain, *groupID)
	if next == nil || !next.IsActive() || !IsGroupContextValid(next) {
		return nil, ctx, false
	}
	return next, ctx, true
}

// orangeChainGuardContextKey 标记「本 ctx 已在分组链的组内选号」。
//
// 外壳组内选号时递归调用公开入口（这样上游函数体可以原样留在上游文件里，不必劈成
// InGroup 副本）；被调用的入口看到本标记就直接执行原逻辑，不再推进链——保证一次请求
// 里链只推进一次，也不会 A→B→A 抖动。
type orangeChainGuardContextKey struct{}

// withOrangeChainGuard 给「组内选号」的 ctx 打上链外壳标记。
func withOrangeChainGuard(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, orangeChainGuardContextKey{}, true)
}

// orangeChainGuardActive 报告 ctx 是否已由链外壳接管（即当前正在组内选号）。
func orangeChainGuardActive(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	guarded, _ := ctx.Value(orangeChainGuardContextKey{}).(bool)
	return guarded
}

// orangeChainOutcome 打包「组内选号结果 + 是否已由链外壳处理」。
//
// Handled=false 表示本请求没有分组链（或已在组内选号），调用方必须继续执行上游原有逻辑，
// 此时 Value/Err 无意义——这正是「无该配置时行为与上游完全一致」的落地点。
type orangeChainOutcome[T any] struct {
	Value   T
	Err     error
	Handled bool
}

// selectWithOrangeGroupChain 是选号入口的通用外壳：先按当前分组选号，只有"分组内无可用账号"
// 时才沿链推进；命中后续分组后激活请求级路由，让限流/计费/用量跟随实际分组。
func selectWithOrangeGroupChain[T any](
	ctx context.Context,
	groupID *int64,
	requestedModel string,
	selectInGroup func(context.Context, *int64) (T, error),
	isHit func(T) bool,
	markRouted func(T, int64),
) (T, error) {
	primaryGroupID := derefGroupID(groupID)
	// Orange 特有：当前分组配置层面不支持该模型时，按链优先级预跳到第一个支持的分组。
	ctx, groupID = orangePreselectChainGroupForModel(ctx, groupID, requestedModel)
	result, err := selectInGroup(withOrangeChainGuard(ctx), groupID)

	for attempt := 1; attempt <= maxOrangeChainAttempts; attempt++ {
		if err == nil && isHit(result) {
			break
		}
		next, nextCtx, ok := orangeNextChainGroup(ctx, groupID, err)
		if !ok {
			break
		}
		nextGroupID := next.ID
		slog.Info("orange_key_group_chain_attempt",
			"primary_group_id", primaryGroupID,
			"next_group_id", nextGroupID,
			"model", requestedModel,
			"attempt", attempt)
		result, err = selectInGroup(withOrangeChainGuard(nextCtx), &nextGroupID)
		ctx = nextCtx
		groupID = &nextGroupID
		if err == nil && isHit(result) {
			activateOrangeKeyRouting(ctx, nextGroupID)
			if markRouted != nil {
				markRouted(result, nextGroupID)
			}
			slog.Info("orange_key_group_chain_selected",
				"primary_group_id", primaryGroupID,
				"routed_group_id", nextGroupID,
				"model", requestedModel,
				"attempt", attempt)
			break
		}
	}
	return result, err
}

// orangeAccountSelectionHit 报告 *Account 选号是否命中。
func orangeAccountSelectionHit(account *Account) bool { return account != nil }

// orangeSelectionResultHit 报告 *AccountSelectionResult 选号是否命中。
func orangeSelectionResultHit(selection *AccountSelectionResult) bool {
	return selection != nil && selection.Account != nil
}

// orangeOpenAIScheduleResult 把 OpenAI 调度器的三返回值打包，便于复用同一套链推进逻辑。
type orangeOpenAIScheduleResult struct {
	Selection *AccountSelectionResult
	Decision  OpenAIAccountScheduleDecision
	Err       error
}

func (r orangeOpenAIScheduleResult) hit() bool {
	return r.Err == nil && r.Selection != nil && r.Selection.Account != nil
}

// selectWithOrangeGroupChainOpenAISchedule 是 OpenAI 调度器三返回值入口的链推进外壳。
func selectWithOrangeGroupChainOpenAISchedule(
	ctx context.Context,
	groupID *int64,
	requestedModel string,
	selectInGroup func(context.Context, *int64) orangeOpenAIScheduleResult,
) orangeOpenAIScheduleResult {
	primaryGroupID := derefGroupID(groupID)
	// Orange 特有：当前分组配置层面不支持该模型时，按链优先级预跳到第一个支持的分组。
	ctx, groupID = orangePreselectChainGroupForModel(ctx, groupID, requestedModel)
	result := selectInGroup(withOrangeChainGuard(ctx), groupID)

	for attempt := 1; attempt <= maxOrangeChainAttempts; attempt++ {
		if result.hit() {
			break
		}
		next, nextCtx, ok := orangeNextChainGroup(ctx, groupID, result.Err)
		if !ok {
			break
		}
		nextGroupID := next.ID
		slog.Info("orange_key_group_chain_attempt",
			"primary_group_id", primaryGroupID,
			"next_group_id", nextGroupID,
			"model", requestedModel,
			"attempt", attempt)
		result = selectInGroup(withOrangeChainGuard(nextCtx), &nextGroupID)
		ctx = nextCtx
		groupID = &nextGroupID
		if result.hit() {
			activateOrangeKeyRouting(ctx, nextGroupID)
			slog.Info("orange_key_group_chain_selected",
				"primary_group_id", primaryGroupID,
				"routed_group_id", nextGroupID,
				"model", requestedModel,
				"attempt", attempt)
			break
		}
	}
	return result
}

// ---- 上游选号入口的薄挂载点外壳 ----
//
// 每个外壳只做两件事：无链时返回 Handled=false 让上游原逻辑照跑；有链时按链推进。
// 组内选号一律递归回调「公开入口本身」（ctx 已带链标记，不会再推进），因此上游函数体
// 可以原样留在上游文件里，只需在入口加 2~4 行挂载。

// orangeChainGatewaySelectAccountForModelExclusions 挂载 GatewayService.SelectAccountForModelWithExclusions。
func (s *GatewayService) orangeChainGatewaySelectAccountForModelExclusions(ctx context.Context, groupID *int64, sessionHash string, requestedModel string, excludedIDs map[int64]struct{}) orangeChainOutcome[*Account] {
	if orangeChainGuardActive(ctx) {
		return orangeChainOutcome[*Account]{}
	}
	account, err := selectWithOrangeGroupChain(ctx, groupID, requestedModel,
		func(groupCtx context.Context, chainGroupID *int64) (*Account, error) {
			return s.SelectAccountForModelWithExclusions(groupCtx, chainGroupID, sessionHash, requestedModel, excludedIDs)
		}, orangeAccountSelectionHit, nil)
	return orangeChainOutcome[*Account]{Value: account, Err: err, Handled: true}
}

// orangeChainGatewaySelectAccountWithLoadAwareness 挂载 GatewayService.SelectAccountWithLoadAwareness。
func (s *GatewayService) orangeChainGatewaySelectAccountWithLoadAwareness(ctx context.Context, groupID *int64, sessionHash string, requestedModel string, excludedIDs map[int64]struct{}, metadataUserID string, sub2apiUserID int64) orangeChainOutcome[*AccountSelectionResult] {
	if orangeChainGuardActive(ctx) {
		return orangeChainOutcome[*AccountSelectionResult]{}
	}
	selection, err := selectWithOrangeGroupChain(ctx, groupID, requestedModel,
		func(groupCtx context.Context, chainGroupID *int64) (*AccountSelectionResult, error) {
			return s.SelectAccountWithLoadAwareness(groupCtx, chainGroupID, sessionHash, requestedModel, excludedIDs, metadataUserID, sub2apiUserID)
		}, orangeSelectionResultHit, nil)
	return orangeChainOutcome[*AccountSelectionResult]{Value: selection, Err: err, Handled: true}
}

// orangeChainOpenAISelectAccountForModelExclusions 挂载 OpenAI 的账号选择原语。
//
// OpenAI 侧两个公开入口（SelectAccountForModelWithExclusions / SelectAccountForTokenCount）
// 都收敛到同一个私有原语 selectAccountForModelWithExclusions，且没有其他调用方，因此挂载点
// 放在该原语入口即可等价覆盖两者：组内沿用原来的「每个分组重新取一次配额自动暂停设置」。
func (s *OpenAIGatewayService) orangeChainOpenAISelectAccountForModelExclusions(ctx context.Context, groupID *int64, platform string, sessionHash string, requestedModel string, excludedIDs map[int64]struct{}, requireCompact bool, stickyAccountID int64, requiredCapability OpenAIEndpointCapability, preferLowUpstreamRate bool) orangeChainOutcome[*Account] {
	if orangeChainGuardActive(ctx) {
		return orangeChainOutcome[*Account]{}
	}
	account, err := selectWithOrangeGroupChain(ctx, groupID, requestedModel,
		func(groupCtx context.Context, chainGroupID *int64) (*Account, error) {
			return s.selectAccountForModelWithExclusions(s.withOpenAIQuotaAutoPauseContext(groupCtx), chainGroupID, platform, sessionHash, requestedModel, excludedIDs, requireCompact, stickyAccountID, requiredCapability, preferLowUpstreamRate)
		}, orangeAccountSelectionHit, nil)
	return orangeChainOutcome[*Account]{Value: account, Err: err, Handled: true}
}

// orangeChainOpenAISelectAccountWithLoadAwareness 挂载 OpenAIGatewayService.SelectAccountWithLoadAwareness。
func (s *OpenAIGatewayService) orangeChainOpenAISelectAccountWithLoadAwareness(ctx context.Context, groupID *int64, sessionHash string, requestedModel string, excludedIDs map[int64]struct{}) orangeChainOutcome[*AccountSelectionResult] {
	if orangeChainGuardActive(ctx) {
		return orangeChainOutcome[*AccountSelectionResult]{}
	}
	selection, err := selectWithOrangeGroupChain(ctx, groupID, requestedModel,
		func(groupCtx context.Context, chainGroupID *int64) (*AccountSelectionResult, error) {
			return s.SelectAccountWithLoadAwareness(groupCtx, chainGroupID, sessionHash, requestedModel, excludedIDs)
		}, orangeSelectionResultHit, nil)
	return orangeChainOutcome[*AccountSelectionResult]{Value: selection, Err: err, Handled: true}
}

// orangeChainOpenAISelectAccountWithScheduler 挂载 OpenAI 调度原语。
//
// 三个公开调度入口（SelectAccountWithScheduler / ForCapability / ForImages 的两次内部调用）
// 都收敛到同一私有原语，且没有其他调用方；ForImages 需要「整入口换组」语义，因此它另有
// 自己的入口级挂载，链推进期间由链标记抑制本挂载（与改造前逐行等价）。
func (s *OpenAIGatewayService) orangeChainOpenAISelectAccountWithScheduler(ctx context.Context, groupID *int64, previousResponseID string, sessionHash string, requestedModel string, excludedIDs map[int64]struct{}, requiredTransport OpenAIUpstreamTransport, requiredCapability OpenAIEndpointCapability, requiredImageCapability OpenAIImagesCapability, requireCompact bool, platform string, previousResponseCanMove bool, useUpstreamTokenCost bool) orangeChainOutcome[orangeOpenAIScheduleResult] {
	if orangeChainGuardActive(ctx) {
		return orangeChainOutcome[orangeOpenAIScheduleResult]{}
	}
	result := selectWithOrangeGroupChainOpenAISchedule(ctx, groupID, requestedModel,
		func(groupCtx context.Context, chainGroupID *int64) orangeOpenAIScheduleResult {
			selection, decision, err := s.selectAccountWithScheduler(groupCtx, chainGroupID, previousResponseID, sessionHash, requestedModel, excludedIDs, requiredTransport, requiredCapability, requiredImageCapability, requireCompact, platform, previousResponseCanMove, useUpstreamTokenCost)
			return orangeOpenAIScheduleResult{Selection: selection, Decision: decision, Err: err}
		})
	return orangeChainOutcome[orangeOpenAIScheduleResult]{Value: result, Handled: true}
}

// orangeChainOpenAISelectAccountWithSchedulerForImages 挂载 SelectAccountWithSchedulerForImages：
// 生图入口内部有 native→basic 两次选号 + 回退分支，必须整入口换组（不能按单次选号换）。
func (s *OpenAIGatewayService) orangeChainOpenAISelectAccountWithSchedulerForImages(ctx context.Context, groupID *int64, sessionHash string, requestedModel string, excludedIDs map[int64]struct{}, requiredCapability OpenAIImagesCapability) orangeChainOutcome[orangeOpenAIScheduleResult] {
	if orangeChainGuardActive(ctx) {
		return orangeChainOutcome[orangeOpenAIScheduleResult]{}
	}
	result := selectWithOrangeGroupChainOpenAISchedule(ctx, groupID, requestedModel,
		func(groupCtx context.Context, chainGroupID *int64) orangeOpenAIScheduleResult {
			selection, decision, err := s.SelectAccountWithSchedulerForImages(groupCtx, chainGroupID, sessionHash, requestedModel, excludedIDs, requiredCapability)
			return orangeOpenAIScheduleResult{Selection: selection, Decision: decision, Err: err}
		})
	return orangeChainOutcome[orangeOpenAIScheduleResult]{Value: result, Handled: true}
}
