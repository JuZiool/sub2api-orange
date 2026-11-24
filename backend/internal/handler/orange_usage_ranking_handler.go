// Orange 特有：用量排行（Token 排行榜）的 HTTP 处理器。
//
// 端点：GET /api/v1/usage/ranking（需登录）
//
// 设计说明：
//   - 为了零改动上游 usage_handler.go，处理器以「同包扩展方法」形式加在既有 *UsageHandler 上；
//   - 排行服务实例放在包级注册表里按 handler 复用（60s 结果缓存必须命中同一实例），
//     上游 UsageHandler 结构体因此不需要新增字段；
//   - 邮箱按旧 Orange 的脱敏规则处理，响应不返回任何费用字段（避免暴露全站营收）。
package handler

import (
	"errors"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
)

// orangeRankingServiceRegistry 按 UsageHandler 实例复用 Orange 排行服务。
// 进程内单实例才能让 60s 结果缓存与 singleflight 生效。
var orangeRankingServiceRegistry sync.Map // map[*UsageHandler]*service.OrangeRankingService

// orangeRankingItem 排行榜条目（用户榜带 user_id/email，模型榜带 model）。
type orangeRankingItem struct {
	Rank         int64  `json:"rank"`
	UserID       int64  `json:"user_id,omitempty"`
	Email        string `json:"email,omitempty"`
	Model        string `json:"model,omitempty"`
	Requests     int64  `json:"requests"`
	InputTokens  int64  `json:"input_tokens"`
	OutputTokens int64  `json:"output_tokens"`
	CacheTokens  int64  `json:"cache_tokens"`
	TotalTokens  int64  `json:"total_tokens"`
}

// orangeRankingResponse 排行榜响应体。
// period_start/period_end 是展示用自然区间；window_end 是真实查询上界（排他）。
type orangeRankingResponse struct {
	Dimension   string              `json:"dimension"`
	Range       string              `json:"range"`
	Timezone    string              `json:"timezone"`
	PeriodStart string              `json:"period_start"`
	PeriodEnd   string              `json:"period_end"`
	WindowEnd   string              `json:"window_end"`
	GeneratedAt string              `json:"generated_at"`
	Cached      bool                `json:"cached"`
	HasMore     bool                `json:"has_more"`
	Items       []orangeRankingItem `json:"items"`
}

// OrangeRanking 返回全站 Token 用量排行（用户榜 / 模型榜，支持时间范围与分页）。
// GET /api/v1/usage/ranking
//
// 错误口径：401 未登录、403 开关关闭、400 参数非法、503 排行仓储不可用或查询超时、500 其它。
func (h *UsageHandler) OrangeRanking(c *gin.Context) {
	if _, ok := middleware2.GetAuthSubjectFromContext(c); !ok {
		response.Unauthorized(c, "User not authenticated")
		return
	}

	limit, offset, ok := parseOrangeRankingPagination(c)
	if !ok {
		return
	}

	query, err := service.NormalizeOrangeRankingQuery(
		c.Query("dimension"),
		c.Query("range"),
		c.Query("timezone"),
		limit,
		offset,
	)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	result, err := h.orangeRankingService().Rank(c.Request.Context(), query)
	if err != nil {
		// 开关关闭：返回可识别的 403，前端据此显示"功能已关闭"。
		if errors.Is(err, service.ErrOrangeRankingDisabled) {
			response.Forbidden(c, "Token ranking is disabled by administrator")
			return
		}
		response.ErrorFrom(c, err)
		return
	}

	items := make([]orangeRankingItem, 0, len(result.Items))
	for _, row := range result.Items {
		item := orangeRankingItem{
			Rank:         row.Rank,
			Requests:     row.Requests,
			InputTokens:  row.InputTokens,
			OutputTokens: row.OutputTokens,
			CacheTokens:  row.CacheTokens,
			TotalTokens:  row.TotalTokens,
		}
		if result.Dimension == service.OrangeRankingDimensionModel {
			item.Model = row.Model
		} else {
			item.UserID = row.UserID
			item.Email = maskOrangeRankingEmail(row.Email)
		}
		items = append(items, item)
	}

	response.Success(c, orangeRankingResponse{
		Dimension:   string(result.Dimension),
		Range:       string(result.Range),
		Timezone:    result.Timezone,
		PeriodStart: result.PeriodStart.Format("2006-01-02"),
		PeriodEnd:   result.PeriodEnd.Format("2006-01-02"),
		WindowEnd:   result.WindowEnd.Format("2006-01-02"),
		GeneratedAt: result.GeneratedAt.Format(time.RFC3339),
		Cached:      result.Cached,
		HasMore:     result.HasMore,
		Items:       items,
	})
}

// orangeRankingService 返回与当前 handler 绑定的排行服务（首次调用时创建并缓存）。
func (h *UsageHandler) orangeRankingService() *service.OrangeRankingService {
	if h == nil {
		return service.NewOrangeRankingService(nil, nil)
	}
	if cached, ok := orangeRankingServiceRegistry.Load(h); ok {
		if svc, ok := cached.(*service.OrangeRankingService); ok && svc != nil {
			return svc
		}
	}
	// 开关门禁与 Orange 开关服务共用同一实例（含 30s 缓存）。
	created := service.NewOrangeRankingService(h.usageService, orangeSettingsServiceFor(h.settingService))
	if actual, loaded := orangeRankingServiceRegistry.LoadOrStore(h, created); loaded {
		if svc, ok := actual.(*service.OrangeRankingService); ok && svc != nil {
			return svc
		}
	}
	return created
}

// parseOrangeRankingPagination 解析 limit/offset（非整数 → 400，越界由 service 归一化）。
func parseOrangeRankingPagination(c *gin.Context) (limit, offset int, ok bool) {
	limit = service.OrangeRankingDefaultLimit
	if raw := strings.TrimSpace(c.Query("limit")); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil {
			response.ErrorFrom(c, service.ErrOrangeRankingInvalidLimit)
			return 0, 0, false
		}
		limit = value
	}
	if raw := strings.TrimSpace(c.Query("offset")); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil {
			response.ErrorFrom(c, service.ErrOrangeRankingInvalidOffset)
			return 0, 0, false
		}
		offset = value
	}
	return limit, offset, true
}

// maskOrangeRankingEmail 邮箱脱敏（沿用旧 Orange 语义，按 rune 处理 Unicode 本地名）：
//   - 无法按 @ 拆分 → "***"；
//   - 本地名 1 个字符 → "x***@domain"；
//   - 2–5 个字符 → "首***尾@domain"；
//   - 超过 5 个字符 → "前 3***后 2@domain"。
func maskOrangeRankingEmail(email string) string {
	email = strings.TrimSpace(email)
	local, domain, ok := strings.Cut(email, "@")
	if !ok || local == "" || domain == "" {
		return "***"
	}
	runes := []rune(local)
	switch {
	case len(runes) <= 1:
		return string(runes) + "***@" + domain
	case len(runes) <= 5:
		return string(runes[0]) + "***" + string(runes[len(runes)-1]) + "@" + domain
	default:
		return string(runes[:3]) + "***" + string(runes[len(runes)-2:]) + "@" + domain
	}
}
