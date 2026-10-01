package service

import (
	"context"
	"errors"
	"log/slog"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
)

type apiKeyGroupFallbackContextKey struct{}
type apiKeyGroupFallbackAttemptContextKey struct{}

type apiKeyGroupFallbackRouting struct {
	primaryGroupID int64
	primaryGroup   *Group
	fallbackGroup  *Group
	apiKey         *APIKey
}

func isAPIKeyFallbackActive(apiKey *APIKey) bool {
	return apiKey != nil && apiKey.GroupID != nil && apiKey.FallbackGroupID != nil &&
		*apiKey.GroupID > 0 && *apiKey.GroupID == *apiKey.FallbackGroupID
}

// apiKeyFallbackRoutingForGroup returns the request-local API-key fallback
// routing state when the requested group has actually switched to the
// fallback group.  Most billing code can inspect the APIKey directly; a few
// long-lived flows (for example OpenAI Live) only carry a LiveCallIdentity and
// therefore need the context marker to distinguish a real fallback from an
// ordinary request that happens to use the same group ID.
func apiKeyFallbackRoutingForGroup(ctx context.Context, groupID int64) (apiKeyGroupFallbackRouting, bool) {
	if ctx == nil || groupID <= 0 {
		return apiKeyGroupFallbackRouting{}, false
	}
	routing, ok := ctx.Value(apiKeyGroupFallbackContextKey{}).(apiKeyGroupFallbackRouting)
	if !ok || routing.apiKey == nil || routing.fallbackGroup == nil {
		return apiKeyGroupFallbackRouting{}, false
	}
	if !isAPIKeyFallbackActive(routing.apiKey) || routing.fallbackGroup.ID != groupID {
		return apiKeyGroupFallbackRouting{}, false
	}
	return routing, true
}

// WithAPIKeyGroupFallbackRouting installs API-key-level fallback routing.
// Once the fallback group is selected, the request-local API key is switched to
// that group so downstream billing and usage attribution follow the actual route.
func WithAPIKeyGroupFallbackRouting(ctx context.Context, apiKey *APIKey) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if apiKey == nil || apiKey.GroupID == nil || apiKey.FallbackGroupID == nil ||
		apiKey.Group == nil || apiKey.FallbackGroup == nil ||
		*apiKey.GroupID <= 0 || *apiKey.FallbackGroupID <= 0 ||
		*apiKey.GroupID == *apiKey.FallbackGroupID ||
		apiKey.Group.Platform != apiKey.FallbackGroup.Platform ||
		!apiKey.FallbackGroup.IsActive() || !IsGroupContextValid(apiKey.FallbackGroup) {
		return ctx
	}
	ctx = context.WithValue(ctx, apiKeyGroupFallbackContextKey{}, apiKeyGroupFallbackRouting{
		primaryGroupID: *apiKey.GroupID,
		primaryGroup:   apiKey.Group,
		fallbackGroup:  apiKey.FallbackGroup,
		apiKey:         apiKey,
	})
	return ctx
}

func activateAPIKeyFallbackRouting(ctx context.Context, fallbackGroupID int64) bool {
	if ctx == nil || fallbackGroupID <= 0 {
		return false
	}
	routing, ok := ctx.Value(apiKeyGroupFallbackContextKey{}).(apiKeyGroupFallbackRouting)
	if !ok || routing.apiKey == nil || routing.fallbackGroup == nil ||
		routing.fallbackGroup.ID != fallbackGroupID {
		return false
	}

	// APIKeyService materializes a fresh APIKey for every authenticated request,
	// so changing these pointers is request-local and cannot pollute the auth cache.
	// Mutate the group object already held in the request context as well. This
	// makes group-level RPM/concurrency and downstream context lookups observe the
	// same effective group without requiring every handler to replace its context.
	if contextGroup, ok := ctx.Value(ctxkey.Group).(*Group); ok && contextGroup != nil {
		*contextGroup = *routing.fallbackGroup
	}
	// Keep the original GroupID pointer. Parsed request structures and other
	// request-local objects may already reference it; replacing the pointer would
	// leave them observing the primary group after fallback activation.
	if routing.apiKey.GroupID != nil {
		*routing.apiKey.GroupID = fallbackGroupID
	}
	// Keep the canonical fallback object on the API key. Besides making pointer
	// identity predictable for callers, this avoids retaining a partially hydrated
	// primary-group object for pricing and subscription decisions.
	routing.apiKey.Group = routing.fallbackGroup

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

// ActivateAPIKeyFallbackRouting activates the request-local fallback route and
// reports whether the supplied group was the configured fallback for this
// request. Handlers that resume an asynchronous operation can use the result
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
	if attempted, _ := ctx.Value(apiKeyGroupFallbackAttemptContextKey{}).(bool); attempted {
		return nil, ctx, false
	}
	if forcePlatform, _ := ctx.Value(ctxkey.ForcePlatform).(string); forcePlatform != "" {
		return nil, ctx, false
	}
	routing, ok := ctx.Value(apiKeyGroupFallbackContextKey{}).(apiKeyGroupFallbackRouting)
	if !ok || routing.primaryGroupID != *groupID || routing.fallbackGroup == nil || !routing.fallbackGroup.IsActive() {
		return nil, ctx, false
	}
	fallbackCtx := context.WithValue(ctx, apiKeyGroupFallbackAttemptContextKey{}, true)
	return routing.fallbackGroup, fallbackCtx, true
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
	fallbackGroup, fallbackCtx, ok := apiKeyFallbackGroupForSelection(ctx, groupID, err)
	if !ok {
		return account, err
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
	}
	return account, err
}
