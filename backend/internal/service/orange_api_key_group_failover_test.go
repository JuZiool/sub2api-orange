// Orange 特有：分组链「上游故障换组」原语的单元测试。
//
// 覆盖需求里可纯函数化的判定：
//   - 只有链上当前分组之后、未尝试过、且 active 的分组才会被推进；
//   - 无链 / 强制平台 / 链走完 / 预算耗尽 一律不推进（不回绕）；
//   - 切组判定只认 5xx、408 与可重试的上游故障，确定性错误与额度/权限类不推进；
//   - 激活走请求级指针替换（新 GroupID 指针 + 清 RPM 覆盖），不污染共享对象。
package service

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
)

// orangeFailoverTestChain 构造「链首 + 之后各候选」的分组链、请求级路由上下文与
// ctxkey.Group 对象（与鉴权中间件安装的形态一致）。
func orangeFailoverTestChain(t *testing.T, ids ...int64) (*APIKey, context.Context, *Group) {
	t.Helper()
	groups := make([]*Group, 0, len(ids))
	for _, id := range ids {
		groups = append(groups, orangePolicyTestGroup(id, PlatformAnthropic))
	}
	apiKey := orangePolicyTestAPIKey(groups...)
	ctxGroup := *apiKey.Group
	ctx := context.WithValue(context.Background(), ctxkey.Group, &ctxGroup)
	return apiKey, WithOrangeKeyGroupRouting(ctx, apiKey), &ctxGroup
}

func TestOrangeChainNextGroupAfterFailureForwardOnly(t *testing.T) {
	t.Run("从链首推进到下一组且不改指针", func(t *testing.T) {
		apiKey, ctx, ctxGroup := orangeFailoverTestChain(t, 11, 12, 13)
		headPointer := apiKey.GroupID
		headGroupID := *apiKey.GroupID

		next, ok := OrangeChainNextGroupAfterFailure(ctx, apiKey, map[int64]struct{}{})
		if !ok || next == nil || next.ID != 12 {
			t.Fatalf("expected next group 12, got %+v (ok=%v)", next, ok)
		}
		// 纯查询：请求级路由与上下文分组都不能被改写。
		if apiKey.Group == nil || apiKey.Group.ID != 11 || *apiKey.GroupID != headGroupID || apiKey.GroupID != headPointer {
			t.Fatalf("query must not rewrite api key, got group=%+v groupID=%v", apiKey.Group, apiKey.GroupID)
		}
		if ctxGroup.ID != 11 {
			t.Fatalf("query must not rewrite context group, got %d", ctxGroup.ID)
		}
	})

	t.Run("跳过已尝试分组", func(t *testing.T) {
		apiKey, ctx, _ := orangeFailoverTestChain(t, 11, 12, 13)
		// B 已被其他机制标记为不可用：推进时必须跳过它。
		next, ok := OrangeChainNextGroupAfterFailure(ctx, apiKey, map[int64]struct{}{12: {}})
		if !ok || next == nil || next.ID != 13 {
			t.Fatalf("expected next group 13 after skipping tried group, got %+v (ok=%v)", next, ok)
		}
	})

	t.Run("尝试集合达到链长减一时预算耗尽", func(t *testing.T) {
		apiKey, ctx, _ := orangeFailoverTestChain(t, 11, 12, 13)
		if _, ok := OrangeChainNextGroupAfterFailure(ctx, apiKey, map[int64]struct{}{11: {}, 12: {}}); ok {
			t.Fatal("exhausted switch budget must stop group failover")
		}
	})

	t.Run("链走完不回绕", func(t *testing.T) {
		apiKey, ctx, _ := orangeFailoverTestChain(t, 11, 12)
		if _, ok := OrangeChainNextGroupAfterFailure(ctx, apiKey, map[int64]struct{}{11: {}}); ok {
			t.Fatal("chain tail must not advance to any group")
		}
		// 尝试集合为空时，两段链仍只允许推进一次（预算 = 链长-1）。
		if _, ok := OrangeChainNextGroupAfterFailure(ctx, apiKey, map[int64]struct{}{}); !ok {
			t.Fatal("head of a 2-group chain must still advance once")
		}
	})

	t.Run("预算为链长减一", func(t *testing.T) {
		apiKey, ctx, ctxGroup := orangeFailoverTestChain(t, 21, 22, 23)
		failed := make(map[int64]struct{})
		switches := 0
		for {
			current := *apiKey.GroupID
			next, ok := OrangeChainNextGroupAfterFailure(ctx, apiKey, failed)
			if !ok {
				break
			}
			failed[current] = struct{}{}
			if !ActivateOrangeChainGroupForRequest(ctx, apiKey, next) {
				t.Fatal("activation must succeed for a chain member")
			}
			switches++
			if switches > 8 {
				t.Fatal("switch budget must be bounded by chain length")
			}
		}
		if switches != 2 {
			t.Fatalf("expected exactly chainLen-1 = 2 switches, got %d", switches)
		}
		if *apiKey.GroupID != 23 || ctxGroup.ID != 23 {
			t.Fatalf("expected to end on group 23, got apiKey=%d ctx=%d", *apiKey.GroupID, ctxGroup.ID)
		}
	})

	t.Run("无链不推进", func(t *testing.T) {
		head := orangePolicyTestGroup(31, PlatformAnthropic)
		headID := head.ID
		plain := &APIKey{ID: 2, GroupID: &headID, Group: head}
		ctx := context.WithValue(context.Background(), ctxkey.Group, head)
		if _, ok := OrangeChainNextGroupAfterFailure(ctx, plain, map[int64]struct{}{}); ok {
			t.Fatal("plain key without chain must never advance")
		}
	})

	t.Run("强制平台路由不推进", func(t *testing.T) {
		apiKey, ctx, _ := orangeFailoverTestChain(t, 41, 42)
		forced := context.WithValue(ctx, ctxkey.ForcePlatform, PlatformAntigravity)
		if _, ok := OrangeChainNextGroupAfterFailure(forced, apiKey, map[int64]struct{}{}); ok {
			t.Fatal("forced platform routing must not switch groups")
		}
	})

	t.Run("停用的候选被跳过", func(t *testing.T) {
		apiKey, ctx, _ := orangeFailoverTestChain(t, 51, 52, 53)
		// 链在装载时已按 active 过滤；这里模拟请求期内分组被停用的竞态。
		apiKey.Groups[1].Group.Status = StatusDisabled
		next, ok := OrangeChainNextGroupAfterFailure(ctx, apiKey, map[int64]struct{}{})
		if !ok || next == nil || next.ID != 53 {
			t.Fatalf("expected to skip disabled group 52 and pick 53, got %+v (ok=%v)", next, ok)
		}
	})

	t.Run("当前分组不在链上不推进", func(t *testing.T) {
		apiKey, ctx, _ := orangeFailoverTestChain(t, 61, 62)
		routing, ok := orangeKeyRoutingFromContext(ctx)
		if !ok || routing.apiKey == nil {
			t.Fatal("sanity: chain context must carry request-level routing")
		}
		// 模拟请求级路由指向链外分组（例如上游 fallback 分组：换组没有意义）。
		foreign := int64(999)
		routing.apiKey.GroupID = &foreign
		if _, advanced := OrangeChainNextGroupAfterFailure(ctx, apiKey, map[int64]struct{}{}); advanced {
			t.Fatal("current group outside the chain must not advance")
		}
	})
}

func TestActivateOrangeChainGroupForRequest(t *testing.T) {
	t.Run("切换请求级指针并清理 RPM 覆盖", func(t *testing.T) {
		apiKey, ctx, ctxGroup := orangeFailoverTestChain(t, 71, 72)
		override := 483
		apiKey.User = &User{UserGroupRPMOverride: &override, UserGroupRPMOverrideGroupID: 71}
		originalGroupIDPointer := apiKey.GroupID

		next, ok := OrangeChainNextGroupAfterFailure(ctx, apiKey, map[int64]struct{}{})
		if !ok {
			t.Fatal("expected a next group")
		}
		if !ActivateOrangeChainGroupForRequest(ctx, apiKey, next) {
			t.Fatal("activation must succeed")
		}
		if apiKey.Group == nil || apiKey.Group.ID != 72 {
			t.Fatalf("expected api key group 72, got %+v", apiKey.Group)
		}
		if apiKey.GroupID == nil || *apiKey.GroupID != 72 {
			t.Fatalf("expected api key group id 72, got %v", apiKey.GroupID)
		}
		// 新指针：认证缓存快照共享的 GroupID 指针禁止原地写。
		if apiKey.GroupID == originalGroupIDPointer {
			t.Fatal("group id pointer must be replaced, not rewritten in place")
		}
		if ctxGroup.ID != 72 {
			t.Fatalf("context group must be rewritten in place, got %d", ctxGroup.ID)
		}
		if apiKey.User.UserGroupRPMOverride != nil || apiKey.User.UserGroupRPMOverrideGroupID != 0 {
			t.Fatal("UserGroupRPMOverride must be cleared on group switch")
		}
	})

	t.Run("请求级当前分组随激活更新", func(t *testing.T) {
		apiKey, ctx, _ := orangeFailoverTestChain(t, 101, 102)
		if got := OrangeChainCurrentGroupID(ctx, apiKey); got != 101 {
			t.Fatalf("expected current group 101, got %d", got)
		}
		next, ok := OrangeChainNextGroupAfterFailure(ctx, apiKey, map[int64]struct{}{})
		if !ok || !ActivateOrangeChainGroupForRequest(ctx, apiKey, next) {
			t.Fatal("activation must succeed")
		}
		if got := OrangeChainCurrentGroupID(ctx, apiKey); got != 102 {
			t.Fatalf("expected current group 102 after activation, got %d", got)
		}
		// 无链上下文返回 0（handler 据此判定「不切组」）。
		if got := OrangeChainCurrentGroupID(context.Background(), nil); got != 0 {
			t.Fatalf("expected 0 for context without routing, got %d", got)
		}
	})

	t.Run("链外分组不激活", func(t *testing.T) {
		apiKey, ctx, _ := orangeFailoverTestChain(t, 81, 82)
		if ActivateOrangeChainGroupForRequest(ctx, apiKey, orangePolicyTestGroup(999, PlatformAnthropic)) {
			t.Fatal("group outside the chain must not be activated")
		}
		if apiKey.Group == nil || apiKey.Group.ID != 81 {
			t.Fatalf("api key group must stay on head, got %+v", apiKey.Group)
		}
	})

	t.Run("无链上下文不激活", func(t *testing.T) {
		head := orangePolicyTestGroup(91, PlatformAnthropic)
		headID := head.ID
		plain := &APIKey{ID: 3, GroupID: &headID, Group: head}
		ctx := context.WithValue(context.Background(), ctxkey.Group, head)
		if ActivateOrangeChainGroupForRequest(ctx, plain, orangePolicyTestGroup(92, PlatformAnthropic)) {
			t.Fatal("plain key without chain must not activate another group")
		}
	})
}

func TestOrangeIsUpstreamFaultForGroupFailover(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{name: "nil", err: nil, want: false},
		{name: "503", err: &UpstreamFailoverError{StatusCode: http.StatusServiceUnavailable}, want: true},
		{name: "500", err: &UpstreamFailoverError{StatusCode: http.StatusInternalServerError}, want: true},
		{name: "502 连接失败", err: &UpstreamFailoverError{StatusCode: http.StatusBadGateway}, want: true},
		{name: "504 上游超时", err: &UpstreamFailoverError{StatusCode: http.StatusGatewayTimeout}, want: true},
		{name: "408 请求超时", err: &UpstreamFailoverError{StatusCode: http.StatusRequestTimeout}, want: true},
		{name: "529 过载", err: &UpstreamFailoverError{StatusCode: 529}, want: true},
		{name: "wrapped 503", err: fmt.Errorf("forward: %w", &UpstreamFailoverError{StatusCode: http.StatusServiceUnavailable}), want: true},
		{name: "400 参数错误", err: &UpstreamFailoverError{StatusCode: http.StatusBadRequest}, want: false},
		{name: "401 凭据", err: &UpstreamFailoverError{StatusCode: http.StatusUnauthorized}, want: false},
		{name: "402 额度", err: &UpstreamFailoverError{StatusCode: http.StatusPaymentRequired}, want: false},
		{name: "403 权限", err: &UpstreamFailoverError{StatusCode: http.StatusForbidden}, want: false},
		{name: "404", err: &UpstreamFailoverError{StatusCode: http.StatusNotFound}, want: false},
		{name: "429 限流", err: &UpstreamFailoverError{StatusCode: http.StatusTooManyRequests}, want: false},
		{name: "上游显式停止换号", err: &UpstreamFailoverError{StatusCode: http.StatusServiceUnavailable, NextAccountAction: NextAccountStop}, want: false},
		{name: "未知状态码", err: &UpstreamFailoverError{}, want: false},
		{name: "非 failover 错误", err: errors.New("content policy blocked"), want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := OrangeIsUpstreamFaultForGroupFailover(tc.err); got != tc.want {
				t.Fatalf("OrangeIsUpstreamFaultForGroupFailover(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}
