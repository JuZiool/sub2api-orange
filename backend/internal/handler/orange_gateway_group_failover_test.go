// Orange 特有：handler 层「上游故障换组」助手的单元测试。
//
// 覆盖 service 原语在 handler 侧的语义：
//   - 只有上游故障（5xx / 超时 / 连接失败）才允许切组，确定性错误不切；
//   - 无链（普通 Key）绝不切换，行为与上游完全一致；
//   - 每次请求最多切 (链长-1) 次，且只向前推进；
//   - 切组后请求级 apiKey / ctxkey.Group 指向新分组，失败账号集合由调用方重置。
package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// orangeHandlerTestGroup 构造一个「可用 + 上下文完整」的分组。
func orangeHandlerTestGroup(id int64) *service.Group {
	return &service.Group{
		ID:       id,
		Name:     "orange-handler-test-group",
		Platform: service.PlatformAnthropic,
		Status:   service.StatusActive,
		Hydrated: true,
	}
}

// orangeHandlerTestAPIKey 构造带分组链的请求级 API Key（链首 = 第一个分组）。
func orangeHandlerTestAPIKey(groups ...*service.Group) *service.APIKey {
	entries := make([]*service.APIKeyGroupEntry, 0, len(groups))
	for i, group := range groups {
		entries = append(entries, &service.APIKeyGroupEntry{Group: group, Priority: i + 1})
	}
	headID := groups[0].ID
	return &service.APIKey{ID: 7, GroupID: &headID, Group: groups[0], Groups: entries}
}

// orangeHandlerTestContext 构造带请求级分组路由的 gin 上下文（与鉴权中间件一致）。
func orangeHandlerTestContext(t *testing.T, apiKey *service.APIKey) (*gin.Context, *service.Group) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctxGroup := *apiKey.Group
	ctx := context.WithValue(context.Background(), ctxkey.Group, &ctxGroup)
	ctx = service.WithOrangeKeyGroupRouting(ctx, apiKey)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil).WithContext(ctx)
	return c, &ctxGroup
}

func TestOrangeChainFailoverSwitch(t *testing.T) {
	t.Run("上游故障耗尽后切到链上的下一组", func(t *testing.T) {
		head := orangeHandlerTestGroup(101)
		tail := orangeHandlerTestGroup(102)
		apiKey := orangeHandlerTestAPIKey(head, tail)
		c, ctxGroup := orangeHandlerTestContext(t, apiKey)
		failedGroupIDs := make(map[int64]struct{})

		switchedKey, ok := orangeChainFailoverSwitch(c, apiKey, failedGroupIDs)
		require.True(t, ok)
		require.NotNil(t, switchedKey)
		require.Equal(t, tail.ID, switchedKey.Group.ID)
		require.NotNil(t, switchedKey.GroupID)
		require.Equal(t, tail.ID, *switchedKey.GroupID)
		require.Equal(t, tail.ID, ctxGroup.ID)
		require.Equal(t, map[int64]struct{}{head.ID: {}}, failedGroupIDs)

		// 链长 2：预算已用尽，再调用必须拒绝（不回绕到链首）。
		_, ok = orangeChainFailoverSwitch(c, apiKey, failedGroupIDs)
		require.False(t, ok)
	})

	t.Run("最多切链长减一次且只向前", func(t *testing.T) {
		first := orangeHandlerTestGroup(111)
		second := orangeHandlerTestGroup(112)
		third := orangeHandlerTestGroup(113)
		apiKey := orangeHandlerTestAPIKey(first, second, third)
		c, _ := orangeHandlerTestContext(t, apiKey)
		failedGroupIDs := make(map[int64]struct{})

		switchedKey, ok := orangeChainFailoverSwitch(c, apiKey, failedGroupIDs)
		require.True(t, ok)
		require.Equal(t, second.ID, switchedKey.Group.ID)

		switchedKey, ok = orangeChainFailoverSwitch(c, switchedKey, failedGroupIDs)
		require.True(t, ok)
		require.Equal(t, third.ID, switchedKey.Group.ID)

		_, ok = orangeChainFailoverSwitch(c, switchedKey, failedGroupIDs)
		require.False(t, ok, "chain tail must not loop back to head")
		require.Len(t, failedGroupIDs, 2)
	})

	t.Run("普通 Key 无链时不切换", func(t *testing.T) {
		head := orangeHandlerTestGroup(121)
		headID := head.ID
		apiKey := &service.APIKey{ID: 8, GroupID: &headID, Group: head}
		ctxGroup := *head
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		ctx := context.WithValue(context.Background(), ctxkey.Group, &ctxGroup)
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil).WithContext(ctx)
		failedGroupIDs := make(map[int64]struct{})

		switchedKey, ok := orangeChainFailoverSwitch(c, apiKey, failedGroupIDs)
		require.False(t, ok)
		require.Nil(t, switchedKey)
		require.Empty(t, failedGroupIDs)
		require.Equal(t, head.ID, *apiKey.GroupID)
	})

	t.Run("强制平台路由不切换", func(t *testing.T) {
		head := orangeHandlerTestGroup(131)
		tail := orangeHandlerTestGroup(132)
		apiKey := orangeHandlerTestAPIKey(head, tail)
		c, _ := orangeHandlerTestContext(t, apiKey)
		c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), ctxkey.ForcePlatform, service.PlatformAntigravity))

		_, ok := orangeChainFailoverSwitch(c, apiKey, make(map[int64]struct{}))
		require.False(t, ok)
	})
}

func TestOrangeUpstreamFaultAllowsGroupFailover(t *testing.T) {
	cases := []struct {
		name string
		err  *service.UpstreamFailoverError
		want bool
	}{
		{name: "nil", err: nil, want: false},
		{name: "503", err: &service.UpstreamFailoverError{StatusCode: http.StatusServiceUnavailable}, want: true},
		{name: "502 连接失败", err: &service.UpstreamFailoverError{StatusCode: http.StatusBadGateway}, want: true},
		{name: "504 上游超时", err: &service.UpstreamFailoverError{StatusCode: http.StatusGatewayTimeout}, want: true},
		{name: "408 请求超时", err: &service.UpstreamFailoverError{StatusCode: http.StatusRequestTimeout}, want: true},
		{name: "400 参数错误", err: &service.UpstreamFailoverError{StatusCode: http.StatusBadRequest}, want: false},
		{name: "401 凭据", err: &service.UpstreamFailoverError{StatusCode: http.StatusUnauthorized}, want: false},
		{name: "403 权限", err: &service.UpstreamFailoverError{StatusCode: http.StatusForbidden}, want: false},
		{name: "404", err: &service.UpstreamFailoverError{StatusCode: http.StatusNotFound}, want: false},
		{name: "429 限流", err: &service.UpstreamFailoverError{StatusCode: http.StatusTooManyRequests}, want: false},
		{name: "上游显式停止换号", err: &service.UpstreamFailoverError{StatusCode: http.StatusServiceUnavailable, NextAccountAction: service.NextAccountStop}, want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, orangeUpstreamFaultAllowsGroupFailover(tc.err))
		})
	}
}

func TestOrangeRefreshChainGroupRequestContext(t *testing.T) {
	head := orangeHandlerTestGroup(141)
	tail := orangeHandlerTestGroup(142)
	apiKey := orangeHandlerTestAPIKey(head, tail)
	c, _ := orangeHandlerTestContext(t, apiKey)

	// 上一分组残留「单账号退避重试」标记：切组后必须清掉。
	singleRetryCtx := service.WithSingleAccountRetry(c.Request.Context(), true, false)
	c.Request = c.Request.WithContext(singleRetryCtx)
	singleRetry, ok := service.SingleAccountRetryFromContext(c.Request.Context())
	require.True(t, ok)
	require.True(t, singleRetry)

	h := &GatewayHandler{cfg: &config.Config{}}
	h.orangeRefreshChainGroupRequestContext(c, apiKey)

	singleRetry, ok = service.SingleAccountRetryFromContext(c.Request.Context())
	require.True(t, ok)
	require.False(t, singleRetry, "chain group switch must reset the single-account retry flag")
	// 请求级路由链必须仍在上下文里（切组不能丢掉链本身）。
	require.True(t, apiKey.HasGroupChain())
}
