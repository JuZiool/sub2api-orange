// Orange 特有：分组利润统计 HTTP 处理器的接入测试。
//
// 覆盖契约里与前端共享的关键点：
//   - GET 的 data.items[*] 字段名与 generated_at / cached 结构；
//   - ids 解析失败 → 400（不查库）；仓储未装配 → 503（不 panic）；
//   - PUT 的分组 ID / 请求体校验 → 400，分组不存在 → 404，
//     成功写入返回 {group_id, upstream_rate_multiplier}。
package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// orangeProfitSettingRepoStub 只为把 Orange 分组利润 store 挂到 SettingService 上。
type orangeProfitSettingRepoStub struct {
	store service.OrangeGroupProfitStore
}

func (s *orangeProfitSettingRepoStub) Get(context.Context, string) (*service.Setting, error) {
	return nil, service.ErrSettingNotFound
}

func (s *orangeProfitSettingRepoStub) GetValue(context.Context, string) (string, error) {
	return "", nil
}

func (s *orangeProfitSettingRepoStub) Set(context.Context, string, string) error { return nil }

func (s *orangeProfitSettingRepoStub) GetMultiple(context.Context, []string) (map[string]string, error) {
	return map[string]string{}, nil
}

func (s *orangeProfitSettingRepoStub) SetMultiple(context.Context, map[string]string) error {
	return nil
}

func (s *orangeProfitSettingRepoStub) GetAll(context.Context) (map[string]string, error) {
	return map[string]string{}, nil
}

func (s *orangeProfitSettingRepoStub) Delete(context.Context, string) error { return nil }

// OrangeGroupProfitStore 暴露包级 store 能力（对应 repository 层的同名 provider）。
func (s *orangeProfitSettingRepoStub) OrangeGroupProfitStore() (service.OrangeGroupProfitStore, bool) {
	if s == nil || s.store == nil {
		return nil, false
	}
	return s.store, true
}

// orangeProfitStoreStub 内存版 store，只服务 handler 层契约测试。
type orangeProfitStoreStub struct {
	rates  map[int64]float64
	stats  map[int64]service.OrangeGroupProfitStats
	exists map[int64]bool
	upsert map[int64]float64
}

func (s *orangeProfitStoreStub) OrangeListGroupIDs(context.Context, int) ([]int64, error) {
	return []int64{1, 2}, nil
}

func (s *orangeProfitStoreStub) OrangeGroupExists(_ context.Context, groupID int64) (bool, error) {
	if s.exists == nil {
		return true, nil
	}
	return s.exists[groupID], nil
}

func (s *orangeProfitStoreStub) OrangeLoadGroupUpstreamRates(_ context.Context, groupIDs []int64) (map[int64]float64, error) {
	rates := make(map[int64]float64, len(groupIDs))
	for _, groupID := range groupIDs {
		if rate, ok := s.rates[groupID]; ok {
			rates[groupID] = rate
		}
	}
	return rates, nil
}

func (s *orangeProfitStoreStub) OrangeUpsertGroupUpstreamRate(_ context.Context, groupID int64, rate float64) error {
	if s.upsert == nil {
		s.upsert = make(map[int64]float64)
	}
	s.upsert[groupID] = rate
	return nil
}

func (s *orangeProfitStoreStub) OrangeGroupProfitStats(
	_ context.Context,
	groupIDs []int64,
	_, _, _ time.Time,
) (map[int64]service.OrangeGroupProfitStats, error) {
	stats := make(map[int64]service.OrangeGroupProfitStats, len(groupIDs))
	for _, groupID := range groupIDs {
		if row, ok := s.stats[groupID]; ok {
			stats[groupID] = row
		}
	}
	return stats, nil
}

// orangeProfitTestHandler 构造把 stub store 挂到 SettingService 上的 SettingHandler。
func orangeProfitTestHandler(t *testing.T, store service.OrangeGroupProfitStore) *SettingHandler {
	t.Helper()
	gin.SetMode(gin.TestMode)
	settingService := service.NewSettingService(&orangeProfitSettingRepoStub{store: store}, nil)
	return &SettingHandler{settingService: settingService}
}

func TestOrangeAdminGroupProfitResponseContract(t *testing.T) {
	store := &orangeProfitStoreStub{
		rates: map[int64]float64{1: 0.5},
		stats: map[int64]service.OrangeGroupProfitStats{
			1: {
				TodayRevenue:           20,
				TodayUpstreamPrice:     8,
				YesterdayRevenue:       5,
				YesterdayUpstreamPrice: 2,
				TotalRevenue:           200,
				TotalUpstreamPrice:     80,
			},
		},
	}
	h := orangeProfitTestHandler(t, store)

	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodGet,
		"/api/v1/admin/orange/groups/profit?ids=1,2&timezone=Asia/Shanghai", nil)
	h.OrangeAdminGroupProfit(ctx)

	require.Equal(t, http.StatusOK, rec.Code)

	var body struct {
		Code int `json:"code"`
		Data struct {
			Items       []map[string]any `json:"items"`
			GeneratedAt string           `json:"generated_at"`
			Cached      bool             `json:"cached"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.Equal(t, 0, body.Code)
	require.NotEmpty(t, body.Data.GeneratedAt)
	require.False(t, body.Data.Cached)
	require.Len(t, body.Data.Items, 2)

	// 14 个契约字段必须齐全（字段名与前端共享，禁止随意改名）。
	requiredKeys := []string{
		"group_id", "upstream_rate_multiplier",
		"today_revenue", "today_upstream_price", "today_upstream_cost", "today_profit",
		"yesterday_revenue", "yesterday_upstream_price", "yesterday_upstream_cost", "yesterday_profit",
		"total_revenue", "total_upstream_price", "total_upstream_cost", "total_profit",
	}
	for _, item := range body.Data.Items {
		require.Len(t, item, len(requiredKeys))
		for _, key := range requiredKeys {
			require.Contains(t, item, key)
		}
	}

	first := body.Data.Items[0]
	require.InDelta(t, 1, first["group_id"], 1e-9)
	require.InDelta(t, 0.5, first["upstream_rate_multiplier"], 1e-9)
	require.InDelta(t, 20, first["today_revenue"], 1e-9)
	require.InDelta(t, 8, first["today_upstream_price"], 1e-9)
	require.InDelta(t, 4, first["today_upstream_cost"], 1e-9)
	require.InDelta(t, 16, first["today_profit"], 1e-9)
	require.InDelta(t, 1, first["yesterday_upstream_cost"], 1e-9)
	require.InDelta(t, 4, first["yesterday_profit"], 1e-9)
	require.InDelta(t, 40, first["total_upstream_cost"], 1e-9)
	require.InDelta(t, 160, first["total_profit"], 1e-9)

	// 无用量记录的分组：全 0，倍率默认 1.0。
	second := body.Data.Items[1]
	require.InDelta(t, 2, second["group_id"], 1e-9)
	require.InDelta(t, service.OrangeGroupUpstreamRateDefault, second["upstream_rate_multiplier"], 1e-9)
	require.InDelta(t, 0, second["today_profit"], 1e-9)
	require.InDelta(t, 0, second["total_profit"], 1e-9)
}

func TestOrangeAdminGroupProfitRejectsInvalidIDs(t *testing.T) {
	h := orangeProfitTestHandler(t, &orangeProfitStoreStub{})

	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/api/v1/admin/orange/groups/profit?ids=abc", nil)
	h.OrangeAdminGroupProfit(ctx)

	require.Equal(t, http.StatusBadRequest, rec.Code)
	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.Equal(t, "ORANGE_GROUP_PROFIT_INVALID_IDS", body["reason"])
}

func TestOrangeAdminGroupProfitUnavailableWithoutStore(t *testing.T) {
	h := orangeProfitTestHandler(t, nil)

	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/api/v1/admin/orange/groups/profit?ids=1", nil)
	h.OrangeAdminGroupProfit(ctx)

	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
}

func TestOrangeAdminUpdateGroupUpstreamRateRejectsInvalidInput(t *testing.T) {
	h := orangeProfitTestHandler(t, &orangeProfitStoreStub{})

	// 分组 ID 非法。
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodPut, "/api/v1/admin/orange/groups/abc/upstream-rate",
		strings.NewReader(`{"upstream_rate_multiplier":1}`))
	ctx.Params = gin.Params{{Key: "id", Value: "abc"}}
	h.OrangeAdminUpdateGroupUpstreamRate(ctx)
	require.Equal(t, http.StatusBadRequest, rec.Code)

	// 请求体缺少字段。
	rec = httptest.NewRecorder()
	ctx, _ = gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodPut, "/api/v1/admin/orange/groups/10/upstream-rate",
		strings.NewReader(`{}`))
	ctx.Params = gin.Params{{Key: "id", Value: "10"}}
	h.OrangeAdminUpdateGroupUpstreamRate(ctx)
	require.Equal(t, http.StatusBadRequest, rec.Code)

	// 请求体不是 JSON。
	rec = httptest.NewRecorder()
	ctx, _ = gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodPut, "/api/v1/admin/orange/groups/10/upstream-rate",
		strings.NewReader(`not-json`))
	ctx.Params = gin.Params{{Key: "id", Value: "10"}}
	h.OrangeAdminUpdateGroupUpstreamRate(ctx)
	require.Equal(t, http.StatusBadRequest, rec.Code)

	// 倍率越界。
	rec = httptest.NewRecorder()
	ctx, _ = gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodPut, "/api/v1/admin/orange/groups/10/upstream-rate",
		strings.NewReader(`{"upstream_rate_multiplier":1000.5}`))
	ctx.Params = gin.Params{{Key: "id", Value: "10"}}
	h.OrangeAdminUpdateGroupUpstreamRate(ctx)
	require.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestOrangeAdminUpdateGroupUpstreamRateNotFound(t *testing.T) {
	store := &orangeProfitStoreStub{exists: map[int64]bool{}} // 分组不存在
	h := orangeProfitTestHandler(t, store)

	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodPut, "/api/v1/admin/orange/groups/99/upstream-rate",
		strings.NewReader(`{"upstream_rate_multiplier":0.5}`))
	ctx.Params = gin.Params{{Key: "id", Value: "99"}}
	h.OrangeAdminUpdateGroupUpstreamRate(ctx)

	require.Equal(t, http.StatusNotFound, rec.Code)
	require.Empty(t, store.upsert)
}

func TestOrangeAdminUpdateGroupUpstreamRateResponseContract(t *testing.T) {
	store := &orangeProfitStoreStub{}
	h := orangeProfitTestHandler(t, store)

	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodPut, "/api/v1/admin/orange/groups/10/upstream-rate",
		strings.NewReader(`{"upstream_rate_multiplier":0.85}`))
	ctx.Params = gin.Params{{Key: "id", Value: "10"}}
	h.OrangeAdminUpdateGroupUpstreamRate(ctx)

	require.Equal(t, http.StatusOK, rec.Code)
	var body struct {
		Data struct {
			GroupID                int64   `json:"group_id"`
			UpstreamRateMultiplier float64 `json:"upstream_rate_multiplier"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.Equal(t, int64(10), body.Data.GroupID)
	require.InDelta(t, 0.85, body.Data.UpstreamRateMultiplier, 1e-9)
	require.InDelta(t, 0.85, store.upsert[10], 1e-9)
}
