//go:build unit

package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type codexQuotaOverdraftProbeRepoStub struct {
	AccountRepository
	account        *Account
	tempPauseCalls int
}

func (r *codexQuotaOverdraftProbeRepoStub) GetByID(context.Context, int64) (*Account, error) {
	return r.account, nil
}

func (r *codexQuotaOverdraftProbeRepoStub) UpdateExtra(_ context.Context, _ int64, updates map[string]any) error {
	if r.account.Extra == nil {
		r.account.Extra = make(map[string]any)
	}
	for key, value := range updates {
		r.account.Extra[key] = value
	}
	return nil
}

func (r *codexQuotaOverdraftProbeRepoStub) SetTempUnschedulable(_ context.Context, _ int64, until time.Time, reason string) error {
	r.tempPauseCalls++
	r.account.TempUnschedulableUntil = codexQuotaOverdraftTimePtr(until)
	r.account.TempUnschedulableReason = reason
	return nil
}

func (r *codexQuotaOverdraftProbeRepoStub) ClearTempUnschedulable(context.Context, int64) error {
	r.account.TempUnschedulableUntil = nil
	r.account.TempUnschedulableReason = ""
	return nil
}

func (r *codexQuotaOverdraftProbeRepoStub) ClearRateLimit(context.Context, int64) error {
	r.account.RateLimitResetAt = nil
	return nil
}

func TestCodexQuotaOverdraftProbeIncrementCoversCyclesAndFiveAttempts(t *testing.T) {
	now := time.Date(2026, time.August, 13, 14, 0, 0, 0, time.UTC)
	account := &Account{
		ID:       77,
		Platform: PlatformOpenAI,
		Type:     AccountTypeOAuth,
		Extra: map[string]any{
			"codex_5h_used_percent": 100,
			"codex_5h_reset_at":     now.Add(5 * time.Hour).Format(time.RFC3339),
		},
	}
	signal, exhausted := codexQuotaOverdraftSignalFromAccount(account, nil, now)
	require.True(t, exhausted)
	require.Equal(t, "5h", signal.Window)
	require.Equal(t, now.Add(5*time.Hour), signal.RecoverAt)

	repo := &codexQuotaOverdraftProbeRepoStub{account: account}
	coordinator := &CodexQuotaOverdraftCoordinator{accountRepo: repo, now: func() time.Time { return now }}
	models := make([]string, 0, codexQuotaOverdraftProbeAttemptLimit)
	coordinator.probeAttemptForTest = func(_ context.Context, _ *Account, model string) codexQuotaOverdraftProbeResult {
		models = append(models, model)
		return codexQuotaOverdraftProbeResult{Status: "retry", ReasonCode: "quota_limited", StatusCode: 429, Model: model}
	}
	state := newCodexQuotaOverdraftPendingStateForOrangeTest(signal, now)
	coordinator.runProbePlan(account.ID, signal, "gpt-5.4", state)
	require.Equal(t, codexQuotaOverdraftProbeFailed, state.Status)
	require.Equal(t, codexQuotaOverdraftProbeAttemptLimit, state.Attempts)
	require.Len(t, models, codexQuotaOverdraftProbeAttemptLimit)
	require.Equal(t, 1, repo.tempPauseCalls)
}

func TestCodexQuotaOverdraftProbeIncrementInconclusiveAndModelNotFoundNeverPause(t *testing.T) {
	now := time.Date(2026, time.August, 13, 14, 0, 0, 0, time.UTC)
	for _, result := range []codexQuotaOverdraftProbeResult{
		{Status: "inconclusive", ReasonCode: "request_timeout", StatusCode: 504, Model: "gpt-5.4"},
		{Status: "retry", ReasonCode: "model_not_found", StatusCode: 404, Model: "gpt-5.4"},
	} {
		account := &Account{ID: 78, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Extra: map[string]any{"codex_5h_used_percent": 100, "codex_5h_reset_at": now.Add(5 * time.Hour).Format(time.RFC3339)}}
		repo := &codexQuotaOverdraftProbeRepoStub{account: account}
		coordinator := &CodexQuotaOverdraftCoordinator{accountRepo: repo, now: func() time.Time { return now }}
		coordinator.probeAttemptForTest = func(context.Context, *Account, string) codexQuotaOverdraftProbeResult { return result }
		signal, _ := codexQuotaOverdraftSignalFromAccount(account, nil, now)
		state := newCodexQuotaOverdraftPendingStateForOrangeTest(signal, now)
		coordinator.runProbePlan(account.ID, signal, "gpt-5.4", state)
		require.Equal(t, codexQuotaOverdraftProbeInconclusive, state.Status)
		require.Zero(t, repo.tempPauseCalls)
	}
}

func TestCodexQuotaOverdraftSignalBoundariesAndWindows(t *testing.T) {
	now := time.Date(2026, time.August, 13, 14, 0, 0, 0, time.UTC)
	for _, used := range []float64{94, 99} {
		account := &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth, Extra: map[string]any{
			"codex_5h_used_percent": used,
			"codex_5h_reset_at":     now.Add(5 * time.Hour).Format(time.RFC3339),
		}}
		_, exhausted := codexQuotaOverdraftSignalFromAccount(account, nil, now)
		require.False(t, exhausted, "%.0f%% must remain below the hard exhausted boundary", used)
	}

	fiveReset := now.Add(5 * time.Hour)
	sevenReset := now.Add(7 * 24 * time.Hour)
	account := &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth, Extra: map[string]any{
		"codex_5h_used_percent": 100,
		"codex_5h_reset_at":     fiveReset.Format(time.RFC3339),
	}}
	five, exhausted := codexQuotaOverdraftSignalFromAccount(account, nil, now)
	require.True(t, exhausted)
	require.Equal(t, "5h", five.Window)
	require.Equal(t, fiveReset, five.RecoverAt)

	account.Extra["codex_7d_used_percent"] = 100
	account.Extra["codex_7d_reset_at"] = sevenReset.Format(time.RFC3339)
	multiple, exhausted := codexQuotaOverdraftSignalFromAccount(account, nil, now)
	require.True(t, exhausted)
	require.Equal(t, "multiple", multiple.Window)
	require.Equal(t, sevenReset, multiple.RecoverAt)
	require.Contains(t, multiple.CycleKey, "5h:")
	require.Contains(t, multiple.CycleKey, "|7d:")

	account.Extra["codex_5h_used_percent"] = 0
	seven, exhausted := codexQuotaOverdraftSignalFromAccount(account, nil, now)
	require.True(t, exhausted)
	require.Equal(t, "7d", seven.Window)
	require.Equal(t, sevenReset, seven.RecoverAt)
}

func newCodexQuotaOverdraftPendingStateForOrangeTest(signal codexQuotaOverdraftSignal, now time.Time) *CodexQuotaOverdraftProbeState {
	return &CodexQuotaOverdraftProbeState{
		Status:            codexQuotaOverdraftProbePending,
		QuotaWindow:       signal.Window,
		CycleKey:          signal.CycleKey,
		Limit:             codexQuotaOverdraftProbeAttemptLimit,
		StartedAt:         now,
		RecoverAt:         codexQuotaOverdraftTimePtr(signal.RecoverAt),
		FiveHourRecoverAt: cloneTimePtr(signal.FiveHourRecoverAt),
		SevenDayRecoverAt: cloneTimePtr(signal.SevenDayRecoverAt),
	}
}
