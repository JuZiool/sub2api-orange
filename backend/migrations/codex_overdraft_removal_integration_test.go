//go:build integration

package migrations

import (
	"context"
	"database/sql"
	"os"
	"testing"
	"time"

	_ "github.com/lib/pq"
	"github.com/stretchr/testify/require"
)

func TestRemoveCodexQuotaOverdraftMigrationPreservesOtherAccountState(t *testing.T) {
	dsn := os.Getenv("ORANGE_MIGRATION_TEST_DSN")
	if dsn == "" {
		t.Skip("需要显式 ORANGE_MIGRATION_TEST_DSN 指定隔离测试数据库")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	db, err := sql.Open("postgres", dsn)
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	tx, err := db.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()
	// Transaction-local tables shadow the real tables, so the real migration SQL
	// can be exercised without touching shared integration fixtures or settings.
	_, err = tx.ExecContext(ctx, `
		CREATE TEMP TABLE accounts (
			id BIGINT PRIMARY KEY,
			extra JSONB,
			temp_unschedulable_until TIMESTAMPTZ,
			temp_unschedulable_reason TEXT,
			rate_limited_at TIMESTAMPTZ,
			rate_limit_reset_at TIMESTAMPTZ,
			updated_at TIMESTAMPTZ NOT NULL
		) ON COMMIT DROP;
		CREATE TEMP TABLE settings (key TEXT PRIMARY KEY, value TEXT) ON COMMIT DROP;
		CREATE TEMP TABLE scheduler_outbox (
			id BIGSERIAL PRIMARY KEY,
			event_type TEXT NOT NULL,
			account_id BIGINT
		) ON COMMIT DROP;
	`)
	require.NoError(t, err)

	stamp := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	pauseUntil := stamp.Add(24 * time.Hour)
	resetAt := stamp.Add(48 * time.Hour)
	const unrelatedExtra = `{"codex_primary_used_percent":100,"proxy_pool":[1,2],"model":"keep"}`
	fixtures := []struct {
		id        int64
		extra     string
		reason    string
		wantExtra string
		clear     bool
		changed   bool
	}{
		{1, `{"codex_quota_overdraft_disabled":true,"codex_quota_overdraft_enabled":false,"codex_quota_overdraft_probe":{"status":"passed"},"codex_primary_used_percent":100,"proxy_pool":[1,2],"model":"keep"}`,
			`{"source":"codex_quota_overdraft","error":"confirmed"}`, unrelatedExtra, true, true},
		{2, `{"codex_quota_overdraft_probe":{"status":"failed"},"keep":true}`,
			`{"source":"account_scheduling_threshold","error":"quota exhausted"}`, `{"keep":true}`, false, true},
		{3, `{"codex_quota_overdraft_disabled":false,"keep":3}`,
			`invalid codex_quota_overdraft JSON {`, `{"keep":3}`, false, true},
		{4, unrelatedExtra, `plain text codex_quota_overdraft`, unrelatedExtra, false, false},
		{5, unrelatedExtra, `{"source":"manual","nested":{"source":"codex_quota_overdraft"}}`, unrelatedExtra, false, false},
		{6, unrelatedExtra, `{"source":"codex_quota_overdraft_other"}`, unrelatedExtra, false, false},
		{7, unrelatedExtra, `[{"source":"codex_quota_overdraft"}]`, unrelatedExtra, false, false},
		{8, `["codex_quota_overdraft_probe"]`, `manual pause`, `["codex_quota_overdraft_probe"]`, false, false},
		{9, `null`, `manual pause`, `null`, false, false},
		{10, unrelatedExtra, `{"source":"codex_quota_overdraft"}`, unrelatedExtra, true, true},
		{11, `"codex_quota_overdraft_probe"`, `"codex_quota_overdraft"`, `"codex_quota_overdraft_probe"`, false, false},
		{12, `{"nested":{"codex_quota_overdraft_probe":"keep"},"codex_quota_overdraft_other":true}`,
			`{"source":"manual","error":"codex_quota_overdraft"}`, `{"nested":{"codex_quota_overdraft_probe":"keep"},"codex_quota_overdraft_other":true}`, false, false},
		{13, unrelatedExtra, `{"source":"codex_quota_overdraft","invalid":"\u0000"}`, unrelatedExtra, false, false},
		{14, unrelatedExtra, `{"source":"codex_quota_overdraft","invalid":1e1000000}`, unrelatedExtra, false, false},
		{15, unrelatedExtra, `{"source":"codex_quota_over\u0064raft"}`, unrelatedExtra, true, true},
	}
	for _, fixture := range fixtures {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO accounts (id, extra, temp_unschedulable_until,
				temp_unschedulable_reason, rate_limited_at, rate_limit_reset_at, updated_at)
			VALUES ($1, $2::jsonb, $3, $4, $5, $6, $5)
		`, fixture.id, fixture.extra, pauseUntil, fixture.reason, stamp, resetAt)
		require.NoError(t, err)
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO settings (key, value) VALUES
		('codex_quota_overdraft_enabled', 'true'), ('codex_quota_overdraft_other', 'keep'), ('site_name', 'Orange')`)
	require.NoError(t, err)

	migrationSQL, err := FS.ReadFile("9007_remove_codex_quota_overdraft.sql")
	require.NoError(t, err)
	for run := 0; run < 2; run++ {
		_, err := tx.ExecContext(ctx, string(migrationSQL))
		require.NoErrorf(t, err, "migration run %d", run+1)
	}

	for _, fixture := range fixtures {
		var extra string
		var until sql.NullTime
		var reason sql.NullString
		var limitedAt, actualReset, updatedAt time.Time
		require.NoError(t, tx.QueryRowContext(ctx, `
			SELECT extra::text, temp_unschedulable_until, temp_unschedulable_reason,
				rate_limited_at, rate_limit_reset_at, updated_at
			FROM accounts WHERE id = $1
		`, fixture.id).Scan(&extra, &until, &reason, &limitedAt, &actualReset, &updatedAt))
		require.JSONEqf(t, fixture.wantExtra, extra, "account %d metadata", fixture.id)
		require.True(t, limitedAt.Equal(stamp), "account %d normal rate limit", fixture.id)
		require.True(t, actualReset.Equal(resetAt), "account %d normal reset", fixture.id)
		if fixture.clear {
			require.False(t, until.Valid, "account %d dedicated pause", fixture.id)
			require.False(t, reason.Valid, "account %d dedicated reason", fixture.id)
		} else {
			require.True(t, until.Valid && until.Time.Equal(pauseUntil), "account %d unrelated pause", fixture.id)
			require.Equal(t, fixture.reason, reason.String, "account %d unrelated reason", fixture.id)
		}
		var notifications int
		require.NoError(t, tx.QueryRowContext(ctx, `
			SELECT COUNT(*) FROM scheduler_outbox WHERE account_id = $1 AND event_type = 'account_changed'
		`, fixture.id).Scan(&notifications))
		if fixture.changed {
			require.Equal(t, 1, notifications, "account %d change notification is idempotent", fixture.id)
			require.True(t, updatedAt.After(stamp), "account %d change timestamp", fixture.id)
		} else {
			require.Zero(t, notifications, "account %d unchanged", fixture.id)
			require.True(t, updatedAt.Equal(stamp), "account %d unchanged timestamp", fixture.id)
		}
	}
	var removedSettings int
	require.NoError(t, tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM settings WHERE key = 'codex_quota_overdraft_enabled'`).Scan(&removedSettings))
	require.Zero(t, removedSettings)
	var remainingSettings int
	require.NoError(t, tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM settings WHERE (key, value) IN (('codex_quota_overdraft_other', 'keep'), ('site_name', 'Orange'))`).Scan(&remainingSettings))
	require.Equal(t, 2, remainingSettings)
}
