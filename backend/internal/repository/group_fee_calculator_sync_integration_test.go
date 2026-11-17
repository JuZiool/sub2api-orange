//go:build integration

package repository

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	dbmigrations "github.com/Wei-Shaw/sub2api/migrations"
	"github.com/stretchr/testify/require"
)

func TestGroupRepositoryFeeCalculatorSyncPersistence(t *testing.T) {
	ctx := context.Background()
	tx := testEntTx(t)
	repo := newGroupRepositoryWithSQL(tx.Client(), tx)
	group := &service.Group{
		Name: "calculator-sync", Platform: service.PlatformOpenAI,
		RateMultiplier: 1, Status: service.StatusActive, SubscriptionType: service.SubscriptionTypeStandard,
	}
	require.NoError(t, repo.Create(ctx, group))

	loaded, err := repo.GetByIDLite(ctx, group.ID)
	require.NoError(t, err)
	require.False(t, loaded.FeeCalculatorSyncEnabled)
	for _, enabled := range []bool{true, false} {
		loaded.FeeCalculatorSyncEnabled = enabled
		require.NoError(t, repo.Update(ctx, loaded))
		loaded, err = repo.GetByIDLite(ctx, group.ID)
		require.NoError(t, err)
		require.Equal(t, enabled, loaded.FeeCalculatorSyncEnabled)
	}

	optedIn := &service.Group{
		Name: "calculator-sync-opt-in", Platform: service.PlatformOpenAI,
		RateMultiplier: 1, Status: service.StatusActive, SubscriptionType: service.SubscriptionTypeStandard,
		FeeCalculatorSyncEnabled: true,
	}
	require.NoError(t, repo.Create(ctx, optedIn))
	loaded, err = repo.GetByIDLite(ctx, optedIn.ID)
	require.NoError(t, err)
	require.True(t, loaded.FeeCalculatorSyncEnabled)
}

func TestMigration9010FeeCalculatorSyncDefaultsOffAndReplaysSafely(t *testing.T) {
	ctx := context.Background()
	tx := testTx(t)
	_, err := tx.ExecContext(ctx, "ALTER TABLE groups DROP COLUMN fee_calculator_sync_enabled")
	require.NoError(t, err)

	var legacyID int64
	require.NoError(t, tx.QueryRowContext(ctx, `
INSERT INTO groups (name, platform, rate_multiplier, status)
VALUES ('calculator-legacy', 'openai', 1, 'active') RETURNING id
`).Scan(&legacyID))
	migrationSQL, err := dbmigrations.FS.ReadFile("9010_group_fee_calculator_sync.sql")
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, string(migrationSQL))
	require.NoError(t, err)
	var legacyEnabled bool
	require.NoError(t, tx.QueryRowContext(ctx,
		"SELECT fee_calculator_sync_enabled FROM groups WHERE id = $1", legacyID).Scan(&legacyEnabled))
	require.False(t, legacyEnabled)

	var newEnabled bool
	require.NoError(t, tx.QueryRowContext(ctx, `
INSERT INTO groups (name, platform, rate_multiplier, status)
VALUES ('calculator-new', 'openai', 1, 'active') RETURNING fee_calculator_sync_enabled
`).Scan(&newEnabled))
	require.False(t, newEnabled)

	_, err = tx.ExecContext(ctx,
		"UPDATE groups SET fee_calculator_sync_enabled = TRUE WHERE id = $1", legacyID)
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, string(migrationSQL))
	require.NoError(t, err)
	require.NoError(t, tx.QueryRowContext(ctx,
		"SELECT fee_calculator_sync_enabled FROM groups WHERE id = $1", legacyID).Scan(&legacyEnabled))
	require.True(t, legacyEnabled, "replaying migration must preserve explicit opt-ins")
}
