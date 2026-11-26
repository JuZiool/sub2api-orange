// Orange 特有：分组链的「显式自愈」——链成员失去绑定权限 / 分组已被删除时，由用户主动触发修剪并写库。
//
// v0.2.25-8 起：自愈**只在显式 heal 接口触发**（POST /keys/:id/group-chain/heal）。
// GET /keys/:id/group-chain 已改为纯读，不再有写库副作用 —— 旧实现在 GET 里修剪链成员并把结果
// 写回数据库，前端只要少提交一个成员就永久丢配置（缺陷 D3）。
//
// 场景：管理员把某个分组从「公开」改成「专属」（或撤销 user_allowed_groups 授权），或分组被删除，
// 用户 API Key 的分组链里仍残留该分组。本文件提供：
//
//  1. orangeChainSelfHealPlan / orangeChainHealPlan：纯函数，算出「该保留哪些分组、新链首是谁、
//     模式怎么记」；
//  2. OrangeSelfHealKeyGroupChain：把方案落库（api_keys.group_id + orange_api_key_group_chain
//     + 模式元数据），并失效链缓存与认证快照缓存；
//  3. OrangeHealAPIKeyGroupChain：heal 接口的编排入口（重载 owner + Key → 自愈 → 返回完整视图）。
//
// 修剪口径：只修剪 state ∈ {no_permission, missing} 的候选；disabled（可能只是临时停用）与
// scope_mismatch（用户可能正在改平台配置）一律保留，见 orangeChainMemberStatePrunable。
//
// 请求链路（鉴权中间件）不写库，只做请求内的链首自愈（见 orange_api_key_group_chain.go 的
// healOrangeKeyChainHead），避免在热路径上写数据库。
//
// 与上游/参考项目的差异（有意为之）：官方 sub2api 单分组 Key 直接 403、不改库；参考项目
// sub2api-ranxi2021 的多分组密钥只在请求时跳过，列表照旧显示。Orange 选择「显式自愈 + 用户确认」。
package service

import (
	"context"
	"log/slog"
)

// orangeChainPlanCandidate 是修剪方案的候选：GroupID 恒有效，Group 为 nil 表示分组已删除/读不到。
type orangeChainPlanCandidate struct {
	GroupID int64
	Group   *Group
}

// orangeChainSelfHealPlanResult 是一次修剪方案。
type orangeChainSelfHealPlanResult struct {
	// Primary 是修剪后的链首（= 写回 api_keys.group_id 的分组）。
	Primary *Group
	// Tail 是修剪后的链候选（按原优先级顺序，不含 Primary）。
	Tail []int64
	// Mode 是修剪后的链模式：长度不足 2 时为空串（清链并删除模式元数据）。
	Mode string
	// Pruned 是被移除且仍可读的分组（保留名称供界面提示）；已删除的分组只出现在 PrunedInfos 里。
	Pruned []*Group
	// PrunedInfos 是本次被移除的**全部**分组（含已删除的，名称退化为 "#id"），供界面提示。
	PrunedInfos []OrangePrunedChainGroup
}

// OrangePrunedChainGroup 是被链自愈移除的分组（保留名称，供界面提示"已自动移除 xxx 分组"）。
type OrangePrunedChainGroup struct {
	ID   int64
	Name string
}

// orangeChainSelfHealResult 是自愈执行结果（供接口返回给前端提示）。
type orangeChainSelfHealResult struct {
	// Changed 表示确实改写了数据库。
	Changed bool
	// PrunedGroupIDs 是被移除的分组 ID（失去绑定权限 / 分组已删除）。
	PrunedGroupIDs []int64
	// PrunedGroups 是同一批分组（含名称），供前端在提示里显示分组名而不是 #id。
	PrunedGroups []OrangePrunedChainGroup
	// Promoted 是被提升为新链首的分组；nil 表示链首未变。
	Promoted *Group
}

// orangeChainSelfHealPlan 计算分组链的修剪方案（纯函数，便于单测）。
//
// 输入：head（链首 = api_keys.group_id，允许为 nil）、candidates（链候选，顺序即优先级）、
// persistedMode（当前持久化的模式，可能是空串）。
//
// 规则：
//  1. 失去绑定权限（no_permission）的分组被移除（Pruned）；
//  2. 链首被移除时，把第一个「仍可用（active + 上下文完整）」的保留分组提升为链首；
//  3. 保留分组 <2 个时模式返回空串（清链，Key 退化为普通密钥）；
//  4. 没有任何分组失去权限、或所有分组都失去权限（无从提升，交给用户自己改主分组）时
//     返回 changed=false，调用方不得写库。
//
// 本函数是旧签名的兼容包装：只关心「仍可读的分组」，因此**不会**修剪已删除的候选
// （与改造前完全一致）。显式 heal 接口走 orangeChainHealPlan（额外修剪 missing）。
func orangeChainSelfHealPlan(user *User, head *Group, candidates []*Group, persistedMode string) (orangeChainSelfHealPlanResult, bool) {
	pairs := make([]orangeChainPlanCandidate, 0, len(candidates)+1)
	for _, group := range candidates {
		if group == nil || group.ID <= 0 {
			// 读不到的分组：旧签名无法携带 ID，按旧语义跳过（不修剪）。
			pairs = append(pairs, orangeChainPlanCandidate{})
			continue
		}
		pairs = append(pairs, orangeChainPlanCandidate{GroupID: group.ID, Group: group})
	}
	return orangeChainHealPlan(user, head, 0, pairs, persistedMode)
}

// orangeChainHealPlan 计算修剪方案（纯函数）：在 orangeChainSelfHealPlan 的语义之上，
// 额外把「分组已删除（Group 为 nil 但 GroupID 有效）」的候选按 missing 修剪掉。
//
// headGroupID 用于链首分组已读不到的场景（此时 head 为 nil，但仍要记入被移除项）。
func orangeChainHealPlan(user *User, head *Group, headGroupID int64, candidates []orangeChainPlanCandidate, persistedMode string) (orangeChainSelfHealPlanResult, bool) {
	if head != nil && head.ID > 0 {
		headGroupID = head.ID
	}
	all := make([]orangeChainPlanCandidate, 0, len(candidates)+1)
	if head != nil || headGroupID > 0 {
		all = append(all, orangeChainPlanCandidate{GroupID: headGroupID, Group: head})
	}
	all = append(all, candidates...)

	kept := make([]orangeChainPlanCandidate, 0, len(all))
	pruned := make([]*Group, 0, len(all))
	prunedInfos := make([]OrangePrunedChainGroup, 0, len(all))
	seen := make(map[int64]struct{}, len(all))
	for _, candidate := range all {
		groupID := candidate.GroupID
		if candidate.Group != nil && candidate.Group.ID > 0 {
			groupID = candidate.Group.ID
		}
		if groupID <= 0 {
			continue
		}
		if _, dup := seen[groupID]; dup {
			continue
		}
		seen[groupID] = struct{}{}

		state := orangeChainMemberStateOf(head, candidate.Group, user)
		if !orangeChainMemberStatePrunable(state) {
			kept = append(kept, candidate)
			continue
		}
		prunedInfos = append(prunedInfos, OrangePrunedChainGroup{
			ID:   groupID,
			Name: orangeChainGroupDisplayName(candidate.Group, groupID),
		})
		if candidate.Group != nil {
			pruned = append(pruned, candidate.Group)
		}
	}
	if len(prunedInfos) == 0 {
		return orangeChainSelfHealPlanResult{}, false
	}

	// 新链首必须是「现在就能用」的分组；全部都不可用时保持原链不动。
	var primary *Group
	for _, candidate := range kept {
		if candidate.Group != nil && candidate.Group.IsActive() && IsGroupContextValid(candidate.Group) {
			primary = candidate.Group
			break
		}
	}
	if primary == nil {
		return orangeChainSelfHealPlanResult{}, false
	}

	tail := make([]int64, 0, len(kept)-1)
	for _, candidate := range kept {
		groupID := candidate.GroupID
		if candidate.Group != nil && candidate.Group.ID > 0 {
			groupID = candidate.Group.ID
		}
		if groupID != primary.ID {
			tail = append(tail, groupID)
		}
	}
	mode := ""
	if len(kept) >= 2 {
		mode = orangeResolveGroupChainMode(persistedMode, len(kept))
		if mode == OrangeGroupChainModeNone {
			mode = OrangeGroupChainModeFallback
		}
	}
	return orangeChainSelfHealPlanResult{Primary: primary, Tail: tail, Mode: mode, Pruned: pruned, PrunedInfos: prunedInfos}, true
}

// OrangeSelfHealKeyGroupChain 按用户当前权限修剪某个 Key 的分组链并落库（幂等）。
//
// 只在显式 heal 接口（OrangeHealAPIKeyGroupChain）里调用，GET 读路径不再触发。
// apiKey 必须是本请求内新加载的对象（GetByID）且已挂上重载后的 owner；链首被提升时
// 会就地更新 Group/GroupID 供上层构造视图。
func (s *APIKeyService) OrangeSelfHealKeyGroupChain(ctx context.Context, apiKey *APIKey) orangeChainSelfHealResult {
	empty := orangeChainSelfHealResult{}
	if s == nil || apiKey == nil || apiKey.ID <= 0 || apiKey.User == nil {
		return empty
	}
	repo, ok := s.apiKeyRepo.(orangeGroupChainRepository)
	if !ok {
		return empty
	}
	headID := int64(0)
	if apiKey.GroupID != nil {
		headID = *apiKey.GroupID
	}
	entry, _ := s.orangeGroupChainEntry(ctx, apiKey.ID)
	candidates := make([]orangeChainPlanCandidate, 0, len(entry.members))
	for i, member := range entry.members {
		var group *Group
		if i < len(entry.groups) {
			group = entry.groups[i]
		}
		candidates = append(candidates, orangeChainPlanCandidate{GroupID: member.GroupID, Group: group})
	}
	plan, changed := orangeChainHealPlan(apiKey.User, apiKey.Group, headID, candidates,
		orangeResolveGroupChainMode(entry.mode, len(entry.members)))
	if !changed {
		return empty
	}
	if err := repo.OrangeReplaceGroupChainAndPrimary(ctx, apiKey.ID, plan.Primary.ID, plan.Tail, plan.Mode); err != nil {
		slog.Warn("orange_key_group_chain_self_heal_failed",
			"api_key_id", apiKey.ID, "error", err)
		return empty
	}
	s.orangeInvalidateAfterChainWrite(ctx, apiKey)

	promoted := (*Group)(nil)
	if apiKey.Group == nil || apiKey.Group.ID != plan.Primary.ID {
		promoted = plan.Primary
		groupID := plan.Primary.ID
		apiKey.GroupID = &groupID
		// 复制一份再挂到请求级对象上：plan.Primary 可能来自进程内链缓存，请求不得持有缓存指针。
		apiKey.Group = cloneOrangeGroup(plan.Primary)
	}
	apiKey.GroupChainMode = plan.Mode
	prunedIDs := make([]int64, 0, len(plan.PrunedInfos))
	prunedGroups := make([]OrangePrunedChainGroup, 0, len(plan.PrunedInfos))
	for _, group := range plan.PrunedInfos {
		prunedIDs = append(prunedIDs, group.ID)
		prunedGroups = append(prunedGroups, group)
	}
	slog.Info("orange_key_group_chain_self_healed",
		"api_key_id", apiKey.ID,
		"pruned_group_ids", prunedIDs,
		"primary_group_id", plan.Primary.ID,
		"promoted", promoted != nil)
	return orangeChainSelfHealResult{Changed: true, PrunedGroupIDs: prunedIDs, PrunedGroups: prunedGroups, Promoted: promoted}
}

// OrangeHealAPIKeyGroupChain 是 POST /keys/:id/group-chain/heal 的编排入口（v0.2.25-8）。
//
// 这是「读后写库」的唯一入口：归属校验 → 重载 Key 与 owner（含 allowed_groups）→ 执行自愈写库 →
// 返回「全部成员 + state」的完整视图，并把本次真正移除的分组放进 AutoPrunedGroups/AutoPrunedGroupIDs。
func (s *APIKeyService) OrangeHealAPIKeyGroupChain(ctx context.Context, userID, apiKeyID int64) (OrangeAPIKeyGroupChainView, error) {
	empty := OrangeAPIKeyGroupChainView{}
	if s == nil || apiKeyID <= 0 {
		return empty, ErrAPIKeyNotFound
	}
	owned, err := s.VerifyOwnership(ctx, userID, []int64{apiKeyID})
	if err != nil {
		return empty, err
	}
	if len(owned) == 0 {
		return empty, ErrAPIKeyNotFound
	}
	apiKey, err := s.apiKeyRepo.GetByID(ctx, apiKeyID)
	if err != nil || apiKey == nil {
		return empty, ErrAPIKeyNotFound
	}
	// owner 一律重载：apiKeyRepo.GetByID 不预载 allowed_groups，直接用它判权限会误删专属分组（D1）。
	if _, err := s.orangeLoadChainOwner(ctx, apiKey); err != nil {
		return empty, err
	}
	healed := s.OrangeSelfHealKeyGroupChain(ctx, apiKey)

	view := s.OrangeBuildAPIKeyGroupChainViewFull(ctx, apiKey)
	view.AutoPrunedGroupIDs = healed.PrunedGroupIDs
	view.AutoPrunedGroups = healed.PrunedGroups
	if healed.Promoted != nil {
		view.AutoPromotedGroupID = healed.Promoted.ID
	}
	return view, nil
}
