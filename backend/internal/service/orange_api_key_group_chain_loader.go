// Orange 特有：API Key 分组优先级链的装载、进程内缓存与读写编排。
//
// 数据来源是我们自己的表 orange_api_key_group_chain；**不修改**上游认证缓存快照
// （api_key_auth_cache*.go 零改动），而是在鉴权中间件阶段调用
// APIKeyService.OrangeAttachGroupChain 把链挂到请求级 *APIKey 上。
//
// 缓存说明：链路数据按 apiKeyID 缓存在进程内（TTL 45s，落在设计要求的 30–60s 区间），
// 避免每个请求都多一次（或多次）分组查询；写链与分组变更后必须调用
// InvalidateOrangeGroupChainCache / OrangeInvalidateGroupChainCacheByGroup 清理。
package service

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

// Orange 特有分组链的错误码（对外暴露 reason，供前端区分提示）。
var (
	// ErrOrangeGroupChainInvalid 表示链上存在停用/不可绑定/跨平台/跨订阅类型的分组。
	ErrOrangeGroupChainInvalid = infraerrors.BadRequest("ORANGE_GROUP_CHAIN_INVALID",
		"分组链无效：分组必须可用、同平台、同订阅类型，且首项必须存在")
	// ErrOrangeGroupChainDuplicate 表示链上出现重复分组且无法消歧。
	ErrOrangeGroupChainDuplicate = infraerrors.BadRequest("ORANGE_GROUP_CHAIN_DUPLICATE",
		"分组链包含重复分组")
	// ErrOrangeGroupChainTooLong 表示链长度超过上限。
	ErrOrangeGroupChainTooLong = infraerrors.Conflict("ORANGE_GROUP_CHAIN_TOO_LONG",
		"分组链过长：链首（主分组）之外最多绑定 8 个候选分组")
	// ErrOrangeGroupChainModeInvalid 表示 mode 取值非法（只接受 fallback / aggregate）。
	ErrOrangeGroupChainModeInvalid = infraerrors.BadRequest("ORANGE_GROUP_CHAIN_MODE_INVALID",
		"分组模式无效：只支持 fallback（兜底分组）或 aggregate（聚合密钥）")
	// ErrOrangeGroupChainTooFew 表示聚合模式下的分组数量不足。
	ErrOrangeGroupChainTooFew = infraerrors.BadRequest("ORANGE_GROUP_CHAIN_TOO_FEW",
		"聚合密钥至少需要两个分组")
	// ErrOrangeGroupChainFallbackLimit 表示兜底模式下的分组数量超过上限。
	ErrOrangeGroupChainFallbackLimit = infraerrors.BadRequest("ORANGE_GROUP_CHAIN_FALLBACK_LIMIT",
		"兜底模式最多只能设置一个兜底分组")
	// ErrOrangeGroupChainUnavailable 表示仓储层缺少 Orange 分组链能力（部署不完整）。
	ErrOrangeGroupChainUnavailable = infraerrors.InternalServer("ORANGE_GROUP_CHAIN_UNAVAILABLE",
		"当前部署不支持分组链功能")
)

const (
	// orangeGroupChainCacheTTL 是进程内链缓存的存活时间。
	orangeGroupChainCacheTTL = 45 * time.Second
	// orangeGroupChainCacheMaxEntries 是进程内链缓存的条目上限，超出后整体清空（低频、代价可控）。
	orangeGroupChainCacheMaxEntries = 8192
)

// orangeGroupChainRepository 是仓储层为本功能提供的裸 SQL 能力。
// 通过类型断言取用，不扩展上游 APIKeyRepository 接口定义（避免大量测试替身改动）。
type orangeGroupChainRepository interface {
	OrangeReplaceGroupChain(ctx context.Context, apiKeyID int64, groupIDs []int64) error
	OrangeReplaceGroupChainAndPrimary(ctx context.Context, apiKeyID, primaryGroupID int64, tail []int64, mode string) error
	OrangeSetGroupChainMode(ctx context.Context, apiKeyID int64, mode string) error
	OrangeLoadGroupChains(ctx context.Context, apiKeyIDs []int64) (map[int64][]OrangeGroupChainMember, error)
	OrangeLoadGroupChainModes(ctx context.Context, apiKeyIDs []int64) (map[int64]string, error)
	OrangeListAPIKeyIDsByChainGroup(ctx context.Context, groupID int64) ([]int64, error)
}

// orangeGroupChainCacheEntry 是缓存项：原始链候选（按 priority 升序）+ 对齐的水化分组 + 展示模式。
// groups[i] 为 nil 表示该候选分组已软删除或不可读；mode 已按"持久化优先、无记录则按候选数推断"解析。
type orangeGroupChainCacheEntry struct {
	expiresAt time.Time
	members   []OrangeGroupChainMember
	groups    []*Group
	mode      string
}

var orangeGroupChainCache = struct {
	mu      sync.RWMutex
	entries map[int64]orangeGroupChainCacheEntry
}{entries: make(map[int64]orangeGroupChainCacheEntry)}

// InvalidateOrangeGroupChainCache 清理单个 API Key 的链缓存。
// 写链接口与对外暴露的分组变更钩子必须调用它（进程内缓存，无需 Redis）。
func InvalidateOrangeGroupChainCache(apiKeyID int64) {
	if apiKeyID <= 0 {
		return
	}
	orangeGroupChainCache.mu.Lock()
	delete(orangeGroupChainCache.entries, apiKeyID)
	orangeGroupChainCache.mu.Unlock()
}

// orangeGroupChainModeForCount 按链首之后的候选数量给出模式。
func orangeGroupChainModeForCount(candidates int) string {
	switch {
	case candidates <= 0:
		return OrangeGroupChainModeNone
	case candidates == 1:
		return OrangeGroupChainModeFallback
	default:
		return OrangeGroupChainModeAggregate
	}
}

// orangeResolveGroupChainMode 解析一个 Key 的展示模式：持久化的用户意图优先
// （aggregate 与 fallback 的路由语义相同，只有界面差异，所以必须记住用户选的是哪种），
// 没有 meta 记录（或值异常）时才按候选数推断：0 → none、1 → fallback、>=2 → aggregate。
func orangeResolveGroupChainMode(persisted string, candidates int) string {
	switch persisted {
	case OrangeGroupChainModeFallback, OrangeGroupChainModeAggregate:
		return persisted
	default:
		return orangeGroupChainModeForCount(candidates)
	}
}

// orangeNormalizeGroupChainMode 归一化调用方传入的模式并做数量校验：
//   - 空串（或 none）→ 按分组数量推断：<=2 个分组 → fallback，>=3 个 → aggregate；
//   - 显式 fallback → 最多 2 个分组（链首 + 1 个兜底分组）；
//   - 显式 aggregate → 至少 2 个分组（链首 + 1 个候选），否则"聚合"没有意义。
//
// groupCount 是去重后的完整链长度（含链首）。
func orangeNormalizeGroupChainMode(mode string, groupCount int) (string, error) {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "", OrangeGroupChainModeNone:
		if groupCount >= 3 {
			return OrangeGroupChainModeAggregate, nil
		}
		return OrangeGroupChainModeFallback, nil
	case OrangeGroupChainModeFallback:
		if groupCount > 2 {
			return "", ErrOrangeGroupChainFallbackLimit
		}
		return OrangeGroupChainModeFallback, nil
	case OrangeGroupChainModeAggregate:
		if groupCount < 2 {
			return "", ErrOrangeGroupChainTooFew
		}
		return OrangeGroupChainModeAggregate, nil
	default:
		return "", ErrOrangeGroupChainModeInvalid
	}
}

// OrangeAttachGroupChain 把分组优先级链挂到请求级 API Key 上，并做链首失效自愈。
// 鉴权中间件在 apiKey 已放入上下文后调用（每次请求都会走到）。
func (s *APIKeyService) OrangeAttachGroupChain(ctx context.Context, apiKey *APIKey) *APIKey {
	if apiKey == nil {
		return nil
	}
	s.orangeAttachGroupChain(ctx, apiKey, true)
	return apiKey
}

// OrangeAttachGroupChains 批量挂载链（列表接口用，避免逐条查询）。
func (s *APIKeyService) OrangeAttachGroupChains(ctx context.Context, keys []APIKey) {
	if s == nil || len(keys) == 0 {
		return
	}
	ids := make([]int64, 0, len(keys))
	for i := range keys {
		if keys[i].ID > 0 {
			ids = append(ids, keys[i].ID)
		}
	}
	entries := s.orangeGroupChainEntries(ctx, ids)
	if len(entries) == 0 {
		return
	}
	for i := range keys {
		entry, ok := entries[keys[i].ID]
		if !ok {
			continue
		}
		keys[i].GroupChainMode = orangeResolveGroupChainMode(entry.mode, len(entry.members))
		if len(entry.members) == 0 {
			continue
		}
		keys[i].Groups = orangeBuildAPIKeyGroupEntries(keys[i].Group, entry)
	}
}

// orangeAttachGroupChain 装载链并挂到 apiKey.Groups；heal 为 true 时执行链首失效自愈。
func (s *APIKeyService) orangeAttachGroupChain(ctx context.Context, apiKey *APIKey, heal bool) {
	if s == nil || apiKey == nil || apiKey.ID <= 0 {
		return
	}
	entry, ok := s.orangeGroupChainEntry(ctx, apiKey.ID)
	// 模式随链一起装载：无候选的普通 Key 也要带上持久化模式，供列表/详情区分「兜底分组 / 聚合密钥」。
	apiKey.GroupChainMode = orangeResolveGroupChainMode(entry.mode, len(entry.members))
	if !ok || len(entry.members) == 0 {
		apiKey.Groups = nil
		return
	}
	apiKey.Groups = orangeBuildAPIKeyGroupEntries(apiKey.Group, entry)
	if heal {
		healOrangeKeyChainHead(ctx, apiKey)
	}
}

// orangeBuildAPIKeyGroupEntries 把"链首 + 候选"拼成完整链（priority 从 1 起连续编号）。
// 候选分组的 *Group 是缓存的每请求浅拷贝，避免请求内改写污染进程内缓存。
func orangeBuildAPIKeyGroupEntries(head *Group, entry orangeGroupChainCacheEntry) []*APIKeyGroupEntry {
	entries := make([]*APIKeyGroupEntry, 0, len(entry.members)+1)
	if head != nil && head.ID > 0 {
		entries = append(entries, &APIKeyGroupEntry{Group: head, Priority: 1})
	}
	for i := range entry.members {
		var group *Group
		if i < len(entry.groups) {
			group = cloneOrangeGroup(entry.groups[i])
		}
		if group == nil || group.ID <= 0 {
			continue
		}
		entries = append(entries, &APIKeyGroupEntry{Group: group, Priority: len(entries) + 1})
	}
	if len(entries) == 0 {
		return nil
	}
	return entries
}

// cloneOrangeGroup 浅拷贝分组对象（切片/映射字段按只读共享）。
func cloneOrangeGroup(group *Group) *Group {
	if group == nil {
		return nil
	}
	cloned := *group
	return &cloned
}

// orangeGroupChainEntry 读取单个 Key 的缓存项（未命中则按需装载）。
func (s *APIKeyService) orangeGroupChainEntry(ctx context.Context, apiKeyID int64) (orangeGroupChainCacheEntry, bool) {
	if apiKeyID <= 0 {
		return orangeGroupChainCacheEntry{}, false
	}
	entries := s.orangeGroupChainEntries(ctx, []int64{apiKeyID})
	entry, ok := entries[apiKeyID]
	if !ok || len(entry.members) == 0 {
		return entry, false
	}
	return entry, true
}

// orangeGroupChainEntries 批量读取链缓存，未命中的 Key 回源数据库并回填缓存。
// 空链（无候选）同样会被缓存，避免普通 Key 每个请求都打库。
func (s *APIKeyService) orangeGroupChainEntries(ctx context.Context, apiKeyIDs []int64) map[int64]orangeGroupChainCacheEntry {
	out := make(map[int64]orangeGroupChainCacheEntry, len(apiKeyIDs))
	if s == nil || len(apiKeyIDs) == 0 {
		return out
	}

	now := time.Now()
	missing := make([]int64, 0, len(apiKeyIDs))
	orangeGroupChainCache.mu.RLock()
	for _, id := range apiKeyIDs {
		if id <= 0 {
			continue
		}
		entry, ok := orangeGroupChainCache.entries[id]
		if ok && now.Before(entry.expiresAt) {
			out[id] = entry
			continue
		}
		missing = append(missing, id)
	}
	orangeGroupChainCache.mu.RUnlock()
	if len(missing) == 0 {
		return out
	}

	repo, ok := s.apiKeyRepo.(orangeGroupChainRepository)
	if !ok {
		return out
	}
	members, err := repo.OrangeLoadGroupChains(ctx, missing)
	if err != nil {
		slog.Warn("orange_key_group_chain_load_failed", "api_key_count", len(missing), "error", err)
		return out
	}
	// 模式元数据是"锦上添花"：读取失败时退化为按候选数推断（兜底模式 = 1 个候选）。
	modes, modeErr := repo.OrangeLoadGroupChainModes(ctx, missing)
	if modeErr != nil {
		slog.Warn("orange_key_group_chain_mode_load_failed", "api_key_count", len(missing), "error", modeErr)
	}

	loaded := make(map[int64]orangeGroupChainCacheEntry, len(missing))
	expiresAt := now.Add(orangeGroupChainCacheTTL)
	for _, id := range missing {
		chainMembers := members[id]
		loaded[id] = orangeGroupChainCacheEntry{
			expiresAt: expiresAt,
			members:   chainMembers,
			groups:    orangeHydrateChainGroups(ctx, s.groupRepo, chainMembers),
			mode:      orangeResolveGroupChainMode(modes[id], len(chainMembers)),
		}
		out[id] = loaded[id]
	}

	orangeGroupChainCache.mu.Lock()
	if len(orangeGroupChainCache.entries)+len(loaded) > orangeGroupChainCacheMaxEntries {
		orangeGroupChainCache.entries = make(map[int64]orangeGroupChainCacheEntry, orangeGroupChainCacheMaxEntries)
	}
	for id, entry := range loaded {
		orangeGroupChainCache.entries[id] = entry
	}
	orangeGroupChainCache.mu.Unlock()
	return out
}

// orangeHydrateChainGroups 把链候选水化为 *Group；已软删/不可读的候选记为 nil。
func orangeHydrateChainGroups(ctx context.Context, groupRepo GroupRepository, members []OrangeGroupChainMember) []*Group {
	if len(members) == 0 || groupRepo == nil {
		return nil
	}
	groups := make([]*Group, len(members))
	for i, member := range members {
		if member.GroupID <= 0 {
			continue
		}
		group, err := groupRepo.GetByIDLite(ctx, member.GroupID)
		if err != nil || group == nil {
			slog.Debug("orange_key_group_chain_member_unavailable",
				"group_id", member.GroupID, "error", err)
			continue
		}
		groups[i] = group
	}
	return groups
}

// OrangeInvalidateGroupChainCacheByGroup 在分组配置（倍率/白名单/状态）变更后清理链缓存。
//
// 分组增删改由管理员流程触发，属上游 service（group/admin_group）的接线点，
// 本功能只提供能力：调用方在分组变更后调用一次即可。
func (s *APIKeyService) OrangeInvalidateGroupChainCacheByGroup(ctx context.Context, groupID int64) {
	if s == nil || groupID <= 0 {
		return
	}
	if repo, ok := s.apiKeyRepo.(orangeGroupChainRepository); ok {
		apiKeyIDs, err := repo.OrangeListAPIKeyIDsByChainGroup(ctx, groupID)
		if err != nil {
			slog.Warn("orange_key_group_chain_group_lookup_failed", "group_id", groupID, "error", err)
		}
		for _, apiKeyID := range apiKeyIDs {
			InvalidateOrangeGroupChainCache(apiKeyID)
		}
	}
	s.InvalidateAuthCacheByGroupID(ctx, groupID)
}

// OrangeAPIKeyGroupChainMemberView 是链上一项（含分组详情），供 handler 映射 DTO。
type OrangeAPIKeyGroupChainMemberView struct {
	GroupID  int64
	Priority int
	Group    *Group
	// Available 表示当前用户是否还能使用该分组（与请求时链成员过滤同源判定）。
	Available bool
	// UnavailableReason 在 Available=false 时给出原因，取值见 OrangeGroupChainUnavailableReason*。
	UnavailableReason string
}

// Orange 特有：链成员「不可用」的原因码（链详情接口对外暴露，前端据此展示文案）。
const (
	// OrangeGroupChainUnavailableReasonNotEntitled 表示用户已不再拥有该分组的绑定权限
	// （分组被改成专属但未授权给该 Key 的主人，或该用户的公开分组访问被限制）。
	OrangeGroupChainUnavailableReasonNotEntitled = "not_entitled"
)

// OrangeAPIKeyGroupChainView 是 GET/PUT /keys/:id/group-chain 的返回视图。
type OrangeAPIKeyGroupChainView struct {
	APIKeyID       int64
	Mode           string
	PrimaryGroupID int64
	Members        []OrangeAPIKeyGroupChainMemberView
}

// OrangeGetAPIKeyGroupChain 读取某个 Key 的分组链视图（仅 Key 拥有者可读）。
func (s *APIKeyService) OrangeGetAPIKeyGroupChain(ctx context.Context, userID, apiKeyID int64) (OrangeAPIKeyGroupChainView, error) {
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
	return s.OrangeBuildAPIKeyGroupChainView(ctx, apiKey), nil
}

// OrangeBuildAPIKeyGroupChainView 构建链视图：members[0] 恒为链首（= api_keys.group_id），
// 软删/停用的成员不返回，其余成员按 priority 升序。
//
// 每个成员额外带「当前用户是否还能使用该分组」（Available / UnavailableReason）：
// 分组被改成专属或撤销授权后，链上残留的成员在请求时会被跳过，界面必须能解释
// 「我配过这个分组，但现在没有权限了」，而不是让用户看到一个空下拉框。
func (s *APIKeyService) OrangeBuildAPIKeyGroupChainView(ctx context.Context, apiKey *APIKey) OrangeAPIKeyGroupChainView {
	view := OrangeAPIKeyGroupChainView{Mode: OrangeGroupChainModeNone}
	if s == nil || apiKey == nil {
		return view
	}
	view.APIKeyID = apiKey.ID
	if apiKey.GroupID != nil {
		view.PrimaryGroupID = *apiKey.GroupID
	}
	owner := apiKey.User

	entry, _ := s.orangeGroupChainEntry(ctx, apiKey.ID)
	// 模式取"持久化值（无记录则按候选数推断）"：聚合密钥即使只配了 2 个分组也必须显示成聚合。
	view.Mode = orangeResolveGroupChainMode(entry.mode, len(entry.members))

	chain := make([]OrangeAPIKeyGroupChainMemberView, 0, len(entry.members)+1)
	head := apiKey.Group
	if head != nil && head.ID > 0 && head.IsActive() && IsGroupContextValid(head) {
		chain = append(chain, orangeChainMemberView(head, 1, owner))
	} else {
		head = nil
	}
	for i := range entry.members {
		if i >= len(entry.groups) || entry.groups[i] == nil {
			continue
		}
		group := entry.groups[i]
		if !group.IsActive() || !IsGroupContextValid(group) {
			continue
		}
		if head != nil && (group.Platform != head.Platform || group.SubscriptionType != head.SubscriptionType) {
			continue
		}
		chain = append(chain, orangeChainMemberView(cloneOrangeGroup(group), len(chain)+1, owner))
	}
	if len(chain) == 0 {
		return view
	}
	view.Members = chain
	return view
}

// orangeChainMemberView 组装链详情的一项，并标注「当前用户是否还能使用该分组」。
//
// 判定与请求时完全同源（orangeGroupBindableForUser）：界面所见 = 请求实际会用的链。
func orangeChainMemberView(group *Group, priority int, user *User) OrangeAPIKeyGroupChainMemberView {
	member := OrangeAPIKeyGroupChainMemberView{
		GroupID:   group.ID,
		Priority:  priority,
		Group:     group,
		Available: true,
	}
	if !orangeGroupBindableForUser(user, group) {
		member.Available = false
		member.UnavailableReason = OrangeGroupChainUnavailableReasonNotEntitled
	}
	return member
}

// OrangeSetAPIKeyGroupChain 设置某个 Key 的分组优先级链（groupIDs[0] 成为新的主分组）。
//
// groupIDs 为空表示清空链（保留当前主分组）并删除模式元数据；否则要求：全部可用、可绑定、
// 同平台、同订阅类型、去重后不超过 8 组。写链首（api_keys.group_id）、写候选与写模式在同一事务内完成。
//
// mode 是展示模式（fallback / aggregate），空串表示按分组数量推断：<=2 个分组 → fallback，
// >=3 个 → aggregate。模式只影响界面，路由（选号推进）对两者完全一致。
func (s *APIKeyService) OrangeSetAPIKeyGroupChain(ctx context.Context, userID, apiKeyID int64, groupIDs []int64, mode string) (OrangeAPIKeyGroupChainView, error) {
	empty := OrangeAPIKeyGroupChainView{}
	if s == nil || apiKeyID <= 0 {
		return empty, ErrAPIKeyNotFound
	}

	// 归属校验：非拥有者按"Key 不存在"处理（404），不泄漏他人 Key 的存在性。
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
	repo, ok := s.apiKeyRepo.(orangeGroupChainRepository)
	if !ok {
		return empty, ErrOrangeGroupChainUnavailable
	}

	unique := orangeDedupeGroupIDs(groupIDs)
	if len(unique) == 0 {
		// 清链：链候选与模式元数据在同一事务内删除（仓储层负责），保留当前主分组。
		if err := repo.OrangeReplaceGroupChain(ctx, apiKeyID, nil); err != nil {
			return empty, fmt.Errorf("replace orange key group chain: %w", err)
		}
		s.orangeInvalidateAfterChainWrite(ctx, apiKey)
		return s.orangeReloadGroupChainView(ctx, apiKeyID)
	}
	if len(unique) > orangeChainMaxCandidates+1 {
		return empty, ErrOrangeGroupChainTooLong
	}
	resolvedMode, err := orangeNormalizeGroupChainMode(mode, len(unique))
	if err != nil {
		return empty, err
	}

	user, err := s.userRepo.GetByID(ctx, userID)
	if err != nil {
		return empty, fmt.Errorf("get user: %w", err)
	}
	groups := make([]*Group, 0, len(unique))
	for _, groupID := range unique {
		group, getErr := s.groupRepo.GetByID(ctx, groupID)
		if getErr != nil || group == nil {
			return empty, ErrGroupNotFound
		}
		if !group.IsActive() || !s.canUserBindGroup(ctx, user, group) {
			return empty, ErrOrangeGroupChainInvalid
		}
		groups = append(groups, group)
	}
	head := groups[0]
	for _, group := range groups[1:] {
		// 链上不允许跨平台（网关在中间件阶段已按主分组平台分发）与跨订阅类型
		// （会让后序分组的订阅限额校验被绕过），见方案文档 §5.6 与 D5/D6。
		if group.Platform != head.Platform || group.SubscriptionType != head.SubscriptionType {
			return empty, ErrOrangeGroupChainInvalid
		}
	}

	tail := make([]int64, 0, len(groups)-1)
	for _, group := range groups[1:] {
		tail = append(tail, group.ID)
	}
	if err := repo.OrangeReplaceGroupChainAndPrimary(ctx, apiKeyID, head.ID, tail, resolvedMode); err != nil {
		return empty, fmt.Errorf("replace orange key group chain: %w", err)
	}
	s.orangeInvalidateAfterChainWrite(ctx, apiKey)
	return s.orangeReloadGroupChainView(ctx, apiKeyID)
}

// orangeReloadGroupChainView 重新装载 Key 并返回链视图（写操作后返回最新状态）。
func (s *APIKeyService) orangeReloadGroupChainView(ctx context.Context, apiKeyID int64) (OrangeAPIKeyGroupChainView, error) {
	InvalidateOrangeGroupChainCache(apiKeyID)
	apiKey, err := s.apiKeyRepo.GetByID(ctx, apiKeyID)
	if err != nil || apiKey == nil {
		return OrangeAPIKeyGroupChainView{}, ErrAPIKeyNotFound
	}
	return s.OrangeBuildAPIKeyGroupChainView(ctx, apiKey), nil
}

// orangeInvalidateAfterChainWrite 写链后清理链缓存与认证快照缓存。
func (s *APIKeyService) orangeInvalidateAfterChainWrite(ctx context.Context, apiKey *APIKey) {
	if apiKey == nil {
		return
	}
	InvalidateOrangeGroupChainCache(apiKey.ID)
	if apiKey.Key != "" {
		s.InvalidateAuthCacheByKey(ctx, apiKey.Key)
	}
}

// orangeDedupeGroupIDs 去重并保留首次出现顺序，同时丢弃非法值（<=0）。
func orangeDedupeGroupIDs(groupIDs []int64) []int64 {
	if len(groupIDs) == 0 {
		return nil
	}
	unique := make([]int64, 0, len(groupIDs))
	seen := make(map[int64]struct{}, len(groupIDs))
	for _, groupID := range groupIDs {
		if groupID <= 0 {
			continue
		}
		if _, ok := seen[groupID]; ok {
			continue
		}
		seen[groupID] = struct{}{}
		unique = append(unique, groupID)
	}
	return unique
}
