package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"

	entsql "entgo.io/ent/dialect/sql"
)

// ClaimCodexQuotaOverdraftProbe atomically reserves one quota cycle. A cycle is
// claimed at most once across all sub2api replicas.
func (r *accountRepository) ClaimCodexQuotaOverdraftProbe(
	ctx context.Context,
	id int64,
	state *service.CodexQuotaOverdraftProbeState,
) (bool, error) {
	if state == nil || strings.TrimSpace(state.CycleKey) == "" {
		return false, nil
	}
	payload, err := json.Marshal(state)
	if err != nil {
		return false, err
	}
	var expectedUpdatedAt any
	if !state.ExpectedUpdatedAt.IsZero() {
		expectedUpdatedAt = state.ExpectedUpdatedAt
	}
	result, err := r.sql.ExecContext(ctx, `
		UPDATE accounts
		SET extra = COALESCE(extra, '{}'::jsonb) || jsonb_build_object($1::text, $2::jsonb),
			updated_at = NOW()
		WHERE id = $3
			AND deleted_at IS NULL
			AND ($5::timestamptz IS NULL OR updated_at = $5)
			AND (
				COALESCE(extra #>> '{codex_quota_overdraft_probe,cycle_key}', '') <> $4
				OR (
					COALESCE(extra #>> '{codex_quota_overdraft_probe,status}', '') = 'pending'
					AND CASE
						WHEN extra #>> '{codex_quota_overdraft_probe,started_at}' ~ '^[0-9]{4}-[0-9]{2}-[0-9]{2}T'
							THEN (extra #>> '{codex_quota_overdraft_probe,started_at}')::timestamptz
						ELSE updated_at
					END <= NOW() - INTERVAL '2 minutes'
				)
				OR (
					COALESCE(extra #>> '{codex_quota_overdraft_probe,status}', '') = 'inconclusive'
					AND CASE
						WHEN extra #>> '{codex_quota_overdraft_probe,retry_at}' ~ '^[0-9]{4}-[0-9]{2}-[0-9]{2}T'
							THEN (extra #>> '{codex_quota_overdraft_probe,retry_at}')::timestamptz
						ELSE updated_at + INTERVAL '2 minutes'
					END <= NOW()
				)
			)
	`, service.CodexQuotaOverdraftProbeExtraKey, string(payload), id, state.CycleKey, expectedUpdatedAt)
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	if err != nil || affected == 0 {
		return false, err
	}
	r.syncSchedulerAccountSnapshot(ctx, id)
	return true, nil
}

// PersistCodexQuotaOverdraftProbeUnlessFailed stores a non-failure result while
// preserving a terminal failure already confirmed for the same quota cycle.
func (r *accountRepository) PersistCodexQuotaOverdraftProbeUnlessFailed(
	ctx context.Context,
	id int64,
	state *service.CodexQuotaOverdraftProbeState,
) (bool, error) {
	if state == nil || state.Status == "failed" || strings.TrimSpace(state.CycleKey) == "" {
		return false, nil
	}
	payload, err := json.Marshal(state)
	if err != nil {
		return false, err
	}
	result, err := r.sql.ExecContext(ctx, `
		UPDATE accounts
		SET extra = COALESCE(extra, '{}'::jsonb) || jsonb_build_object($1::text, $2::jsonb),
			updated_at = NOW()
		WHERE id = $3
			AND deleted_at IS NULL
			AND (
				COALESCE(extra #>> '{codex_quota_overdraft_probe,cycle_key}', '') <> $4
				OR COALESCE(extra #>> '{codex_quota_overdraft_probe,status}', '') <> 'failed'
			)
	`, service.CodexQuotaOverdraftProbeExtraKey, string(payload), id, state.CycleKey)
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	if err != nil || affected == 0 {
		return false, err
	}
	r.syncSchedulerAccountSnapshot(ctx, id)
	return true, nil
}

// ClearCodexQuotaOverdraftPauseIfState clears stale scheduling state only when
// the persisted overdraft result is still the expected available state.
func (r *accountRepository) ClearCodexQuotaOverdraftPauseIfState(
	ctx context.Context,
	id int64,
	cycleKey string,
	status string,
	expectedTempReason string,
	expectedRateLimitResetAt *time.Time,
) (bool, bool, error) {
	if id <= 0 || strings.TrimSpace(cycleKey) == "" || (status != "passed" && status != "recovered") {
		return false, false, nil
	}
	beginner, ok := r.sql.(codexQuotaOverdraftTxBeginner)
	if !ok {
		return false, false, errors.New("account repository does not support transactions")
	}
	tx, err := beginner.BeginTx(ctx, nil)
	if err != nil {
		return false, false, err
	}
	defer func() { _ = tx.Rollback() }()

	var clearedRateLimit, clearedTemp bool
	err = tx.QueryRowContext(ctx, `
		WITH target AS (
			SELECT id,
				(
					($5::timestamptz IS NOT NULL)
					AND rate_limit_reset_at = $5
				) AS clear_rate_limit,
				($4::text <> '' AND COALESCE(temp_unschedulable_reason = $4, FALSE)) AS clear_temp
			FROM accounts
			WHERE id = $1
				AND deleted_at IS NULL
				AND extra #>> '{codex_quota_overdraft_probe,cycle_key}' = $2
				AND extra #>> '{codex_quota_overdraft_probe,status}' = $3
			FOR UPDATE
		)
		UPDATE accounts AS a
		SET rate_limited_at = CASE WHEN target.clear_rate_limit THEN NULL ELSE a.rate_limited_at END,
			rate_limit_reset_at = CASE WHEN target.clear_rate_limit THEN NULL ELSE a.rate_limit_reset_at END,
			temp_unschedulable_until = CASE WHEN target.clear_temp THEN NULL ELSE a.temp_unschedulable_until END,
			temp_unschedulable_reason = CASE WHEN target.clear_temp THEN NULL ELSE a.temp_unschedulable_reason END,
			updated_at = NOW()
		FROM target
		WHERE a.id = target.id
			AND (target.clear_rate_limit OR target.clear_temp)
		RETURNING target.clear_rate_limit, target.clear_temp
	`, id, cycleKey, status, expectedTempReason, expectedRateLimitResetAt).Scan(&clearedRateLimit, &clearedTemp)
	if errors.Is(err, sql.ErrNoRows) {
		return false, false, nil
	}
	if err != nil {
		return false, false, err
	}
	if err := enqueueSchedulerOutbox(ctx, tx, service.SchedulerOutboxEventAccountChanged, &id, nil, nil); err != nil {
		return false, false, err
	}
	if err := tx.Commit(); err != nil {
		return false, false, err
	}
	r.syncSchedulerAccountSnapshotDetached(ctx, id)
	return clearedRateLimit, clearedTemp, nil
}

type codexQuotaOverdraftTxBeginner interface {
	BeginTx(context.Context, *sql.TxOptions) (*sql.Tx, error)
}

// FinalizeCodexQuotaOverdraftProbeFailed persists the terminal probe state,
// account pause, and scheduler notification in one transaction.
func (r *accountRepository) FinalizeCodexQuotaOverdraftProbeFailed(
	ctx context.Context,
	id int64,
	state *service.CodexQuotaOverdraftProbeState,
	until time.Time,
	reason string,
) (bool, error) {
	if state == nil || state.Status != "failed" || strings.TrimSpace(state.CycleKey) == "" || until.IsZero() {
		return false, nil
	}
	payload, err := json.Marshal(state)
	if err != nil {
		return false, err
	}
	beginner, ok := r.sql.(codexQuotaOverdraftTxBeginner)
	if !ok {
		return false, errors.New("account repository does not support transactions")
	}
	tx, err := beginner.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()

	result, err := tx.ExecContext(ctx, `
		UPDATE accounts
		SET extra = COALESCE(extra, '{}'::jsonb) || jsonb_build_object($1::text, $2::jsonb),
			temp_unschedulable_until = CASE
				WHEN temp_unschedulable_until IS NULL OR temp_unschedulable_until < $3 THEN $3
				ELSE temp_unschedulable_until
			END,
			temp_unschedulable_reason = CASE
				WHEN temp_unschedulable_until IS NULL OR temp_unschedulable_until < $3 THEN $4
				ELSE temp_unschedulable_reason
			END,
			updated_at = NOW()
		WHERE id = $5
			AND deleted_at IS NULL
			AND extra #>> '{codex_quota_overdraft_probe,cycle_key}' = $6
			AND (
				extra #>> '{codex_quota_overdraft_probe,status}' IN ('pending', 'failed')
				OR ($7::boolean AND extra #>> '{codex_quota_overdraft_probe,status}' IN ('passed', 'inconclusive', 'recovered'))
			)
	`, service.CodexQuotaOverdraftProbeExtraKey, string(payload), until, reason, id, state.CycleKey, state.ReasonCode == "business_quota_limited")
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	if affected == 0 {
		return false, nil
	}
	if err := enqueueSchedulerOutbox(ctx, tx, service.SchedulerOutboxEventAccountChanged, &id, nil, nil); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	r.syncSchedulerAccountSnapshotDetached(ctx, id)
	return true, nil
}

func extendCodexQuotaOverdraftTempUnschedulablePredicates(
	ctx context.Context,
	s *entsql.Selector,
	predicates []*entsql.Predicate,
) []*entsql.Predicate {
	if !service.CodexQuotaOverdraftSchedulingEnabled(ctx) {
		return predicates
	}
	reasonCol := s.C("temp_unschedulable_reason")
	return append(predicates, entsql.And(
		entsql.EQ(s.C("platform"), service.PlatformOpenAI),
		entsql.EQ(s.C("type"), service.AccountTypeOAuth),
		entsql.IsNull(s.C("parent_account_id")),
		// Missing means inherit the global switch; only an explicit disabled=true
		// disables overdraft for this account.
		entsql.ExprP("COALESCE("+s.C("extra")+" ->> '"+service.CodexQuotaOverdraftDisabledExtraKey+"', 'false') <> 'true'"),

		entsql.Contains(reasonCol, `"source":"`+service.AccountSchedulingThresholdReasonSource+`"`),
	))
}
