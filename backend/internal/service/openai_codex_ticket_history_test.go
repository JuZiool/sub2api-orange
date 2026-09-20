//go:build unit

package service

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestCodexTicketInjectMissConcurrentWithRunningProbe(t *testing.T) {
	upstream := &ticketScheduledUpstream{requests: make(chan ticketScheduledRequest, 1)}
	svc := ticketTestService(t, config.OpenAICodexTicketConfig{Enabled: true, FailClosed: true, Models: []string{"gpt-6-astra"}, HarvestProxyURL: "http://proxy.example:80"}, upstream)
	account := activeTicketAccounts(7)[0]
	ctx, cancel := context.WithCancel(context.Background())
	defer func() { cancel(); svc.openaiCodexTicketScheduler.workers.Wait() }()
	svc.dispatchOpenAICodexTickets(ctx, []Account{account})
	call := nextTicketRequest(t, upstream)
	const count = 100
	errs := make(chan error, count)
	var wg sync.WaitGroup
	for i := 0; i < count; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- svc.applyOpenAICodexTicket(ctx, &account, call.model, http.Header{})
			statuses := OpenAICodexTicketStatuses(&account, svc.openAICodexTicketConfig(), time.Now())
			svc.EnrichOpenAICodexTicketDiagnostics(&account, statuses)
			svc.OpenAICodexTicketHistory(ctx, &account, "")
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.ErrorIs(t, err, ErrOpenAICodexTicketUnavailable)
	}
	close(call.done)
	svc.openaiCodexTicketScheduler.workers.Wait()
	statuses := OpenAICodexTicketStatuses(&account, svc.openAICodexTicketConfig(), time.Now())
	svc.EnrichOpenAICodexTicketDiagnostics(&account, statuses)
	require.Equal(t, count, statuses[0].InjectMisses)
	require.NotNil(t, statuses[0].LastInjectMissAt)
	require.Equal(t, 1, statuses[0].Attempts, "requests must never alter scheduler attempts")
	require.Equal(t, 1, statuses[0].Failures)
	require.Zero(t, statuses[0].Successes)
	events := svc.OpenAICodexTicketHistory(ctx, &account, "")
	require.Len(t, events, 1)
	require.Equal(t, "failure", events[0].Outcome)
	require.Equal(t, "length_mismatch", events[0].ErrorCode)
	require.Equal(t, 292, events[0].TargetLength)
}

func TestCodexTicketInjectMissOnlyCountsEligibleOutboundInjection(t *testing.T) {
	for _, name := range []string{"gateway_off", "account_off", "non_target", "non_oauth", "candidate_only", "valid", "fail_open", "fail_closed"} {
		t.Run(name, func(t *testing.T) {
			svc := ticketTestService(t, config.OpenAICodexTicketConfig{Enabled: true, FailClosed: true}, nil)
			account := ticketTestAccount(1)
			model := "gpt-6-astra"
			switch name {
			case "gateway_off":
				svc.cfg.Gateway.OpenAICodexTicket.Enabled = false
			case "account_off":
				account.Extra = map[string]any{OpenAICodexTicketEnabledExtraKey: false}
			case "non_target":
				model = "other-model"
			case "non_oauth":
				account.Type = AccountTypeAPIKey
			case "valid":
				svc.storeOpenAICodexTicket(context.Background(), account, &openAICodexTicket{Model: model, State: fakeCodexTicketState(292), Length: 292, ExpiresAt: time.Now().Add(time.Hour)})
			case "fail_open":
				svc.cfg.Gateway.OpenAICodexTicket.FailClosed = false
			}
			if name == "candidate_only" {
				require.True(t, svc.openAICodexTicketBlocksAccount(account, model))
			} else {
				err := svc.applyOpenAICodexTicket(context.Background(), account, model, http.Header{})
				if name == "fail_closed" {
					require.ErrorIs(t, err, ErrOpenAICodexTicketUnavailable)
				} else {
					require.NoError(t, err)
				}
			}
			r := &svc.openaiCodexTicketScheduler
			if name == "fail_open" || name == "fail_closed" {
				require.Equal(t, 1, r.telemetry[openAICodexTicketKey(1, model)].injectMisses)
			} else {
				require.Empty(t, r.telemetry)
			}
			require.Empty(t, r.jobs, "business requests must not create scheduler jobs")
		})
	}
}

func TestCodexTicketHistoryBoundedOrderedPrivateAndIsolated(t *testing.T) {
	svc := ticketTestService(t, config.OpenAICodexTicketConfig{Enabled: true}, nil)
	r := &svc.openaiCodexTicketScheduler
	now := time.Now()
	for i := 0; i < 31; i++ {
		for _, model := range []string{"gpt-6-astra", "gpt-5.6-sol"} {
			r.recordProbeEvent(1, model, nil, 200, 292, 292, 1, now, now)
		}
	}
	r.recordProbeEvent(2, "gpt-6-astra", &openAICodexTicketProbeError{Code: "private-code", Message: "private-token-response"}, 500, 0, 332, 2, now, now)
	r.recordProbeEvent(1, "gpt-6-astra", &openAICodexTicketProbeError{Code: "canceled"}, 0, 0, 292, 1, now, now)
	require.Len(t, r.telemetry[openAICodexTicketKey(1, "gpt-6-astra")].events, 20)
	all := svc.OpenAICodexTicketHistory(context.Background(), ticketTestAccount(1), "")
	require.Len(t, all, 20)
	require.Equal(t, "canceled", all[0].Outcome)
	for i := 1; i < len(all); i++ {
		require.Greater(t, all[i-1].ID, all[i].ID)
	}
	filtered := svc.OpenAICodexTicketHistory(context.Background(), ticketTestAccount(1), "gpt-5.6-sol")
	require.Len(t, filtered, 20)
	for _, event := range filtered {
		require.Equal(t, "gpt-5.6-sol", event.Model)
	}
	other := svc.OpenAICodexTicketHistory(context.Background(), ticketTestAccount(2), "")
	require.Len(t, other, 1)
	require.Equal(t, "invalid_response", other[0].ErrorCode)
	payload, err := json.Marshal(other)
	require.NoError(t, err)
	require.NotContains(t, string(payload), "private")
	all[0].Model = "mutated"
	require.Equal(t, "gpt-6-astra", svc.OpenAICodexTicketHistory(context.Background(), ticketTestAccount(1), "")[0].Model)
	require.Empty(t, svc.OpenAICodexTicketHistory(context.Background(), ticketTestAccount(1), "unconfigured"))
	account := ticketTestAccount(1)
	account.Extra = map[string]any{OpenAICodexTicketEnabledExtraKey: false}
	require.Empty(t, svc.OpenAICodexTicketHistory(context.Background(), account, ""))
	svc.cfg.Gateway.OpenAICodexTicket.Enabled = false
	require.Empty(t, svc.OpenAICodexTicketHistory(context.Background(), ticketTestAccount(1), ""))
	svc.dispatchOpenAICodexTickets(context.Background(), nil)
	require.Empty(t, r.telemetry)
}

func TestCodexTicketHistoryRecordsSuccessfulProbe(t *testing.T) {
	upstream := &codexTicketFuncUpstream{do: func(*http.Request) (*http.Response, error) {
		h := http.Header{}
		h.Set(openAICodexTurnStateHeader, fakeCodexTicketState(292))
		return &http.Response{StatusCode: 200, Header: h, Body: http.NoBody}, nil
	}}
	svc := ticketTestService(t, config.OpenAICodexTicketConfig{Enabled: true, HarvestProxyURL: "http://secret:password@proxy.example:80"}, upstream)
	account := ticketTestAccount(1)
	svc.probeOnceOpenAICodexTicket(context.Background(), account, "gpt-6-astra")
	events := svc.OpenAICodexTicketHistory(context.Background(), account, "")
	require.Len(t, events, 1)
	require.Equal(t, "success", events[0].Outcome)
	require.Equal(t, 292, events[0].Length)
	require.GreaterOrEqual(t, events[0].DurationMs, int64(0))
	statuses := OpenAICodexTicketStatuses(account, svc.openAICodexTicketConfig(), time.Now())
	svc.EnrichOpenAICodexTicketDiagnostics(account, statuses)
	require.Equal(t, 1, statuses[0].Successes)
	require.Zero(t, statuses[0].Failures)
	payload, err := json.Marshal(statuses)
	require.NoError(t, err)
	require.NotContains(t, string(payload), "events")
	payload, err = json.Marshal(events)
	require.NoError(t, err)
	for _, secret := range []string{"password", "proxy.example", fakeCodexTicketState(292)} {
		require.NotContains(t, string(payload), secret)
	}
}

func TestCodexTicketHistoryCancellationDoesNotCountFailure(t *testing.T) {
	upstream := &ticketScheduledUpstream{requests: make(chan ticketScheduledRequest, 1)}
	svc := ticketTestService(t, config.OpenAICodexTicketConfig{Enabled: true, Models: []string{"gpt-6-astra"}, HarvestProxyURL: "http://proxy.example:80"}, upstream)
	account := activeTicketAccounts(1)[0]
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	svc.dispatchOpenAICodexTickets(ctx, []Account{account})
	nextTicketRequest(t, upstream)
	cancel()
	svc.openaiCodexTicketScheduler.workers.Wait()
	events := svc.OpenAICodexTicketHistory(context.Background(), &account, "")
	require.Len(t, events, 1)
	require.Equal(t, "canceled", events[0].Outcome)
	require.Equal(t, "canceled", events[0].ErrorCode)
	statuses := OpenAICodexTicketStatuses(&account, svc.openAICodexTicketConfig(), time.Now())
	svc.EnrichOpenAICodexTicketDiagnostics(&account, statuses)
	require.Zero(t, statuses[0].Failures)
	require.Zero(t, statuses[0].Successes)
	require.Equal(t, 1, statuses[0].Attempts)
}
