package migrations

import (
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestOrangeConstraintMigrationsOrdering 防止「迁移排序覆盖平台白名单」回归。
//
// 迁移执行器按文件名排序执行，Orange 的 9001 对 user_platform_quotas 与
// composite_model_routes 是无条件 DROP + 重建且不含 opencode_go，而它排在
// 上游 238 之后。因此必须有 9003 在最后把约束修复为同时包含两个平台的超集。
func TestOrangeConstraintMigrationsOrdering(t *testing.T) {
	pattern := []string{
		"237_add_minimax_platform.sql",
		"238_opencode_go_platform.sql",
		"9001_add_minimax_platform.sql",
		"9003_fix_opencode_go_constraints.sql",
	}

	// 实际执行顺序必须与预期一致：237 -> 238 -> 9001 -> 9003
	sorted := append([]string(nil), pattern...)
	sort.Strings(sorted)
	require.Equal(t, pattern, sorted,
		"迁移执行顺序（文件名排序）变化，需重新评估 opencode_go 白名单是否会被覆盖")

	readSQL := func(name string) string {
		content, err := FS.ReadFile(name)
		require.NoError(t, err, "迁移文件缺失: %s", name)
		return strings.Join(strings.Fields(string(content)), " ")
	}

	// 9001 确实不含 opencode_go —— 这正是需要 9003 的原因
	require.NotContains(t, readSQL("9001_add_minimax_platform.sql"), "opencode_go",
		"9001 若已包含 opencode_go，说明该迁移被修改（已应用迁移不可改），需重新评估 9003")

	// 9003 必须把四处约束都修复为同时含 minimax 与 opencode_go 的超集
	fix := readSQL("9003_fix_opencode_go_constraints.sql")
	for _, expected := range []string{
		"CHECK (platform IN ('anthropic', 'openai', 'gemini', 'antigravity', 'grok', 'kimi', 'zhipu', 'deepseek', 'minimax', 'opencode_go'))",
		"CHECK (target_platform IN ('anthropic', 'openai', 'gemini', 'antigravity', 'grok', 'kimi', 'zhipu', 'deepseek', 'minimax', 'opencode_go'))",
		"CHECK (provider IN ('openai', 'anthropic', 'gemini', 'grok', 'antigravity', 'kimi', 'zhipu', 'deepseek', 'minimax', 'opencode_go'))",
		"position('minimax' IN monitor_constraint_def) = 0",
		"position('opencode_go' IN monitor_constraint_def) = 0",
		"position('minimax' IN template_constraint_def) = 0",
		"position('opencode_go' IN template_constraint_def) = 0",
	} {
		require.Contains(t, fix, expected, "9003 缺少约束片段")
	}
}
