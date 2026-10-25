package service

import (
	"context"
	"errors"
	"log/slog"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
)

type apiKeyGroupFallbackContextKey struct{}
type apiKeyGroupFallbackAttemptContextKey struct{}

// apiKeyGroupChainEntry 是聚合密钥链上的一项。
type apiKeyGroupChainEntry struct {
	Group    *Group
	Priority int
}

// apiKeyGroupFallbackRouting 是请求内的分组路由链。
//
// 普通密钥（无 chain）等价于长度 1 的链；单兜底密钥（fallback_group_id）
// 等价于长度 2 的链；聚合密钥为 N 级。所有情形共用同一套「按当前 groupID
// 定位、取下一项」的推进逻辑，因此旧行为完全兼容。
type apiKeyGroupFallbackRouting struct {
	primaryGroupID int64
	chain          []apiKeyGroupChainEntry
	apiKey         *APIKey
}

// apiKeyRoutingChain 返回该 API Key 的分组优先级链（含主分组，按 priority 升序）。
// 未配置聚合链时回退到「主分组 + 可选单兜底」，保持既有语义。
func apiKeyRoutingChain(apiKey *APIKey) []apiKeyGroupChainEntry {
	if apiKey == nil || apiKey.GroupID == nil || *apiKey.GroupID <= 0 {
		return nil
	}
	if len(apiKey.Groups) > 0 {
		chain := make([]apiKeyGroupChainEntry, 0, len(apiKey.Groups))
		for _, entry := range apiKey.Groups {
			if entry == nil || entry.Group == nil || entry.Group.ID <= 0 {
				continue
			}
			chain = append(chain, apiKeyGroupChainEntry{Group: entry.Group, Priority: entry.Priority})
		}
		if len(chain) > 0 {
			return chain
		}
	}
	if apiKey.Group == nil {
		return nil
	}
	chain := []apiKeyGroupChainEntry{{Group: apiKey.Group, Priority: 1}}
	if apiKey.FallbackGroup != nil && apiKey.FallbackGroup.ID > 0 && apiKey.FallbackGroup.ID != apiKey.Group.ID {
		chain = append(chain, apiKeyGroupChainEntry{Group: apiKey.FallbackGroup, Priority: 2})
	}
	return chain
}

// WithAPIKeyGroupFallbackRouting installs API-key-level group routing.
// Once a later group in the chain is selected, the request-local API key is
// switched to that group so downstream billing and usage attribution follow
// the actual route.
func WithAPIKeyGroupFallbackRouting(ctx context.Context, apiKey *APIKey) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if apiKey == nil || apiKey.GroupID == nil || *apiKey.GroupID <= 0 {
		return ctx
	}
	chain := apiKeyRoutingChain(apiKey)
	if len(chain) < 2 {
		return ctx
	}

	// 链上首项必须就是 Key 当前的主分组，否则说明链已失效（例如主分组刚被
	// 移除但快照尚未刷新），此时不做任何请求级改写，按普通密钥处理。
	if chain[0].Group.ID != *apiKey.GroupID || !chain[0].Group.IsActive() || !IsGroupContextValid(chain[0].Group) {
		return ctx
	}
	// 平台必须一致：网关的分发与协议转换在中间件阶段已按主分组平台确定，
	// 跨平台链会在错误的网关上被调度。校验阶段已拦截，这里防御性跳过。
	for _, entry := range chain[1:] {
		if entry.Group == nil || !entry.Group.IsActive() || !IsGroupContextValid(entry.Group) ||
			entry.Group.Platform != chain[0].Group.Platform {
			return ctx
		}
	}

	return context.WithValue(ctx, apiKeyGroupFallbackContextKey{}, apiKeyGroupFallbackRouting{
		primaryGroupID: chain[0].Group.ID,
		chain:          chain,
		apiKey:         apiKey,
	})
}

func activateAPIKeyFallbackRouting(ctx context.Context, routedGroupID int64) bool {
	if ctx == nil || routedGroupID <= 0 {
		return false
	}
	routing, ok := ctx.Value(apiKeyGroupFallbackContextKey{}).(apiKeyGroupFallbackRouting)
	if !ok || routing.apiKey == nil || len(routing.chain) == 0 {
		return false
	}
	routed := apiKeyChainGroup(routing.chain, routedGroupID)
	if routed == nil {
		return false
	}

	// APIKeyService materializes a fresh APIKey for every authenticated request,
	// so changing these pointers is request-local and cannot pollute the auth cache.
	// Mutate the group object already held in the request context as well. This
	// makes group-level RPM/concurrency and downstream context lookups observe the
	// same effective group without requiring every handler to replace its context.
	if contextGroup, ok := ctx.Value(ctxkey.Group).(*Group); ok && contextGroup != nil {
		*contextGroup = *routed
	}
	// Keep the original GroupID pointer. Parsed request structures and other
	// request-local objects may already reference it; replacing the pointer would
	// leave them observing the primary group after fallback activation.
	if routing.apiKey.GroupID != nil {
		*routing.apiKey.GroupID = routedGroupID
	}
	// Keep the canonical routed object on the API key. Besides making pointer
	// identity predictable for callers, this avoids retaining a partially hydrated
	// primary-group object for pricing and subscription decisions.
	routing.apiKey.Group = routed

	// The auth snapshot caches the primary (user, group) RPM override. It must not
	// leak into fallback billing; nil makes the billing service resolve the override
	// for the routed group from its own cache/repository.
	if routing.apiKey.User != nil && routing.apiKey.User.UserGroupRPMOverride != nil {
		userCopy := *routing.apiKey.User
		userCopy.UserGroupRPMOverride = nil
		routing.apiKey.User = &userCopy
	}
	return true
}

func apiKeyChainGroup(chain []apiKeyGroupChainEntry, groupID int64) *Group {
	for _, entry := range chain {
		if entry.Group != nil && entry.Group.ID == groupID {
			return entry.Group
		}
	}
	return nil
}

// ActivateAPIKeyFallbackRouting activates the request-local fallback route and
// reports whether the supplied group belongs to this request's routing chain.
// Handlers that resume an asynchronous operation can use the result
// to decide whether to restore the persisted routed group.
func ActivateAPIKeyFallbackRouting(ctx context.Context, fallbackGroupID int64) bool {
	return activateAPIKeyFallbackRouting(ctx, fallbackGroupID)
}

func apiKeyFallbackGroupForSelection(ctx context.Context, groupID *int64, selectionErr error) (*Group, context.Context, bool) {
	if ctx == nil || groupID == nil || *groupID <= 0 {
		return nil, ctx, false
	}
	if !errors.Is(selectionErr, ErrNoAvailableAccounts) && !errors.Is(selectionErr, ErrNoAvailableCompactAccounts) {
		return nil, ctx, false
	}
	if forcePlatform, _ := ctx.Value(ctxkey.ForcePlatform).(string); forcePlatform != "" {
		return nil, ctx, false
	}
	routing, ok := ctx.Value(apiKeyGroupFallbackContextKey{}).(apiKeyGroupFallbackRouting)
	if !ok || len(routing.chain) < 2 {
		return nil, ctx, false
	}
	// groupID 可能已经是链上的中间分组（前一次推进的结果），按当前位置取下一项。
	// 走到链尾时 apiKeyNextChainGroup 返回 nil，推进自然终止。
	if apiKeyChainGroup(routing.chain, *groupID) == nil {
		return nil, ctx, false
	}
	next := apiKeyNextChainGroup(routing.chain, *groupID)
	if next == nil || !next.IsActive() {
		return nil, ctx, false
	}
	fallbackCtx := context.WithValue(ctx, apiKeyGroupFallbackAttemptContextKey{}, *groupID)
	return next, fallbackCtx, true
}

// apiKeyNextChainGroup 返回 groupID 在链上的下一个可用分组。
func apiKeyNextChainGroup(chain []apiKeyGroupChainEntry, groupID int64) *Group {
	for i, entry := range chain {
		if entry.Group == nil || entry.Group.ID != groupID {
			continue
		}
		for j := i + 1; j < len(chain); j++ {
			if chain[j].Group != nil && chain[j].Group.ID > 0 && chain[j].Group.ID != groupID {
				return chain[j].Group
			}
		}
		return nil
	}
	return nil
}

func markAPIKeyFallbackSelection(ctx context.Context, selection *AccountSelectionResult, fallbackGroupID int64) *AccountSelectionResult {
	if selection != nil && selection.Account != nil {
		selection.RoutedGroupID = fallbackGroupID
		selection.UsedAPIKeyFallback = true
		activateAPIKeyFallbackRouting(ctx, fallbackGroupID)
	}
	return selection
}

func selectAccountWithAPIKeyGroupFallback(
	ctx context.Context,
	groupID *int64,
	requestedModel string,
	selectInGroup func(context.Context, *int64) (*Account, error),
) (*Account, error) {
	primaryGroupID := derefGroupID(groupID)
	account, err := selectInGroup(ctx, groupID)

	// 聚合密钥可能有多级：只要当前分组无可用账号且链上还有下一组，就继续推进。
	for attempt := 0; attempt < maxAPIKeyGroupChainAttempts; attempt++ {
		if err == nil && account != nil {
			break
		}
		fallbackGroup, fallbackCtx, ok := apiKeyFallbackGroupForSelection(ctx, groupID, err)
		if !ok {
			break
		}
		fallbackGroupID := fallbackGroup.ID
		slog.Info("api_key_group_fallback_attempt",
			"primary_group_id", primaryGroupID,
			"fallback_group_id", fallbackGroupID,
			"model", requestedModel)
		account, err = selectInGroup(fallbackCtx, &fallbackGroupID)
		if err == nil && account != nil {
			activateAPIKeyFallbackRouting(ctx, fallbackGroupID)
			slog.Info("api_key_group_fallback_selected",
				"primary_group_id", primaryGroupID,
				"fallback_group_id", fallbackGroupID,
				"account_id", account.ID,
				"model", requestedModel)
			break
		}
		// 继续尝试链上的下一组：后续判定以刚失败的分组作为「当前分组」。
		nextGroupID := fallbackGroupID
		groupID = &nextGroupID
		ctx = fallbackCtx
	}
	return account, err
}

// maxAPIKeyGroupChainAttempts 是单请求内分组链推进的上限，防止异常数据造成死循环。
const maxAPIKeyGroupChainAttempts = 16

// apiKeyGroupChainStep 报告当前分组失败后链上的下一组，并返回带推进标记的 ctx。
// 供不使用 selectAccountWithAPIKeyGroupFallback 的调度器（OpenAI 账号调度、
// 图片调度等）复用同一套链式推进语义。
func apiKeyGroupChainStep(ctx context.Context, groupID *int64, selectionErr error) (*Group, context.Context, bool) {
	return apiKeyFallbackGroupForSelection(ctx, groupID, selectionErr)
}

// withAPIKeyFallbackRateResolution freezes the configured chain's rates at
// the same request boundary as the primary snapshot. Resolving the fallbacks
// does not recurse because their IDs differ from primaryGroupID.
func withAPIKeyFallbackRateResolution(ctx context.Context, group *Group, resolution *RateResolution, resolve func(*Group) *RateResolution) *RateResolution {
	if ctx == nil || group == nil || resolution == nil {
		return resolution
	}
	routing, ok := ctx.Value(apiKeyGroupFallbackContextKey{}).(apiKeyGroupFallbackRouting)
	if !ok || group.ID != routing.primaryGroupID || len(routing.chain) < 2 {
		return resolution
	}
	groupIDs := make([]int64, 0, len(routing.chain)-1)
	fallbacks := make([]*RateResolution, 0, len(routing.chain)-1)
	for _, entry := range routing.chain[1:] {
		if entry.Group == nil || entry.Group.ID <= 0 || entry.Group.ID == group.ID {
			continue
		}
		groupIDs = append(groupIDs, entry.Group.ID)
		fallbacks = append(fallbacks, resolve(entry.Group))
	}
	if len(groupIDs) > 0 {
		resolution.fallbackGroupIDs = groupIDs
		resolution.fallbacks = fallbacks
	}
	return resolution
}

// forAPIKey selects the frozen rate of the actual route, including when an
// asynchronous usage task no longer carries the request's routing context.
func (r *RateResolution) forAPIKey(apiKey *APIKey) *RateResolution {
	if r == nil || apiKey == nil || apiKey.GroupID == nil {
		return r
	}
	for i, groupID := range r.fallbackGroupIDs {
		if groupID == *apiKey.GroupID && i < len(r.fallbacks) && r.fallbacks[i] != nil {
			return r.fallbacks[i]
		}
	}
	return r
}
