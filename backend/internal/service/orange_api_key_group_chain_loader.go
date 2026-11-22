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
	OrangeReplaceGroupChainAndPrimary(ctx context.Context, apiKeyID, primaryGroupID int64, tail []int64) error
	OrangeLoadGroupChains(ctx context.Context, apiKeyIDs []int64) (map[int64][]OrangeGroupChainMember, error)
	OrangeListAPIKeyIDsByChainGroup(ctx context.Context, groupID int64) ([]int64, error)
}

// orangeGroupChainCacheEntry 是缓存项：原始链候选（按 priority 升序）+ 对齐的水化分组。
// groups[i] 为 nil 表示该候选分组已软删除或不可读。
type orangeGroupChainCacheEntry struct {
	expiresAt time.Time
	members   []OrangeGroupChainMember
	groups    []*Group
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
		if !ok || len(entry.members) == 0 {
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

	loaded := make(map[int64]orangeGroupChainCacheEntry, len(missing))
	expiresAt := now.Add(orangeGroupChainCacheTTL)
	for _, id := range missing {
		chainMembers := members[id]
		loaded[id] = orangeGroupChainCacheEntry{
			expiresAt: expiresAt,
			members:   chainMembers,
			groups:    orangeHydrateChainGroups(ctx, s.groupRepo, chainMembers),
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
}

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
func (s *APIKeyService) OrangeBuildAPIKeyGroupChainView(ctx context.Context, apiKey *APIKey) OrangeAPIKeyGroupChainView {
	view := OrangeAPIKeyGroupChainView{Mode: OrangeGroupChainModeNone}
	if s == nil || apiKey == nil {
		return view
	}
	view.APIKeyID = apiKey.ID
	if apiKey.GroupID != nil {
		view.PrimaryGroupID = *apiKey.GroupID
	}

	entry, _ := s.orangeGroupChainEntry(ctx, apiKey.ID)
	view.Mode = orangeGroupChainModeForCount(len(entry.members))

	chain := make([]*APIKeyGroupEntry, 0, len(entry.members)+1)
	head := apiKey.Group
	if head != nil && head.ID > 0 && head.IsActive() && IsGroupContextValid(head) {
		chain = append(chain, &APIKeyGroupEntry{Group: head, Priority: 1})
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
		chain = append(chain, &APIKeyGroupEntry{Group: cloneOrangeGroup(group), Priority: len(chain) + 1})
	}
	if len(chain) == 0 {
		return view
	}
	view.Members = make([]OrangeAPIKeyGroupChainMemberView, 0, len(chain))
	for _, item := range chain {
		view.Members = append(view.Members, OrangeAPIKeyGroupChainMemberView{
			GroupID:  item.Group.ID,
			Priority: item.Priority,
			Group:    item.Group,
		})
	}
	return view
}

// OrangeSetAPIKeyGroupChain 设置某个 Key 的分组优先级链（groupIDs[0] 成为新的主分组）。
//
// groupIDs 为空表示清空链（保留当前主分组）；否则要求：全部可用、可绑定、同平台、
// 同订阅类型、去重后不超过 8 组。写链首（api_keys.group_id）与写候选在同一事务内完成。
func (s *APIKeyService) OrangeSetAPIKeyGroupChain(ctx context.Context, userID, apiKeyID int64, groupIDs []int64) (OrangeAPIKeyGroupChainView, error) {
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
		if err := repo.OrangeReplaceGroupChain(ctx, apiKeyID, nil); err != nil {
			return empty, fmt.Errorf("replace orange key group chain: %w", err)
		}
		s.orangeInvalidateAfterChainWrite(ctx, apiKey)
		return s.orangeReloadGroupChainView(ctx, apiKeyID)
	}
	if len(unique) > orangeChainMaxCandidates+1 {
		return empty, ErrOrangeGroupChainTooLong
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
	if err := repo.OrangeReplaceGroupChainAndPrimary(ctx, apiKeyID, head.ID, tail); err != nil {
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
