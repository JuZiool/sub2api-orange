// Orange 特有：分组上游成本倍率（orange_group_upstream_rate）与分组利润统计的仓储实现。
//
// 设计约束：
//   - 自有数据只落 Orange 自建表 orange_group_upstream_rate（迁移 8004 创建）：
//     不 ALTER 上游表、不新增列、不写 groups / usage_logs / settings；
//   - 对上游 groups（分组列表、存在性）与 usage_logs（收入/上游原价聚合）只做只读 SELECT；
//   - 不新增 ent 实体、不改 wire 装配：方法挂在 *settingRepository 上，与 orange_settings_repo.go
//     同一收口（Orange 自有裸 SQL 全部复用 ProvideSQLDB 拿到的 *sql.DB），
//     service 层用类型断言取用，不扩展上游 SettingRepository 接口，避免牵连 stub 与上游文件；
//   - 统计口径（旧 Orange）：revenue = SUM(usage_logs.actual_cost)，
//     upstream_price = SUM(usage_logs.total_cost)，upstream_cost = upstream_price * 倍率，
//     profit = revenue - upstream_cost（成本与利润在 service 层计算，仓储只吐原始聚合值）。
package repository

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/lib/pq"
)

// orangeGroupUpstreamRateTable 分组上游成本倍率表（Orange 自建，迁移 8004 创建）。
const orangeGroupUpstreamRateTable = "orange_group_upstream_rate"

// 编译期断言：*settingRepository 必须满足 service 层的 Orange 分组利润仓储契约。
var _ service.OrangeGroupProfitStore = (*settingRepository)(nil)

// orangeGroupProfitListIDsQuery 只读列出未软删分组 ID（升序），用于 ids 缺省时返回全部分组。
const orangeGroupProfitListIDsQuery = `
	SELECT id
	FROM groups
	WHERE deleted_at IS NULL
	ORDER BY id ASC
	LIMIT $1
`

// orangeGroupProfitExistsQuery 只读判断分组是否存在（未软删），用于 PUT 倍率的 404 判定。
const orangeGroupProfitExistsQuery = `
	SELECT EXISTS (
		SELECT 1 FROM groups WHERE id = $1 AND deleted_at IS NULL
	)
`

// orangeGroupUpstreamRateSelectQuery 读 Orange 自建表里的分组倍率（未登记的分组不出现在结果里）。
const orangeGroupUpstreamRateSelectQuery = `
	SELECT group_id, upstream_rate_multiplier::double precision
	FROM ` + orangeGroupUpstreamRateTable + `
	WHERE group_id = ANY($1)
`

// orangeGroupUpstreamRateUpsertQuery 写入/更新单个分组的倍率（只写 Orange 自建表）。
const orangeGroupUpstreamRateUpsertQuery = `
	INSERT INTO ` + orangeGroupUpstreamRateTable + ` (group_id, upstream_rate_multiplier, updated_at)
	VALUES ($1, $2, NOW())
	ON CONFLICT (group_id) DO UPDATE
	SET upstream_rate_multiplier = EXCLUDED.upstream_rate_multiplier,
	    updated_at = NOW()
`

// orangeGroupProfitStatsQuery 单条 SQL 聚合 usage_logs（只读），一次扫描用 FILTER 拆出三个窗口：
//
//	$2 = 今天 00:00（含），$3 = 明天 00:00（排他），$4 = 昨天 00:00（含）
//
// 三个窗口口径：
//   - today：[today_start, tomorrow_start)
//   - yesterday：[yesterday_start, today_start)
//   - total：截至 tomorrow_start 的全部历史（上界排他，与"今天"窗口共用上界，
//     顺带排除时钟漂移产生的未来行）
//
// revenue 取 actual_cost（旧 Orange 口径：实际扣除费用），上游原价取 total_cost（原始总费用）。
// WHERE 上界 created_at < $3 让扫描范围保持有界；分组过滤走 group_id（线上有 (group_id, created_at) 索引）。
const orangeGroupProfitStatsQuery = `
	SELECT
		ul.group_id,
		COALESCE(SUM(ul.actual_cost) FILTER (WHERE ul.created_at >= $2 AND ul.created_at < $3), 0)::double precision AS today_revenue,
		COALESCE(SUM(ul.total_cost)  FILTER (WHERE ul.created_at >= $2 AND ul.created_at < $3), 0)::double precision AS today_upstream_price,
		COALESCE(SUM(ul.actual_cost) FILTER (WHERE ul.created_at >= $4 AND ul.created_at < $2), 0)::double precision AS yesterday_revenue,
		COALESCE(SUM(ul.total_cost)  FILTER (WHERE ul.created_at >= $4 AND ul.created_at < $2), 0)::double precision AS yesterday_upstream_price,
		COALESCE(SUM(ul.actual_cost), 0)::double precision AS total_revenue,
		COALESCE(SUM(ul.total_cost), 0)::double precision AS total_upstream_price
	FROM usage_logs ul
	WHERE ul.group_id = ANY($1)
	  AND ul.created_at < $3
	GROUP BY ul.group_id
`

// OrangeGroupProfitStore 把 *settingRepository 暴露为 service.OrangeGroupProfitStore。
// 第二个返回值表示能力是否可用（ent 客户端缺失时返回 false → service 回 503）。
func (r *settingRepository) OrangeGroupProfitStore() (service.OrangeGroupProfitStore, bool) {
	if r == nil || r.client == nil {
		return nil, false
	}
	return r, true
}

// OrangeListGroupIDs 只读列出未软删的分组 ID（升序，最多 limit 个）。
func (r *settingRepository) OrangeListGroupIDs(ctx context.Context, limit int) (groupIDs []int64, err error) {
	if limit <= 0 {
		return nil, fmt.Errorf("orange group profit: invalid group id limit %d", limit)
	}
	db, err := r.orangeDB()
	if err != nil {
		return nil, err
	}

	rows, err := db.QueryContext(ctx, orangeGroupProfitListIDsQuery, limit)
	if err != nil {
		return nil, err
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil && err == nil {
			err = closeErr
			groupIDs = nil
		}
	}()

	groupIDs = make([]int64, 0, limit)
	for rows.Next() {
		var groupID int64
		if err = rows.Scan(&groupID); err != nil {
			return nil, err
		}
		groupIDs = append(groupIDs, groupID)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	return groupIDs, nil
}

// OrangeGroupExists 只读判断分组是否存在（未软删）。
func (r *settingRepository) OrangeGroupExists(ctx context.Context, groupID int64) (bool, error) {
	if groupID <= 0 {
		return false, fmt.Errorf("orange group profit: invalid group id %d", groupID)
	}
	db, err := r.orangeDB()
	if err != nil {
		return false, err
	}

	var exists bool
	if err := db.QueryRowContext(ctx, orangeGroupProfitExistsQuery, groupID).Scan(&exists); err != nil {
		return false, err
	}
	return exists, nil
}

// OrangeLoadGroupUpstreamRates 读取给定分组的上游成本倍率。
// 契约：无倍率记录的分组视作 1.0，因此返回 map 会先按 1.0 预填全部请求的分组，
// 再用 orange_group_upstream_rate 里的实际值覆盖（service 层仍有同等默认值作为兜底）。
func (r *settingRepository) OrangeLoadGroupUpstreamRates(ctx context.Context, groupIDs []int64) (rates map[int64]float64, err error) {
	rates = make(map[int64]float64, len(groupIDs))
	if len(groupIDs) == 0 {
		return rates, nil
	}
	if err := orangeGroupProfitValidateGroupIDs(groupIDs); err != nil {
		return nil, err
	}
	for _, groupID := range groupIDs {
		rates[groupID] = service.OrangeGroupUpstreamRateDefault
	}
	db, err := r.orangeDB()
	if err != nil {
		return nil, err
	}

	rows, err := db.QueryContext(ctx, orangeGroupUpstreamRateSelectQuery, pq.Array(groupIDs))
	if err != nil {
		return nil, err
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil && err == nil {
			err = closeErr
			rates = nil
		}
	}()

	for rows.Next() {
		var (
			groupID int64
			rate    float64
		)
		if err = rows.Scan(&groupID, &rate); err != nil {
			return nil, err
		}
		rates[groupID] = rate
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	return rates, nil
}

// OrangeUpsertGroupUpstreamRate 写入/更新单个分组的上游成本倍率（只写 Orange 自建表）。
// 分组是否存在由 service 层先做只读校验（不存在回 404），这里不再 JOIN 上游表。
func (r *settingRepository) OrangeUpsertGroupUpstreamRate(ctx context.Context, groupID int64, rate float64) error {
	if err := orangeGroupProfitValidateGroupIDs([]int64{groupID}); err != nil {
		return err
	}
	if math.IsNaN(rate) || math.IsInf(rate, 0) || rate < 0 {
		return fmt.Errorf("orange group profit: invalid upstream rate %v for group %d", rate, groupID)
	}
	db, err := r.orangeDB()
	if err != nil {
		return err
	}

	if _, err := db.ExecContext(ctx, orangeGroupUpstreamRateUpsertQuery, groupID, rate); err != nil {
		return fmt.Errorf("orange group profit: upsert upstream rate for group %d: %w", groupID, err)
	}
	return nil
}

// OrangeGroupProfitStats 单条 SQL 聚合 usage_logs（只读），返回每个分组三窗口的收入与上游原价。
// 没有任何用量记录的分组不会出现在返回 map 中（由 service 层按全 0 处理）。
func (r *settingRepository) OrangeGroupProfitStats(
	ctx context.Context,
	groupIDs []int64,
	todayStart, tomorrowStart, yesterdayStart time.Time,
) (stats map[int64]service.OrangeGroupProfitStats, err error) {
	stats = make(map[int64]service.OrangeGroupProfitStats, len(groupIDs))
	if len(groupIDs) == 0 {
		return stats, nil
	}
	if err := orangeGroupProfitValidateGroupIDs(groupIDs); err != nil {
		return nil, err
	}
	if !tomorrowStart.After(todayStart) || !todayStart.After(yesterdayStart) {
		return nil, fmt.Errorf(
			"orange group profit: invalid windows yesterday=%s today=%s tomorrow=%s",
			yesterdayStart, todayStart, tomorrowStart)
	}
	db, err := r.orangeDB()
	if err != nil {
		return nil, err
	}

	rows, err := db.QueryContext(ctx, orangeGroupProfitStatsQuery,
		pq.Array(groupIDs), todayStart, tomorrowStart, yesterdayStart)
	if err != nil {
		return nil, err
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil && err == nil {
			err = closeErr
			stats = nil
		}
	}()

	for rows.Next() {
		var (
			groupID int64
			row     service.OrangeGroupProfitStats
		)
		if err = rows.Scan(
			&groupID,
			&row.TodayRevenue,
			&row.TodayUpstreamPrice,
			&row.YesterdayRevenue,
			&row.YesterdayUpstreamPrice,
			&row.TotalRevenue,
			&row.TotalUpstreamPrice,
		); err != nil {
			return nil, err
		}
		stats[groupID] = row
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	return stats, nil
}

// orangeGroupProfitValidateGroupIDs 校验分组 ID 列表（>0），避免把明显非法的参数发到数据库。
func orangeGroupProfitValidateGroupIDs(groupIDs []int64) error {
	if len(groupIDs) == 0 {
		return errors.New("orange group profit: group ids are required")
	}
	for _, groupID := range groupIDs {
		if groupID <= 0 {
			return fmt.Errorf("orange group profit: invalid group id %d", groupID)
		}
	}
	return nil
}
