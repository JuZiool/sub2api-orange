// Orange 特有：分组利润统计服务层的单元测试。
//
// 覆盖需求里可纯函数化/可注入的部分：
//   - 利润公式（利润 = 收入 - 上游原价 * 倍率），含 0 倍率与默认 1.0；
//   - ids 解析 / 去重 / 排序 / 上限 / 非法值；
//   - 缓存键（ids 排序、时区、当天日期）与"命中缓存不重复查库"；
//   - 按用户时区自然日切窗（today / tomorrow / yesterday 三个窗口）；
//   - 倍率取值校验（范围 + 4 位小数）与写库后缓存失效。
package service

import (
	"context"
	"errors"
	"math"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/timezone"

	"github.com/stretchr/testify/require"
)

// orangeGroupProfitStoreStub 记录调用次数与入参，用于验证服务层的查询与缓存行为。
type orangeGroupProfitStoreStub struct {
	groupIDs  []int64
	listErr   error
	exists    map[int64]bool
	existsErr error
	rates     map[int64]float64
	ratesErr  error
	stats     map[int64]OrangeGroupProfitStats
	statsErr  error
	upsertErr error

	listCalls   int
	existsCalls int
	rateCalls   int
	statsCalls  int
	upsertCalls int
	upserted    map[int64]float64

	lastTodayStart     time.Time
	lastTomorrowStart  time.Time
	lastYesterdayStart time.Time
}

func (s *orangeGroupProfitStoreStub) OrangeListGroupIDs(_ context.Context, limit int) ([]int64, error) {
	s.listCalls++
	if s.listErr != nil {
		return nil, s.listErr
	}
	out := s.groupIDs
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (s *orangeGroupProfitStoreStub) OrangeGroupExists(_ context.Context, groupID int64) (bool, error) {
	s.existsCalls++
	if s.existsErr != nil {
		return false, s.existsErr
	}
	if s.exists == nil {
		return true, nil
	}
	return s.exists[groupID], nil
}

func (s *orangeGroupProfitStoreStub) OrangeLoadGroupUpstreamRates(_ context.Context, groupIDs []int64) (map[int64]float64, error) {
	s.rateCalls++
	if s.ratesErr != nil {
		return nil, s.ratesErr
	}
	rates := make(map[int64]float64, len(groupIDs))
	for _, groupID := range groupIDs {
		if rate, ok := s.rates[groupID]; ok {
			rates[groupID] = rate
		}
	}
	return rates, nil
}

func (s *orangeGroupProfitStoreStub) OrangeUpsertGroupUpstreamRate(_ context.Context, groupID int64, rate float64) error {
	s.upsertCalls++
	if s.upsertErr != nil {
		return s.upsertErr
	}
	if s.upserted == nil {
		s.upserted = make(map[int64]float64)
	}
	s.upserted[groupID] = rate
	return nil
}

func (s *orangeGroupProfitStoreStub) OrangeGroupProfitStats(
	_ context.Context,
	groupIDs []int64,
	todayStart, tomorrowStart, yesterdayStart time.Time,
) (map[int64]OrangeGroupProfitStats, error) {
	s.statsCalls++
	s.lastTodayStart = todayStart
	s.lastTomorrowStart = tomorrowStart
	s.lastYesterdayStart = yesterdayStart
	if s.statsErr != nil {
		return nil, s.statsErr
	}
	stats := make(map[int64]OrangeGroupProfitStats, len(groupIDs))
	for _, groupID := range groupIDs {
		if row, ok := s.stats[groupID]; ok {
			stats[groupID] = row
		}
	}
	return stats, nil
}

// newOrangeGroupProfitTestService 构造注入固定时钟与 stub 仓储的服务实例。
func newOrangeGroupProfitTestService(store *orangeGroupProfitStoreStub, now time.Time) *OrangeGroupProfitService {
	svc := &OrangeGroupProfitService{cache: make(map[string]*orangeGroupProfitCacheEntry), now: func() time.Time { return now }}
	if store != nil {
		svc.store = store
	}
	return svc
}

func TestOrangeGroupProfitWindowFormula(t *testing.T) {
	cases := []struct {
		name        string
		revenue     float64
		upstreamPrc float64
		rate        float64
		wantCost    float64
		wantProfit  float64
	}{
		{name: "半价上游", revenue: 10, upstreamPrc: 4, rate: 0.5, wantCost: 2, wantProfit: 8},
		{name: "免费上游（0 倍率）", revenue: 10, upstreamPrc: 4, rate: 0, wantCost: 0, wantProfit: 10},
		{name: "默认倍率 1.0", revenue: 10, upstreamPrc: 4, rate: OrangeGroupUpstreamRateDefault, wantCost: 4, wantProfit: 6},
		{name: "亏损（倍率大于 1）", revenue: 1, upstreamPrc: 4, rate: 2, wantCost: 8, wantProfit: -7},
		{name: "无用量记录", revenue: 0, upstreamPrc: 0, rate: 0.85, wantCost: 0, wantProfit: 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cost, profit := orangeGroupProfitWindow(tc.revenue, tc.upstreamPrc, tc.rate)
			require.InDelta(t, tc.wantCost, cost, 1e-12)
			require.InDelta(t, tc.wantProfit, profit, 1e-12)
		})
	}
}

func TestBuildOrangeGroupProfitItemsDefaultsAndZeroRate(t *testing.T) {
	items := buildOrangeGroupProfitItems(
		[]int64{1, 2},
		map[int64]float64{2: 0},
		map[int64]OrangeGroupProfitStats{
			1: {
				TodayRevenue:           12.5,
				TodayUpstreamPrice:     5,
				YesterdayRevenue:       3,
				YesterdayUpstreamPrice: 1,
				TotalRevenue:           100,
				TotalUpstreamPrice:     40,
			},
		},
	)
	require.Len(t, items, 2)

	// 分组 1：无倍率记录 → 默认 1.0。
	first := items[0]
	require.Equal(t, int64(1), first.GroupID)
	require.InDelta(t, OrangeGroupUpstreamRateDefault, first.UpstreamRateMultiplier, 1e-12)
	require.InDelta(t, 12.5, first.TodayRevenue, 1e-12)
	require.InDelta(t, 5, first.TodayUpstreamCost, 1e-12)
	require.InDelta(t, 7.5, first.TodayProfit, 1e-12)
	require.InDelta(t, 1, first.YesterdayUpstreamCost, 1e-12)
	require.InDelta(t, 2, first.YesterdayProfit, 1e-12)
	require.InDelta(t, 40, first.TotalUpstreamCost, 1e-12)
	require.InDelta(t, 60, first.TotalProfit, 1e-12)

	// 分组 2：0 倍率 + 无用量记录 → 全 0，倍率回 0（免费上游）。
	second := items[1]
	require.Equal(t, int64(2), second.GroupID)
	require.InDelta(t, 0, second.UpstreamRateMultiplier, 1e-12)
	require.InDelta(t, 0, second.TodayRevenue, 1e-12)
	require.InDelta(t, 0, second.TodayUpstreamCost, 1e-12)
	require.InDelta(t, 0, second.TodayProfit, 1e-12)
	require.InDelta(t, 0, second.TotalProfit, 1e-12)
}

func TestParseOrangeGroupProfitIDs(t *testing.T) {
	cases := []struct {
		name    string
		raw     string
		want    []int64
		wantErr bool
	}{
		{name: "缺省返回全部分组", raw: "", want: nil},
		{name: "全空白视为缺省", raw: "  ", want: nil},
		{name: "去空格并升序", raw: "3, 1,2", want: []int64{1, 2, 3}},
		{name: "去重", raw: "2,2,1", want: []int64{1, 2}},
		{name: "0 非法", raw: "0", wantErr: true},
		{name: "负数非法", raw: "-1", wantErr: true},
		{name: "非数字非法", raw: "abc", wantErr: true},
		{name: "空片段非法", raw: "1,,2", wantErr: true},
		{name: "小数非法", raw: "1.5", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseOrangeGroupProfitIDs(tc.raw)
			if tc.wantErr {
				require.Error(t, err)
				require.ErrorIs(t, err, ErrOrangeGroupProfitInvalidIDs)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}

func TestParseOrangeGroupProfitIDsCapsAtMaxGroups(t *testing.T) {
	parts := make([]string, 0, OrangeGroupProfitMaxGroups+50)
	for id := int64(1); id <= OrangeGroupProfitMaxGroups+50; id++ {
		parts = append(parts, strconv.FormatInt(id, 10))
	}
	got, err := ParseOrangeGroupProfitIDs(strings.Join(parts, ","))
	require.NoError(t, err)
	require.Len(t, got, OrangeGroupProfitMaxGroups)
	require.Equal(t, int64(1), got[0])
	require.Equal(t, int64(OrangeGroupProfitMaxGroups), got[len(got)-1])
}

func TestParseOrangeGroupProfitGroupID(t *testing.T) {
	id, err := ParseOrangeGroupProfitGroupID(" 42 ")
	require.NoError(t, err)
	require.Equal(t, int64(42), id)

	for _, raw := range []string{"", "abc", "0", "-3", "1.5"} {
		_, err := ParseOrangeGroupProfitGroupID(raw)
		require.ErrorIs(t, err, ErrOrangeGroupProfitInvalidGroupID, "raw=%q", raw)
	}
}

func TestNormalizeOrangeGroupUpstreamRate(t *testing.T) {
	valid := map[float64]float64{
		0:      0,
		1000:   1000,
		0.85:   0.85,
		0.1234: 0.1234,
		1:      1,
	}
	for input, want := range valid {
		got, err := NormalizeOrangeGroupUpstreamRate(input)
		require.NoError(t, err, "input=%v", input)
		require.InDelta(t, want, got, 1e-12)
	}

	invalid := []float64{-0.0001, -1, 1000.0001, 2000, 0.12345, 1.00001, math.NaN(), math.Inf(1), math.Inf(-1)}
	for _, input := range invalid {
		_, err := NormalizeOrangeGroupUpstreamRate(input)
		require.ErrorIs(t, err, ErrOrangeGroupProfitInvalidRate, "input=%v", input)
	}
}

func TestOrangeGroupProfitCacheKey(t *testing.T) {
	day := time.Date(2026, 11, 22, 0, 0, 0, 0, time.FixedZone("CST", 8*3600))

	sorted := orangeGroupProfitCacheKey([]int64{1, 2, 3}, "Asia/Shanghai", day)
	require.Equal(t, sorted, orangeGroupProfitCacheKey([]int64{1, 2, 3}, "Asia/Shanghai", day))
	require.NotEqual(t, sorted, orangeGroupProfitCacheKey([]int64{1, 2}, "Asia/Shanghai", day))
	require.NotEqual(t, sorted, orangeGroupProfitCacheKey([]int64{1, 2, 3}, "UTC", day))
	require.NotEqual(t, sorted, orangeGroupProfitCacheKey([]int64{1, 2, 3}, "Asia/Shanghai", day.AddDate(0, 0, 1)))
	require.NotEqual(t, sorted, orangeGroupProfitCacheKey(nil, "Asia/Shanghai", day))

	all := orangeGroupProfitCacheKey(nil, "Asia/Shanghai", day)
	require.Equal(t, all, orangeGroupProfitCacheKey([]int64{}, "Asia/Shanghai", day))
	require.Contains(t, all, "all")
	require.Contains(t, sorted, "1,2,3")
}

func TestOrangeGroupProfitServiceQueryWindowsAndCache(t *testing.T) {
	now := time.Date(2026, 11, 22, 19, 33, 53, 0, time.FixedZone("CST", 8*3600))
	store := &orangeGroupProfitStoreStub{
		rates: map[int64]float64{1: 0.5},
		stats: map[int64]OrangeGroupProfitStats{
			1: {TodayRevenue: 20, TodayUpstreamPrice: 8, YesterdayRevenue: 5, YesterdayUpstreamPrice: 2,
				TotalRevenue: 200, TotalUpstreamPrice: 80},
		},
	}
	svc := newOrangeGroupProfitTestService(store, now)

	result, err := svc.Query(context.Background(), []int64{2, 1}, "Asia/Shanghai")
	require.NoError(t, err)
	require.False(t, result.Cached)
	require.Equal(t, "Asia/Shanghai", result.Timezone)
	require.Len(t, result.Items, 2)

	// 分组顺序按 ids 升序；倍率缺省 1.0。
	require.Equal(t, int64(1), result.Items[0].GroupID)
	require.InDelta(t, 0.5, result.Items[0].UpstreamRateMultiplier, 1e-12)
	require.InDelta(t, 4, result.Items[0].TodayUpstreamCost, 1e-12)
	require.InDelta(t, 16, result.Items[0].TodayProfit, 1e-12)
	require.InDelta(t, 1, result.Items[0].YesterdayUpstreamCost, 1e-12)
	require.InDelta(t, 4, result.Items[0].YesterdayProfit, 1e-12)
	require.InDelta(t, 40, result.Items[0].TotalUpstreamCost, 1e-12)
	require.InDelta(t, 160, result.Items[0].TotalProfit, 1e-12)

	require.Equal(t, int64(2), result.Items[1].GroupID)
	require.InDelta(t, OrangeGroupUpstreamRateDefault, result.Items[1].UpstreamRateMultiplier, 1e-12)
	require.InDelta(t, 0, result.Items[1].TotalProfit, 1e-12)

	// 三个窗口按用户时区自然日：今天 00:00 / 明天 00:00 / 昨天 00:00。
	shanghai, err := time.LoadLocation("Asia/Shanghai")
	require.NoError(t, err)
	require.True(t, store.lastTodayStart.Equal(time.Date(2026, 11, 22, 0, 0, 0, 0, shanghai)))
	require.True(t, store.lastTomorrowStart.Equal(time.Date(2026, 11, 23, 0, 0, 0, 0, shanghai)))
	require.True(t, store.lastYesterdayStart.Equal(time.Date(2026, 11, 21, 0, 0, 0, 0, shanghai)))
	require.Equal(t, "Asia/Shanghai", store.lastTodayStart.Location().String())

	// 第二次相同参数命中缓存：不再查库，Cached=true。
	cached, err := svc.Query(context.Background(), []int64{1, 2}, "Asia/Shanghai")
	require.NoError(t, err)
	require.True(t, cached.Cached)
	require.Equal(t, 1, store.statsCalls)
	require.Equal(t, 1, store.rateCalls)
	// 命中缓存返回副本：修改结果不影响缓存内容。
	cached.Items[0].TotalProfit = -1
	again, err := svc.Query(context.Background(), []int64{1, 2}, "Asia/Shanghai")
	require.NoError(t, err)
	require.InDelta(t, 160, again.Items[0].TotalProfit, 1e-12)
}

func TestOrangeGroupProfitServiceQueryDefaultsToAllGroups(t *testing.T) {
	now := time.Date(2026, 11, 22, 8, 0, 0, 0, time.UTC)
	store := &orangeGroupProfitStoreStub{
		groupIDs: []int64{7, 5},
		rates:    map[int64]float64{7: 2},
		stats:    map[int64]OrangeGroupProfitStats{},
	}
	svc := newOrangeGroupProfitTestService(store, now)

	result, err := svc.Query(context.Background(), nil, "")
	require.NoError(t, err)
	require.Equal(t, 1, store.listCalls)
	require.Len(t, result.Items, 2)
	// 仓储列出的分组同样被归一化（升序）。
	require.Equal(t, []int64{5, 7}, []int64{result.Items[0].GroupID, result.Items[1].GroupID})
	require.InDelta(t, 2, result.Items[1].UpstreamRateMultiplier, 1e-12)
}

func TestOrangeGroupProfitServiceTimezoneFallback(t *testing.T) {
	now := time.Date(2026, 11, 22, 8, 0, 0, 0, time.UTC)
	store := &orangeGroupProfitStoreStub{groupIDs: []int64{1}}
	svc := newOrangeGroupProfitTestService(store, now)

	result, err := svc.Query(context.Background(), []int64{1}, "Not/AZone")
	require.NoError(t, err)
	require.Equal(t, timezone.Name(), result.Timezone)
	require.Equal(t, timezone.Location().String(), result.TodayStart.Location().String())
}

func TestOrangeGroupProfitServiceUnavailable(t *testing.T) {
	svc := NewOrangeGroupProfitService(nil)
	require.Nil(t, svc.store)

	_, err := svc.Query(context.Background(), nil, "")
	require.ErrorIs(t, err, ErrOrangeGroupProfitUnavailable)

	_, err = svc.UpdateUpstreamRate(context.Background(), 1, 1)
	require.ErrorIs(t, err, ErrOrangeGroupProfitUnavailable)
}

func TestOrangeGroupProfitServiceUpdateUpstreamRate(t *testing.T) {
	now := time.Date(2026, 11, 22, 8, 0, 0, 0, time.UTC)
	store := &orangeGroupProfitStoreStub{
		exists: map[int64]bool{10: true},
		rates:  map[int64]float64{10: 1},
		stats:  map[int64]OrangeGroupProfitStats{10: {TotalRevenue: 100, TotalUpstreamPrice: 50}},
	}
	svc := newOrangeGroupProfitTestService(store, now)

	// 先查一次填缓存。
	first, err := svc.Query(context.Background(), []int64{10}, "UTC")
	require.NoError(t, err)
	require.InDelta(t, 50, first.Items[0].TotalUpstreamCost, 1e-12)

	stored, err := svc.UpdateUpstreamRate(context.Background(), 10, 0.25)
	require.NoError(t, err)
	require.InDelta(t, 0.25, stored, 1e-12)
	require.InDelta(t, 0.25, store.upserted[10], 1e-12)

	// 写库后缓存被清空：下一次查询重新读库（新倍率生效），且不是缓存命中。
	store.rates[10] = 0.25
	after, err := svc.Query(context.Background(), []int64{10}, "UTC")
	require.NoError(t, err)
	require.False(t, after.Cached)
	require.Equal(t, 2, store.statsCalls)
	require.InDelta(t, 12.5, after.Items[0].TotalUpstreamCost, 1e-12)
	require.InDelta(t, 87.5, after.Items[0].TotalProfit, 1e-12)

	// 分组不存在：404 语义，不写库。
	_, err = svc.UpdateUpstreamRate(context.Background(), 99, 0.5)
	require.ErrorIs(t, err, ErrGroupNotFound)
	require.Equal(t, 1, store.upsertCalls)

	// 倍率非法：直接 400 语义，不查库不写库。
	_, err = svc.UpdateUpstreamRate(context.Background(), 10, 1.23456)
	require.ErrorIs(t, err, ErrOrangeGroupProfitInvalidRate)
	require.Equal(t, 1, store.upsertCalls)

	// 分组 ID 非法。
	_, err = svc.UpdateUpstreamRate(context.Background(), 0, 1)
	require.ErrorIs(t, err, ErrOrangeGroupProfitInvalidGroupID)
}

func TestOrangeGroupProfitServiceQueryErrors(t *testing.T) {
	now := time.Date(2026, 11, 22, 8, 0, 0, 0, time.UTC)

	deadlineStore := &orangeGroupProfitStoreStub{statsErr: context.DeadlineExceeded}
	svc := newOrangeGroupProfitTestService(deadlineStore, now)
	_, err := svc.Query(context.Background(), []int64{1}, "UTC")
	require.ErrorIs(t, err, ErrOrangeGroupProfitTimeout)

	canceledStore := &orangeGroupProfitStoreStub{ratesErr: context.Canceled}
	svc = newOrangeGroupProfitTestService(canceledStore, now)
	_, err = svc.Query(context.Background(), []int64{1}, "UTC")
	require.ErrorIs(t, err, ErrOrangeGroupProfitUnavailable)

	plainStore := &orangeGroupProfitStoreStub{listErr: errors.New("boom")}
	svc = newOrangeGroupProfitTestService(plainStore, now)
	_, err = svc.Query(context.Background(), nil, "UTC")
	require.Error(t, err)
	require.NotErrorIs(t, err, ErrOrangeGroupProfitTimeout)
}

// 保证 stub 的排序假设与服务一致（防止有人把 ids 归一化挪走后测试失效）。
func TestOrangeGroupProfitNormalizeIDsSortsInput(t *testing.T) {
	ids := []int64{9, 3, 9, 1}
	normalized, err := NormalizeOrangeGroupProfitIDs(ids)
	require.NoError(t, err)
	require.True(t, sort.SliceIsSorted(normalized, func(i, j int) bool { return normalized[i] < normalized[j] }))
	require.Equal(t, []int64{1, 3, 9}, normalized)
	// 原切片不被修改。
	require.Equal(t, []int64{9, 3, 9, 1}, ids)
}
