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
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

// OrangeGroupChainPruneReason 是「保存将移除分组链成员」冲突的对外 reason（前端据此弹确认框）。
const OrangeGroupChainPruneReason = "ORANGE_GROUP_CHAIN_PRUNE_CONFIRM"

// Orange 特有分组链的错误码（对外暴露 reason，供前端区分提示）。
var (
	// ErrOrangeGroupChainInvalid 表示链首（主分组）不可用：不存在 / 已停用 / 用户无权绑定。
	//
	// v0.2.25-8 起只对链首做强校验：候选成员允许停用 / 跨平台 / 跨订阅类型 / 无权限
	// （读视图会把它们标成 disabled / scope_mismatch / no_permission 置灰提示），
	// 因此本错误不再表示"整条链上有坏成员"。
	ErrOrangeGroupChainInvalid = infraerrors.BadRequest("ORANGE_GROUP_CHAIN_INVALID",
		"主分组无效：主分组必须存在、已启用，且你有权使用")
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
	// ErrOrangeGroupChainPruneConfirm 是删除保护的基线错误（409 + reason）。
	// 真正返回给调用方的是 OrangeGroupChainPruneError（带被移除分组清单，Unwrap 到本错误）；
	// errors.Is 只比较 code+reason，因此动态 message 不影响判定。
	ErrOrangeGroupChainPruneConfirm = infraerrors.Conflict(OrangeGroupChainPruneReason,
		"保存将移除分组链上的分组，确认请重试")
)

// OrangeGroupChainPruneError 是 PUT /keys/:id/group-chain 的「保存将移除分组」冲突（409）。
//
// 为什么单独定类型：409 的响应需要同时给出人可读文案（message）与结构化清单
// （metadata.pending_removals），handler 用 errors.As 取出 Removed 后再写响应；
// 只靠 *ApplicationError 的 message 无法稳定还原清单。
//
// Unwrap 指向 ErrOrangeGroupChainPruneConfirm，因此既有调用方/测试仍然可以用
// errors.Is(err, ErrOrangeGroupChainPruneConfirm) 与 infraerrors.IsConflict(err) 判定。
type OrangeGroupChainPruneError struct {
	// Removed 是本次保存会从链上移除的分组（含名称；分组已删除时名称退化为 "#id"）。
	Removed []OrangePrunedChainGroup
}

// Error 实现 error；文案与 HTTP 响应的 message 完全一致。
func (e *OrangeGroupChainPruneError) Error() string { return e.Message() }

// Unwrap 让 errors.Is(err, ErrOrangeGroupChainPruneConfirm) 继续成立（保留 409 语义）。
//
// 返回的基线错误带**本次**的动态文案：这样 infraerrors.Message(err) / Reason(err) / Code(err)
// 与 e.Message() 完全一致（reason/code 不变，errors.Is 只比较这两项，判定不受影响）。
func (e *OrangeGroupChainPruneError) Unwrap() error {
	base := infraerrors.Clone(ErrOrangeGroupChainPruneConfirm)
	base.Message = e.Message()
	return base
}

// RemovedGroups 返回本次会被移除的分组（防御性拷贝，调用方可安全修改）。
func (e *OrangeGroupChainPruneError) RemovedGroups() []OrangePrunedChainGroup {
	if e == nil || len(e.Removed) == 0 {
		return nil
	}
	out := make([]OrangePrunedChainGroup, len(e.Removed))
	copy(out, e.Removed)
	return out
}

// Message 返回「保存将移除 N 个分组：名1、名2；确认请重试」。
//
// 名单与计数必须一致：名称为空时用 "#id" 兜底（分组已被删除时无法取到名称）。
func (e *OrangeGroupChainPruneError) Message() string {
	removed := e.RemovedGroups()
	names := make([]string, 0, len(removed))
	for _, group := range removed {
		name := strings.TrimSpace(group.Name)
		if name == "" {
			name = orangeChainGroupDisplayName(nil, group.ID)
		}
		if name == "" {
			continue
		}
		names = append(names, name)
	}
	message := fmt.Sprintf("保存将移除 %d 个分组", len(removed))
	if len(names) > 0 {
		message += "：" + strings.Join(names, "、")
	}
	return message + "；确认请重试"
}

// PendingRemovalsJSON 返回结构化清单 `[{"group_id":1,"name":"a"}]`，供响应 metadata 使用。
// 空清单返回 "[]"（前端可只依赖 message，不必判空）。
func (e *OrangeGroupChainPruneError) PendingRemovalsJSON() string {
	type pendingRemoval struct {
		GroupID int64  `json:"group_id"`
		Name    string `json:"name"`
	}
	removed := e.RemovedGroups()
	pending := make([]pendingRemoval, 0, len(removed))
	for _, group := range removed {
		name := strings.TrimSpace(group.Name)
		if name == "" {
			name = orangeChainGroupDisplayName(nil, group.ID)
		}
		pending = append(pending, pendingRemoval{GroupID: group.ID, Name: name})
	}
	raw, err := json.Marshal(pending)
	if err != nil {
		// 理论上不可能（只含 int64 / string）；退化成空清单，绝不因为序列化让保存失败。
		return "[]"
	}
	return string(raw)
}

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
//
// v0.2.25-8 起列表与详情口径一致：**返回全部成员 + state**，不再按权限静默丢弃成员
// （旧实现把「用户无权限」的候选从列表里删掉，导致列表与编辑弹窗分组数量对不上）。
//
// 列表接口的 APIKey 通常只预载了主分组（Group），没有带 allowed_groups 的 User；
// 因此这里**一律**用 userRepo.GetByID 重载 owner（不再优先用 key.User：它可能来自不含
// allowed_groups 的查询，会把手里的专属分组误判成 no_permission）。查询失败则退回「不判权限」。
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
	owners := make(map[int64]*User, 2)
	ownerOf := func(key *APIKey) *User {
		if key == nil || key.UserID <= 0 || s.userRepo == nil {
			return nil
		}
		if user, ok := owners[key.UserID]; ok {
			return user
		}
		user, err := s.userRepo.GetByID(ctx, key.UserID)
		if err != nil {
			slog.Warn("orange_key_group_chain_owner_load_failed",
				"user_id", key.UserID, "error", err)
			user = nil
		}
		owners[key.UserID] = user
		return user
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
		keys[i].Groups = orangeBuildAPIKeyGroupEntriesFull(keys[i].Group, derefGroupID(keys[i].GroupID), entry, ownerOf(&keys[i]))
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
	apiKey.Groups = orangeBuildAPIKeyGroupEntries(apiKey.Group, entry, apiKey.User)
	if heal {
		healOrangeKeyChainHead(ctx, apiKey)
	}
}

// orangeBuildAPIKeyGroupEntries 把"链首 + 候选"拼成完整链（priority 从 1 起连续编号）。
// 候选分组的 *Group 是缓存的每请求浅拷贝，避免请求内改写污染进程内缓存。
//
// **鉴权/网关路径专用**（OrangeAttachGroupChain → middleware → 选号）：user 非空时过滤掉
// 「用户已无绑定权限」的候选（分组被改为专属 / 撤销授权）。过滤口径与改造前逐行一致
// （只判权限；停用/上下文完整性由下游 orangeUsableKeyChain / OrangeChainGroups 再判），
// 不要为了列表展示改成返回全部成员（列表口径见 orangeBuildAPIKeyGroupEntriesFull）。
func orangeBuildAPIKeyGroupEntries(head *Group, entry orangeGroupChainCacheEntry, user *User) []*APIKeyGroupEntry {
	entries := make([]*APIKeyGroupEntry, 0, len(entry.members)+1)
	if head != nil && head.ID > 0 {
		entries = append(entries, &APIKeyGroupEntry{
			GroupID: head.ID, Group: head, Priority: 1, State: orangeChainMemberStateOf(head, head, user),
		})
	}
	for i := range entry.members {
		var group *Group
		if i < len(entry.groups) {
			group = cloneOrangeGroup(entry.groups[i])
		}
		if group == nil || group.ID <= 0 {
			continue
		}
		if !orangeGroupBindableForUser(user, group) {
			continue
		}
		entries = append(entries, &APIKeyGroupEntry{
			GroupID: group.ID, Group: group, Priority: len(entries) + 1, State: orangeChainMemberStateOf(head, group, user),
		})
	}
	if len(entries) == 0 {
		return nil
	}
	return entries
}

// orangeBuildAPIKeyGroupEntriesFull 把"链首 + 全部候选"拼成完整链，并为每项标注 state。
//
// **列表接口专用**（OrangeAttachGroupChains）：与详情接口口径完全一致（契约 B）——
// 链上每个成员都会输出，连分组已被删除（GetByIDLite 读不到）的候选也保留一行
// { GroupID, Priority, State: missing }，只是没有 *Group 详情（DTO 的 group 是 omitempty）。
// 这样"列表看到的分组数量"与"编辑弹窗里的成员数量"永远一致，前端不会因为少一行而
// 误把成员当成已被删除、进而静默裁剪用户配置。
//
// headGroupID 是密钥的主分组 ID（api_keys.group_id）：链首分组本身已被删除时（head 为 nil）
// 仍要以 state=missing 保留那一行，否则列表会比详情少一行（契约 A/B 要求覆盖链首）。
func orangeBuildAPIKeyGroupEntriesFull(head *Group, headGroupID int64, entry orangeGroupChainCacheEntry, user *User) []*APIKeyGroupEntry {
	entries := make([]*APIKeyGroupEntry, 0, len(entry.members)+1)
	headID := int64(0)
	if head != nil && head.ID > 0 {
		headID = head.ID
		entries = append(entries, &APIKeyGroupEntry{
			GroupID: head.ID, Group: head, Priority: 1, State: orangeChainMemberStateOf(head, head, user),
		})
	} else if headGroupID > 0 {
		// 链首分组读不到（已删除 / 主分组被清空）：保留一行 missing，与详情视图一致。
		headID = headGroupID
		entries = append(entries, &APIKeyGroupEntry{
			GroupID: headGroupID, Priority: 1, State: OrangeChainMemberStateMissing,
		})
	}
	for i := range entry.members {
		var group *Group
		if i < len(entry.groups) {
			group = cloneOrangeGroup(entry.groups[i])
		}
		groupID := entry.members[i].GroupID
		if group != nil && group.ID > 0 {
			groupID = group.ID
		}
		if groupID <= 0 {
			continue
		}
		// 链首不会重复出现在候选里；脏数据时以链首那一行为准。
		if groupID == headID {
			continue
		}
		entries = append(entries, &APIKeyGroupEntry{
			GroupID:  groupID,
			Group:    group,
			Priority: len(entries) + 1,
			State:    orangeChainMemberStateOf(head, group, user),
		})
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
//
// v0.2.25-8 起返回链上**全部**成员（链首 + 全部候选），成员是否可用由 State 表达：
// available / disabled / scope_mismatch / no_permission / missing。Group 为 nil 表示分组已被删除
// （state=missing），此时只保留 GroupID 供界面提示。
type OrangeAPIKeyGroupChainMemberView struct {
	GroupID  int64
	Priority int
	Group    *Group
	State    OrangeChainMemberState
}

// OrangeAPIKeyGroupChainView 是 GET/PUT/heal /keys/:id/group-chain 的返回视图。
type OrangeAPIKeyGroupChainView struct {
	APIKeyID       int64
	Mode           string
	PrimaryGroupID int64
	Members        []OrangeAPIKeyGroupChainMemberView
	// AutoPrunedGroupIDs 是本次**显式 heal** 真正从链里移除的分组（失去绑定权限 / 分组已删）。
	// GET 是纯读，这两个字段恒为空（v0.2.25-8）。
	AutoPrunedGroupIDs []int64
	// AutoPrunedGroups 是同一批分组（含名称），供提示里显示分组名而不是 #id。
	AutoPrunedGroups []OrangePrunedChainGroup
	// AutoPromotedGroupID 是链首被自动替换后的新主分组 ID（0 表示链首未变）。
	AutoPromotedGroupID int64
}

// orangeLoadChainOwner 用 userRepo 重载 Key 拥有者（含 allowed_groups 全量授权），并挂到
// apiKey.User 上，供后续权限判定与视图构造使用；失败时返回错误，调用方**不得写库**。
//
// 为什么不能直接用 apiKeyRepo.GetByID 的结果：该查询只 WithUser()，不预载 allowed_groups 边，
// apiKey.User.AllowedGroups 恒为空 —— 专属分组会被误判成「用户无权限」（缺陷 D1），
// 旧实现更会据此把分组从链里删掉。
//
// userRepo 为 nil 只可能出现在单元测试里（服务由 wire 装配，恒非空），此时沿用 apiKey.User。
func (s *APIKeyService) orangeLoadChainOwner(ctx context.Context, apiKey *APIKey) (*User, error) {
	if apiKey == nil {
		return nil, ErrAPIKeyNotFound
	}
	if s == nil || s.userRepo == nil || apiKey.UserID <= 0 {
		return apiKey.User, nil
	}
	user, err := s.userRepo.GetByID(ctx, apiKey.UserID)
	if err != nil {
		return nil, fmt.Errorf("get user: %w", err)
	}
	if user == nil {
		return nil, fmt.Errorf("get user %d: empty result", apiKey.UserID)
	}
	apiKey.User = user
	return user, nil
}

// OrangeGetAPIKeyGroupChain 读取某个 Key 的分组链视图（仅 Key 拥有者可读）。
//
// v0.2.25-8 起是**纯读**：不再触发任何自愈写库（旧实现在 GET 里修剪链成员并把结果写回数据库，
// 前端只要少提交就永久丢配置 —— 缺陷 D3）。需要真正清理失效成员时由前端显式调用
// POST /keys/:id/group-chain/heal（见 OrangeHealAPIKeyGroupChain）。
//
// 用户权限一律用 userRepo 重载后的 owner 判定（见 orangeLoadChainOwner），失败返回错误。
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
	if _, err := s.orangeLoadChainOwner(ctx, apiKey); err != nil {
		return empty, err
	}
	return s.OrangeBuildAPIKeyGroupChainViewFull(ctx, apiKey), nil
}

// OrangeBuildAPIKeyGroupChainViewFull 构建「全部成员 + state」的链视图（v0.2.25-8 契约）。
//
// members[0] 恒为链首（= api_keys.group_id，读不到时以 state=missing 保留该行）；链上每个候选
// 都会出现，包括停用、跨平台、无权限、已被删除的成员 —— 只有这样用户在编辑弹窗里才能看到
// 自己配置的完整链。是否可用由每个成员的 state 表达，请求/网关侧只取 available。
//
// 注意：调用方必须先把 owner（含 allowed_groups）挂到 apiKey.User 上（见 orangeLoadChainOwner），
// 本函数刻意不查库，避免列表/视图构造路径上出现隐藏查询。
func (s *APIKeyService) OrangeBuildAPIKeyGroupChainViewFull(ctx context.Context, apiKey *APIKey) OrangeAPIKeyGroupChainView {
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

	members := make([]OrangeAPIKeyGroupChainMemberView, 0, len(entry.members)+1)
	head := apiKey.Group
	if head == nil || head.ID <= 0 {
		head = nil
		// 链首分组读不到（已删 / 主分组被清空）：也要返回一行 missing，前端才不会丢行。
		if view.PrimaryGroupID > 0 {
			members = append(members, OrangeAPIKeyGroupChainMemberView{
				GroupID: view.PrimaryGroupID, Priority: 1, State: OrangeChainMemberStateMissing,
			})
		}
	} else {
		members = append(members, OrangeAPIKeyGroupChainMemberView{
			GroupID:  head.ID,
			Priority: 1,
			Group:    cloneOrangeGroup(head),
			State:    orangeChainMemberStateOf(head, head, owner),
		})
	}
	for i := range entry.members {
		var group *Group
		if i < len(entry.groups) {
			group = cloneOrangeGroup(entry.groups[i])
		}
		groupID := entry.members[i].GroupID
		if group != nil && group.ID > 0 {
			groupID = group.ID
		}
		if groupID <= 0 {
			continue
		}
		// 链首不会重复出现在候选里；脏数据时以链首那一行为准。
		// Orange 特有：用 PrimaryGroupID 判断（链首分组已删除时 head 为 nil，但仍已补了一行 missing）。
		if view.PrimaryGroupID > 0 && groupID == view.PrimaryGroupID {
			continue
		}
		members = append(members, OrangeAPIKeyGroupChainMemberView{
			GroupID:  groupID,
			Priority: len(members) + 1,
			Group:    group,
			State:    orangeChainMemberStateOf(head, group, owner),
		})
	}
	view.Members = members
	return view
}

// OrangeBuildAPIKeyGroupChainView 是旧签名的兼容视图：只返回「本次请求真正可用」的成员
// （state=available；链首例外 —— 链首的权限由上游鉴权中间件单独判定，视图里保留）。
//
// 暂无生产调用方，仅为兼容既有测试/外部调用保留（网关与其它只读链路用的都是
// OrangeChainGroups / orangeUsableKeyChain，不经过本函数）。新代码一律用
// OrangeBuildAPIKeyGroupChainViewFull（v0.2.25-8 契约要求"返回全部成员 + state"）。
func (s *APIKeyService) OrangeBuildAPIKeyGroupChainView(ctx context.Context, apiKey *APIKey) OrangeAPIKeyGroupChainView {
	view := s.OrangeBuildAPIKeyGroupChainViewFull(ctx, apiKey)
	if len(view.Members) == 0 {
		return view
	}
	available := make([]OrangeAPIKeyGroupChainMemberView, 0, len(view.Members))
	for _, member := range view.Members {
		usable := orangeChainMemberStateUsable(member.State)
		// 链首保留：与改造前的视图一致（链首是否放行由鉴权中间件决定，不在视图里判权限）。
		if !usable && member.Priority == 1 && member.Group != nil &&
			member.State == OrangeChainMemberStateNoPermission {
			usable = true
		}
		if !usable {
			continue
		}
		member.Priority = len(available) + 1
		available = append(available, member)
	}
	view.Members = available
	return view
}

// OrangeSetGroupChainInput 是 PUT /keys/:id/group-chain 的写入参数（v0.2.25-8 契约 v2）。
type OrangeSetGroupChainInput struct {
	// GroupIDs 是有序分组 ID 列表：GroupIDs[0] 成为新的主分组（链首），空表示清空链与模式。
	GroupIDs []int64
	// Mode 是展示模式（fallback / aggregate），空串表示按分组数量推断。
	Mode string
	// ExpectedGroupIDs 是客户端**当前看到的完整成员集合**（可选）。服务端据此算出
	// 「本次保存会移除哪些分组」；未提供时用服务端当前集合代替（契约 C3：旧客户端同样受保护），
	// 未显式确认（ConfirmPrune=true）则返回 409 ORANGE_GROUP_CHAIN_PRUNE_CONFIRM。
	ExpectedGroupIDs []int64
	// ConfirmPrune 表示用户已确认「本次保存会移除分组」。
	ConfirmPrune bool
}

// OrangeSetAPIKeyGroupChain 设置某个 Key 的分组优先级链（GroupIDs[0] 成为新的主分组）。
//
// 写入校验（v0.2.25-8 契约 C2，只对链首做强校验）：
//   - GroupIDs[0]：必须存在 + 已启用 + 用户可绑定 → 否则 404 GROUP_NOT_FOUND / 400 ORANGE_GROUP_CHAIN_INVALID；
//   - GroupIDs[1:]（候选）：只要求「分组存在」或「该 ID 当前已在链上」，停用 / 跨平台 /
//     跨订阅类型 / 无权限一律接受 —— 读视图会把这些成员标成 disabled / scope_mismatch /
//     no_permission 置灰提示，保存时不能因为"用户明明看得见"就 400 拒绝；已删除的分组
//     只要仍在链上就保留 ID（用户配置不丢）。
//
// GroupIDs 为空表示清空链（保留当前主分组）并删除模式元数据；去重、<=8 个候选、mode 校验
// 的语义不变。写链首（api_keys.group_id）、写候选与写模式在同一事务内完成。
//
// Mode 是展示模式（fallback / aggregate），空串表示按分组数量推断：<=2 个分组 → fallback，
// >=3 个 → aggregate。模式只影响界面，路由（选号推进）对两者完全一致。
//
// 删除保护（契约 C1/C3）：removed = (expected_group_ids ?? 服务端当前集合) − GroupIDs，
// 任何「本次要移除的成员」都必须先由用户确认（confirm_prune=true），否则返回
// *OrangeGroupChainPruneError（409，reason=ORANGE_GROUP_CHAIN_PRUNE_CONFIRM）——
// 前端漏提交一个成员、或旧客户端直接全量替换，都不能再静默删掉用户配置。
func (s *APIKeyService) OrangeSetAPIKeyGroupChain(ctx context.Context, userID, apiKeyID int64, input OrangeSetGroupChainInput) (OrangeAPIKeyGroupChainView, error) {
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

	unique := orangeDedupeGroupIDs(input.GroupIDs)
	// 服务端当前链的全部成员（链首 + 全部候选）：既是删除保护的基准集合，
	// 也是「候选位允许保留已删除分组」的判定依据。
	current := s.orangeCurrentChainGroupIDs(ctx, repo, apiKey)

	// C1/C3 删除保护：removed = (expected_group_ids ?? 服务端当前集合) − group_ids。
	expected := orangeDedupeGroupIDs(input.ExpectedGroupIDs)
	if len(expected) == 0 {
		expected = current
	}
	if removed := orangeChainRemovedGroupIDs(expected, unique, apiKey.GroupID); len(removed) > 0 && !input.ConfirmPrune {
		return empty, &OrangeGroupChainPruneError{Removed: s.orangeResolvePrunedChainGroups(ctx, apiKey, removed)}
	}

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
	resolvedMode, err := orangeNormalizeGroupChainMode(input.Mode, len(unique))
	if err != nil {
		return empty, err
	}

	user, err := s.userRepo.GetByID(ctx, userID)
	if err != nil {
		return empty, fmt.Errorf("get user: %w", err)
	}
	onChain := make(map[int64]struct{}, len(current))
	for _, groupID := range current {
		onChain[groupID] = struct{}{}
	}

	headID := unique[0]
	tail := make([]int64, 0, len(unique)-1)
	for i, groupID := range unique {
		group, getErr := s.groupRepo.GetByID(ctx, groupID)
		exists := getErr == nil && group != nil
		if i == 0 {
			// 链首强校验（保持改动前的行为）：必须存在、已启用、用户可绑定。
			if !exists {
				return empty, ErrGroupNotFound
			}
			if !group.IsActive() || !s.canUserBindGroup(ctx, user, group) {
				return empty, ErrOrangeGroupChainInvalid
			}
			continue
		}
		if exists {
			// 候选放宽：存在即可。停用 / 跨平台 / 跨订阅类型 / 无权限都不再拒绝 ——
			// 读视图会返回这些成员并标注 state，保存时 1:1 提交必须能落库。
			tail = append(tail, groupID)
			continue
		}
		// 分组已被删除：只要该 ID 当前仍在链上就保留，用户配置不丢（契约 C2）。
		if _, kept := onChain[groupID]; kept {
			tail = append(tail, groupID)
			continue
		}
		return empty, ErrGroupNotFound
	}

	if err := repo.OrangeReplaceGroupChainAndPrimary(ctx, apiKeyID, headID, tail, resolvedMode); err != nil {
		return empty, fmt.Errorf("replace orange key group chain: %w", err)
	}
	s.orangeInvalidateAfterChainWrite(ctx, apiKey)
	return s.orangeReloadGroupChainView(ctx, apiKeyID)
}

// orangeCurrentChainGroupIDs 返回服务端当前链的完整成员 ID：链首在前，随后是全部候选（按 priority）。
//
// 刻意读库而不是读缓存：删除保护必须基于库里的真实集合（进程内缓存 TTL 45s，用它可能漏掉
// 刚写入的成员，从而让一次"静默裁剪"漏过确认）。读库失败时退回进程内缓存并记日志 ——
// 宁可少拦一次，也不要因为一次读失败让用户完全无法保存。
func (s *APIKeyService) orangeCurrentChainGroupIDs(ctx context.Context, repo orangeGroupChainRepository, apiKey *APIKey) []int64 {
	if s == nil || apiKey == nil || apiKey.ID <= 0 {
		return nil
	}
	ids := make([]int64, 0, 4)
	if apiKey.GroupID != nil && *apiKey.GroupID > 0 {
		ids = append(ids, *apiKey.GroupID)
	}
	var members []OrangeGroupChainMember
	loadFailed := false
	if repo != nil {
		loaded, err := repo.OrangeLoadGroupChains(ctx, []int64{apiKey.ID})
		if err != nil {
			loadFailed = true
			slog.Warn("orange_key_group_chain_current_load_failed",
				"api_key_id", apiKey.ID, "error", err)
		} else {
			members = loaded[apiKey.ID]
		}
	}
	if loadFailed {
		if entry, ok := s.orangeGroupChainEntry(ctx, apiKey.ID); ok {
			members = entry.members
		}
	}
	for _, member := range members {
		if member.GroupID > 0 {
			ids = append(ids, member.GroupID)
		}
	}
	return orangeDedupeGroupIDs(ids)
}

// orangeChainRemovedGroupIDs 返回 expected 中「不在 kept 里」的分组 ID（保持 expected 顺序）。
//
// expected 为客户端看到的当前成员集合；客户端未提供时调用方传入服务端当前集合
// （契约 C3：旧客户端同样必须经确认才能裁剪）。清链（kept 为空）时主分组本身不会被删除
// （api_keys.group_id 保留），因此不计入移除，避免「删除兜底/聚合链」这种正常操作被要求
// 二次确认；但链上的候选确实会被删掉，仍会触发 409。
func orangeChainRemovedGroupIDs(expected, kept []int64, primaryGroupID *int64) []int64 {
	keptSet := make(map[int64]struct{}, len(kept))
	for _, groupID := range kept {
		keptSet[groupID] = struct{}{}
	}
	clearing := len(kept) == 0
	removed := make([]int64, 0, len(expected))
	for _, groupID := range orangeDedupeGroupIDs(expected) {
		if _, ok := keptSet[groupID]; ok {
			continue
		}
		if clearing && primaryGroupID != nil && *primaryGroupID == groupID {
			continue
		}
		removed = append(removed, groupID)
	}
	return removed
}

// orangeResolvePrunedChainGroups 尽力解析被移除分组的名称（供 409 提示显示分组名而不是 #id）：
// 优先本次读到的链成员与主分组（内存对象），缺失时回查 groupRepo，最后退化为 "#id"。
func (s *APIKeyService) orangeResolvePrunedChainGroups(ctx context.Context, apiKey *APIKey, groupIDs []int64) []OrangePrunedChainGroup {
	known := make(map[int64]*Group, len(groupIDs)+2)
	if apiKey != nil && apiKey.Group != nil && apiKey.Group.ID > 0 {
		known[apiKey.Group.ID] = apiKey.Group
	}
	if s != nil && apiKey != nil && apiKey.ID > 0 {
		if entry, ok := s.orangeGroupChainEntry(ctx, apiKey.ID); ok {
			for _, group := range entry.groups {
				if group != nil && group.ID > 0 {
					known[group.ID] = group
				}
			}
		}
	}
	out := make([]OrangePrunedChainGroup, 0, len(groupIDs))
	for _, groupID := range groupIDs {
		group := known[groupID]
		if group == nil && s != nil && s.groupRepo != nil {
			if loaded, err := s.groupRepo.GetByID(ctx, groupID); err == nil && loaded != nil {
				group = loaded
			}
		}
		out = append(out, OrangePrunedChainGroup{
			ID:   groupID,
			Name: orangeChainGroupDisplayName(group, groupID),
		})
	}
	return out
}

// orangeReloadGroupChainView 重新装载 Key 并返回**完整**链视图（写操作后返回最新状态）。
//
// 契约 E：链**已经写进数据库**，此时视图重建失败（典型：owner 重载失败）绝不能再回 5xx ——
// 否则用户会以为没保存成功、反复重试，而请求侧的链其实已经生效。失败时降级为
// 「不带 state 的基础视图」并记日志（见 orangeBuildBasicGroupChainView）。
func (s *APIKeyService) orangeReloadGroupChainView(ctx context.Context, apiKeyID int64) (OrangeAPIKeyGroupChainView, error) {
	InvalidateOrangeGroupChainCache(apiKeyID)
	apiKey, err := s.apiKeyRepo.GetByID(ctx, apiKeyID)
	if err != nil || apiKey == nil {
		// 连 Key 本体都读不到：写操作本身已成功，这里同样不能回 5xx（同上）。
		slog.Warn("orange_key_group_chain_view_reload_failed",
			"api_key_id", apiKeyID, "error", err)
		return OrangeAPIKeyGroupChainView{APIKeyID: apiKeyID, Mode: OrangeGroupChainModeNone}, nil
	}
	if _, err := s.orangeLoadChainOwner(ctx, apiKey); err != nil {
		slog.Warn("orange_key_group_chain_view_degraded",
			"api_key_id", apiKeyID, "reason", "owner_load_failed", "error", err)
		return s.orangeBuildBasicGroupChainView(ctx, apiKey), nil
	}
	return s.OrangeBuildAPIKeyGroupChainViewFull(ctx, apiKey), nil
}

// orangeBuildBasicGroupChainView 构建「不含 state」的基础链视图（写后降级路径专用，契约 E）。
//
// 成员集合与顺序仍与完整视图一致，只是不带可用性判定：拿不到 owner 时判权限会误报
// no_permission（专属分组被误判），不如让前端按"未知"展示。State 留空，由 dto 层
// 映射成默认值（见 dto.orangeChainMemberStateString），避免前端把整条链置灰。
func (s *APIKeyService) orangeBuildBasicGroupChainView(ctx context.Context, apiKey *APIKey) OrangeAPIKeyGroupChainView {
	view := OrangeAPIKeyGroupChainView{Mode: OrangeGroupChainModeNone}
	if s == nil || apiKey == nil {
		return view
	}
	view.APIKeyID = apiKey.ID
	if apiKey.GroupID != nil {
		view.PrimaryGroupID = *apiKey.GroupID
	}
	entry, _ := s.orangeGroupChainEntry(ctx, apiKey.ID)
	view.Mode = orangeResolveGroupChainMode(entry.mode, len(entry.members))

	members := make([]OrangeAPIKeyGroupChainMemberView, 0, len(entry.members)+1)
	head := apiKey.Group
	headID := int64(0)
	if head != nil && head.ID > 0 {
		headID = head.ID
		members = append(members, OrangeAPIKeyGroupChainMemberView{
			GroupID: head.ID, Priority: 1, Group: cloneOrangeGroup(head),
		})
	} else if view.PrimaryGroupID > 0 {
		headID = view.PrimaryGroupID
		members = append(members, OrangeAPIKeyGroupChainMemberView{GroupID: view.PrimaryGroupID, Priority: 1})
	}
	for i := range entry.members {
		var group *Group
		if i < len(entry.groups) {
			group = cloneOrangeGroup(entry.groups[i])
		}
		groupID := entry.members[i].GroupID
		if group != nil && group.ID > 0 {
			groupID = group.ID
		}
		if groupID <= 0 || groupID == headID {
			continue
		}
		members = append(members, OrangeAPIKeyGroupChainMemberView{
			GroupID: groupID, Priority: len(members) + 1, Group: group,
		})
	}
	view.Members = members
	return view
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
