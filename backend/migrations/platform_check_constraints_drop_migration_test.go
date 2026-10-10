package migrations

import (
	"crypto/sha256"
	"fmt"
	"io/fs"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDropPlatformCheckConstraintsMigration(t *testing.T) {
	content, err := FS.ReadFile("242_drop_platform_check_constraints.sql")
	require.NoError(t, err)

	sql := strings.Join(strings.Fields(string(content)), " ")
	require.Contains(t, sql,
		"ALTER TABLE user_platform_quotas DROP CONSTRAINT IF EXISTS user_platform_quotas_platform_check;")
	require.Contains(t, sql,
		"ALTER TABLE composite_model_routes DROP CONSTRAINT IF EXISTS composite_model_routes_target_platform_check;")
	require.NotContains(t, sql, "ADD CONSTRAINT")
	require.NotContains(t, sql, "DROP CONSTRAINT IF EXISTS channel_monitors_provider_check")
	require.NotContains(t, sql, "DROP CONSTRAINT IF EXISTS channel_monitor_request_templates_provider_check")
}

// Platform membership is validated against the application catalog after 242.
// A later migration must not restore a database whitelist that can drift from it.
func TestLaterMigrationsDoNotRestorePlatformCheckConstraints(t *testing.T) {
	const droppedAt = "242_drop_platform_check_constraints.sql"
	// Orange 特有：仅容许已发布且内容不可变的历史补丁，9005 负责最终移除。
	historical := map[string]string{
		"9001_add_minimax_platform.sql":        "f03d1e3244021fcb2667cb812f7c1ba948a9305cdeff4e7b1b89c83ec5adff76",
		"9003_fix_opencode_go_constraints.sql": "9a56ef7734734c9c666433cef209f58f1588c6a7599c6c2c4f0e74bae581bf5a",
	}
	entries, err := fs.ReadDir(FS, ".")
	require.NoError(t, err)
	comments := regexp.MustCompile(`(?ms)/\*.*?\*/|--[^\n]*`)
	addConstraint := regexp.MustCompile(`(?i)\bADD\s+CONSTRAINT\s+"?(user_platform_quotas_platform_check|composite_model_routes_target_platform_check)\b`)
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") || entry.Name() <= droppedAt {
			continue
		}
		t.Run(entry.Name(), func(t *testing.T) {
			content, err := FS.ReadFile(entry.Name())
			require.NoError(t, err)
			if checksum, ok := historical[entry.Name()]; ok {
				require.Equal(t, checksum, fmt.Sprintf("%x", sha256.Sum256(content)), "已发布迁移不可改动")
				return
			}
			sql := comments.ReplaceAll(content, nil)
			require.Nil(t, addConstraint.Find(sql), "platform CHECK constraints removed in 242 must remain managed by application validation")
		})
	}
}

func TestOrangeFinalMigrationDropsHistoricalPlatformConstraints(t *testing.T) {
	const final = "9005_drop_platform_check_constraints.sql"
	content, err := FS.ReadFile(final)
	require.NoError(t, err)
	sql := strings.Join(strings.Fields(string(content)), " ")
	for _, stmt := range []string{
		"ALTER TABLE user_platform_quotas DROP CONSTRAINT IF EXISTS user_platform_quotas_platform_check;",
		"ALTER TABLE composite_model_routes DROP CONSTRAINT IF EXISTS composite_model_routes_target_platform_check;",
	} {
		require.Contains(t, sql, stmt)
	}
	require.NotContains(t, sql, "ADD CONSTRAINT")
	require.NotContains(t, sql, "DROP CONSTRAINT IF EXISTS channel_monitors_provider_check")
	require.NotContains(t, sql, "DROP CONSTRAINT IF EXISTS channel_monitor_request_templates_provider_check")
	require.Greater(t, final, "9003_fix_opencode_go_constraints.sql")
}
