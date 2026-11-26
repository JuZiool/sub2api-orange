// Orange 特有：API Key 分组优先级链（密钥级兜底分组 / 聚合分组）的响应 DTO 与映射函数。
//
// 契约见 文档/功能分析/2026-11-22-密钥分组兜底与聚合分组恢复分析.md §5.6（v0.2.25-8 修订）：
//
//	GET  /api/v1/keys/:id/group-chain       → { api_key_id, mode, primary_group_id, members[] }
//	PUT  /api/v1/keys/:id/group-chain       → 同上（写入后返回最新链）
//	POST /api/v1/keys/:id/group-chain/heal  → 同上（含 auto_pruned_*）
//
// 链上**全部**成员都会返回，每个成员带 state：
// available / disabled / scope_mismatch / no_permission / missing。
//
// 另外给上游 dto.APIKey 增加 groups 字段（链列表展示用）：元素同样带 state，
// 且与链详情一样返回**全部**成员（含 state=missing，此时只有 group_id/priority/state）。
package dto

import "github.com/Wei-Shaw/sub2api/internal/service"

// APIKeyGroupEntry 是 dto.APIKey.Groups 的元素：链上一项（含链首）。
type APIKeyGroupEntry struct {
	GroupID  int64 `json:"group_id"`
	Priority int   `json:"priority"`
	// State 是成员可用性状态（与链详情口径一致）：请求只会被路由到 available 成员。
	// Orange 特有：omitempty 只服务于「写后重建视图降级」场景（owner 读不到时 State 为空），
	// 正常路径恒有值；缺省时前端按 available 处理。
	State string `json:"state,omitempty"`
	// Group 在分组已删除（state=missing）时为空，此时只有 group_id/priority/state 三个字段。
	Group *Group `json:"group,omitempty"`
}

// OrangeAPIKeyGroupChainMember 是链详情接口 members[] 的元素。
//
// v0.2.25-8 起包含链上**全部**成员（链首 + 全部候选），是否可用由 State 表达：
// available / disabled / scope_mismatch / no_permission / missing。
// 前端据此置灰/提示，而不是像旧实现那样"直接不显示"（那会让用户看不到也写不回自己的配置）。
type OrangeAPIKeyGroupChainMember struct {
	Priority int    `json:"priority"`
	GroupID  int64  `json:"group_id"`
	// Orange 特有：omitempty 只服务于「写后重建视图降级」场景（State 为空表示状态未知，
	// 前端按 available 处理），正常路径恒有值。
	State string `json:"state,omitempty"`
	// Group 在分组已删除/读不到（state=missing）时为空，此时只有 group_id 可供提示。
	Group *Group `json:"group,omitempty"`
}

// OrangePrunedChainGroup 是链自愈时被移除的分组（只用于提示，不参与任何判定）。
type OrangePrunedChainGroup struct {
	GroupID int64  `json:"group_id"`
	Name    string `json:"name"`
}

// orangeChainMemberStateString 把 service 层的状态映射成对外字符串。
// 零值只出现在「写链成功后 owner 重载失败」的降级视图里，按空串输出（配合 omitempty 省略该字段），
// 由前端按 available 兜底；正常路径恒有值。
func orangeChainMemberStateString(state service.OrangeChainMemberState) string {
	return string(state)
}

// OrangeAPIKeyGroupChain 是 GET/PUT /keys/:id/group-chain 的响应体。
type OrangeAPIKeyGroupChain struct {
	APIKeyID       int64                          `json:"api_key_id"`
	Mode           string                         `json:"mode"` // none | fallback | aggregate
	PrimaryGroupID int64                          `json:"primary_group_id"`
	Members        []OrangeAPIKeyGroupChainMember `json:"members"`
	// AutoPrunedGroupIDs 是本次读取时自动移除的分组（已失去绑定权限），前端据此提示"已自动更新密钥"。
	AutoPrunedGroupIDs []int64 `json:"auto_pruned_group_ids"`
	// AutoPrunedGroups 是同一批分组（含名称），供提示里显示分组名而不是 #id。
	AutoPrunedGroups []OrangePrunedChainGroup `json:"auto_pruned_groups"`
	// AutoPromotedGroupID 是链首被自动替换后的新主分组（0 表示未变）。
	AutoPromotedGroupID int64 `json:"auto_promoted_group_id"`
}

// Localized 就地本地化链上各分组的名称与描述并返回自身。
func (c *OrangeAPIKeyGroupChain) Localized(acceptLanguage string) *OrangeAPIKeyGroupChain {
	if c == nil {
		return nil
	}
	for i := range c.Members {
		c.Members[i].Group.Localized(acceptLanguage)
	}
	return c
}

// OrangeAPIKeyGroupChainFromService 把服务层链视图映射成响应 DTO。
func OrangeAPIKeyGroupChainFromService(view service.OrangeAPIKeyGroupChainView) *OrangeAPIKeyGroupChain {
	out := &OrangeAPIKeyGroupChain{
		APIKeyID:            view.APIKeyID,
		Mode:                view.Mode,
		PrimaryGroupID:      view.PrimaryGroupID,
		Members:             make([]OrangeAPIKeyGroupChainMember, 0, len(view.Members)),
		AutoPrunedGroupIDs:  view.AutoPrunedGroupIDs,
		AutoPrunedGroups:    make([]OrangePrunedChainGroup, 0, len(view.AutoPrunedGroups)),
		AutoPromotedGroupID: view.AutoPromotedGroupID,
	}
	if out.AutoPrunedGroupIDs == nil {
		out.AutoPrunedGroupIDs = []int64{}
	}
	for _, pruned := range view.AutoPrunedGroups {
		out.AutoPrunedGroups = append(out.AutoPrunedGroups, OrangePrunedChainGroup{
			GroupID: pruned.ID,
			Name:    pruned.Name,
		})
	}
	for _, member := range view.Members {
		out.Members = append(out.Members, OrangeAPIKeyGroupChainMember{
			Priority: member.Priority,
			GroupID:  member.GroupID,
			State:    orangeChainMemberStateString(member.State),
			Group:    GroupFromServiceShallow(member.Group),
		})
	}
	return out
}

// apiKeyGroupEntriesFromService 把服务层链（含链首）映射成 dto.APIKey.Groups。
// 普通 Key（未配置链）返回空数组，前端可据此回退到上游单分组展示。
//
// 契约 B（v0.2.25-8）：列表与链详情成员集合**完全一致** —— 分组已被删除的成员
// （state=missing，没有 *Group 详情）同样输出一行 { group_id, priority, state }，
// 只有 group 字段为空（本 DTO 的 Group 是 omitempty）。否则前端会以为"列表比弹窗多/少几行"，
// 进而在保存时静默裁掉用户配置。
func apiKeyGroupEntriesFromService(entries []*service.APIKeyGroupEntry) []APIKeyGroupEntry {
	if len(entries) == 0 {
		return []APIKeyGroupEntry{}
	}
	out := make([]APIKeyGroupEntry, 0, len(entries))
	for _, entry := range entries {
		if entry == nil {
			continue
		}
		groupID := entry.GroupID
		if entry.Group != nil && entry.Group.ID > 0 {
			groupID = entry.Group.ID
		}
		if groupID <= 0 {
			continue
		}
		out = append(out, APIKeyGroupEntry{
			GroupID:  groupID,
			Priority: entry.Priority,
			State:    orangeChainMemberStateString(entry.State),
			Group:    GroupFromServiceShallow(entry.Group),
		})
	}
	return out
}
