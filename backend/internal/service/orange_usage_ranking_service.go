// Orange 特有：用量排行（Token 排行榜）服务层。
//
// 职责：
//   - 定义排行维度（用户榜 / 模型榜）、时间范围（今日 / 本周 / 本月）、行与结果类型；
//   - 时间窗计算：按 IANA 时区的自然日/周/月，非法时区回落服务端时区；
//   - 60s 进程内缓存 + singleflight（同一时间窗只打一次库）+ 8s 查询超时；
//   - limit ≤ 50 / offset ≤ 1000 归一化（handler 侧非法参数已按 400 拒绝）；
//   - 开关关闭时返回可识别错误 ErrOrangeRankingDisabled（handler 回 403）。
//
// 与上游的隔离方式：排行仓储接口 OrangeRankingRepository 是 Orange 自有接口，
// 用运行时类型断言从 UsageService 取用，**不加入**上游 UsageLogRepository
// （否则要连带修改上游 6 处 stub 与测试文件）。
package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/timezone"
	"golang.org/x/sync/singleflight"
)

// 排行查询的取值约束（与 文档/功能分析/2026-11-22-用量排行与系统设置功能开关恢复分析.md §7.4 一致）。
const (
	// OrangeRankingDefaultLimit 默认每页条数。
	OrangeRankingDefaultLimit = 10
	// OrangeRankingMaxLimit 每页条数上限（超过按上限截断）。
	OrangeRankingMaxLimit = 50
	// OrangeRankingMaxOffset 偏移量上限（超过按上限截断）。
	OrangeRankingMaxOffset = 1000

	// orangeRankingCacheTTL 结果缓存时长：排行是全窗口聚合，缓存用于吸收重复打开页面。
	orangeRankingCacheTTL = 60 * time.Second
	// orangeRankingQueryTimeout 单次聚合查询超时（大表上避免长时间占用连接）。
	orangeRankingQueryTimeout = 8 * time.Second
	// orangeRankingCacheMaxEntries 缓存条目上限，超过后先清过期、再整体清空（简单兜底）。
	orangeRankingCacheMaxEntries = 512
)

// OrangeRankingDimension 排行维度。
type OrangeRankingDimension string

const (
	// OrangeRankingDimensionUser 用户榜（按用户聚合 token，邮箱在 handler 脱敏）。
	OrangeRankingDimensionUser OrangeRankingDimension = "user"
	// OrangeRankingDimensionModel 模型榜（按请求模型维度聚合 token，不含用户信息）。
	OrangeRankingDimensionModel OrangeRankingDimension = "model"
)

// OrangeRankingRange 排行时间范围。
type OrangeRankingRange string

const (
	// OrangeRankingRangeToday 今日。
	OrangeRankingRangeToday OrangeRankingRange = "today"
	// OrangeRankingRangeWeek 本周（周一起，查询窗口截止到今天）。
	OrangeRankingRangeWeek OrangeRankingRange = "week"
	// OrangeRankingRangeMonth 本月（1 号起，查询窗口截止到今天）。
	OrangeRankingRangeMonth OrangeRankingRange = "month"
)

var (
	// ErrOrangeRankingDisabled 功能开关已关闭（handler 回 403，前端据此显示"功能已关闭"）。
	ErrOrangeRankingDisabled = infraerrors.Forbidden("ORANGE_TOKEN_RANKING_DISABLED", "token ranking is disabled by administrator")
	// ErrOrangeRankingUnavailable 排行仓储不可用（未装配或仓储未就绪）。
	ErrOrangeRankingUnavailable = infraerrors.ServiceUnavailable("ORANGE_RANKING_UNAVAILABLE", "orange token ranking is unavailable")
	// ErrOrangeRankingTimeout 聚合查询超时。
	ErrOrangeRankingTimeout = infraerrors.ServiceUnavailable("ORANGE_RANKING_TIMEOUT", "token ranking query timed out, please retry later")
	// ErrOrangeRankingInvalidDimension 维度参数非法。
	ErrOrangeRankingInvalidDimension = infraerrors.BadRequest("ORANGE_RANKING_INVALID_DIMENSION", "invalid dimension, expected user or model")
	// ErrOrangeRankingInvalidRange 时间范围参数非法。
	ErrOrangeRankingInvalidRange = infraerrors.BadRequest("ORANGE_RANKING_INVALID_RANGE", "invalid range, expected today, week or month")
	// ErrOrangeRankingInvalidLimit 每页条数参数非法（非整数）。
	ErrOrangeRankingInvalidLimit = infraerrors.BadRequest("ORANGE_RANKING_INVALID_LIMIT", "invalid limit")
	// ErrOrangeRankingInvalidOffset 偏移量参数非法（非整数）。
	ErrOrangeRankingInvalidOffset = infraerrors.BadRequest("ORANGE_RANKING_INVALID_OFFSET", "invalid offset")
)

// OrangeRankingRow 排行行（仓储 → 服务）。
// Email 为未脱敏原始值，脱敏在 handler 完成；Model 仅在模型榜填充。
type OrangeRankingRow struct {
	Rank         int64
	UserID       int64
	Email        string
	Model        string
	Requests     int64
	InputTokens  int64
	OutputTokens int64
	CacheTokens  int64
	TotalTokens  int64
}

// OrangeRankingQuery 归一化后的排行查询参数。
type OrangeRankingQuery struct {
	Dimension OrangeRankingDimension
	Range     OrangeRankingRange
	// Timezone 用户时区（IANA 名）；为空或非法时按服务端时区处理。
	Timezone string
	Limit    int
	Offset   int
}

// OrangeRankingResult 排行结果。
type OrangeRankingResult struct {
	Dimension OrangeRankingDimension
	Range     OrangeRankingRange
	// Timezone 实际生效的时区名（非法入参已回落服务端时区）。
	Timezone string
	// PeriodStart / PeriodEnd 展示用自然区间（周榜到本周日、月榜到月末）。
	PeriodStart time.Time
	PeriodEnd   time.Time
	// WindowStart / WindowEnd 真实查询窗口（WindowEnd 排他，恒为"明天 00:00"）。
	WindowStart time.Time
	WindowEnd   time.Time
	// GeneratedAt 结果生成时间（缓存命中时为首次生成时间）。
	GeneratedAt time.Time
	// Cached 本次响应是否来自缓存。
	Cached bool
	// HasMore 是否还有下一页（用"多取 1 行"判定，不做 COUNT）。
	HasMore bool
	Items   []OrangeRankingRow
}

// OrangeFeatureGate Orange 功能开关读取器（由 OrangeSettingsService 实现）。
type OrangeFeatureGate interface {
	IsEnabled(ctx context.Context, key string) (bool, error)
}

// OrangeRankingRepository Orange 自有排行仓储接口（不加入上游 UsageLogRepository）。
type OrangeRankingRepository interface {
	GetOrangeUserTokenRanking(ctx context.Context, startTime, endTime time.Time, limit, offset int) ([]OrangeRankingRow, error)
	GetOrangeModelTokenRanking(ctx context.Context, startTime, endTime time.Time, limit, offset int) ([]OrangeRankingRow, error)
}

// orangeRankingCacheEntry 进程内缓存条目。
type orangeRankingCacheEntry struct {
	result    *OrangeRankingResult
	expiresAt time.Time
}

// OrangeRankingService 用量排行服务。
type OrangeRankingService struct {
	repo OrangeRankingRepository
	gate OrangeFeatureGate

	mu    sync.Mutex
	cache map[string]*orangeRankingCacheEntry
	sf    singleflight.Group
	now   func() time.Time
}

// NewOrangeRankingService 从既有 UsageService 构造排行服务。
// 用类型断言取 Orange 自有仓储能力；断言失败时 repo 为 nil（排行接口返回 503）。
func NewOrangeRankingService(usage *UsageService, gate OrangeFeatureGate) *OrangeRankingService {
	var repo OrangeRankingRepository
	if usage != nil && usage.usageRepo != nil {
		if r, ok := usage.usageRepo.(OrangeRankingRepository); ok {
			repo = r
		}
	}
	return &OrangeRankingService{
		repo:  repo,
		gate:  gate,
		cache: make(map[string]*orangeRankingCacheEntry),
		now:   time.Now,
	}
}

// ParseOrangeRankingDimension 解析维度参数；空值取默认 user。
func ParseOrangeRankingDimension(raw string) (OrangeRankingDimension, error) {
	switch OrangeRankingDimension(strings.ToLower(strings.TrimSpace(raw))) {
	case "", OrangeRankingDimensionUser:
		return OrangeRankingDimensionUser, nil
	case OrangeRankingDimensionModel:
		return OrangeRankingDimensionModel, nil
	default:
		return "", ErrOrangeRankingInvalidDimension
	}
}

// ParseOrangeRankingRange 解析时间范围参数；空值取默认 today。
func ParseOrangeRankingRange(raw string) (OrangeRankingRange, error) {
	switch OrangeRankingRange(strings.ToLower(strings.TrimSpace(raw))) {
	case "", OrangeRankingRangeToday:
		return OrangeRankingRangeToday, nil
	case OrangeRankingRangeWeek:
		return OrangeRankingRangeWeek, nil
	case OrangeRankingRangeMonth:
		return OrangeRankingRangeMonth, nil
	default:
		return "", ErrOrangeRankingInvalidRange
	}
}

// NormalizeOrangeRankingQuery 校验并归一化查询参数。
// limit 超过上限按上限截断（≤0 取默认 10）；offset 负数归零、超上限截断。
func NormalizeOrangeRankingQuery(dimensionRaw, rangeRaw, userTZ string, limit, offset int) (OrangeRankingQuery, error) {
	dimension, err := ParseOrangeRankingDimension(dimensionRaw)
	if err != nil {
		return OrangeRankingQuery{}, err
	}
	rankingRange, err := ParseOrangeRankingRange(rangeRaw)
	if err != nil {
		return OrangeRankingQuery{}, err
	}
	if limit <= 0 {
		limit = OrangeRankingDefaultLimit
	}
	if limit > OrangeRankingMaxLimit {
		limit = OrangeRankingMaxLimit
	}
	if offset < 0 {
		offset = 0
	}
	if offset > OrangeRankingMaxOffset {
		offset = OrangeRankingMaxOffset
	}

	return OrangeRankingQuery{
		Dimension: dimension,
		Range:     rankingRange,
		Timezone:  strings.TrimSpace(userTZ),
		Limit:     limit,
		Offset:    offset,
	}, nil
}

// Rank 执行排行查询：开关校验 → 时间窗 → 缓存 → singleflight 聚合（8s 超时）。
func (s *OrangeRankingService) Rank(ctx context.Context, query OrangeRankingQuery) (*OrangeRankingResult, error) {
	if s == nil || s.repo == nil {
		return nil, ErrOrangeRankingUnavailable
	}

	// 1) 功能开关：关闭时返回可识别错误，由 handler 映射 403。
	// 读取失败时按默认值（开启）继续，不让面板抖动直接打断排行功能；日志由开关服务在失败窗口起点记录。
	if s.gate != nil {
		enabled, err := s.gate.IsEnabled(ctx, OrangeSettingTokenRankingEnabled)
		if err != nil && !errors.Is(err, ErrOrangeSettingStoreUnavailable) {
			slog.Warn("orange token ranking: feature gate read failed, fallback to default", "error", err)
		}
		if !enabled {
			return nil, ErrOrangeRankingDisabled
		}
	}

	// 2) 归一化（handler 已归一化；这里再做一次防御，避免直接调用方传入越界值）。
	normalized, err := NormalizeOrangeRankingQuery(string(query.Dimension), string(query.Range), query.Timezone, query.Limit, query.Offset)
	if err != nil {
		return nil, err
	}
	query = normalized

	now := s.nowTime()
	tzName, loc := resolveOrangeRankingTimezone(query.Timezone)
	periodStart, periodEnd, windowStart, windowEnd := orangeRankingPeriod(query.Range, loc, now)
	key := orangeRankingCacheKey(query, tzName, windowStart, windowEnd)
	if cached, ok := s.cachedResult(key); ok {
		return cached, nil
	}

	// 多取 1 行用于 has_more 判定（不做 COUNT）。
	fetchLimit := query.Limit + 1

	value, err, _ := s.sf.Do(key, func() (any, error) {
		// 二次检查：排队等待的 goroutine 不应重复查询。
		if cached, ok := s.cachedResult(key); ok {
			return cached, nil
		}
		// 独立 context + 超时：断开请求取消链，客户端断连后查询仍可完成并写入缓存。
		queryCtx, cancel := context.WithTimeout(context.WithoutCancel(orangeRankingContext(ctx)), orangeRankingQueryTimeout)
		defer cancel()

		var (
			rows     []OrangeRankingRow
			queryErr error
		)
		switch query.Dimension {
		case OrangeRankingDimensionModel:
			rows, queryErr = s.repo.GetOrangeModelTokenRanking(queryCtx, windowStart, windowEnd, fetchLimit, query.Offset)
		default:
			rows, queryErr = s.repo.GetOrangeUserTokenRanking(queryCtx, windowStart, windowEnd, fetchLimit, query.Offset)
		}
		if queryErr != nil {
			if errors.Is(queryErr, context.DeadlineExceeded) || errors.Is(queryCtx.Err(), context.DeadlineExceeded) {
				return nil, ErrOrangeRankingTimeout.WithCause(queryErr)
			}
			if errors.Is(queryErr, context.Canceled) {
				return nil, ErrOrangeRankingUnavailable.WithCause(queryErr)
			}
			return nil, fmt.Errorf("query orange token ranking: %w", queryErr)
		}

		hasMore := false
		if len(rows) > query.Limit {
			rows = rows[:query.Limit]
			hasMore = true
		}
		if rows == nil {
			rows = make([]OrangeRankingRow, 0)
		}

		result := &OrangeRankingResult{
			Dimension:   query.Dimension,
			Range:       query.Range,
			Timezone:    tzName,
			PeriodStart: periodStart,
			PeriodEnd:   periodEnd,
			WindowStart: windowStart,
			WindowEnd:   windowEnd,
			GeneratedAt: s.nowTime(),
			HasMore:     hasMore,
			Items:       rows,
		}
		s.storeResult(key, result)
		return result, nil
	})
	if err != nil {
		return nil, err
	}
	result, _ := value.(*OrangeRankingResult)
	if result == nil {
		return nil, ErrOrangeRankingUnavailable
	}
	return result, nil
}

// cachedResult 命中未过期的缓存时返回副本（Cached=true），避免调用方修改共享对象。
func (s *OrangeRankingService) cachedResult(key string) (*OrangeRankingResult, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.cache) == 0 {
		return nil, false
	}
	entry := s.cache[key]
	if entry == nil || entry.result == nil {
		return nil, false
	}
	now := s.nowTime()
	if now.After(entry.expiresAt) {
		delete(s.cache, key)
		return nil, false
	}
	clone := *entry.result
	clone.Cached = true
	return &clone, true
}

// storeResult 写入缓存（存的是 Cached=false 的原始结果）。
func (s *OrangeRankingService) storeResult(key string, result *OrangeRankingResult) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cache == nil {
		s.cache = make(map[string]*orangeRankingCacheEntry)
	}
	if len(s.cache) >= orangeRankingCacheMaxEntries {
		// 先清过期条目；仍超限则整体清空（排行缓存可随时重建，无需精细淘汰）。
		now := s.nowTime()
		for cacheKey, entry := range s.cache {
			if entry == nil || now.After(entry.expiresAt) {
				delete(s.cache, cacheKey)
			}
		}
		if len(s.cache) >= orangeRankingCacheMaxEntries {
			s.cache = make(map[string]*orangeRankingCacheEntry)
		}
	}
	clone := *result
	clone.Cached = false
	s.cache[key] = &orangeRankingCacheEntry{
		result:    &clone,
		expiresAt: s.nowTime().Add(orangeRankingCacheTTL),
	}
}

// nowTime 取当前时间（允许测试注入）。
func (s *OrangeRankingService) nowTime() time.Time {
	if s != nil && s.now != nil {
		return s.now()
	}
	return time.Now()
}

// orangeRankingCacheKey 缓存键：维度 + 范围 + 时区 + 真实窗口 + 分页。
func orangeRankingCacheKey(query OrangeRankingQuery, tzName string, windowStart, windowEnd time.Time) string {
	return fmt.Sprintf("orange:rank:%s:%s:%s:%d:%d:%d:%d",
		query.Dimension, query.Range, tzName, windowStart.Unix(), windowEnd.Unix(), query.Limit, query.Offset)
}

// resolveOrangeRankingTimezone 解析 IANA 时区；为空或非法时回落服务端配置时区。
func resolveOrangeRankingTimezone(userTZ string) (string, *time.Location) {
	userTZ = strings.TrimSpace(userTZ)
	if userTZ != "" {
		if loc, err := time.LoadLocation(userTZ); err == nil {
			return userTZ, loc
		}
	}
	return timezone.Name(), timezone.Location()
}

// orangeRankingPeriod 计算查询窗口与展示区间（均按用户时区的自然日/周/月）。
//
//	windowStart/windowEnd：真实查询窗口，windowEnd 排他且恒为"明天 00:00"；
//	periodStart/periodEnd：展示用自然区间，周榜到本周日、月榜到月末，可能与查询上界不同。
func orangeRankingPeriod(rng OrangeRankingRange, loc *time.Location, now time.Time) (periodStart, periodEnd, windowStart, windowEnd time.Time) {
	local := now.In(loc)
	todayStart := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, loc)
	windowEnd = todayStart.AddDate(0, 0, 1)

	switch rng {
	case OrangeRankingRangeWeek:
		weekday := int(todayStart.Weekday())
		if weekday == 0 {
			weekday = 7 // 周日算第 7 天，周一为一周起点
		}
		periodStart = todayStart.AddDate(0, 0, -(weekday - 1))
		periodEnd = periodStart.AddDate(0, 0, 6)
		windowStart = periodStart
	case OrangeRankingRangeMonth:
		periodStart = time.Date(local.Year(), local.Month(), 1, 0, 0, 0, 0, loc)
		periodEnd = periodStart.AddDate(0, 1, -1)
		windowStart = periodStart
	default:
		periodStart = todayStart
		periodEnd = todayStart
		windowStart = todayStart
	}
	return periodStart, periodEnd, windowStart, windowEnd
}

// orangeRankingContext 归一化 context（nil 时用 Background）。
func orangeRankingContext(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}
