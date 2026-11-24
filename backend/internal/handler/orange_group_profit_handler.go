// Orange 特有：分组利润统计与分组上游成本倍率的 HTTP 处理器。
//
// 端点（管理员，鉴权由 routes/orange.go 的 admin 路由组保证）：
//   - GET /api/v1/admin/orange/groups/profit?ids=1,2,3&timezone=Asia/Shanghai
//   - PUT /api/v1/admin/orange/groups/:id/upstream-rate
//
// 设计说明：
//   - 为了零改动上游 setting_handler.go / wire.go，方法以「同包扩展方法」形式加在既有 *SettingHandler 上；
//   - 利润服务实例放在包级注册表里按 SettingService 复用（60s 结果缓存必须命中同一实例），
//     上游 SettingHandler 结构体因此不需要新增字段；
//   - 只读 usage_logs / groups，只写 Orange 自建表 orange_group_upstream_rate。
package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
)

// orangeGroupUpstreamRateBodyMaxBytes 倍率更新请求体上限（单个数字字段，正常不到 128B）。
const orangeGroupUpstreamRateBodyMaxBytes = 4 << 10

// orangeGroupProfitServiceRegistry 按 SettingService 实例复用分组利润服务。
// 进程内单实例才能让 60s 结果缓存与 singleflight 生效。
var orangeGroupProfitServiceRegistry sync.Map // map[*service.SettingService]*service.OrangeGroupProfitService

// orangeGroupProfitItemResponse 单个分组的利润统计响应条目（字段与共享契约逐一对齐）。
type orangeGroupProfitItemResponse struct {
	GroupID                int64   `json:"group_id"`
	UpstreamRateMultiplier float64 `json:"upstream_rate_multiplier"`

	TodayRevenue       float64 `json:"today_revenue"`
	TodayUpstreamPrice float64 `json:"today_upstream_price"`
	TodayUpstreamCost  float64 `json:"today_upstream_cost"`
	TodayProfit        float64 `json:"today_profit"`

	YesterdayRevenue       float64 `json:"yesterday_revenue"`
	YesterdayUpstreamPrice float64 `json:"yesterday_upstream_price"`
	YesterdayUpstreamCost  float64 `json:"yesterday_upstream_cost"`
	YesterdayProfit        float64 `json:"yesterday_profit"`

	TotalRevenue       float64 `json:"total_revenue"`
	TotalUpstreamPrice float64 `json:"total_upstream_price"`
	TotalUpstreamCost  float64 `json:"total_upstream_cost"`
	TotalProfit        float64 `json:"total_profit"`
}

// orangeGroupProfitResponse 分组利润统计响应体。
type orangeGroupProfitResponse struct {
	Items []orangeGroupProfitItemResponse `json:"items"`
	// GeneratedAt RFC3339 时间戳（缓存命中时为首次生成时间）。
	GeneratedAt string `json:"generated_at"`
	Cached      bool   `json:"cached"`
}

// orangeGroupUpstreamRateResponse 倍率更新响应体。
type orangeGroupUpstreamRateResponse struct {
	GroupID                int64   `json:"group_id"`
	UpstreamRateMultiplier float64 `json:"upstream_rate_multiplier"`
}

// OrangeAdminGroupProfit 返回分组利润统计（今天 / 昨天 / 累计）。
// GET /api/v1/admin/orange/groups/profit
//
// 错误口径：400 ids 非法、503 仓储不可用或查询超时、400/500 其它（由 service 错误映射）。
func (h *SettingHandler) OrangeAdminGroupProfit(c *gin.Context) {
	ids, err := service.ParseOrangeGroupProfitIDs(c.Query("ids"))
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	result, err := h.orangeGroupProfitService().Query(orangeGroupProfitRequestContext(c), ids, c.Query("timezone"))
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	items := make([]orangeGroupProfitItemResponse, 0, len(result.Items))
	for _, item := range result.Items {
		items = append(items, orangeGroupProfitItemResponse{
			GroupID:                item.GroupID,
			UpstreamRateMultiplier: item.UpstreamRateMultiplier,
			TodayRevenue:           item.TodayRevenue,
			TodayUpstreamPrice:     item.TodayUpstreamPrice,
			TodayUpstreamCost:      item.TodayUpstreamCost,
			TodayProfit:            item.TodayProfit,
			YesterdayRevenue:       item.YesterdayRevenue,
			YesterdayUpstreamPrice: item.YesterdayUpstreamPrice,
			YesterdayUpstreamCost:  item.YesterdayUpstreamCost,
			YesterdayProfit:        item.YesterdayProfit,
			TotalRevenue:           item.TotalRevenue,
			TotalUpstreamPrice:     item.TotalUpstreamPrice,
			TotalUpstreamCost:      item.TotalUpstreamCost,
			TotalProfit:            item.TotalProfit,
		})
	}

	response.Success(c, orangeGroupProfitResponse{
		Items:       items,
		GeneratedAt: result.GeneratedAt.Format(time.RFC3339),
		Cached:      result.Cached,
	})
}

// OrangeAdminUpdateGroupUpstreamRate 更新单个分组的上游成本倍率。
// PUT /api/v1/admin/orange/groups/:id/upstream-rate
//
// 错误口径：400 分组 ID / 倍率非法、404 分组不存在、503 仓储不可用、500 其它。
func (h *SettingHandler) OrangeAdminUpdateGroupUpstreamRate(c *gin.Context) {
	groupID, err := service.ParseOrangeGroupProfitGroupID(c.Param("id"))
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	rate, err := parseOrangeGroupUpstreamRatePayload(c)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	stored, err := h.orangeGroupProfitService().UpdateUpstreamRate(
		orangeGroupProfitRequestContext(c), groupID, rate)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	response.Success(c, orangeGroupUpstreamRateResponse{
		GroupID:                groupID,
		UpstreamRateMultiplier: stored,
	})
}

// orangeGroupProfitService 返回与当前 handler 绑定的分组利润服务（首次调用时创建并缓存）。
func (h *SettingHandler) orangeGroupProfitService() *service.OrangeGroupProfitService {
	if h == nil {
		return service.NewOrangeGroupProfitService(nil)
	}
	return orangeGroupProfitServiceFor(h.settingService)
}

// orangeGroupProfitServiceFor 返回与给定 SettingService 绑定的分组利润服务单例。
func orangeGroupProfitServiceFor(settingService *service.SettingService) *service.OrangeGroupProfitService {
	if settingService == nil {
		return service.NewOrangeGroupProfitService(nil)
	}
	if cached, ok := orangeGroupProfitServiceRegistry.Load(settingService); ok {
		if svc, ok := cached.(*service.OrangeGroupProfitService); ok && svc != nil {
			return svc
		}
	}
	created := service.NewOrangeGroupProfitService(settingService)
	if actual, loaded := orangeGroupProfitServiceRegistry.LoadOrStore(settingService, created); loaded {
		if svc, ok := actual.(*service.OrangeGroupProfitService); ok && svc != nil {
			return svc
		}
	}
	return created
}

// parseOrangeGroupUpstreamRatePayload 解析倍率请求体：必须是形如
// {"upstream_rate_multiplier": 0.85} 的 JSON 对象，且字段为数字（缺字段 / 字符串 / null 一律 400）。
// 取值范围与精度由 service.NormalizeOrangeGroupUpstreamRate 校验。
func parseOrangeGroupUpstreamRatePayload(c *gin.Context) (float64, error) {
	if c == nil || c.Request == nil || c.Request.Body == nil {
		return 0, service.ErrOrangeGroupProfitInvalidPayload
	}

	var payload struct {
		UpstreamRateMultiplier *float64 `json:"upstream_rate_multiplier"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(c.Writer, c.Request.Body, orangeGroupUpstreamRateBodyMaxBytes))
	if err := decoder.Decode(&payload); err != nil {
		return 0, service.ErrOrangeGroupProfitInvalidPayload.WithCause(err)
	}
	if payload.UpstreamRateMultiplier == nil {
		return 0, service.ErrOrangeGroupProfitInvalidPayload
	}
	return *payload.UpstreamRateMultiplier, nil
}

// orangeGroupProfitRequestContext 取请求 context（测试里可能没有 Request，回落 Background）。
func orangeGroupProfitRequestContext(c *gin.Context) context.Context {
	if c != nil && c.Request != nil {
		return c.Request.Context()
	}
	return context.Background()
}
