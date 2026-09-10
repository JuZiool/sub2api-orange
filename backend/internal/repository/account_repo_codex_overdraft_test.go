//go:build unit

package repository

import (
	"context"
	"errors"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestAccountRepositoryCodexOverdraftClaimUsesSnapshotCAS(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	now := time.Now().UTC()
	state := &service.CodexQuotaOverdraftProbeState{
		Status:            "pending",
		CycleKey:          "5h:quota-cycle",
		StartedAt:         now,
		ExpectedUpdatedAt: now.Add(-time.Minute),
	}
	mock.ExpectExec(`(?s)UPDATE accounts.*updated_at = NOW\(\).*updated_at = \$5.*codex_quota_overdraft_probe`).
		WithArgs(service.CodexQuotaOverdraftProbeExtraKey, sqlmock.AnyArg(), int64(77), state.CycleKey, sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))

	repo := newAccountRepositoryWithSQL(nil, db, nil)
	claimed, err := repo.ClaimCodexQuotaOverdraftProbe(context.Background(), 77, state)
	require.NoError(t, err)
	require.True(t, claimed)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAccountRepositoryCodexOverdraftNonFailedStatePreservesFailedTerminalState(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	state := &service.CodexQuotaOverdraftProbeState{Status: "passed", CycleKey: "5h:quota-cycle"}
	mock.ExpectExec(`(?s)UPDATE accounts.*codex_quota_overdraft_probe.*status.*failed`).
		WithArgs(service.CodexQuotaOverdraftProbeExtraKey, sqlmock.AnyArg(), int64(77), state.CycleKey).
		WillReturnResult(sqlmock.NewResult(0, 0))

	repo := newAccountRepositoryWithSQL(nil, db, nil)
	persisted, err := repo.PersistCodexQuotaOverdraftProbeUnlessFailed(context.Background(), 77, state)
	require.NoError(t, err)
	require.False(t, persisted)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAccountRepositoryCodexOverdraftFinalizeRollsBackWhenOutboxFails(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	now := time.Now().UTC()
	until := now.Add(5 * time.Hour)
	state := &service.CodexQuotaOverdraftProbeState{
		Status:    "failed",
		CycleKey:  "5h:quota-cycle",
		StartedAt: now,
		RecoverAt: &until,
	}
	reason := service.BuildTempUnschedReasonPayload("codex_quota_overdraft", "quota exhausted")

	mock.ExpectBegin()
	mock.ExpectExec(`(?s)UPDATE accounts.*temp_unschedulable_until.*codex_quota_overdraft_probe`).
		WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg(), until, reason, int64(77), state.CycleKey, false).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO scheduler_outbox")).
		WillReturnError(errors.New("outbox unavailable"))
	mock.ExpectRollback()

	repo := newAccountRepositoryWithSQL(nil, db, nil)
	finalized, err := repo.FinalizeCodexQuotaOverdraftProbeFailed(context.Background(), 77, state, until, reason)
	require.ErrorContains(t, err, "outbox unavailable")
	require.False(t, finalized)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAccountRepositoryCodexOverdraftClearPauseUsesAtomicTransaction(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	now := time.Now().UTC()
	resetAt := now.Add(2 * time.Hour)
	reason := service.BuildTempUnschedReasonPayload("codex_quota_overdraft", "quota exhausted")
	mock.ExpectBegin()
	mock.ExpectQuery(`(?s)WITH target AS.*codex_quota_overdraft_probe.*FOR UPDATE.*RETURNING target.clear_rate_limit, target.clear_temp`).
		WithArgs(int64(77), "5h:quota-cycle", "passed", reason, &resetAt).
		WillReturnRows(sqlmock.NewRows([]string{"clear_rate_limit", "clear_temp"}).AddRow(true, true))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO scheduler_outbox")).
		WithArgs(service.SchedulerOutboxEventAccountChanged, int64(77), nil, nil, sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	repo := newAccountRepositoryWithSQL(nil, db, nil)
	clearedRate, clearedTemp, err := repo.ClearCodexQuotaOverdraftPauseIfState(
		context.Background(), 77, "5h:quota-cycle", "passed", reason, &resetAt,
	)
	require.NoError(t, err)
	require.True(t, clearedRate)
	require.True(t, clearedTemp)
	require.NoError(t, mock.ExpectationsWereMet())
}
