// Orange 特有：网关 failover 耗尽后的「分组链推进」（密钥级兜底 / 聚合密钥）。
//
// 语义：普通 Key（无链）完全走上游原有路径——本文件的助手在无链时恒返回 false，
// 调用方立刻回落到原来的错误返回点。只有配置了分组链的 Key，在
// 「本组账号已经换完（failover 耗尽）」且「最后一次错误属于上游故障」时，才把请求切到
// 链上的下一个分组、重置请求内 failover 状态，然后让外层/当前重试循环在新分组里重新选号。
//
// 三条红线（与需求逐条对应）：
//  1. 只有上游故障（5xx / 上游超时 / 连接失败等可重试类）才切组，判定见
//     service.OrangeIsUpstreamFaultForGroupFailover；确定性错误（400/401/403/404、
//     内容策略、参数错误、额度/权限类）保持原行为直接返回；
//  2. 流式内容已经写出语义字节时不切组：所有调用点都在「本次尝试未写出语义字节」的
//     failover 分支内（写出字节的分支已提前 return），因此无需在此重复判断；
//  3. 每次请求最多切 (链长-1) 次，且只向前推进；已尝试分组记录在请求级
//     failedGroupIDs 里（每个请求 new 一份，跨外层重试循环复用），不会 A→B→A 抖动。
package handler

import (
	"context"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// orangeChainFailoverSwitch 在 failover 耗尽时把请求切到分组链上的下一个分组。
//
// failedGroupIDs 是请求级「已尝试过分组」集合，既用于跳过重复分组，也充当切组预算。
// 返回新的请求级 APIKey（调用方必须用它替换自己的局部 apiKey）与是否切换成功；
// 返回 false 时调用方必须保持原有错误返回路径：请求级分组不会被改写（唯一例外是
// 极少数「已登记已尝试、激活却失败」的情况，此时同样不会切组，只是该分组不会被重试）。
func orangeChainFailoverSwitch(
	c *gin.Context,
	currentAPIKey *service.APIKey,
	failedGroupIDs map[int64]struct{},
) (*service.APIKey, bool) {
	if c == nil || c.Request == nil || currentAPIKey == nil || currentAPIKey.GroupID == nil || failedGroupIDs == nil {
		return nil, false
	}
	ctx := c.Request.Context()
	next, ok := service.OrangeChainNextGroupAfterFailure(ctx, currentAPIKey, failedGroupIDs)
	if !ok || next == nil {
		return nil, false
	}
	currentGroupID := service.OrangeChainCurrentGroupID(ctx, currentAPIKey)
	if currentGroupID <= 0 || currentGroupID == next.ID {
		return nil, false
	}
	// 先登记本分组「已尝试」再激活：即使激活失败，也不会再次尝试同一分组。
	failedGroupIDs[currentGroupID] = struct{}{}
	if !service.ActivateOrangeChainGroupForRequest(ctx, currentAPIKey, next) {
		return nil, false
	}
	logger.FromContext(ctx).Warn("orange.chain_group_failover",
		zap.Int64("api_key_id", currentAPIKey.ID),
		zap.Int64("from_group_id", currentGroupID),
		zap.Int64("to_group_id", next.ID),
		zap.Int("attempted_group_count", len(failedGroupIDs)),
	)
	return currentAPIKey, true
}

// orangeUpstreamFaultAllowsGroupFailover 报告 failover 耗尽时的最后一次错误是否属于
// 「上游故障」（5xx / 上游超时 / 连接失败等可重试类），只有它允许切组。
// 传入 nil（选号失败但没有上游错误）时返回 false，保持原有「无可用账号」路径。
func orangeUpstreamFaultAllowsGroupFailover(lastFailoverErr *service.UpstreamFailoverError) bool {
	return service.OrangeIsUpstreamFaultForGroupFailover(lastFailoverErr)
}

// orangeGroupChainFailoverState 是「本组换号耗尽后切到分组链下一组」的请求级状态。
//
// failedGroupIDs 记录本次请求已经尝试过的分组：既用于跳过重复分组，也充当
// 「最多切 (链长-1) 次」的预算。普通 Key（无链）时它恒为空，所有切组尝试都返回 false，
// 调用方随即回落到上游原有的错误返回路径——行为与上游完全一致。
type orangeGroupChainFailoverState struct {
	failedGroupIDs map[int64]struct{}
	// openAIReset 是 OpenAI 网关换号循环里切组后需要清零的请求级计数；
	// Claude/Gemini 网关（failover 状态由 *FailoverState 承载）为 nil。
	openAIReset *orangeOpenAIFailoverReset
}

// newOrangeGroupChainFailoverState 创建请求级切组状态（每个请求一份）。
func newOrangeGroupChainFailoverState(openAIReset *orangeOpenAIFailoverReset) *orangeGroupChainFailoverState {
	return &orangeGroupChainFailoverState{
		failedGroupIDs: make(map[int64]struct{}),
		openAIReset:    openAIReset,
	}
}

// switchGatewayFailoverState 是 Claude/Gemini 网关「换号耗尽」插入点的实现：
// 只有「上游故障 + 链上还有下一组」才切组，并把 apiKey / FailoverState 换成新分组与
// 新预算（口径与原代码一致：gemini 走 gemini 预算）。返回 false 表示不切组，
// 调用方立刻回落到上游原有的错误返回路径。
func (s *orangeGroupChainFailoverState) switchGatewayFailoverState(h *GatewayHandler, c *gin.Context, lastErr *service.UpstreamFailoverError, apiKey **service.APIKey, failoverState **FailoverState, isGemini bool, hasBoundSession bool, refresh bool) bool {
	if s == nil || h == nil || apiKey == nil || *apiKey == nil || failoverState == nil {
		return false
	}
	if !orangeUpstreamFaultAllowsGroupFailover(lastErr) {
		return false
	}
	next, switched := orangeChainFailoverSwitch(c, *apiKey, s.failedGroupIDs)
	if !switched {
		return false
	}
	*apiKey = next
	if refresh {
		h.orangeRefreshChainGroupRequestContext(c, next)
	}
	if isGemini {
		*failoverState = NewFailoverState(h.maxAccountSwitchesGemini, hasBoundSession)
		return true
	}
	*failoverState = NewFailoverState(h.maxAccountSwitches, hasBoundSession)
	return true
}

// switchGatewayMessagesFailover 是 Messages 主分支「换号耗尽」插入点的实现：
// 切组 + 刷新请求级分组上下文 + 释放上一分组已注册的会话槽（新分组成功时
// Messages 的 defer 不会释放它们，见 orangeReleaseChainGroupSessions）。
func (s *orangeGroupChainFailoverState) switchGatewayMessagesFailover(h *GatewayHandler, c *gin.Context, lastErr *service.UpstreamFailoverError, currentAPIKey **service.APIKey, failoverState **FailoverState, sessionSlotAccounts *map[int64]*service.Account, sessionKey string, hasBoundSession bool) bool {
	if s == nil || h == nil || currentAPIKey == nil || *currentAPIKey == nil || failoverState == nil {
		return false
	}
	if !orangeUpstreamFaultAllowsGroupFailover(lastErr) {
		return false
	}
	next, switched := orangeChainFailoverSwitch(c, *currentAPIKey, s.failedGroupIDs)
	if !switched {
		return false
	}
	*currentAPIKey = next
	h.orangeRefreshChainGroupRequestContext(c, next)
	if sessionSlotAccounts != nil {
		*sessionSlotAccounts = h.orangeReleaseChainGroupSessions(*sessionSlotAccounts, sessionKey)
	}
	*failoverState = NewFailoverState(h.maxAccountSwitches, hasBoundSession)
	return true
}

// switchOpenAIFailover 是 OpenAI 网关（chat completions / responses / messages）插入点的实现：
// 切组成功后按 openAIReset 清零换号计数，让新分组从零开始选号。
func (s *orangeGroupChainFailoverState) switchOpenAIFailover(c *gin.Context, lastErr *service.UpstreamFailoverError, apiKey **service.APIKey) bool {
	if s == nil || apiKey == nil || *apiKey == nil {
		return false
	}
	if !orangeUpstreamFaultAllowsGroupFailover(lastErr) {
		return false
	}
	next, switched := orangeChainFailoverSwitch(c, *apiKey, s.failedGroupIDs)
	if !switched {
		return false
	}
	*apiKey = next
	if s.openAIReset != nil {
		s.openAIReset.reset()
	}
	return true
}

// orangeOpenAIFailoverReset 保存 OpenAI 网关换号循环里需要在切组后清零的请求级计数。
//
// 只处理非 nil 字段，因此 Responses / Messages / ChatCompletions 三个入口可以共用同一份
// 清理逻辑；字段集合与原实现逐个赋值完全一致。
type orangeOpenAIFailoverReset struct {
	SwitchCount      *int
	FirstOutputCount *int
	ProfitVetoCount  *int
	FailedAccountIDs *map[int64]struct{}
	SameAccountRetry *map[int64]int
	LastFailoverErr  **service.UpstreamFailoverError
	OAuth429State    *service.OpenAIOAuth429FailoverState
}

// reset 把绑定的计数逐项清零（新分组从零开始选号）。
func (r *orangeOpenAIFailoverReset) reset() {
	if r == nil {
		return
	}
	if r.SwitchCount != nil {
		*r.SwitchCount = 0
	}
	if r.FirstOutputCount != nil {
		*r.FirstOutputCount = 0
	}
	if r.ProfitVetoCount != nil {
		*r.ProfitVetoCount = 0
	}
	if r.FailedAccountIDs != nil {
		*r.FailedAccountIDs = make(map[int64]struct{})
	}
	if r.SameAccountRetry != nil {
		*r.SameAccountRetry = make(map[int64]int)
	}
	if r.LastFailoverErr != nil {
		*r.LastFailoverErr = nil
	}
	if r.OAuth429State != nil {
		*r.OAuth429State = service.OpenAIOAuth429FailoverState{}
	}
}

// orangeRefreshChainGroupRequestContext 在切组成功后刷新请求级上下文：
// 清掉上一分组残留的「单账号退避重试」标记，并按新分组重挂「是否单账号 antigravity
// 分组」的延迟判定（网关服务按分组 ID 在首次读取时求值，沿用旧分组会得出错误结论）。
func (h *GatewayHandler) orangeRefreshChainGroupRequestContext(c *gin.Context, apiKey *service.APIKey) {
	if c == nil || c.Request == nil || apiKey == nil {
		return
	}
	ctx := service.WithSingleAccountRetry(c.Request.Context(), false, h.metadataBridgeEnabled())
	if h.gatewayService != nil {
		ctx = h.gatewayService.WithSingleAntigravityAccountGroupProbe(ctx, apiKey.GroupID)
	}
	c.Request = c.Request.WithContext(ctx)
}

// orangeReleaseChainGroupSessions 在切组前释放上一分组已注册的会话槽。
//
// 上一分组的账号在本次请求里已经确定性失败（failover 耗尽），但它们注册的会话（
// checkAndRegisterSession）仍然占着 max_sessions 配额；请求最终在新分组成功时
// Messages 的 defer 不会释放它们（upstreamServedSession=true），必须在这里主动释放，
// 否则失败请求会把会话槽卡满整个空闲窗口。返回空 map 供调用方重置记账。
func (h *GatewayHandler) orangeReleaseChainGroupSessions(
	sessionSlotAccounts map[int64]*service.Account,
	sessionKey string,
) map[int64]*service.Account {
	if h.gatewayService != nil {
		for _, account := range sessionSlotAccounts {
			h.gatewayService.ReleaseAccountSession(context.Background(), account, sessionKey)
		}
	}
	return make(map[int64]*service.Account)
}
