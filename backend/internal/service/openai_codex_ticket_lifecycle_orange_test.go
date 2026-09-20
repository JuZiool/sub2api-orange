package service

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestCodexTicketAccountIdentityStableAcrossTokenRefresh(t *testing.T) {
	account := ticketTestAccount(41)
	account.Credentials = map[string]any{"access_token": "a", "refresh_token": "r", "chatgpt_account_id": "acc-1"}
	first := CodexTicketAccountIdentity(account)
	account.Credentials["access_token"] = "b"
	account.Credentials["refresh_token"] = "r2"
	require.Equal(t, first, CodexTicketAccountIdentity(account), "token refresh must not change the ticket owner")

	// Reauthorization to another principal must invalidate the identity.
	account.Credentials["chatgpt_account_id"] = "acc-2"
	require.NotEqual(t, first, CodexTicketAccountIdentity(account))

	// Falls back to email when account/user ids are absent.
	bare := ticketTestAccount(42)
	bare.Credentials = map[string]any{"email": "owner@example.com"}
	require.NotEmpty(t, CodexTicketAccountIdentity(bare))
	require.NotEqual(t, CodexTicketAccountIdentity(bare), CodexTicketAccountIdentity(account))
}

func TestCodexTicketLifecycleBeginRejectsUnconfirmedAndNotDue(t *testing.T) {
	now := time.Now()
	expiry := now.Add(30 * time.Minute)
	due := now.Add(-time.Minute)

	state := &CodexTicketLifecycle{Phase: "ready", ExpiresAt: &expiry, NextAt: &due}
	require.NoError(t, state.begin(now, "lease-1"))
	require.True(t, state.Pending)
	require.Equal(t, "pre_running", state.Phase)
	require.Equal(t, 1, state.Attempts)

	// A pending attempt must never be repeated (process crash semantics).
	require.ErrorIs(t, state.begin(now, "lease-2"), ErrCodexTicketAlreadyTried)

	// Not due yet.
	future := now.Add(10 * time.Minute)
	fresh := &CodexTicketLifecycle{Phase: "ready", ExpiresAt: &expiry, NextAt: &future}
	require.ErrorIs(t, fresh.begin(now, "lease-3"), ErrCodexTicketNotDue)
	require.False(t, fresh.Pending)
}

func TestCodexTicketLifecycleFiniteRenewalStopsAfterLimit(t *testing.T) {
	now := time.Now()
	expired := now.Add(-time.Minute)
	due := now.Add(-time.Minute)
	state := &CodexTicketLifecycle{Phase: "retry", ExpiresAt: &expired, NextAt: &due}
	state.Attempts = codexTicketMaxPendingAttempts - 1
	require.NoError(t, state.begin(now, "lease"))
	state.complete(now, nil, "rate_limited", "Upstream rate limit reached", false)
	require.Equal(t, "stopped", state.Phase, "renewal must stop after the finite attempt budget")
	require.Nil(t, state.NextAt)
	require.False(t, state.Pending)
	require.Equal(t, "rate_limited", state.LastCode)
}

func TestCodexTicketLifecycleFailureKeepsUsableTicket(t *testing.T) {
	now := time.Now()
	expiry := now.Add(20 * time.Minute)
	due := now.Add(-time.Minute)
	state := &CodexTicketLifecycle{Phase: "ready", ExpiresAt: &expiry, NextAt: &due}
	require.NoError(t, state.begin(now, "lease"))
	state.complete(now, nil, "network", "Harvest connection failed", false)
	require.Equal(t, "retry", state.Phase)
	require.NotNil(t, state.NextAt)
	require.True(t, state.NextAt.After(now), "failed pre-refresh must keep the previous ticket usable")
}

func TestCodexTicketLifecycleSuccessSchedulesRenewal(t *testing.T) {
	now := time.Now()
	expiry := now.Add(time.Hour)
	state := &CodexTicketLifecycle{Phase: "pre_running", Pending: true, ExpiresAt: nil}
	next := expiry.Add(-codexTicketRenewLead)
	state.complete(now, &openAICodexTicket{ExpiresAt: expiry}, "ok", "", true)
	require.Equal(t, "ready", state.Phase)
	require.False(t, state.Pending)
	require.NotNil(t, state.ExpiresAt)
	require.WithinDuration(t, next, *state.NextAt, time.Second)
}

func TestCodexTicketSchedulerSkipsAutomaticAttemptWhenPendingUnconfirmed(t *testing.T) {
	upstream := &codexTicketFuncUpstream{do: func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: http.NoBody}, nil
	}}
	svc := ticketTestService(t, config.OpenAICodexTicketConfig{Enabled: true, HarvestProxyURL: "http://pool.example:80"}, upstream)
	account := activeTicketAccounts(1)[0]
	// Simulate a crash mid-attempt: pending set, never settled.
	account.Extra = map[string]any{
		OpenAICodexTicketEnabledExtraKey: true,
		codexTicketRuntimeExtraKey("gpt-6-astra"): &CodexTicketLifecycle{
			Phase: "pre_running", Pending: true, LeaseID: "dead",
		},
	}
	svc.startOpenAICodexTicketProbe(context.Background(), &account, "gpt-6-astra", svc.openAICodexTicketConfig(), []string{"http://pool.example:80"}, time.Now(), false)
	statuses := OpenAICodexTicketStatuses(&account, svc.openAICodexTicketConfig(), time.Now())
	svc.EnrichOpenAICodexTicketDiagnostics(&account, statuses)
	require.Equal(t, "interrupted", statuses[0].LastErrorCode)
	require.True(t, statuses[0].Paused)
}

type codexTicketManualRepo struct {
	AccountRepository
	account Account
}

func (r *codexTicketManualRepo) GetByID(context.Context, int64) (*Account, error) {
	return &r.account, nil
}
func (r *codexTicketManualRepo) UpdateExtra(context.Context, int64, map[string]any) error { return nil }

func TestHarvestOpenAICodexTicketNowSuccessAndStop(t *testing.T) {
	state := fakeCodexTicketState(292)
	upstream := &codexTicketFuncUpstream{do: func(*http.Request) (*http.Response, error) {
		h := http.Header{}
		h.Set(openAICodexTurnStateHeader, state)
		return &http.Response{StatusCode: 200, Header: h, Body: io.NopCloser(strings.NewReader("data: {}\n\n"))}, nil
	}}
	svc := ticketTestService(t, config.OpenAICodexTicketConfig{Enabled: true, HarvestProxyURL: "http://pool.example:80", Models: []string{"gpt-6-astra"}}, upstream)
	account := activeTicketAccounts(1)[0]
	svc.accountRepo = &codexTicketManualRepo{account: account}

	result := svc.HarvestOpenAICodexTicketNow(context.Background(), 1, "gpt-6-astra")
	require.True(t, result.Success, "code=%s", result.Code)
	require.Equal(t, 292, result.Length)
	require.NotNil(t, result.ExpiresAt)

	require.NoError(t, svc.StopOpenAICodexTicketRenewal(context.Background(), 1, []string{"gpt-6-astra"}))
	// The lifecycle state lives on the persisted account copy.
	persisted := svc.accountRepo.(*codexTicketManualRepo).account
	statuses := OpenAICodexTicketStatuses(&persisted, svc.openAICodexTicketConfig(), time.Now())
	require.Equal(t, "stopped", statuses[0].Phase)
	require.True(t, statuses[0].RenewalStopped)
}

func TestHarvestOpenAICodexTicketNowRejectsDisabledModelAndProxy(t *testing.T) {
	svc := ticketTestService(t, config.OpenAICodexTicketConfig{Enabled: true, Models: []string{"gpt-6-astra"}}, nil)
	account := activeTicketAccounts(1)[0]
	svc.accountRepo = &codexTicketManualRepo{account: account}

	require.Equal(t, "invalid_model", svc.HarvestOpenAICodexTicketNow(context.Background(), 1, "gpt-5.5").Code)
	require.Equal(t, "no_proxy", svc.HarvestOpenAICodexTicketNow(context.Background(), 1, "gpt-6-astra").Code)

	off := ticketTestService(t, config.OpenAICodexTicketConfig{Enabled: false, Models: []string{"gpt-6-astra"}}, nil)
	off.accountRepo = &codexTicketManualRepo{account: account}
	require.Equal(t, "disabled", off.HarvestOpenAICodexTicketNow(context.Background(), 1, "gpt-6-astra").Code)
}
