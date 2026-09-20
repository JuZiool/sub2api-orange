//go:build unit

package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

type ticketScheduledRequest struct {
	id           int64
	model, proxy string
	done         chan struct{}
}
type ticketScheduledUpstream struct {
	HTTPUpstream
	requests chan ticketScheduledRequest
}

func (u *ticketScheduledUpstream) Do(req *http.Request, proxy string, id int64, _ int) (*http.Response, error) {
	body, _ := io.ReadAll(req.Body)
	call := ticketScheduledRequest{id: id, model: extractOpenAICodexTicketModel(body), proxy: proxy, done: make(chan struct{})}
	select {
	case u.requests <- call:
	case <-req.Context().Done():
		return nil, req.Context().Err()
	}
	select {
	case <-call.done:
	case <-req.Context().Done():
		return nil, req.Context().Err()
	}
	return &http.Response{StatusCode: 200, Header: http.Header{}, Body: http.NoBody}, nil
}
func nextTicketRequest(t *testing.T, u *ticketScheduledUpstream) ticketScheduledRequest {
	t.Helper()
	select {
	case c := <-u.requests:
		return c
	case <-time.After(2 * time.Second):
		t.Fatal("expected a ticket request")
		return ticketScheduledRequest{}
	}
}
func ticketJobIdle(s *OpenAIGatewayService, id int64, model string) bool {
	r := &s.openaiCodexTicketScheduler
	r.mu.Lock()
	defer r.mu.Unlock()
	j := r.jobs[openAICodexTicketKey(id, model)]
	return j != nil && !j.InProgress
}
func activeTicketAccounts(ids ...int64) []Account {
	var accounts []Account
	for _, id := range ids {
		a := ticketTestAccount(id)
		a.Status = StatusActive
		a.Credentials["plan_type"] = "pro"
		accounts = append(accounts, *a)
	}
	return accounts
}
func TestCodexTicketSchedulerSlowAccountDoesNotDelayAnotherRetry(t *testing.T) {
	upstream := &ticketScheduledUpstream{requests: make(chan ticketScheduledRequest, 10)}
	svc := ticketTestService(t, config.OpenAICodexTicketConfig{Enabled: true, HarvestProxyURL: "http://pool.example:80", Models: []string{"gpt-6-astra"}}, upstream)
	ctx, cancel := context.WithCancel(context.Background())
	defer func() { cancel(); svc.openaiCodexTicketScheduler.workers.Wait() }()
	accounts := activeTicketAccounts(1, 2)
	svc.dispatchOpenAICodexTickets(ctx, accounts)
	first, second := nextTicketRequest(t, upstream), nextTicketRequest(t, upstream)
	slow, fast := first, second
	if slow.id != 1 {
		slow, fast = fast, slow
	}
	close(fast.done)
	require.Eventually(t, func() bool { return ticketJobIdle(svc, fast.id, fast.model) }, time.Second, time.Millisecond)
	dueTicketJobs(svc)
	svc.dispatchOpenAICodexTickets(ctx, accounts)
	retry := nextTicketRequest(t, upstream)
	require.Equal(t, fast.id, retry.id, "another account retries before the slow request completes")
	close(retry.done)
	close(slow.done)
}
func TestCodexTicketSchedulerRespectsAllThreeLimitsAndDoesNotOverlapKeys(t *testing.T) {
	upstream := &ticketScheduledUpstream{requests: make(chan ticketScheduledRequest, 10)}
	svc := ticketTestService(t, config.OpenAICodexTicketConfig{Enabled: true, HarvestProxyURL: "http://a.example:80\nhttp://b.example:80", HarvestMaxConcurrent: 2, HarvestProxyConcurrency: 1, HarvestAccountConcurrency: 1}, upstream)
	ctx, cancel := context.WithCancel(context.Background())
	defer func() { cancel(); svc.openaiCodexTicketScheduler.workers.Wait() }()
	accounts := activeTicketAccounts(1, 2, 3, 4)
	svc.dispatchOpenAICodexTickets(ctx, accounts)
	a, b := nextTicketRequest(t, upstream), nextTicketRequest(t, upstream)
	require.NotEqual(t, a.id, b.id)
	require.NotEqual(t, a.proxy, b.proxy)
	svc.dispatchOpenAICodexTickets(ctx, accounts)
	select {
	case <-upstream.requests:
		t.Fatal("exceeded harvest limit")
	default:
	}
	close(a.done)
	close(b.done)
	svc.openaiCodexTicketScheduler.workers.Wait()
	// Fresh, unattempted accounts/models sort before failed jobs, preventing starvation.
	svc.dispatchOpenAICodexTickets(ctx, accounts)
	c, d := nextTicketRequest(t, upstream), nextTicketRequest(t, upstream)
	require.False(t, (c.id == a.id && c.model == a.model) || (c.id == b.id && c.model == b.model))
	require.False(t, (d.id == a.id && d.model == a.model) || (d.id == b.id && d.model == b.model))
	close(c.done)
	close(d.done)
}
func TestCodexTicketSchedulerFailureBackoffAndConfigurationRecovery(t *testing.T) {
	var mu sync.Mutex
	count := 0
	upstream := &codexTicketFuncUpstream{do: func(*http.Request) (*http.Response, error) {
		mu.Lock()
		defer mu.Unlock()
		count++
		return &http.Response{StatusCode: 400, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"error":{"code":"model_not_found","message":"sensitive upstream information"}}`))}, nil
	}}
	svc := ticketTestService(t, config.OpenAICodexTicketConfig{Enabled: true, HarvestProxyURL: "http://user:password@pool.example:80"}, upstream)
	account := activeTicketAccounts(1)[0]
	svc.probeOnceOpenAICodexTicket(context.Background(), &account, "gpt-6-astra")
	statuses := OpenAICodexTicketStatuses(&account, svc.openAICodexTicketConfig(), time.Now())
	svc.EnrichOpenAICodexTicketDiagnostics(&account, statuses)
	status := statuses[0]
	require.Equal(t, 1, status.Attempts)
	require.True(t, status.Paused)
	require.Equal(t, "model_unsupported", status.LastErrorCode)
	require.WithinDuration(t, time.Now().Add(30*time.Minute), *status.NextRetryAt, 2*time.Second)
	payload, err := json.Marshal(statuses)
	require.NoError(t, err)
	for _, secret := range []string{"sensitive upstream information", "password", "tok", "pool.example"} {
		require.NotContains(t, string(payload), secret)
	}
	svc.probeOnceOpenAICodexTicket(context.Background(), &account, "gpt-6-astra")
	require.Equal(t, 1, count)
	account.Credentials["access_token"] = "updated-token"
	svc.probeOnceOpenAICodexTicket(context.Background(), &account, "gpt-6-astra")
	require.Equal(t, 2, count, "updating credentials should release the stale pause")
}
func TestCodexTicketSchedulerProxyCooldownDoesNotPenalizeLengthMismatch(t *testing.T) {
	now := time.Now()
	r := &codexTicketScheduler{}
	r.init()
	proxies := []string{"http://a.example:80", "http://b.example:80"}
	r.proxies[proxies[0]] = &codexTicketProxyHealth{failures: 1, cooldown: now.Add(time.Minute)}
	job := &codexTicketJob{nextProxy: 0}
	proxy, index, _ := r.acquireProxy(job, proxies, 1, now)
	require.Equal(t, proxies[1], proxy)
	require.Equal(t, 2, index)
	proxy, _, retry := r.acquireProxy(&codexTicketJob{}, proxies, 1, now)
	require.Empty(t, proxy)
	require.Equal(t, now.Add(time.Second), retry)
	r.proxies[proxies[1]].active = 0
	r.proxies[proxies[1]].cooldown = now.Add(2 * time.Minute)
	_, _, retry = r.acquireProxy(&codexTicketJob{}, proxies, 1, now)
	require.Equal(t, now.Add(time.Minute), retry)
	proxy, _, _ = r.acquireProxy(&codexTicketJob{}, proxies, 1, now.Add(time.Minute))
	require.Equal(t, proxies[0], proxy, "expired cooldown must restore exploration")

	svc := ticketTestService(t, config.OpenAICodexTicketConfig{Enabled: true, HarvestProxyURL: proxies[0]}, &codexTicketFuncUpstream{do: func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: http.NoBody}, nil
	}})
	account := activeTicketAccounts(1)[0]
	svc.probeOnceOpenAICodexTicket(context.Background(), &account, "gpt-6-astra")
	health := svc.openaiCodexTicketScheduler.proxies[proxies[0]]
	require.Zero(t, health.failures)
	require.True(t, health.cooldown.IsZero())
	statuses := OpenAICodexTicketStatuses(&account, svc.openAICodexTicketConfig(), time.Now())
	svc.EnrichOpenAICodexTicketDiagnostics(&account, statuses)
	require.Equal(t, "length_mismatch", statuses[0].LastErrorCode)
	require.Equal(t, 0, statuses[0].LastLength)
}
func TestCodexTicketSchedulerRateLimitAndNetworkFailures(t *testing.T) {
	for _, test := range []struct {
		name, code string
		status     int
		after      string
		err        error
		minimum    time.Duration
		proxyBad   bool
	}{
		{name: "rate", code: "rate_limited", status: 429, after: "120", minimum: 120 * time.Second},
		{name: "network", code: "network", err: errors.New("connect to http://user:secret@proxy.example failed"), minimum: 6 * time.Second, proxyBad: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			svc := ticketTestService(t, config.OpenAICodexTicketConfig{Enabled: true, HarvestProxyURL: "http://pool.example:80"}, &codexTicketFuncUpstream{do: func(*http.Request) (*http.Response, error) {
				if test.err != nil {
					return nil, test.err
				}
				return &http.Response{StatusCode: test.status, Header: http.Header{"Retry-After": []string{test.after}}, Body: http.NoBody}, nil
			}})
			account := activeTicketAccounts(1)[0]
			before := time.Now()
			svc.probeOnceOpenAICodexTicket(context.Background(), &account, "gpt-6-astra")
			statuses := OpenAICodexTicketStatuses(&account, svc.openAICodexTicketConfig(), time.Now())
			svc.EnrichOpenAICodexTicketDiagnostics(&account, statuses)
			require.Equal(t, test.code, statuses[0].LastErrorCode)
			require.False(t, statuses[0].NextRetryAt.Before(before.Add(test.minimum)))
			require.Equal(t, test.proxyBad, !svc.openaiCodexTicketScheduler.proxies["http://pool.example:80"].cooldown.IsZero())
		})
	}
}
func TestCodexTicketSchedulerOffCancelsInflightAndClearsDiagnostics(t *testing.T) {
	upstream := &ticketScheduledUpstream{requests: make(chan ticketScheduledRequest, 10)}
	svc := ticketTestService(t, config.OpenAICodexTicketConfig{Enabled: true, HarvestProxyURL: "http://pool.example:80"}, upstream)
	accounts := activeTicketAccounts(1)
	svc.dispatchOpenAICodexTickets(context.Background(), accounts)
	_ = nextTicketRequest(t, upstream)
	svc.cfg.Gateway.OpenAICodexTicket.Enabled = false
	svc.dispatchOpenAICodexTickets(context.Background(), accounts)
	done := make(chan struct{})
	go func() { svc.openaiCodexTicketScheduler.workers.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("disabling gateway did not cancel in-flight request")
	}
	require.Empty(t, svc.openaiCodexTicketScheduler.jobs)
	require.Zero(t, svc.openaiCodexTicketScheduler.active)
	require.Empty(t, OpenAICodexTicketStatuses(&accounts[0], svc.openAICodexTicketConfig(), time.Now()))
}
func TestCodexTicketSchedulerMissingPlanVisibleAndNoProxyDoesNotCountAttempt(t *testing.T) {
	svc := ticketTestService(t, config.OpenAICodexTicketConfig{Enabled: true}, &httpUpstreamRecorder{})
	account := ticketTestAccount(1)
	svc.probeOnceOpenAICodexTicket(context.Background(), account, "gpt-6-astra")
	statuses := OpenAICodexTicketStatuses(account, svc.openAICodexTicketConfig(), time.Now())
	svc.EnrichOpenAICodexTicketDiagnostics(account, statuses)
	require.False(t, statuses[0].PlanKnown)
	require.Zero(t, statuses[0].Attempts)
	require.Equal(t, "no_proxy", statuses[0].LastErrorCode)
	account.Credentials["plan_type"] = "team"
	require.True(t, codexTicketPlanKnown(account))
}
func TestCodexTicketScheduler401RefreshUsesCoordinatorAndRateLimit(t *testing.T) {
	account := activeTicketAccounts(1)[0]
	account.Credentials["refresh_token"] = "refresh"
	account.Credentials["expires_at"] = fmt.Sprint(time.Now().Add(24 * time.Hour).Unix())
	repo := &refreshAPIAccountRepo{account: &account}
	executor := &refreshAPIExecutorStub{credentials: map[string]any{"access_token": "new-token", "refresh_token": "new-refresh", "expires_at": fmt.Sprint(time.Now().Add(24 * time.Hour).Unix())}}
	svc := ticketTestService(t, config.OpenAICodexTicketConfig{}, nil)
	svc.openAITokenProvider = &OpenAITokenProvider{refreshAPI: NewOAuthRefreshAPI(repo, nil), executor: executor}
	require.True(t, svc.refreshOpenAICodexTicketAuthentication(context.Background(), &account, "tok"))
	require.Equal(t, 1, executor.refreshCalls)
	require.False(t, svc.refreshOpenAICodexTicketAuthentication(context.Background(), &account, "new-token"))
	require.Equal(t, 1, executor.refreshCalls)
}

func TestCodexTicketSchedulerSmallProxyPoolDoesNotStarveLaterAccounts(t *testing.T) {
	upstream := &ticketScheduledUpstream{requests: make(chan ticketScheduledRequest, 100)}
	svc := ticketTestService(t, config.OpenAICodexTicketConfig{Enabled: true, HarvestProxyURL: "http://pool.example:80", HarvestMaxConcurrent: 8, HarvestProxyConcurrency: 2, Models: []string{"gpt-6-astra"}}, upstream)
	ctx, cancel := context.WithCancel(context.Background())
	defer func() { cancel(); svc.openaiCodexTicketScheduler.workers.Wait() }()
	var ids []int64
	for id := int64(1); id <= 40; id++ {
		ids = append(ids, id)
	}
	accounts := activeTicketAccounts(ids...)
	seen := map[int64]int{}
	for round := 0; round < 20; round++ {
		dueTicketJobs(svc) // Simulate all retry/capacity deadlines becoming eligible.
		svc.dispatchOpenAICodexTickets(ctx, accounts)
		a, b := nextTicketRequest(t, upstream), nextTicketRequest(t, upstream)
		seen[a.id]++
		seen[b.id]++
		close(a.done)
		close(b.done)
		svc.openaiCodexTicketScheduler.workers.Wait()
	}
	require.Len(t, seen, 40, "never-attempted accounts must be served before cycling old failures")
}

type ticketRefreshCache struct {
	OpenAITokenCache
	value   string
	deletes int
}

func (c *ticketRefreshCache) DeleteAccessToken(context.Context, string) error {
	c.deletes++
	c.value = ""
	return nil
}
func TestCodexTicketSchedulerRefreshClearsConcurrentStaleCacheRefill(t *testing.T) {
	account := activeTicketAccounts(1)[0]
	account.Credentials["refresh_token"] = "refresh"
	repo := &refreshAPIAccountRepo{account: &account}
	cache := &ticketRefreshCache{value: "tok"}
	executor := &refreshAPIExecutorStub{credentials: map[string]any{"access_token": "fresh", "refresh_token": "new"}, onRefresh: func() { cache.value = "tok" }}
	svc := ticketTestService(t, config.OpenAICodexTicketConfig{}, nil)
	svc.openAITokenProvider = &OpenAITokenProvider{refreshAPI: NewOAuthRefreshAPI(repo, nil), executor: executor, tokenCache: cache}
	require.True(t, svc.refreshOpenAICodexTicketAuthentication(context.Background(), &account, "tok"))
	require.Equal(t, 2, cache.deletes)
	require.Empty(t, cache.value)
}

func TestCodexTicketSchedulerReadFailurePreservesRetryAfter(t *testing.T) {
	account := activeTicketAccounts(1)[0]
	svc := ticketTestService(t, config.OpenAICodexTicketConfig{Enabled: true, HarvestProxyURL: "http://pool.example:80"}, &codexTicketFuncUpstream{do: func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 429, Header: http.Header{"Retry-After": []string{"600"}}, Body: http.NoBody}, nil
	}})
	svc.probeOnceOpenAICodexTicket(context.Background(), &account, "gpt-6-astra")
	before := svc.openaiCodexTicketScheduler.jobs[openAICodexTicketKey(1, "gpt-6-astra")]
	svc.accountRepo = &codexTicketLifecycleRepo{list: func(context.Context) ([]Account, error) { return nil, errors.New("database temporarily unavailable") }}
	svc.refreshOpenAICodexTickets(context.Background())
	after := svc.openaiCodexTicketScheduler.jobs[openAICodexTicketKey(1, "gpt-6-astra")]
	require.Same(t, before, after)
	require.Equal(t, "rate_limited", after.LastErrorCode)
	require.True(t, after.NextRetryAt.After(time.Now().Add(9*time.Minute)))
}

func TestCodexTicketSchedulerCoolingProxyKeepsLastFailureVisible(t *testing.T) {
	svc := ticketTestService(t, config.OpenAICodexTicketConfig{Enabled: true, HarvestProxyURL: "http://pool.example:80"}, &codexTicketFuncUpstream{do: func(*http.Request) (*http.Response, error) { return nil, errors.New("connection refused") }})
	account := activeTicketAccounts(1)[0]
	svc.probeOnceOpenAICodexTicket(context.Background(), &account, "gpt-6-astra")
	dueTicketJobs(svc)
	svc.probeOnceOpenAICodexTicket(context.Background(), &account, "gpt-6-astra")
	statuses := OpenAICodexTicketStatuses(&account, svc.openAICodexTicketConfig(), time.Now())
	svc.EnrichOpenAICodexTicketDiagnostics(&account, statuses)
	require.Equal(t, "network", statuses[0].LastErrorCode)
	require.Equal(t, 1, statuses[0].Attempts, "waiting for cooldown is not a network attempt")
	require.True(t, statuses[0].NextRetryAt.After(time.Now()))
}
