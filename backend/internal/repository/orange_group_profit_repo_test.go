// Orange 特有：分组利润仓储的 SQL 形状自检（只读约束 / 只写自建表 / 能力暴露）。
//
// 这里不连数据库，仅做静态校验：一旦有人把只读聚合改成写查询、或把倍率写到上游表，
// 这些断言会立刻失败。
package repository

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// forbiddenWrites 出现在只读 SQL 里即视为违规（"deleted_at" 不属于写入，故不列 "delete"）。
var forbiddenWrites = []string{"insert", "update ", "delete from", "alter ", "drop ", "truncate"}

func TestOrangeGroupProfitStatsQueryIsReadOnlyAggregate(t *testing.T) {
	query := strings.ToLower(orangeGroupProfitStatsQuery)

	require.Contains(t, query, "from usage_logs")
	require.Contains(t, query, "filter (where")
	require.Contains(t, query, "group by ul.group_id")
	// 收入 = SUM(actual_cost)，上游原价 = SUM(total_cost)。
	require.Contains(t, query, "sum(ul.actual_cost)")
	require.Contains(t, query, "sum(ul.total_cost)")
	for _, forbidden := range forbiddenWrites {
		require.NotContains(t, query, forbidden, "profit stats query must stay read-only")
	}
}

func TestOrangeGroupProfitGroupQueriesAreReadOnly(t *testing.T) {
	for name, query := range map[string]string{
		"list":   orangeGroupProfitListIDsQuery,
		"exists": orangeGroupProfitExistsQuery,
	} {
		normalized := strings.ToLower(query)
		require.Contains(t, normalized, "from groups", name)
		require.Contains(t, normalized, "deleted_at is null", name)
		require.True(t, strings.HasPrefix(strings.TrimSpace(normalized), "select"), name)
		for _, forbidden := range forbiddenWrites {
			require.NotContains(t, normalized, forbidden, "%s query must stay read-only", name)
		}
	}
}

func TestOrangeGroupUpstreamRateQueriesTargetOrangeTableOnly(t *testing.T) {
	selectQuery := strings.ToLower(orangeGroupUpstreamRateSelectQuery)
	require.Contains(t, selectQuery, "from "+orangeGroupUpstreamRateTable)
	require.Equal(t, "orange_group_upstream_rate", orangeGroupUpstreamRateTable)
	for _, forbidden := range forbiddenWrites {
		require.NotContains(t, selectQuery, forbidden)
	}

	upsertQuery := strings.ToLower(orangeGroupUpstreamRateUpsertQuery)
	require.Contains(t, upsertQuery, "insert into "+orangeGroupUpstreamRateTable)
	require.Contains(t, upsertQuery, "on conflict (group_id) do update")
	ensureAllWriteTargetsAreOrangeTables(t, upsertQuery)
}

// ensureAllWriteTargetsAreOrangeTables 确认写查询只命中 orange_ 前缀自建表。
func ensureAllWriteTargetsAreOrangeTables(t *testing.T, query string) {
	t.Helper()
	for _, statement := range strings.Split(query, "insert into ") {
		trimmed := strings.TrimSpace(statement)
		if trimmed == "" || trimmed == query {
			continue
		}
		require.True(t, strings.HasPrefix(trimmed, "orange_"),
			"write target must be an orange_ table, got: %s", trimmed)
	}
}

func TestSettingRepositoryExposesOrangeGroupProfitStore(t *testing.T) {
	// 未注入 ent 客户端时能力不可用（service 层回 503，而不是 panic）。
	store, ok := (&settingRepository{}).OrangeGroupProfitStore()
	require.False(t, ok)
	require.Nil(t, store)
}
