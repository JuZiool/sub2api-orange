// Orange 特有：分组链的「上游故障换组」能力。
//
// 背景：既有的链推进（orange_api_key_group_chain.go 的 selectWithOrangeGroupChain /
// orangeNextChainGroup）只认「分组内无可用账号」类错误；上游 5xx / 超时 / 连接失败这类
// 账号级故障被 handler 的 failover 循环吃掉——A 组账号换完就报错，永远不会切到 B 组。
//
// 本文件补齐这一环，但只提供「纯查询 + 请求级激活」两个原语，不碰任何 handler 循环：
//  1. OrangeChainNextGroupAfterFailure：沿请求级链找当前分组之后、未尝试过、且可用的
//     下一组（纯查询，不改任何请求状态，便于单测）；
//  2. ActivateOrangeChainGroupForRequest：把请求级 apiKey.Group / GroupID / ctxkey.Group
//     切到该组，复用 activateOrangeKeyRouting（新 GroupID 指针 + 清 UserGroupRPMOverride），
//     因此限流、计费、用量归属自动跟随新分组。
//
// 切组判定口径（OrangeIsUpstreamFaultForGroupFailover）：只有「上游故障」才允许换组——
// 5xx（含 503/504/520..524）、408 上游超时、连接失败（统一建模为 502 的传输错误）；
// 确定性错误（400/401/403/404、内容策略、参数错误、额度/权限类含 402/429）一律不换组。
//
// 预算：每次请求最多切 (链长-1) 次组，且只向前推进（failedGroupIDs 记录已尝试分组，
// 不可能 A→B→A 抖动）；链走完仍失败时由调用方按原逻辑返回最后一次错误。
package service

import (
	"context"
	"errors"
	"net/http"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
)

// orangeChainCurrentGroupID 返回请求级路由当前实际命中的分组 ID。
//
// 优先读 routing.apiKey（请求内指针替换的唯一权威来源），拿不到时回退到调用方传入的
// apiKey：handler 可能在 fallback 分组分支里持有另一份 *APIKey 快照。
func orangeChainCurrentGroupID(ctx context.Context, apiKey *APIKey) int64 {
	if routing, ok := orangeKeyRoutingFromContext(ctx); ok && routing.apiKey != nil &&
		routing.apiKey.GroupID != nil && *routing.apiKey.GroupID > 0 {
		return *routing.apiKey.GroupID
	}
	if apiKey != nil && apiKey.GroupID != nil && *apiKey.GroupID > 0 {
		return *apiKey.GroupID
	}
	return 0
}

// OrangeChainCurrentGroupID 返回请求级路由当前实际命中的分组 ID。
//
// handler 侧用它登记「已尝试分组」：请求级路由才是切组/预选后的权威来源，handler 的
// 局部 *APIKey 在 fallback 分组分支里可能指向另一份快照。返回 0 表示无链或未安装路由。
func OrangeChainCurrentGroupID(ctx context.Context, apiKey *APIKey) int64 {
	return orangeChainCurrentGroupID(ctx, apiKey)
}

// OrangeChainNextGroupAfterFailure 返回「本分组 failover 耗尽后」应切换到的下一个分组。
//
// failedGroupIDs 是请求级「已尝试过分组」集合：命中的分组会被跳过，因此推进只向前、
// 不会回绕；它的大小同时充当切组预算（整链最多 (链长-1) 次）。
//
// 纯查询：不修改 apiKey / ctx / failedGroupIDs 的任何状态。返回 false 表示不切组，
// 调用方必须保持原有错误返回路径。以下情况一律不推进：
//   - 请求没有请求级分组路由（普通 Key / 未安装链）；
//   - 强制平台路由（/antigravity 等）——换组会把请求送进语义不同的入口；
//   - 当前分组不在链上、链上已无后续候选、切组预算已用尽；
//   - 候选分组已停用或上下文不完整。
func OrangeChainNextGroupAfterFailure(ctx context.Context, apiKey *APIKey, failedGroupIDs map[int64]struct{}) (*Group, bool) {
	if ctx == nil {
		return nil, false
	}
	if forcePlatform, _ := ctx.Value(ctxkey.ForcePlatform).(string); forcePlatform != "" {
		return nil, false
	}
	routing, ok := orangeKeyRoutingFromContext(ctx)
	if !ok || len(routing.chain) < 2 {
		return nil, false
	}
	currentGroupID := orangeChainCurrentGroupID(ctx, apiKey)
	if currentGroupID <= 0 {
		return nil, false
	}
	currentIndex := -1
	for i := range routing.chain {
		if routing.chain[i].Group != nil && routing.chain[i].Group.ID == currentGroupID {
			currentIndex = i
			break
		}
	}
	if currentIndex < 0 {
		return nil, false
	}
	// 预算：整链最多推进 (链长-1) 次。failedGroupIDs 只由切组助手登记「已经尝试过」的
	// 分组，因此它的长度就是已用预算。
	if len(failedGroupIDs) >= len(routing.chain)-1 {
		return nil, false
	}
	for i := currentIndex + 1; i < len(routing.chain); i++ {
		candidate := routing.chain[i].Group
		if candidate == nil || candidate.ID <= 0 || candidate.ID == currentGroupID {
			continue
		}
		if _, tried := failedGroupIDs[candidate.ID]; tried {
			continue
		}
		if !candidate.IsActive() || !IsGroupContextValid(candidate) {
			continue
		}
		return candidate, true
	}
	return nil, false
}

// ActivateOrangeChainGroupForRequest 把请求级路由切到 group，并复用 activateOrangeKeyRouting：
// apiKey.Group 指向链上的分组快照、GroupID 换成新指针（认证缓存里共享的指针禁止原地写）、
// ctxkey.Group 原地覆盖、清理按主分组算出的 UserGroupRPMOverride。
//
// apiKey 允许与 routing.apiKey 不是同一个指针（handler 可能在 fallback 分组分支里克隆过）：
// 两者会被同步到同一分组，保证下一次选号读到新 GroupID。
func ActivateOrangeChainGroupForRequest(ctx context.Context, apiKey *APIKey, group *Group) bool {
	if ctx == nil || group == nil || group.ID <= 0 {
		return false
	}
	routing, ok := orangeKeyRoutingFromContext(ctx)
	if !ok || len(routing.chain) < 2 {
		return false
	}
	routed := orangeChainGroupByID(routing.chain, group.ID)
	if routed == nil {
		return false
	}
	if !activateOrangeKeyRouting(ctx, routed.ID) {
		return false
	}
	if apiKey != nil && routing.apiKey != nil && apiKey != routing.apiKey {
		groupID := routed.ID
		apiKey.GroupID = &groupID
		apiKey.Group = routed
		orangeClearUserGroupRPMOverride(apiKey)
	}
	return true
}

// OrangeIsUpstreamFaultForGroupFailover 报告 failover 错误是否属于「上游故障」（允许切组）。
//
// 只认可重试的上游侧故障：
//   - 5xx（500/502/503/504/520..524/529 等，含连接失败——传输层故障统一建模成 502
//     UpstreamFailoverError，见 gateway_upstream_transport_error.go / openai 同名文件）；
//   - 408 请求超时（部分上游用 408 表达上游侧超时，见 openai_gateway_request_timeout）。
//
// 明确不切组（确定性错误，换分组不会让它成功，保持原行为直接返回）：
//   - 400/401/402/403/404/405/429 等 4xx（含额度/权限/限流类）；
//   - 上游显式要求停止换号的错误（NextAccountAction == NextAccountStop，凭据/配置类）；
//   - 非 UpstreamFailoverError（内容策略拦截、参数校验、本地策略拒绝等）。
func OrangeIsUpstreamFaultForGroupFailover(err error) bool {
	if err == nil {
		return false
	}
	var failoverErr *UpstreamFailoverError
	if !errors.As(err, &failoverErr) || failoverErr == nil {
		return false
	}
	if !failoverErr.ShouldRetryNextAccount() {
		return false
	}
	if failoverErr.StatusCode == http.StatusRequestTimeout {
		return true
	}
	return failoverErr.StatusCode >= http.StatusInternalServerError
}
