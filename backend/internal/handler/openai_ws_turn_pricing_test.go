package handler

import (
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestOpenAIWSTurnPricingCurrentOr(t *testing.T) {
	fallback := time.Date(2024, time.January, 2, 2, 0, 0, 0, time.UTC)

	t.Run("frozen time takes precedence", func(t *testing.T) {
		frozen := fallback.Add(time.Minute)
		var p openAIWSTurnPricing
		p.freeze(frozen)
		require.Equal(t, frozen, p.currentOr(fallback))
	})

	t.Run("zero value falls back to turn start", func(t *testing.T) {
		var p openAIWSTurnPricing
		require.Equal(t, fallback, p.currentOr(fallback))
	})
}

// TestOpenAIWSTurnPricingFreezePerTurn 钉死每个 turn 的 BeforeTurn 都会覆盖
// 上一个 turn 的定价时刻：长连接跨峰谷时后续 turn 不得沿用旧时刻。
func TestOpenAIWSTurnPricingFreezePerTurn(t *testing.T) {
	var p openAIWSTurnPricing
	turn1 := time.Now().Add(-time.Hour)
	turn2 := time.Now()

	p.freeze(turn1)
	require.Equal(t, turn1, p.currentOr(time.Time{}))

	p.freeze(turn2)
	require.Equal(t, turn2, p.currentOr(time.Time{}), "后续 turn 必须使用自己的定价时刻")
}

func TestOpenAIWSTurnRateSnapshotsCaptureBeforeAsyncBilling(t *testing.T) {
	group := &service.Group{
		ModelRateMultipliers: []service.ModelRateMultiplierRule{{Model: "gpt-5.6-sol", Multiplier: 2}},
	}
	resolution := &service.RateResolution{
		RequestedModel: "gpt-5.6-sol",
		MatchedModel:   "gpt-5.6-sol",
		Multiplier:     group.ModelRateMultipliers[0].Multiplier,
		Source:         "model_exact",
	}
	snapshots := newOpenAIWSTurnRateSnapshots(resolution)

	var resolveCalls int
	snapshots.capture(2, func() *service.RateResolution {
		resolveCalls++
		return &service.RateResolution{
			RequestedModel: "gpt-5.6-sol",
			MatchedModel:   "gpt-5.6-sol",
			Multiplier:     group.ModelRateMultipliers[0].Multiplier,
			Source:         "model_exact",
		}
	})
	// 模拟请求已经开始后管理员修改分组倍率；worker 只能拿到 turn 开始时的快照。
	group.ModelRateMultipliers[0].Multiplier = 9
	snapshots.capture(2, func() *service.RateResolution {
		resolveCalls++
		return &service.RateResolution{Multiplier: 99}
	})

	require.Equal(t, 1, resolveCalls)
	require.Equal(t, 2.0, snapshots.take(1).Multiplier)
	require.Equal(t, 2.0, snapshots.take(2).Multiplier)
	require.Nil(t, snapshots.take(2))
}
