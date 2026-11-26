// Orange 特有：分组链成员的可用性状态（v0.2.25-8 契约）。
//
// 背景（缺陷：密钥列表显示 5 个分组，编辑弹窗只剩 3 个）：
// 旧实现在「读视图」里把不可用成员静默丢弃，且把丢弃结果写回数据库。于是
//   - 用户看不到自己配过的分组（永久隐藏，且没有兜底手段写回去）；
//   - 前端只要少提交一个成员，PUT 全量替换就会把该成员永久删掉。
//
// 新契约：读接口一律返回链上**全部**成员，每个成员带一个 state 说明它为什么不能被路由到；
// 界面据此置灰/提示，用户始终看得到完整配置；写库只发生在「用户确认的保存」与显式 heal 接口。
//
// 判定优先级（与改造前的过滤顺序一致，命中即返回）：
//
//	missing → disabled → no_permission → scope_mismatch → available
//
// 即：分组读不到时先报 missing（再说停用没有意义）；停用优先于权限（用户先关心分组是否能用）；
// 权限优先于平台/订阅类型不一致（先告诉用户"你没有这个分组"，而不是"类型不匹配"）。
package service

import (
	"fmt"
	"strings"
)

// OrangeChainMemberState 是链成员在「当前用户权限 + 当前分组配置」下的可用性状态。
//
// 取值是对外 JSON 契约的一部分（前端与 handler DTO 依赖这些字符串），禁止随意改名。
type OrangeChainMemberState string

const (
	// OrangeChainMemberStateAvailable 分组可用：请求会被路由到它。
	OrangeChainMemberStateAvailable OrangeChainMemberState = "available"
	// OrangeChainMemberStateDisabled 分组已停用，或分组上下文不完整（缺平台/状态等路由必需字段）。
	OrangeChainMemberStateDisabled OrangeChainMemberState = "disabled"
	// OrangeChainMemberStateScopeMismatch 分组平台或订阅类型与链首不一致：跨平台会被错误的网关调度，
	// 跨订阅类型会绕过订阅限额校验，因此不能进路由链。
	OrangeChainMemberStateScopeMismatch OrangeChainMemberState = "scope_mismatch"
	// OrangeChainMemberStateNoPermission 用户已无该分组的绑定权限（专属未授权 / 公开分组限制下未授权）。
	OrangeChainMemberStateNoPermission OrangeChainMemberState = "no_permission"
	// OrangeChainMemberStateMissing 分组已被删除或读不到。
	OrangeChainMemberStateMissing OrangeChainMemberState = "missing"
)

// orangeChainMemberStateOf 是链成员状态的**唯一判定入口**（读视图、显式自愈、网关过滤都走它）。
//
// head 是链首（= api_keys.group_id 对应的分组），只用于平台/订阅类型一致性比较；
// group 是待判定的成员（判定链首自身时传 head 本身）。owner 为 nil 时不判权限，
// 与 orangeGroupBindableForUser 保持同一口径：拿不到用户快照时宁可放行，避免把正常链路误判成越权。
func orangeChainMemberStateOf(head, group *Group, owner *User) OrangeChainMemberState {
	if group == nil || group.ID <= 0 {
		return OrangeChainMemberStateMissing
	}
	if !group.IsActive() || !IsGroupContextValid(group) {
		return OrangeChainMemberStateDisabled
	}
	if !orangeGroupBindableForUser(owner, group) {
		return OrangeChainMemberStateNoPermission
	}
	// 链首自身不做 scope 比较：它自己就是比较基准。
	if head != nil && head.ID > 0 && head.ID != group.ID &&
		(head.Platform != group.Platform || head.SubscriptionType != group.SubscriptionType) {
		return OrangeChainMemberStateScopeMismatch
	}
	return OrangeChainMemberStateAvailable
}

// orangeChainMemberStateUsable 报告该状态的分组本次请求是否可以被路由（网关/选号只认 available）。
func orangeChainMemberStateUsable(state OrangeChainMemberState) bool {
	return state == OrangeChainMemberStateAvailable
}

// orangeChainMemberStatePrunable 报告该状态是否允许被**显式自愈（heal）**修剪。
//
// 只修剪 no_permission 与 missing：
//   - disabled：分组可能只是临时停用（充值/维护后恢复），删掉等于永久丢失用户配置；
//   - scope_mismatch：用户可能正在改平台/订阅类型配置（改完就一致了），同样不能删。
//
// 这条口径同时是既有单测 TestOrangeChainSelfHealPlanKeepsInactiveEntitledMemberInTail 的语义。
func orangeChainMemberStatePrunable(state OrangeChainMemberState) bool {
	return state == OrangeChainMemberStateNoPermission || state == OrangeChainMemberStateMissing
}

// orangeChainGroupDisplayName 给出分组在提示文案里的名字；分组已被删除时退化为 "#id"。
func orangeChainGroupDisplayName(group *Group, groupID int64) string {
	if group != nil {
		if name := strings.TrimSpace(group.Name); name != "" {
			return name
		}
	}
	if groupID <= 0 {
		return ""
	}
	return fmt.Sprintf("#%d", groupID)
}
