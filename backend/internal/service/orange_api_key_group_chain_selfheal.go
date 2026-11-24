// Orange 特有：分组链的「权限自愈」——链成员失去绑定权限时自动修剪并写回数据库。
//
// 场景：管理员把某个分组从「公开」改成「专属」（或撤销 user_allowed_groups 授权），用户 API Key
// 的分组链（兜底分组 / 聚合密钥）里仍残留该分组。站长要求「直接不显示这一行，并自动更新密钥」，
// 因此本文件提供：
//
//  1. orangeChainSelfHealPlan：纯函数，算出「该保留哪些分组、新链首是谁、模式怎么记」；
//  2. OrangeSelfHealKeyGroupChain：把方案落库（api_keys.group_id + orange_api_key_group_chain
//     + 模式元数据），并失效链缓存与认证快照缓存。
//
// 调用点只有「打开密钥编辑弹窗」（GET /keys/:id/group-chain）：这是用户主动的读操作，写库代价可忽略，
// 且能保证「界面上看到的链 = 数据库里的链 = 请求实际会用的链」。
// 请求链路（鉴权中间件）不写库，只做请求内的链首自愈（见 orange_api_key_group_chain.go 的
// healOrangeKeyChainHead），避免在热路径上写数据库。
//
// 与上游/参考项目的差异（有意为之）：官方 sub2api 单分组 Key 直接 403、不改库；参考项目
// sub2api-ranxi2021 的多分组密钥只在请求时跳过，列表照旧显示。Orange 选择「读时自愈 + 自动改写」。
package service

import (
	"context"
	"log/slog"
)

// orangeChainSelfHealPlanResult 是一次修剪方案。
type orangeChainSelfHealPlanResult struct {
	// Primary 是修剪后的链首（= 写回 api_keys.group_id 的分组）。
	Primary *Group
	// Tail 是修剪后的链候选（按原优先级顺序，不含 Primary）。
	Tail []int64
	// Mode 是修剪后的链模式：长度不足 2 时为空串（清链并删除模式元数据）。
	Mode string
	// Pruned 是被移除的分组（失去绑定权限；保留名称供界面提示）。
	Pruned []*Group
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
	// PrunedGroupIDs 是被移除的分组 ID（失去绑定权限）。
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
//  1. 失去绑定权限（orangeGroupBindableForUser 失败）的分组被移除（Pruned）；
//  2. 链首被移除时，把第一个「仍可用（active + 上下文完整）」的保留分组提升为链首；
//  3. 保留分组 <2 个时模式返回空串（清链，Key 退化为普通密钥）；
//  4. 没有任何分组失去权限、或所有分组都失去权限（无从提升，交给用户自己改主分组）时
//     返回 changed=false，调用方不得写库。
func orangeChainSelfHealPlan(user *User, head *Group, candidates []*Group, persistedMode string) (orangeChainSelfHealPlanResult, bool) {
	all := make([]*Group, 0, len(candidates)+1)
	if head != nil && head.ID > 0 {
		all = append(all, head)
	}
	all = append(all, candidates...)

	kept := make([]*Group, 0, len(all))
	pruned := make([]*Group, 0, len(all))
	seen := make(map[int64]struct{}, len(all))
	for _, group := range all {
		if group == nil || group.ID <= 0 {
			continue
		}
		if _, dup := seen[group.ID]; dup {
			continue
		}
		seen[group.ID] = struct{}{}
		if !orangeGroupBindableForUser(user, group) {
			pruned = append(pruned, group)
			continue
		}
		kept = append(kept, group)
	}
	if len(pruned) == 0 {
		return orangeChainSelfHealPlanResult{}, false
	}

	// 新链首必须是「现在就能用」的分组；全部都不可用时保持原链不动。
	var primary *Group
	for _, group := range kept {
		if group.IsActive() && IsGroupContextValid(group) {
			primary = group
			break
		}
	}
	if primary == nil {
		return orangeChainSelfHealPlanResult{}, false
	}

	tail := make([]int64, 0, len(kept)-1)
	for _, group := range kept {
		if group.ID != primary.ID {
			tail = append(tail, group.ID)
		}
	}
	mode := ""
	if len(kept) >= 2 {
		mode = orangeResolveGroupChainMode(persistedMode, len(kept))
		if mode == OrangeGroupChainModeNone {
			mode = OrangeGroupChainModeFallback
		}
	}
	return orangeChainSelfHealPlanResult{Primary: primary, Tail: tail, Mode: mode, Pruned: pruned}, true
}

// OrangeSelfHealKeyGroupChain 按用户当前权限修剪某个 Key 的分组链并落库（幂等）。
//
// 只在「打开密钥编辑弹窗」的读路径调用：写库代价可忽略，且能让界面与数据库保持一致。
// apiKey 必须是本请求内新加载的对象（GetByID），链首被提升时会就地更新 Group/GroupID 供上层构造视图。
func (s *APIKeyService) OrangeSelfHealKeyGroupChain(ctx context.Context, apiKey *APIKey) orangeChainSelfHealResult {
	empty := orangeChainSelfHealResult{}
	if s == nil || apiKey == nil || apiKey.ID <= 0 || apiKey.User == nil {
		return empty
	}
	repo, ok := s.apiKeyRepo.(orangeGroupChainRepository)
	if !ok {
		return empty
	}
	entry, _ := s.orangeGroupChainEntry(ctx, apiKey.ID)
	plan, changed := orangeChainSelfHealPlan(apiKey.User, apiKey.Group, entry.groups, orangeResolveGroupChainMode(entry.mode, len(entry.members)))
	if !changed {
		return empty
	}
	if err := repo.OrangeReplaceGroupChainAndPrimary(ctx, apiKey.ID, plan.Primary.ID, plan.Tail, plan.Mode); err != nil {
		slog.Warn("orange_key_group_chain_self_heal_failed",
			"api_key_id", apiKey.ID, "error", err)
		return empty
	}
	InvalidateOrangeGroupChainCache(apiKey.ID)
	if apiKey.Key != "" {
		s.InvalidateAuthCacheByKey(ctx, apiKey.Key)
	}

	promoted := (*Group)(nil)
	if apiKey.Group == nil || apiKey.Group.ID != plan.Primary.ID {
		promoted = plan.Primary
		// 就地更新（本对象由编辑接口新加载，不会被认证缓存共享）：视图与后续 PUT 都以它为准。
		groupID := plan.Primary.ID
		apiKey.GroupID = &groupID
		apiKey.Group = plan.Primary
	}
	apiKey.GroupChainMode = plan.Mode
	prunedIDs := make([]int64, 0, len(plan.Pruned))
	prunedGroups := make([]OrangePrunedChainGroup, 0, len(plan.Pruned))
	for _, group := range plan.Pruned {
		prunedIDs = append(prunedIDs, group.ID)
		prunedGroups = append(prunedGroups, OrangePrunedChainGroup{ID: group.ID, Name: group.Name})
	}
	slog.Info("orange_key_group_chain_self_healed",
		"api_key_id", apiKey.ID,
		"pruned_group_ids", prunedIDs,
		"primary_group_id", plan.Primary.ID,
		"promoted", promoted != nil)
	return orangeChainSelfHealResult{Changed: true, PrunedGroupIDs: prunedIDs, PrunedGroups: prunedGroups, Promoted: promoted}
}
