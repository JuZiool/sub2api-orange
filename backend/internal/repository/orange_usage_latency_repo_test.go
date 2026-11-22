// Orange 特有：首字延迟拆分仓储（orange_usage_latency）的 SQL 形状与读写自检。
//
// 这里既做静态校验（只写 orange_ 自建表、读取只读、单条 IN 查询），
// 也用 sqlmock 校验参数与扫描结果，不连数据库。
package repository

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

// errDBUnavailableForTest 模拟数据库不可用（写入失败只允许冒泡到 service 层的 warn 日志）。
var errDBUnavailableForTest = errors.New("db unavailable")

func TestOrangeUsageLatencyQueriesTargetOrangeTableOnly(t *testing.T) {
	require.Equal(t, "orange_usage_latency", orangeUsageLatencyTable)

	upsertQuery := strings.ToLower(orangeUsageLatencyUpsertQuery)
	require.Contains(t, upsertQuery, "insert into "+orangeUsageLatencyTable)
	require.Contains(t, upsertQuery, "on conflict (request_id) do update")
	require.Contains(t, upsertQuery, "gateway_ms")
	require.Contains(t, upsertQuery, "upstream_ms")
	// 只写 Orange 自建表：不碰 usage_logs 等上游表，也不做 ALTER。
	ensureAllWriteTargetsAreOrangeTables(t, upsertQuery)
	for _, forbidden := range []string{"usage_logs", "alter ", "drop ", "truncate"} {
		require.NotContains(t, upsertQuery, forbidden)
	}

	selectQuery := strings.ToLower(orangeUsageLatencySelectPrefix)
	require.True(t, strings.HasPrefix(strings.TrimSpace(selectQuery), "select"))
	require.Contains(t, selectQuery, "from "+orangeUsageLatencyTable)
	for _, forbidden := range forbiddenWrites {
		require.NotContains(t, selectQuery, forbidden)
	}
}

func TestOrangeUsageLatencySelectQueryUsesSingleInClause(t *testing.T) {
	query, err := orangeUsageLatencySelectQuery(3)
	require.NoError(t, err)
	require.Contains(t, query, "WHERE request_id IN ($1, $2, $3)")
	require.Equal(t, 1, strings.Count(strings.ToLower(query), "select "), "批量读取必须是单条查询")
	require.Equal(t, 1, strings.Count(strings.ToUpper(query), "IN ("))

	_, err = orangeUsageLatencySelectQuery(0)
	require.Error(t, err)
}

func TestOrangeUsageLatencyNormalizeIDs(t *testing.T) {
	longID := strings.Repeat("x", 129)
	ids := normalizeOrangeUsageLatencyIDs([]string{" a ", "", "a", "b", longID})
	require.Equal(t, []string{"a", "b"}, ids)
	require.Nil(t, normalizeOrangeUsageLatencyIDs(nil))

	overflow := make([]string, orangeUsageLatencySelectLimit+10)
	for i := range overflow {
		overflow[i] = strings.Repeat("i", 8) + string(rune('a'+i%26)) + string(rune('a'+i/26))
	}
	require.Len(t, normalizeOrangeUsageLatencyIDs(overflow), orangeUsageLatencySelectLimit)
}

func TestOrangeUsageLatencyUpsert(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := &usageLogRepository{sql: db}

	mock.ExpectExec("INSERT INTO orange_usage_latency").
		WithArgs("local:req-1", 30, 270).
		WillReturnResult(sqlmock.NewResult(0, 1))

	err := repo.OrangeUpsertUsageLatency(context.Background(), " local:req-1 ", service.OrangeUsageLatencyRecord{
		GatewayMs:  30,
		UpstreamMs: 270,
	})
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())

	// 空 request_id 直接拒绝（不产生无效行）。
	require.Error(t, repo.OrangeUpsertUsageLatency(context.Background(), "  ", service.OrangeUsageLatencyRecord{}))

	// 负值夹紧到 0（迁移里两列都有 CHECK >= 0），避免时钟回拨导致整条记录丢失。
	mock.ExpectExec("INSERT INTO orange_usage_latency").
		WithArgs("local:req-2", 0, 0).
		WillReturnResult(sqlmock.NewResult(0, 1))
	require.NoError(t, repo.OrangeUpsertUsageLatency(context.Background(), "local:req-2", service.OrangeUsageLatencyRecord{
		GatewayMs:  -5,
		UpstreamMs: -1,
	}))
	require.NoError(t, mock.ExpectationsWereMet())

	// 写库失败冒泡给 service 层（由 service 记 warn 日志，不影响主流程）。
	mock.ExpectExec("INSERT INTO orange_usage_latency").
		WithArgs("local:req-3", 1, 2).
		WillReturnError(errDBUnavailableForTest)
	err = repo.OrangeUpsertUsageLatency(context.Background(), "local:req-3", service.OrangeUsageLatencyRecord{GatewayMs: 1, UpstreamMs: 2})
	require.ErrorIs(t, err, errDBUnavailableForTest)
}

func TestOrangeUsageLatencyLoad(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := &usageLogRepository{sql: db}

	mock.ExpectQuery(`SELECT request_id, gateway_ms, upstream_ms\s+FROM orange_usage_latency\s+WHERE request_id IN \(\$1, \$2\)`).
		WithArgs("local:req-1", "local:req-2").
		WillReturnRows(sqlmock.NewRows([]string{"request_id", "gateway_ms", "upstream_ms"}).
			AddRow("local:req-1", 30, 270))

	latencies, err := repo.OrangeLoadUsageLatency(context.Background(), []string{"local:req-1", "local:req-1", "local:req-2", ""})
	require.NoError(t, err)
	require.Equal(t, map[string]service.OrangeUsageLatencyRecord{
		"local:req-1": {GatewayMs: 30, UpstreamMs: 270},
	}, latencies, "未采样的 request_id 不出现在结果里")
	require.NoError(t, mock.ExpectationsWereMet())

	// 空列表不发查询。
	empty, err := repo.OrangeLoadUsageLatency(context.Background(), nil)
	require.NoError(t, err)
	require.Empty(t, empty)
}

func TestOrangeUsageLatencyRepositoryWithoutDB(t *testing.T) {
	// 未装配 ent / sql.DB 时返回错误而不是 panic（能力不可用时字段保持 null）。
	repo := &usageLogRepository{}
	require.Error(t, repo.OrangeUpsertUsageLatency(context.Background(), "local:req-1", service.OrangeUsageLatencyRecord{}))
	_, err := repo.OrangeLoadUsageLatency(context.Background(), []string{"local:req-1"})
	require.Error(t, err)

	var nilRepo *usageLogRepository
	require.Error(t, nilRepo.OrangeUpsertUsageLatency(context.Background(), "local:req-1", service.OrangeUsageLatencyRecord{}))
}

func TestUsageLogRepositoryImplementsOrangeUsageLatencyStore(t *testing.T) {
	var repo service.UsageLogRepository = &usageLogRepository{}
	store, ok := repo.(service.OrangeUsageLatencyStore)
	require.True(t, ok)
	require.NotNil(t, store)
}
