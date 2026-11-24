// Orange 特有：API Key 分组优先级链（密钥级兜底分组 / 聚合分组）的响应 DTO 与映射函数。
//
// 契约见 文档/功能分析/2026-11-22-密钥分组兜底与聚合分组恢复分析.md §5.6：
//
//	GET  /api/v1/keys/:id/group-chain  → { api_key_id, mode, primary_group_id, members[] }
//	PUT  /api/v1/keys/:id/group-chain  → 同上（写入后返回最新链）
//
// 另外给上游 dto.APIKey 增加 groups 字段（链列表展示用）。
package dto

import "github.com/Wei-Shaw/sub2api/internal/service"

// APIKeyGroupEntry 是 dto.APIKey.Groups 的元素：链上一项（含链首）。
type APIKeyGroupEntry struct {
	GroupID  int64  `json:"group_id"`
	Priority int    `json:"priority"`
	Group    *Group `json:"group,omitempty"`
}

// OrangeAPIKeyGroupChainMember 是链详情接口 members[] 的元素。
//
// Available / UnavailableReason 由服务层与"请求时链成员过滤"同源判定：分组被改成专属、
// 或撤销授权后，成员仍留在链上但 Available=false，前端据此提示并让用户自愈（保存时剔除）。
type OrangeAPIKeyGroupChainMember struct {
	Priority          int    `json:"priority"`
	GroupID           int64  `json:"group_id"`
	Group             *Group `json:"group,omitempty"`
	Available         bool   `json:"available"`
	UnavailableReason string `json:"unavailable_reason,omitempty"`
}

// OrangeAPIKeyGroupChain 是 GET/PUT /keys/:id/group-chain 的响应体。
type OrangeAPIKeyGroupChain struct {
	APIKeyID       int64                          `json:"api_key_id"`
	Mode           string                         `json:"mode"` // none | fallback | aggregate
	PrimaryGroupID int64                          `json:"primary_group_id"`
	Members        []OrangeAPIKeyGroupChainMember `json:"members"`
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
		APIKeyID:       view.APIKeyID,
		Mode:           view.Mode,
		PrimaryGroupID: view.PrimaryGroupID,
		Members:        make([]OrangeAPIKeyGroupChainMember, 0, len(view.Members)),
	}
	for _, member := range view.Members {
		out.Members = append(out.Members, OrangeAPIKeyGroupChainMember{
			Priority:          member.Priority,
			GroupID:           member.GroupID,
			Group:             GroupFromServiceShallow(member.Group),
			Available:         member.Available,
			UnavailableReason: member.UnavailableReason,
		})
	}
	return out
}

// apiKeyGroupEntriesFromService 把服务层链（含链首）映射成 dto.APIKey.Groups。
// 普通 Key（未配置链）返回空数组，前端可据此回退到上游单分组展示。
func apiKeyGroupEntriesFromService(entries []*service.APIKeyGroupEntry) []APIKeyGroupEntry {
	if len(entries) == 0 {
		return []APIKeyGroupEntry{}
	}
	out := make([]APIKeyGroupEntry, 0, len(entries))
	for _, entry := range entries {
		if entry == nil || entry.Group == nil || entry.Group.ID <= 0 {
			continue
		}
		out = append(out, APIKeyGroupEntry{
			GroupID:  entry.Group.ID,
			Priority: entry.Priority,
			Group:    GroupFromServiceShallow(entry.Group),
		})
	}
	return out
}
