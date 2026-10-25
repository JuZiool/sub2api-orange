package service

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

func newChainTestGroup(id int64, platform string) *Group {
	return &Group{ID: id, Platform: platform, Status: StatusActive, Hydrated: true}
}

// 聚合链：主分组无可用账号时按 priority 依次推进到链尾。
func TestSelectAccountWithAPIKeyGroupFallbackWalksChain(t *testing.T) {
	g1 := newChainTestGroup(1, PlatformAnthropic)
	g2 := newChainTestGroup(2, PlatformAnthropic)
	g3 := newChainTestGroup(3, PlatformAnthropic)
	primary := int64(1)
	apiKey := &APIKey{
		ID:      100,
		GroupID: &primary,
		Group:   g1,
		Groups: []*APIKeyGroupEntry{
			{Group: g1, Priority: 1},
			{Group: g2, Priority: 2},
			{Group: g3, Priority: 3},
		},
	}

	ctx := WithAPIKeyGroupFallbackRouting(context.Background(), apiKey)
	require.NotNil(t, ctx)

	var visited []int64
	account, err := selectAccountWithAPIKeyGroupFallback(ctx, apiKey.GroupID, "claude-x",
		func(selectionCtx context.Context, groupID *int64) (*Account, error) {
			visited = append(visited, *groupID)
			// 只有第 3 个分组有可用账号
			if *groupID == 3 {
				return &Account{ID: 300}, nil
			}
			return nil, ErrNoAvailableAccounts
		})

	require.NoError(t, err)
	require.NotNil(t, account)
	require.Equal(t, []int64{1, 2, 3}, visited, "应按优先级依次尝试链上每个分组")
	require.Equal(t, int64(3), *apiKey.GroupID, "选中后主分组应被请求级改写为实际路由分组")
	require.Equal(t, g3, apiKey.Group)
}

// 链尾仍无可用账号时返回最后一次错误，且不会无限循环。
func TestSelectAccountWithAPIKeyGroupFallbackExhaustsChain(t *testing.T) {
	g1 := newChainTestGroup(11, PlatformAnthropic)
	g2 := newChainTestGroup(12, PlatformAnthropic)
	primary := int64(11)
	apiKey := &APIKey{
		ID:      101,
		GroupID: &primary,
		Group:   g1,
		Groups: []*APIKeyGroupEntry{
			{Group: g1, Priority: 1},
			{Group: g2, Priority: 2},
		},
	}

	ctx := WithAPIKeyGroupFallbackRouting(context.Background(), apiKey)
	calls := 0
	_, err := selectAccountWithAPIKeyGroupFallback(ctx, apiKey.GroupID, "claude-y",
		func(selectionCtx context.Context, groupID *int64) (*Account, error) {
			calls++
			return nil, ErrNoAvailableAccounts
		})

	require.ErrorIs(t, err, ErrNoAvailableAccounts)
	require.Equal(t, 2, calls, "链上 2 个分组各尝试一次后停止")
}

// 旧式单兜底密钥等价于长度 2 的链，行为保持不变。
func TestSelectAccountWithAPIKeyGroupFallbackLegacySingleFallback(t *testing.T) {
	g1 := newChainTestGroup(21, PlatformAnthropic)
	g2 := newChainTestGroup(22, PlatformAnthropic)
	primary := int64(21)
	apiKey := &APIKey{
		ID:            102,
		GroupID:       &primary,
		Group:         g1,
		FallbackGroup: g2,
		FallbackGroupID: func() *int64 {
			id := int64(22)
			return &id
		}(),
	}

	ctx := WithAPIKeyGroupFallbackRouting(context.Background(), apiKey)
	var visited []int64
	account, err := selectAccountWithAPIKeyGroupFallback(ctx, apiKey.GroupID, "claude-z",
		func(selectionCtx context.Context, groupID *int64) (*Account, error) {
			visited = append(visited, *groupID)
			if *groupID == 22 {
				return &Account{ID: 220}, nil
			}
			return nil, ErrNoAvailableAccounts
		})

	require.NoError(t, err)
	require.Equal(t, int64(220), account.ID)
	require.Equal(t, []int64{21, 22}, visited)
}

// 跨平台链不生效：网关已在中间件阶段按主分组平台分发。
func TestWithAPIKeyGroupFallbackRoutingRejectsCrossPlatformChain(t *testing.T) {
	g1 := newChainTestGroup(31, PlatformAnthropic)
	g2 := newChainTestGroup(32, PlatformOpenAI)
	primary := int64(31)
	apiKey := &APIKey{
		ID:      103,
		GroupID: &primary,
		Group:   g1,
		Groups: []*APIKeyGroupEntry{
			{Group: g1, Priority: 1},
			{Group: g2, Priority: 2},
		},
	}

	ctx := WithAPIKeyGroupFallbackRouting(context.Background(), apiKey)
	_, ok := ctx.Value(apiKeyGroupFallbackContextKey{}).(apiKeyGroupFallbackRouting)
	require.False(t, ok, "跨平台链必须被拒绝")
}

// 非"无可用账号"类错误不触发链路推进。
func TestAPIKeyFallbackGroupForSelectionIgnoresOtherErrors(t *testing.T) {
	g1 := newChainTestGroup(41, PlatformAnthropic)
	g2 := newChainTestGroup(42, PlatformAnthropic)
	primary := int64(41)
	apiKey := &APIKey{
		ID:      104,
		GroupID: &primary,
		Group:   g1,
		Groups: []*APIKeyGroupEntry{
			{Group: g1, Priority: 1},
			{Group: g2, Priority: 2},
		},
	}
	ctx := WithAPIKeyGroupFallbackRouting(context.Background(), apiKey)
	_, _, ok := apiKeyFallbackGroupForSelection(ctx, apiKey.GroupID, errors.New("boom"))
	require.False(t, ok)
}
