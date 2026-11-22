// Orange 特有：首字延迟拆分表（orange_usage_latency，迁移 8005）的仓储实现。
//
// 设计约束：
//   - 只写 Orange 自建表 orange_usage_latency；usage_logs 等上游表只读，不新增列；
//   - 不新增 ent 实体、不改 wire 装配：方法挂在既有 *usageLogRepository 上
//     （usage log 与延迟记录同源写入，复用同一条 *sql.DB 连接池）；
//   - service 层用类型断言取用（service.OrangeUsageLatencyStore），
//     不扩展上游 UsageLogRepository 接口，避免牵连所有 stub 与上游文件；
//   - 全程 best-effort：写入失败由 service 层记 warn 日志，不向主流程冒泡。
package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

// orangeUsageLatencyTable 首字延迟拆分表（Orange 自建，迁移 8005 创建）。
const orangeUsageLatencyTable = "orange_usage_latency"

// 编译期断言：*usageLogRepository 必须满足 service 层的 Orange 延迟拆分仓储契约。
var _ service.OrangeUsageLatencyStore = (*usageLogRepository)(nil)

// orangeUsageLatencySelectLimit 单次批量查询的 request_id 上限（列表页一页远小于此值）。
const orangeUsageLatencySelectLimit = 500

// orangeUsageLatencyUpsertQuery 写入/更新一条拆分记录（只写 Orange 自建表）。
// request_id 冲突时覆盖两段耗时：同一请求重放/重试时以最后一次采样为准。
const orangeUsageLatencyUpsertQuery = `
	INSERT INTO ` + orangeUsageLatencyTable + ` (request_id, gateway_ms, upstream_ms, created_at)
	VALUES ($1, $2, $3, NOW())
	ON CONFLICT (request_id) DO UPDATE
	SET gateway_ms = EXCLUDED.gateway_ms,
	    upstream_ms = EXCLUDED.upstream_ms,
	    created_at = EXCLUDED.created_at
`

// orangeUsageLatencySelectPrefix 批量读取的 SQL 前缀，占位符由 orangeUsageLatencySelectQuery 拼出。
const orangeUsageLatencySelectPrefix = `
	SELECT request_id, gateway_ms, upstream_ms
	FROM ` + orangeUsageLatencyTable + `
	WHERE request_id IN (`

// orangeUsageLatencySelectQuery 拼出「单条 IN 查询」（$1..$n），避免逐条往返。
func orangeUsageLatencySelectQuery(n int) (string, error) {
	if n <= 0 {
		return "", errors.New("orange_usage_latency: request id list is empty")
	}
	placeholders := make([]string, n)
	for i := range placeholders {
		placeholders[i] = fmt.Sprintf("$%d", i+1)
	}
	return orangeUsageLatencySelectPrefix + strings.Join(placeholders, ", ") + ")", nil
}

// OrangeUpsertUsageLatency 写入/更新一条首字延迟拆分记录。
func (r *usageLogRepository) OrangeUpsertUsageLatency(ctx context.Context, requestID string, record service.OrangeUsageLatencyRecord) error {
	requestID = strings.TrimSpace(requestID)
	if requestID == "" {
		return errors.New("orange_usage_latency: request_id is required")
	}
	db, err := r.orangeLatencyDB()
	if err != nil {
		return err
	}

	// 迁移里两列都有 CHECK (>= 0)；负值只可能来自时钟回拨，落库前夹紧，避免整条记录丢失。
	gatewayMs := max(record.GatewayMs, 0)
	upstreamMs := max(record.UpstreamMs, 0)
	if _, err := db.ExecContext(ctx, orangeUsageLatencyUpsertQuery, requestID, gatewayMs, upstreamMs); err != nil {
		return fmt.Errorf("orange_usage_latency: upsert %q: %w", requestID, err)
	}
	return nil
}

// OrangeLoadUsageLatency 按 request_id 批量读取拆分记录（单条 IN 查询）。
// 未采样的 request_id 不出现在返回的 map 里，调用方据此保持字段为 null。
func (r *usageLogRepository) OrangeLoadUsageLatency(ctx context.Context, requestIDs []string) (map[string]service.OrangeUsageLatencyRecord, error) {
	ids := normalizeOrangeUsageLatencyIDs(requestIDs)
	if len(ids) == 0 {
		return map[string]service.OrangeUsageLatencyRecord{}, nil
	}
	db, err := r.orangeLatencyDB()
	if err != nil {
		return nil, err
	}
	query, err := orangeUsageLatencySelectQuery(len(ids))
	if err != nil {
		return nil, err
	}
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}

	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("orange_usage_latency: query: %w", err)
	}
	defer func() { _ = rows.Close() }()

	records := make(map[string]service.OrangeUsageLatencyRecord, len(ids))
	for rows.Next() {
		var (
			requestID  string
			gatewayMs  int
			upstreamMs int
		)
		if err := rows.Scan(&requestID, &gatewayMs, &upstreamMs); err != nil {
			return nil, fmt.Errorf("orange_usage_latency: scan: %w", err)
		}
		records[requestID] = service.OrangeUsageLatencyRecord{
			GatewayMs:  gatewayMs,
			UpstreamMs: upstreamMs,
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("orange_usage_latency: rows: %w", err)
	}
	return records, nil
}

// orangeLatencyDB 取 Orange 裸 SQL 用的 *sql.DB：优先仓储自身持有的连接，
// 退化路径复用同包 ProvideSQLDB（不新建连接池）。
func (r *usageLogRepository) orangeLatencyDB() (*sql.DB, error) {
	if r == nil {
		return nil, errors.New("orange_usage_latency: usage log repository is nil")
	}
	if r.db != nil {
		return r.db, nil
	}
	if db, ok := r.sql.(*sql.DB); ok && db != nil {
		return db, nil
	}
	if r.client == nil {
		return nil, errors.New("orange_usage_latency: ent client is nil")
	}
	db, err := ProvideSQLDB(r.client)
	if err != nil {
		return nil, fmt.Errorf("orange_usage_latency: resolve sql db: %w", err)
	}
	if db == nil {
		return nil, errors.New("orange_usage_latency: sql db is nil")
	}
	return db, nil
}

// normalizeOrangeUsageLatencyIDs 去空、去重、限流（列宽 VARCHAR(128) + 单次 IN 上限）。
func normalizeOrangeUsageLatencyIDs(requestIDs []string) []string {
	if len(requestIDs) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(requestIDs))
	out := make([]string, 0, len(requestIDs))
	for _, raw := range requestIDs {
		id := strings.TrimSpace(raw)
		if id == "" || len(id) > 128 {
			continue
		}
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
		if len(out) >= orangeUsageLatencySelectLimit {
			break
		}
	}
	return out
}
