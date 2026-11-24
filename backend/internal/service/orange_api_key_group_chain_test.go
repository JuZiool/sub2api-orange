// Orange 特有：分组链外壳「组内选号」标记的单元测试。
//
// 覆盖「递归自调用代替劈函数体」这条改法的关键前提：
//   - 标记只挂在派生 ctx 上，不会污染父 ctx（父 ctx 就是请求级路由 ctx）；
//   - 派生 ctx（WithTimeout / WithCancel）继续携带标记，取消与超时语义不变；
//   - orangeChainOutcome 零值表示「未处理」，即无链时必须继续执行上游原逻辑。
package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestOrangeChainGuard(t *testing.T) {
	// 显式类型化 nil：用于验证函数对 nil ctx 的兼容，避免 staticcheck SA1012 误判。
	var nilContext context.Context
	require.False(t, orangeChainGuardActive(nilContext))

	parent := context.Background()
	require.False(t, orangeChainGuardActive(parent))

	guarded := withOrangeChainGuard(parent)
	require.True(t, orangeChainGuardActive(guarded))
	// 标记只挂在派生 ctx 上：父 ctx（请求级路由 ctx）不受影响，不会跨请求残留。
	require.False(t, orangeChainGuardActive(parent))

	// 派生 ctx 继续携带标记，且取消/超时语义与原来完全一致（只多一个不可见 value）。
	derived, cancel := context.WithTimeout(guarded, time.Minute)
	defer cancel()
	require.True(t, orangeChainGuardActive(derived))
	cancel()
	select {
	case <-derived.Done():
	default:
		t.Fatal("derived context must be canceled")
	}
	require.True(t, orangeChainGuardActive(derived))
	require.Error(t, derived.Err())
}

func TestOrangeChainOutcomeZeroValue(t *testing.T) {
	// 零值即「未处理」：无链（普通 Key）路径必须让调用方继续执行上游原逻辑。
	var outcome orangeChainOutcome[*Account]
	require.False(t, outcome.Handled)
	require.Nil(t, outcome.Value)
	require.NoError(t, outcome.Err)
}
