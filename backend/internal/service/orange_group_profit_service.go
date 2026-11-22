// Orange 特有：分组利润统计（收入 / 上游原价 / 上游成本 / 利润）与分组上游成本倍率的服务层。
//
// 职责：
//   - 统计口径（旧 Orange）：revenue = SUM(usage_logs.actual_cost)，
//     upstream_price = SUM(usage_logs.total_cost)，
//     upstream_cost = upstream_price * upstream_rate_multiplier，
//     profit = revenue - upstream_cost；
//   - today / yesterday 按用户时区（IANA 名）自然日切窗，total 取全部历史；
//     非法时区回落服务端时区（internal/pkg/timezone）；
//   - ids 解析 / 去重 / 排序 / 上限 200；缺省返回全部分组；
//   - 60s 进程内缓存 + singleflight（同一 ids+时区+当天只打一次库）+ 5s 查询超时；
//   - 倍率写入后立即失效缓存；分组不存在回 ErrGroupNotFound（HTTP 404）。
//
// 与上游的隔离方式：仓储能力由 repository 层通过 OrangeGroupProfitStore() 暴露，
// 本文件用类型断言从 SettingService 的 settingRepo 取用，**不扩展**上游 SettingRepository 接口，
// 因此不牵连任何 stub 或上游文件。
package service

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/timezone"
	"golang.org/x/sync/singleflight"
)

const (
	// OrangeGroupProfitMaxGroups 单次统计涉及的分组数上限（ids 缺省时"全部分组"也按此上限截断）。
	OrangeGroupProfitMaxGroups = 200
	// OrangeGroupUpstreamRateDefault 无倍率记录时的默认倍率（上游成本 = 上游原价）。
	OrangeGroupUpstreamRateDefault = 1.0
	// OrangeGroupUpstreamRateMin 倍率下限（0 表示免费上游）。
	OrangeGroupUpstreamRateMin = 0.0
	// OrangeGroupUpstreamRateMax 倍率上限。
	OrangeGroupUpstreamRateMax = 1000.0

	// orangeGroupUpstreamRateScale 倍率精度：4 位小数（与 orange_group_upstream_rate 的 NUMERIC(10,4) 对齐）。
	orangeGroupUpstreamRateScale = 10000.0
	// orangeGroupUpstreamRateEpsilon 判定"超过 4 位小数"时的浮点容差。
	orangeGroupUpstreamRateEpsilon = 1e-6

	// orangeGroupProfitCacheTTL 结果缓存时长（聚合查询按 ids+时区+当天缓存，吸收管理端重复刷新）。
	orangeGroupProfitCacheTTL = 60 * time.Second
	// orangeGroupProfitQueryTimeout 单次读写超时（大表聚合 + 单行 upsert）。
	orangeGroupProfitQueryTimeout = 5 * time.Second
	// orangeGroupProfitCacheMaxEntries 缓存条目上限（超过后先清过期、再整体清空）。
	orangeGroupProfitCacheMaxEntries = 256
)

var (
	// ErrOrangeGroupProfitUnavailable 仓储不可用（未装配或仓储未就绪）。
	ErrOrangeGroupProfitUnavailable = infraerrors.ServiceUnavailable(
		"ORANGE_GROUP_PROFIT_UNAVAILABLE", "orange group profit store is unavailable")
	// ErrOrangeGroupProfitTimeout 聚合查询超时。
	ErrOrangeGroupProfitTimeout = infraerrors.ServiceUnavailable(
		"ORANGE_GROUP_PROFIT_TIMEOUT", "orange group profit query timed out, please retry later")
	// ErrOrangeGroupProfitInvalidIDs ids 参数非法（非逗号分隔的正整数）。
	ErrOrangeGroupProfitInvalidIDs = infraerrors.BadRequest(
		"ORANGE_GROUP_PROFIT_INVALID_IDS", "invalid ids, expected comma separated positive integers")
	// ErrOrangeGroupProfitInvalidGroupID 路径参数里的分组 ID 非法。
	ErrOrangeGroupProfitInvalidGroupID = infraerrors.BadRequest(
		"ORANGE_GROUP_PROFIT_INVALID_GROUP_ID", "invalid group id")
	// ErrOrangeGroupProfitInvalidRate 倍率取值非法（非数字 / 越界 / 超过 4 位小数）。
	ErrOrangeGroupProfitInvalidRate = infraerrors.BadRequest(
		"ORANGE_GROUP_PROFIT_INVALID_RATE",
		"upstream_rate_multiplier must be a number between 0 and 1000 with at most 4 decimal places")
	// ErrOrangeGroupProfitInvalidPayload 请求体不是合法的倍率 JSON 对象。
	ErrOrangeGroupProfitInvalidPayload = infraerrors.BadRequest(
		"ORANGE_GROUP_PROFIT_INVALID_PAYLOAD",
		`request body must be a JSON object like {"upstream_rate_multiplier": 0.85}`)
)

// OrangeGroupProfitStats 单个分组在一个时间窗内的原始聚合值（仓储 → 服务，未乘倍率）。
// Revenue 对应 SUM(usage_logs.actual_cost)；UpstreamPrice 对应 SUM(usage_logs.total_cost)。
type OrangeGroupProfitStats struct {
	TodayRevenue           float64
	TodayUpstreamPrice     float64
	YesterdayRevenue       float64
	YesterdayUpstreamPrice float64
	TotalRevenue           float64
	TotalUpstreamPrice     float64
}

// OrangeGroupProfitItem 单个分组的利润统计（金额单位与 usage_logs 一致：USD）。
type OrangeGroupProfitItem struct {
	GroupID                int64
	UpstreamRateMultiplier float64

	TodayRevenue       float64
	TodayUpstreamPrice float64
	TodayUpstreamCost  float64
	TodayProfit        float64

	YesterdayRevenue       float64
	YesterdayUpstreamPrice float64
	YesterdayUpstreamCost  float64
	YesterdayProfit        float64

	TotalRevenue       float64
	TotalUpstreamPrice float64
	TotalUpstreamCost  float64
	TotalProfit        float64
}

// OrangeGroupProfitResult 分组利润统计结果。
type OrangeGroupProfitResult struct {
	Items []OrangeGroupProfitItem
	// Timezone 实际生效的时区名（非法入参已回落服务端时区）。
	Timezone string
	// TodayStart 生效时区下"今天"的自然日 00:00（缓存键的一部分）。
	TodayStart time.Time
	// GeneratedAt 结果生成时间（缓存命中时为首次生成时间）。
	GeneratedAt time.Time
	// Cached 本次响应是否来自缓存。
	Cached bool
}

// OrangeGroupProfitStore Orange 分组利润仓储能力（由 repository 层 *settingRepository 实现）。
type OrangeGroupProfitStore interface {
	// OrangeListGroupIDs 只读列出未软删分组 ID（升序，最多 limit 个）。
	OrangeListGroupIDs(ctx context.Context, limit int) ([]int64, error)
	// OrangeGroupExists 只读判断分组是否存在（未软删）。
	OrangeGroupExists(ctx context.Context, groupID int64) (bool, error)
	// OrangeLoadGroupUpstreamRates 读分组倍率；无记录的分组按 1.0 返回
	// （仓储按 1.0 预填，service 层对缺失键再做一次同样兜底）。
	OrangeLoadGroupUpstreamRates(ctx context.Context, groupIDs []int64) (map[int64]float64, error)
	// OrangeUpsertGroupUpstreamRate 写入/更新单个分组倍率（只写 orange_group_upstream_rate）。
	OrangeUpsertGroupUpstreamRate(ctx context.Context, groupID int64, rate float64) error
	// OrangeGroupProfitStats 单条 SQL 聚合 usage_logs（只读）的三窗口收入与上游原价。
	OrangeGroupProfitStats(
		ctx context.Context,
		groupIDs []int64,
		todayStart, tomorrowStart, yesterdayStart time.Time,
	) (map[int64]OrangeGroupProfitStats, error)
}

// orangeGroupProfitStoreProvider 由仓储额外实现，服务层用它做类型断言取 store。
type orangeGroupProfitStoreProvider interface {
	OrangeGroupProfitStore() (OrangeGroupProfitStore, bool)
}

// orangeGroupProfitCacheEntry 进程内缓存条目。
type orangeGroupProfitCacheEntry struct {
	result    *OrangeGroupProfitResult
	expiresAt time.Time
}

// OrangeGroupProfitService 分组利润统计服务。
type OrangeGroupProfitService struct {
	store OrangeGroupProfitStore

	mu    sync.Mutex
	cache map[string]*orangeGroupProfitCacheEntry
	sf    singleflight.Group
	now   func() time.Time
}

// NewOrangeGroupProfitService 基于既有 SettingService 构造分组利润服务。
// 从 SettingService 的 settingRepo 上类型断言 OrangeGroupProfitStore()；断言失败时 store 为 nil，
// 接口返回 503（不影响其它 Orange / 上游功能）。
func NewOrangeGroupProfitService(settingService *SettingService) *OrangeGroupProfitService {
	var store OrangeGroupProfitStore
	if settingService != nil && settingService.settingRepo != nil {
		if provider, ok := settingService.settingRepo.(orangeGroupProfitStoreProvider); ok && provider != nil {
			if s, ok := provider.OrangeGroupProfitStore(); ok && s != nil {
				store = s
			}
		}
	}
	return &OrangeGroupProfitService{
		store: store,
		cache: make(map[string]*orangeGroupProfitCacheEntry),
		now:   time.Now,
	}
}

// ParseOrangeGroupProfitIDs 解析 ids 查询参数（逗号分隔的正整数）。
// 空值（含只含空白）表示"全部符合条件的分组"，返回 nil 且不报错。
func ParseOrangeGroupProfitIDs(raw string) ([]int64, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}

	parts := strings.Split(raw, ",")
	ids := make([]int64, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			return nil, ErrOrangeGroupProfitInvalidIDs.WithMetadata(map[string]string{"ids": raw})
		}
		id, err := strconv.ParseInt(part, 10, 64)
		if err != nil {
			return nil, ErrOrangeGroupProfitInvalidIDs.
				WithMetadata(map[string]string{"ids": raw}).
				WithCause(err)
		}
		ids = append(ids, id)
	}
	return NormalizeOrangeGroupProfitIDs(ids)
}

// ParseOrangeGroupProfitGroupID 解析路径参数里的分组 ID（正整数）。
func ParseOrangeGroupProfitGroupID(raw string) (int64, error) {
	raw = strings.TrimSpace(raw)
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		return 0, ErrOrangeGroupProfitInvalidGroupID.WithMetadata(map[string]string{"group_id": raw})
	}
	return id, nil
}

// NormalizeOrangeGroupProfitIDs 校验分组 ID（>0）、去重、升序，并按上限截断。
// 返回 nil 表示"全部分组"（由调用方决定是否列出真实分组）。
func NormalizeOrangeGroupProfitIDs(ids []int64) ([]int64, error) {
	if len(ids) == 0 {
		return nil, nil
	}

	seen := make(map[int64]struct{}, len(ids))
	normalized := make([]int64, 0, len(ids))
	for _, id := range ids {
		if id <= 0 {
			return nil, ErrOrangeGroupProfitInvalidIDs.
				WithMetadata(map[string]string{"group_id": strconv.FormatInt(id, 10)})
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		normalized = append(normalized, id)
	}
	sort.Slice(normalized, func(i, j int) bool { return normalized[i] < normalized[j] })
	if len(normalized) > OrangeGroupProfitMaxGroups {
		normalized = normalized[:OrangeGroupProfitMaxGroups]
	}
	return normalized, nil
}

// NormalizeOrangeGroupUpstreamRate 校验倍率并归一到 4 位小数。
// 允许范围 [0, 1000]；NaN / Inf / 越界 / 超过 4 位小数一律返回 ErrOrangeGroupProfitInvalidRate。
func NormalizeOrangeGroupUpstreamRate(rate float64) (float64, error) {
	if math.IsNaN(rate) || math.IsInf(rate, 0) {
		return 0, ErrOrangeGroupProfitInvalidRate
	}
	if rate < OrangeGroupUpstreamRateMin || rate > OrangeGroupUpstreamRateMax {
		return 0, ErrOrangeGroupProfitInvalidRate.
			WithMetadata(map[string]string{"upstream_rate_multiplier": strconv.FormatFloat(rate, 'f', -1, 64)})
	}

	scaled := rate * orangeGroupUpstreamRateScale
	rounded := math.Round(scaled)
	if math.Abs(scaled-rounded) > orangeGroupUpstreamRateEpsilon {
		return 0, ErrOrangeGroupProfitInvalidRate.
			WithMetadata(map[string]string{"upstream_rate_multiplier": strconv.FormatFloat(rate, 'f', -1, 64)})
	}

	normalized := rounded / orangeGroupUpstreamRateScale
	if normalized == 0 {
		// 归一 -0，避免响应里出现 "-0"。
		normalized = 0
	}
	return normalized, nil
}

// Query 统计给定分组的利润（ids 为空表示全部分组）。
func (s *OrangeGroupProfitService) Query(ctx context.Context, ids []int64, userTZ string) (*OrangeGroupProfitResult, error) {
	if s == nil || s.store == nil {
		return nil, ErrOrangeGroupProfitUnavailable
	}
	groupIDs, err := NormalizeOrangeGroupProfitIDs(ids)
	if err != nil {
		return nil, err
	}

	now := s.nowTime()
	tzName, loc := resolveOrangeGroupProfitTimezone(userTZ)
	local := now.In(loc)
	todayStart := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, loc)
	tomorrowStart := todayStart.AddDate(0, 0, 1)
	yesterdayStart := todayStart.AddDate(0, 0, -1)

	key := orangeGroupProfitCacheKey(groupIDs, tzName, todayStart)
	if cached, ok := s.cachedResult(key); ok {
		return cached, nil
	}

	value, err, _ := s.sf.Do(key, func() (any, error) {
		// 二次检查：排队等待的 goroutine 不应重复查询。
		if cached, ok := s.cachedResult(key); ok {
			return cached, nil
		}
		// 独立 context + 超时：断开请求取消链，客户端断连后查询仍可完成并写入缓存。
		queryCtx, cancel := context.WithTimeout(
			context.WithoutCancel(orangeGroupProfitContext(ctx)), orangeGroupProfitQueryTimeout)
		defer cancel()

		targets := groupIDs
		if len(targets) == 0 {
			listed, listErr := s.store.OrangeListGroupIDs(queryCtx, OrangeGroupProfitMaxGroups)
			if listErr != nil {
				return nil, orangeGroupProfitQueryError("list groups", listErr)
			}
			targets, listErr = NormalizeOrangeGroupProfitIDs(listed)
			if listErr != nil {
				return nil, listErr
			}
		}

		items := make([]OrangeGroupProfitItem, 0, len(targets))
		if len(targets) > 0 {
			rates, ratesErr := s.store.OrangeLoadGroupUpstreamRates(queryCtx, targets)
			if ratesErr != nil {
				return nil, orangeGroupProfitQueryError("load upstream rates", ratesErr)
			}
			stats, statsErr := s.store.OrangeGroupProfitStats(queryCtx, targets, todayStart, tomorrowStart, yesterdayStart)
			if statsErr != nil {
				return nil, orangeGroupProfitQueryError("load profit stats", statsErr)
			}
			items = buildOrangeGroupProfitItems(targets, rates, stats)
		}

		result := &OrangeGroupProfitResult{
			Items:       items,
			Timezone:    tzName,
			TodayStart:  todayStart,
			GeneratedAt: s.nowTime(),
		}
		s.storeResult(key, result)
		return result, nil
	})
	if err != nil {
		return nil, err
	}
	result, _ := value.(*OrangeGroupProfitResult)
	if result == nil {
		return nil, ErrOrangeGroupProfitUnavailable
	}
	return result, nil
}

// UpdateUpstreamRate 更新单个分组的上游成本倍率；写库后立即失效缓存。
// 分组不存在返回 ErrGroupNotFound（HTTP 404）；返回值是归一化（4 位小数）后实际写库的值。
func (s *OrangeGroupProfitService) UpdateUpstreamRate(ctx context.Context, groupID int64, rate float64) (float64, error) {
	if s == nil || s.store == nil {
		return 0, ErrOrangeGroupProfitUnavailable
	}
	if groupID <= 0 {
		return 0, ErrOrangeGroupProfitInvalidGroupID.
			WithMetadata(map[string]string{"group_id": strconv.FormatInt(groupID, 10)})
	}
	normalized, err := NormalizeOrangeGroupUpstreamRate(rate)
	if err != nil {
		return 0, err
	}

	dbCtx, cancel := context.WithTimeout(orangeGroupProfitContext(ctx), orangeGroupProfitQueryTimeout)
	defer cancel()

	exists, err := s.store.OrangeGroupExists(dbCtx, groupID)
	if err != nil {
		return 0, orangeGroupProfitQueryError("check group", err)
	}
	if !exists {
		return 0, ErrGroupNotFound.WithMetadata(map[string]string{"group_id": strconv.FormatInt(groupID, 10)})
	}
	if err := s.store.OrangeUpsertGroupUpstreamRate(dbCtx, groupID, normalized); err != nil {
		return 0, orangeGroupProfitQueryError("upsert upstream rate", err)
	}

	s.invalidateCache()
	return normalized, nil
}

// buildOrangeGroupProfitItems 按旧 Orange 口径把原始聚合值折算成上游成本与利润。
// 无倍率记录（或倍率非法）按 1.0 处理；无用量记录按全 0 处理。
func buildOrangeGroupProfitItems(
	groupIDs []int64,
	rates map[int64]float64,
	stats map[int64]OrangeGroupProfitStats,
) []OrangeGroupProfitItem {
	items := make([]OrangeGroupProfitItem, 0, len(groupIDs))
	for _, groupID := range groupIDs {
		rate := OrangeGroupUpstreamRateDefault
		if loaded, ok := rates[groupID]; ok && !math.IsNaN(loaded) && !math.IsInf(loaded, 0) && loaded >= 0 {
			rate = loaded
		}
		row := stats[groupID]

		todayCost, todayProfit := orangeGroupProfitWindow(row.TodayRevenue, row.TodayUpstreamPrice, rate)
		yesterdayCost, yesterdayProfit := orangeGroupProfitWindow(row.YesterdayRevenue, row.YesterdayUpstreamPrice, rate)
		totalCost, totalProfit := orangeGroupProfitWindow(row.TotalRevenue, row.TotalUpstreamPrice, rate)

		items = append(items, OrangeGroupProfitItem{
			GroupID:                groupID,
			UpstreamRateMultiplier: rate,
			TodayRevenue:           row.TodayRevenue,
			TodayUpstreamPrice:     row.TodayUpstreamPrice,
			TodayUpstreamCost:      todayCost,
			TodayProfit:            todayProfit,
			YesterdayRevenue:       row.YesterdayRevenue,
			YesterdayUpstreamPrice: row.YesterdayUpstreamPrice,
			YesterdayUpstreamCost:  yesterdayCost,
			YesterdayProfit:        yesterdayProfit,
			TotalRevenue:           row.TotalRevenue,
			TotalUpstreamPrice:     row.TotalUpstreamPrice,
			TotalUpstreamCost:      totalCost,
			TotalProfit:            totalProfit,
		})
	}
	return items
}

// orangeGroupProfitWindow 按旧 Orange 口径计算单个窗口的上游成本与利润：
// 上游成本 = 上游原价 * 倍率；利润 = 收入 - 上游成本。
func orangeGroupProfitWindow(revenue, upstreamPrice, rate float64) (upstreamCost, profit float64) {
	upstreamCost = upstreamPrice * rate
	return upstreamCost, revenue - upstreamCost
}

// cachedResult 命中未过期的缓存时返回副本（Cached=true），避免调用方修改共享对象。
func (s *OrangeGroupProfitService) cachedResult(key string) (*OrangeGroupProfitResult, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.cache) == 0 {
		return nil, false
	}
	entry := s.cache[key]
	if entry == nil || entry.result == nil {
		return nil, false
	}
	if s.nowTime().After(entry.expiresAt) {
		delete(s.cache, key)
		return nil, false
	}
	clone := *entry.result
	clone.Items = append([]OrangeGroupProfitItem(nil), entry.result.Items...)
	clone.Cached = true
	return &clone, true
}

// storeResult 写入缓存（存的是 Cached=false 的原始结果）。
func (s *OrangeGroupProfitService) storeResult(key string, result *OrangeGroupProfitResult) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cache == nil {
		s.cache = make(map[string]*orangeGroupProfitCacheEntry)
	}
	if len(s.cache) >= orangeGroupProfitCacheMaxEntries {
		// 先清过期条目；仍超限则整体清空（利润统计可随时重建，无需精细淘汰）。
		now := s.nowTime()
		for cacheKey, entry := range s.cache {
			if entry == nil || now.After(entry.expiresAt) {
				delete(s.cache, cacheKey)
			}
		}
		if len(s.cache) >= orangeGroupProfitCacheMaxEntries {
			s.cache = make(map[string]*orangeGroupProfitCacheEntry)
		}
	}
	clone := *result
	clone.Items = append([]OrangeGroupProfitItem(nil), result.Items...)
	clone.Cached = false
	s.cache[key] = &orangeGroupProfitCacheEntry{
		result:    &clone,
		expiresAt: s.nowTime().Add(orangeGroupProfitCacheTTL),
	}
}

// invalidateCache 清空进程内缓存（倍率写库后调用，避免返回旧倍率算出的成本/利润）。
func (s *OrangeGroupProfitService) invalidateCache() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cache = make(map[string]*orangeGroupProfitCacheEntry)
}

// nowTime 取当前时间（允许测试注入）。
func (s *OrangeGroupProfitService) nowTime() time.Time {
	if s != nil && s.now != nil {
		return s.now()
	}
	return time.Now()
}

// orangeGroupProfitCacheKey 缓存键：ids（已排序）+ 时区名 + 生效时区的当天 00:00。
// 跨自然日后键自动变化，无需依赖 TTL 淘汰旧窗口数据。
func orangeGroupProfitCacheKey(groupIDs []int64, tzName string, todayStart time.Time) string {
	var builder strings.Builder
	builder.Grow(64 + len(groupIDs)*8)
	builder.WriteString("orange:group-profit:")
	builder.WriteString(tzName)
	builder.WriteByte(':')
	builder.WriteString(strconv.FormatInt(todayStart.Unix(), 10))
	builder.WriteByte(':')
	if len(groupIDs) == 0 {
		builder.WriteString("all")
		return builder.String()
	}
	for index, groupID := range groupIDs {
		if index > 0 {
			builder.WriteByte(',')
		}
		builder.WriteString(strconv.FormatInt(groupID, 10))
	}
	return builder.String()
}

// resolveOrangeGroupProfitTimezone 解析 IANA 时区；为空或非法时回落服务端配置时区。
func resolveOrangeGroupProfitTimezone(userTZ string) (string, *time.Location) {
	userTZ = strings.TrimSpace(userTZ)
	if userTZ != "" {
		if loc, err := time.LoadLocation(userTZ); err == nil {
			return userTZ, loc
		}
	}
	return timezone.Name(), timezone.Location()
}

// orangeGroupProfitQueryError 把仓储/DB 错误归类成可识别的服务错误。
func orangeGroupProfitQueryError(operation string, err error) error {
	if errors.Is(err, context.DeadlineExceeded) {
		return ErrOrangeGroupProfitTimeout.WithCause(err)
	}
	if errors.Is(err, context.Canceled) {
		return ErrOrangeGroupProfitUnavailable.WithCause(err)
	}
	return fmt.Errorf("orange group profit: %s: %w", operation, err)
}

// orangeGroupProfitContext 归一化 context（nil 时用 Background，便于直接调用）。
func orangeGroupProfitContext(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}
