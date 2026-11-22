// Orange 特有：用量排行（Token 排行榜）的只读聚合查询。
//
// 设计约束（见 文档/功能分析/2026-11-22-用量排行与系统设置功能开关恢复分析.md §7.3.1）：
//   - 只对上游 usage_logs / users 做只读 SELECT 聚合：不写入、不新增列、不新增索引；
//   - 复用同包既有资产，避免上游改动：
//   - resolveModelDimensionExpressionWithAlias(usagestats.ModelSourceRequested, "ul")
//     与表达式索引 idx_usage_logs_effective_requested_model_created 完全一致；
//   - usageLogSuccessFilterUL（actual_cost > 0）排除失败请求占位行，对齐上游统计口径；
//   - 行类型 service.OrangeRankingRow 定义在 service 包（repository 已依赖 service，无循环导入）。
package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

// 编译期断言：*usageLogRepository 必须满足 service 层的 Orange 排行仓储契约，
// 避免仓库方法签名漂移后服务层只能在运行时才发现（类型断言失败会退化成 503）。
var _ service.OrangeRankingRepository = (*usageLogRepository)(nil)

// orangeRankingUserQuery 用户榜：时间窗内按用户聚合 token 用量。
// 参数：$1=窗口起点（含），$2=窗口终点（排他），$3=limit，$4=offset。
//
// 排序键 "total_tokens DESC, user_id ASC" 带决定性尾键，保证并列名次可复现；
// 名次由窗口内的 ROW_NUMBER() 计算，因此分页时 rank 依旧是全局名次。
const orangeRankingUserQuery = `
	WITH user_usage AS (
		SELECT
			ul.user_id                                                        AS user_id,
			COALESCE(u.email, '')                                             AS email,
			COUNT(*)                                                          AS requests,
			COALESCE(SUM(ul.input_tokens), 0)                                 AS input_tokens,
			COALESCE(SUM(ul.output_tokens), 0)                                AS output_tokens,
			COALESCE(SUM(ul.cache_creation_tokens + ul.cache_read_tokens), 0)  AS cache_tokens,
			COALESCE(SUM(ul.input_tokens + ul.output_tokens
				+ ul.cache_creation_tokens + ul.cache_read_tokens), 0)         AS total_tokens
		FROM usage_logs ul
		JOIN users u ON u.id = ul.user_id AND u.deleted_at IS NULL
		WHERE ul.created_at >= $1 AND ul.created_at < $2
			AND ` + usageLogSuccessFilterUL + `
		GROUP BY ul.user_id, u.email
	), ranked AS (
		SELECT
			ROW_NUMBER() OVER (ORDER BY total_tokens DESC, user_id ASC) AS rank,
			user_id, email, requests, input_tokens, output_tokens, cache_tokens, total_tokens
		FROM user_usage
	)
	SELECT rank, user_id, email, requests, input_tokens, output_tokens, cache_tokens, total_tokens
	FROM ranked
	ORDER BY rank ASC
	LIMIT $3 OFFSET $4
`

// orangeRankingModelQuery 模型榜：时间窗内按模型维度聚合 token 用量。
// 参数：$1=窗口起点（含），$2=窗口终点（排他），$3=limit，$4=offset。
// 模型维度表达式由 resolveModelDimensionExpressionWithAlias 生成（仅代码内白名单表达式，
// 不含任何外部输入）；模型榜不含用户信息。
const orangeRankingModelQueryTemplate = `
	WITH model_usage AS (
		SELECT
			%s                                                                AS model,
			COUNT(*)                                                          AS requests,
			COALESCE(SUM(ul.input_tokens), 0)                                 AS input_tokens,
			COALESCE(SUM(ul.output_tokens), 0)                                AS output_tokens,
			COALESCE(SUM(ul.cache_creation_tokens + ul.cache_read_tokens), 0)  AS cache_tokens,
			COALESCE(SUM(ul.input_tokens + ul.output_tokens
				+ ul.cache_creation_tokens + ul.cache_read_tokens), 0)         AS total_tokens
		FROM usage_logs ul
		WHERE ul.created_at >= $1 AND ul.created_at < $2
			AND ` + usageLogSuccessFilterUL + `
		GROUP BY 1
	), ranked AS (
		SELECT
			ROW_NUMBER() OVER (ORDER BY total_tokens DESC, model ASC) AS rank,
			model, requests, input_tokens, output_tokens, cache_tokens, total_tokens
		FROM model_usage
	)
	SELECT rank, model, requests, input_tokens, output_tokens, cache_tokens, total_tokens
	FROM ranked
	ORDER BY rank ASC
	LIMIT $3 OFFSET $4
`

// GetOrangeUserTokenRanking 返回时间窗内按用户聚合的 Token 用量排行（只读 usage_logs + users）。
// limit / offset 由 service 层归一化（limit 已按「多取 1 行」放量，用于 has_more 判定）。
func (r *usageLogRepository) GetOrangeUserTokenRanking(
	ctx context.Context,
	startTime, endTime time.Time,
	limit, offset int,
) (results []service.OrangeRankingRow, err error) {
	if err = orangeRankingValidateArgs(r, startTime, endTime, limit, offset); err != nil {
		return nil, err
	}

	rows, err := r.sql.QueryContext(ctx, orangeRankingUserQuery, startTime, endTime, limit, offset)
	if err != nil {
		return nil, err
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil && err == nil {
			err = closeErr
			results = nil
		}
	}()

	results = make([]service.OrangeRankingRow, 0, limit)
	for rows.Next() {
		var row service.OrangeRankingRow
		if err = rows.Scan(
			&row.Rank,
			&row.UserID,
			&row.Email,
			&row.Requests,
			&row.InputTokens,
			&row.OutputTokens,
			&row.CacheTokens,
			&row.TotalTokens,
		); err != nil {
			return nil, err
		}
		results = append(results, row)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	return results, nil
}

// GetOrangeModelTokenRanking 返回时间窗内按模型维度聚合的 Token 用量排行（只读 usage_logs）。
func (r *usageLogRepository) GetOrangeModelTokenRanking(
	ctx context.Context,
	startTime, endTime time.Time,
	limit, offset int,
) (results []service.OrangeRankingRow, err error) {
	if err = orangeRankingValidateArgs(r, startTime, endTime, limit, offset); err != nil {
		return nil, err
	}

	// 仅拼接代码内常量表达式（不含任何用户输入），limit/offset 走参数占位符。
	query := fmt.Sprintf(orangeRankingModelQueryTemplate,
		resolveModelDimensionExpressionWithAlias(usagestats.ModelSourceRequested, "ul"))

	rows, err := r.sql.QueryContext(ctx, query, startTime, endTime, limit, offset)
	if err != nil {
		return nil, err
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil && err == nil {
			err = closeErr
			results = nil
		}
	}()

	results = make([]service.OrangeRankingRow, 0, limit)
	for rows.Next() {
		var row service.OrangeRankingRow
		if err = rows.Scan(
			&row.Rank,
			&row.Model,
			&row.Requests,
			&row.InputTokens,
			&row.OutputTokens,
			&row.CacheTokens,
			&row.TotalTokens,
		); err != nil {
			return nil, err
		}
		results = append(results, row)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	return results, nil
}

// orangeRankingValidateArgs 做最小入参校验，避免把明显非法的窗口/分页参数发到数据库。
func orangeRankingValidateArgs(r *usageLogRepository, startTime, endTime time.Time, limit, offset int) error {
	if r == nil || r.sql == nil {
		return errors.New("orange token ranking: usage log repository is not ready")
	}
	if !endTime.After(startTime) {
		return fmt.Errorf("orange token ranking: invalid time window [%s, %s)", startTime, endTime)
	}
	if limit <= 0 {
		return fmt.Errorf("orange token ranking: invalid limit %d", limit)
	}
	if offset < 0 {
		return fmt.Errorf("orange token ranking: invalid offset %d", offset)
	}
	return nil
}
