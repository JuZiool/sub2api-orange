package service

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"maps"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"go.uber.org/zap"
)

// Diagnostics are process-local, bounded by the currently enabled account/model
// set. Only successful tickets are persisted; retries do not cause database writes.
type OpenAICodexTicketDiagnostics struct {
	Attempts            int        `json:"attempts"`
	Successes           int        `json:"successes"`
	Failures            int        `json:"failures"`
	InjectMisses        int        `json:"inject_misses"`
	LastInjectMissAt    *time.Time `json:"last_inject_miss_at,omitempty"`
	ConsecutiveFailures int        `json:"consecutive_failures"`
	InProgress          bool       `json:"in_progress"`
	LastAttemptAt       *time.Time `json:"last_attempt_at,omitempty"`
	NextRetryAt         *time.Time `json:"next_retry_at,omitempty"`
	LastErrorCode       string     `json:"last_error_code,omitempty"`
	LastError           string     `json:"last_error,omitempty"`
	LastHTTPStatus      int        `json:"last_http_status"`
	LastLength          int        `json:"last_length"`
	LastProxyIndex      int        `json:"last_proxy_index"`
	Paused              bool       `json:"paused"`
	PlanKnown           bool       `json:"plan_known"`
}

type codexTicketJob struct {
	OpenAICodexTicketDiagnostics
	signature [32]byte
	accountID int64
	model     string
	nextProxy int
	cancel    context.CancelFunc
	// T5 生命周期：人工任务 / 身份绑定 / 有限续期。
	identity   string
	manual     bool
	manualDone chan struct{}
	manualCode string
}
type codexTicketProxyHealth struct {
	active   int
	failures int
	cooldown time.Time
}
type codexTicketScheduler struct {
	mu            sync.Mutex
	jobs          map[string]*codexTicketJob
	proxies       map[string]*codexTicketProxyHealth
	active        int
	accountActive map[int64]int
	authRefreshAt map[int64]time.Time
	telemetry     map[string]*codexTicketTelemetry
	nextEventID   uint64
	workers       sync.WaitGroup
}

// Changing credentials, workspace, model configuration or the proxy pool releases
// a suspended job promptly. Frequent usage/ticket writes must not reset backoff.
func codexTicketJobSignature(account *Account, cfg config.OpenAICodexTicketConfig, proxies []string) [32]byte {
	data, _ := json.Marshal(struct {
		Credentials map[string]any
		Type        string
		Models      []string
		Proxies     []string
		Target      int
	}{account.Credentials, account.Type, cfg.Models, proxies, openAICodexTicketTargetLength(account, cfg)})
	return sha256.Sum256(data)
}

func (r *codexTicketScheduler) init() {
	if r.jobs == nil {
		r.jobs = make(map[string]*codexTicketJob)
	}
	if r.proxies == nil {
		r.proxies = make(map[string]*codexTicketProxyHealth)
	}
	if r.accountActive == nil {
		r.accountActive = make(map[int64]int)
	}
	if r.authRefreshAt == nil {
		r.authRefreshAt = make(map[int64]time.Time)
	}
}

func (s *OpenAIGatewayService) cancelOpenAICodexTicketJobs() {
	r := &s.openaiCodexTicketScheduler
	r.mu.Lock()
	defer r.mu.Unlock()
	for key, job := range r.jobs {
		if job.cancel != nil {
			job.cancel()
		}
		delete(r.jobs, key)
	}
	clear(r.authRefreshAt)
	clear(r.telemetry)
}

type codexTicketCandidate struct {
	account     Account
	model       string
	lastAttempt time.Time
}

// Called by one lightweight dispatcher. Work is admitted without blocking on a
// semaphore, so waiting jobs cannot monopolize the pool or delay other accounts.
func (s *OpenAIGatewayService) dispatchOpenAICodexTickets(ctx context.Context, accounts []Account) {
	if ctx.Err() != nil {
		return
	}
	if !s.openAICodexTicketEnabledContext(ctx) {
		s.cancelOpenAICodexTicketJobs()
		return
	}
	cfg := s.openAICodexTicketConfig()
	proxies := s.openAICodexTicketHarvestProxies(ctx)
	now := time.Now()
	activeKeys := make(map[string]bool)
	candidates := make([]codexTicketCandidate, 0)
	r := &s.openaiCodexTicketScheduler
	// Fetch policy before holding the scheduler lock: settings reads may do I/O.
	for _, account := range accounts {
		if account.Status != StatusActive || !s.openAICodexTicketAccountConfig(ctx, &account).Enabled {
			continue
		}
		for _, rawModel := range cfg.Models {
			model := normalizeOpenAICodexTicketModel(rawModel)
			key := openAICodexTicketKey(account.ID, model)
			if model == "" || activeKeys[key] {
				continue
			}
			activeKeys[key] = true
			r.mu.Lock()
			r.init()
			job := r.job(&account, model, cfg, proxies)
			lastAttempt := time.Time{}
			// Fair admission is based on the last actual attempt, not on a
			// moving proxy-capacity wait. Unattempted jobs always come first.
			if job.LastAttemptAt != nil {
				lastAttempt = *job.LastAttemptAt
			}
			r.mu.Unlock()
			candidates = append(candidates, codexTicketCandidate{account: account, model: model, lastAttempt: lastAttempt})
		}
	}
	r.mu.Lock()
	for key, job := range r.jobs {
		if !activeKeys[key] {
			if job.cancel != nil {
				job.cancel()
			}
			delete(r.jobs, key)
		}
	}
	for key := range r.telemetry {
		if !activeKeys[key] {
			delete(r.telemetry, key)
		}
	}
	activeAccounts := make(map[int64]bool)
	for _, candidate := range candidates {
		activeAccounts[candidate.account.ID] = true
	}
	for id := range r.authRefreshAt {
		if !activeAccounts[id] {
			delete(r.authRefreshAt, id)
		}
	}
	configuredProxies := make(map[string]bool)
	for _, proxy := range proxies {
		configuredProxies[proxy] = true
	}
	for proxy, health := range r.proxies {
		if !configuredProxies[proxy] && health.active == 0 {
			delete(r.proxies, proxy)
		}
	}
	r.mu.Unlock()
	sort.SliceStable(candidates, func(i, j int) bool { return candidates[i].lastAttempt.Before(candidates[j].lastAttempt) })
	for _, candidate := range candidates {
		if ctx.Err() != nil {
			return
		}
		s.startOpenAICodexTicketProbe(ctx, &candidate.account, candidate.model, cfg, proxies, now, true)
	}
}

// r.mu must be held. A running job finishes with its original credential snapshot;
// the next dispatch invalidates its retry state when the snapshot has changed.
func (r *codexTicketScheduler) job(account *Account, model string, cfg config.OpenAICodexTicketConfig, proxies []string) *codexTicketJob {
	key := openAICodexTicketKey(account.ID, model)
	signature := codexTicketJobSignature(account, cfg, proxies)
	job := r.jobs[key]
	if job == nil {
		offset := 0
		for i, m := range cfg.Models {
			if strings.TrimSpace(m) == model {
				offset = i
				break
			}
		}
		job = &codexTicketJob{accountID: account.ID, model: model, nextProxy: int(account.ID) + offset, signature: signature}
		r.jobs[key] = job
	} else if job.InProgress && job.signature != signature {
		if job.cancel != nil {
			job.cancel()
		}
	} else if !job.InProgress && job.signature != signature {
		job.signature = signature
		job.NextRetryAt = nil
		job.Paused = false
		job.ConsecutiveFailures = 0
		job.LastErrorCode, job.LastError = "", ""
	}
	job.PlanKnown = codexTicketPlanKnown(account)
	return job
}

func (s *OpenAIGatewayService) startOpenAICodexTicketProbe(ctx context.Context, account *Account, model string, cfg config.OpenAICodexTicketConfig, proxies []string, now time.Time, async bool) bool {
	if ctx.Err() != nil || s.httpUpstream == nil || !s.openAICodexTicketAccountConfig(ctx, account).Enabled {
		return false
	}
	ticket := s.lookupOpenAICodexTicket(account, model)
	r := &s.openaiCodexTicketScheduler
	r.mu.Lock()
	r.init()
	job := r.job(account, model, cfg, proxies)
	if job.InProgress {
		r.mu.Unlock()
		return false
	}
	// T5：凭证身份变化则作废本 job 的续期状态（旧票据归属另一个主体）。
	identity := CodexTicketAccountIdentity(account)
	if job.identity != "" && job.identity != identity {
		job.Paused, job.NextRetryAt, job.ConsecutiveFailures = false, nil, 0
		job.LastErrorCode, job.LastError = "", ""
	}
	job.identity = identity
	// T5：生命周期（租约 / 未确认即停止 / 有限续期）。仅自动路径受此约束，人工不受「未到期」限制。
	state := parseCodexTicketLifecycle(account, model)
	if !job.manual {
		if state != nil && state.Pending {
			// 上次尝试已开始但未结算（进程中断）；绝不自动重发。
			job.LastErrorCode, job.LastError = "interrupted", "Previous attempt was not confirmed; not repeating"
			next := now.Add(30 * time.Minute)
			job.NextRetryAt = &next
			job.Paused = true
			r.mu.Unlock()
			return false
		}
		if state != nil && state.dueForRenewal(now) {
			if err := state.begin(now, codexTicketNewLeaseID()); err != nil {
				// 未到期（含刚进入 retry 等待）——按状态推进下的下次时间重排。
				if state.NextAt != nil {
					job.NextRetryAt = state.NextAt
				}
				if errors.Is(err, ErrCodexTicketAlreadyTried) {
					job.LastErrorCode, job.LastError = "interrupted", "Previous attempt was not confirmed; not repeating"
					job.Paused = true
				}
				r.mu.Unlock()
				return false
			}
			s.persistCodexTicketLifecycle(ctx, account, state)
		}
	}
	if !job.manual && ticket.valid(now, openAICodexTicketTargetLength(account, cfg)) && !ticket.needsRefresh(now, time.Duration(cfg.RefreshBeforeSeconds)*time.Second) {
		next := ticket.ExpiresAt.Add(-time.Duration(cfg.RefreshBeforeSeconds) * time.Second)
		job.NextRetryAt = &next
		job.Paused = false
		r.mu.Unlock()
		return false
	}
	if !job.manual && job.NextRetryAt != nil && now.Before(*job.NextRetryAt) {
		r.mu.Unlock()
		return false
	}
	if r.active >= cfg.HarvestMaxConcurrent || r.accountActive[account.ID] >= cfg.HarvestAccountConcurrency {
		r.mu.Unlock()
		return false
	}
	if len(proxies) == 0 {
		next := now.Add(time.Duration(cfg.HarvestProbeIntervalSeconds) * time.Second)
		job.LastErrorCode, job.LastError = "no_proxy", "No harvest proxy is configured"
		job.NextRetryAt = &next
		r.mu.Unlock()
		return false
	}
	proxy, index, next := r.acquireProxy(job, proxies, cfg.HarvestProxyConcurrency, now)
	if proxy == "" {
		job.NextRetryAt = &next
		// Keep the last actual failure visible while its proxy is cooling down.
		if job.LastErrorCode == "" || job.LastErrorCode == "no_proxy" || job.LastErrorCode == "proxy_cooldown" {
			job.LastErrorCode, job.LastError = "proxy_cooldown", "Waiting for an available harvest proxy"
		}
		r.mu.Unlock()
		return false
	}
	attemptCtx, cancel := context.WithTimeout(ctx, time.Duration(cfg.HarvestAttemptTimeoutSeconds)*time.Second)
	job.InProgress, job.Paused, job.cancel = true, false, cancel
	if job.identity == "" {
		job.identity = CodexTicketAccountIdentity(account)
	}
	job.Attempts++
	job.LastAttemptAt, job.NextRetryAt = &now, nil
	job.LastProxyIndex = index
	r.active++
	r.accountActive[account.ID]++
	r.workers.Add(1)
	r.mu.Unlock()
	acc := *account
	acc.Extra, acc.Credentials = maps.Clone(account.Extra), maps.Clone(account.Credentials)
	run := func() {
		defer r.workers.Done()
		defer cancel()
		s.runOpenAICodexTicketProbe(attemptCtx, &acc, model, proxy, job, cfg)
	}
	if async {
		go run()
	} else {
		run()
	}
	return true
}

// Proxy health is shared across accounts; a ticket-length mismatch never marks
// an exit unhealthy. Account/model offset and round-robin retain fair exploration.
func (r *codexTicketScheduler) acquireProxy(job *codexTicketJob, proxies []string, limit int, now time.Time) (string, int, time.Time) {
	var earliest time.Time
	for offset := 0; offset < len(proxies); offset++ {
		index := (job.nextProxy + offset) % len(proxies)
		proxy := proxies[index]
		health := r.proxies[proxy]
		if health == nil {
			health = &codexTicketProxyHealth{}
			r.proxies[proxy] = health
		}
		availableAt := health.cooldown
		if health.active >= limit && availableAt.Before(now.Add(time.Second)) {
			availableAt = now.Add(time.Second)
		}
		if availableAt.After(now) {
			if earliest.IsZero() || availableAt.Before(earliest) {
				earliest = availableAt
			}
			continue
		}
		health.active++
		job.nextProxy = index + 1
		return proxy, index + 1, time.Time{}
	}
	if earliest.IsZero() {
		earliest = now.Add(time.Second)
	}
	return "", 0, earliest
}

func (s *OpenAIGatewayService) runOpenAICodexTicketProbe(ctx context.Context, account *Account, model, proxy string, job *codexTicketJob, cfg config.OpenAICodexTicketConfig) {
	started := time.Now()
	proxyIndex, attempts := job.LastProxyIndex, job.Attempts
	var result *openAICodexTicketProbeError
	var state string
	status := 0
	token, _, err := s.GetAccessToken(ctx, account)
	if err != nil || strings.TrimSpace(token) == "" {
		result = &openAICodexTicketProbeError{Code: "auth", Message: "Account authentication is unavailable"}
	} else {
		state, status, err = s.fireOpenAICodexTicketProbe(ctx, account, token, model, proxy, time.Duration(cfg.HarvestAttemptTimeoutSeconds)*time.Second)
		if err != nil || status != http.StatusOK {
			result = classifyOpenAICodexTicketProbeError(err, status)
		}
	}
	target := openAICodexTicketTargetLength(account, cfg)
	if result == nil && len(state) != target {
		result = &openAICodexTicketProbeError{Code: "length_mismatch", Message: "Ticket length does not match the account plan", HTTPStatus: status}
	}
	if result == nil && !strings.HasPrefix(state, openAICodexTicketStatePrefix) {
		result = &openAICodexTicketProbeError{Code: "invalid_ticket", Message: "Upstream returned an invalid ticket", HTTPStatus: status}
	}
	refreshed := false
	if result != nil && result.Code == "auth" && status == http.StatusUnauthorized && ctx.Err() == nil {
		refreshed = s.refreshOpenAICodexTicketAuthentication(ctx, account, token)
	}
	if errors.Is(ctx.Err(), context.Canceled) {
		result = classifyOpenAICodexTicketProbeError(ctx.Err(), 0)
	} else if errors.Is(ctx.Err(), context.DeadlineExceeded) && (result == nil || status == 0) {
		result = classifyOpenAICodexTicketProbeError(ctx.Err(), 0)
		// Token retrieval has not contacted the harvest proxy yet.
		if strings.TrimSpace(token) == "" {
			result.ProxyFailure = false
		}
	}
	now := time.Now()
	var storedTicket *openAICodexTicket
	if result == nil {
		storedTicket = &openAICodexTicket{AccountID: account.ID, Model: model, State: state, Length: len(state), CapturedAt: now, ExpiresAt: now.Add(time.Duration(cfg.TTLSeconds) * time.Second), Attempts: attempts}
		s.storeOpenAICodexTicket(ctx, account, storedTicket)
	}
	r := &s.openaiCodexTicketScheduler
	r.mu.Lock()
	r.active--
	r.accountActive[account.ID]--
	if r.accountActive[account.ID] == 0 {
		delete(r.accountActive, account.ID)
	}
	if health := r.proxies[proxy]; health != nil {
		health.active--
		if result != nil && result.ProxyFailure {
			health.failures++
			health.cooldown = now.Add(codexTicketExponentialDelay(15*time.Second, health.failures, 2*time.Minute))
		} else if status > 0 && status != http.StatusProxyAuthRequired && (result == nil || result.Code != "canceled") {
			health.failures = 0
			health.cooldown = time.Time{}
		}
	}
	job.InProgress, job.cancel = false, nil
	job.LastHTTPStatus, job.LastLength = status, len(state)
	if job.manual {
		if result == nil {
			job.manualCode = "ready"
		} else {
			job.manualCode = result.Code
		}
	}
	// T5：结算生命周期——成功登记票据到期与续期时间；失败保留可用旧票并转入 retry。
	if state := parseCodexTicketLifecycle(account, model); state != nil {
		code, message, success := "ok", "", result == nil
		if result != nil {
			code, message = result.Code, result.Message
		}
		state.complete(now, storedTicket, code, message, success)
		if !success {
			// 以生命周期状态覆盖退避：未确认不再自动重发，直到 retry 到期或人为干预。
			if state.Phase == "stopped" {
				job.Paused = true
				job.NextRetryAt = nil
			} else if state.NextAt != nil {
				job.NextRetryAt = state.NextAt
			}
		}
		s.persistCodexTicketLifecycle(ctx, account, state)
	}
	// Removed/disabled jobs must not recreate telemetry after cancellation.
	if r.jobs[openAICodexTicketKey(account.ID, model)] == job {
		r.recordProbeEvent(account.ID, model, result, status, len(state), target, proxyIndex, started, now)
	}
	if result == nil {
		job.ConsecutiveFailures = 0
		job.LastErrorCode, job.LastError = "", ""
		next := now.Add(time.Duration(max(1, cfg.TTLSeconds-cfg.RefreshBeforeSeconds)) * time.Second)
		job.NextRetryAt = &next
	} else if result.Code != "canceled" {
		job.ConsecutiveFailures++
		job.LastErrorCode, job.LastError = result.Code, result.Message
		delay, paused := codexTicketRetryDelay(result, job.ConsecutiveFailures, time.Duration(cfg.HarvestProbeIntervalSeconds)*time.Second)
		if refreshed {
			delay = time.Duration(cfg.HarvestProbeIntervalSeconds) * time.Second
			paused = false
		}
		// Small deterministic jitter avoids synchronized retries without ever
		// shortening Retry-After or the configured minimum interval.
		jitter := time.Duration((uint64(account.ID)*31+uint64(job.Attempts)*17+uint64(len(model))*13)%1000) * time.Millisecond
		next := now.Add(delay + jitter)
		job.NextRetryAt, job.Paused = &next, paused
	}
	r.mu.Unlock()
	if result == nil {
		logger.L().Info("openai_codex_ticket harvested", zap.Int64("account_id", account.ID), zap.String("model", model), zap.Int("harvest_proxy_index", proxyIndex), zap.Int("length", len(state)))
	} else if result.Code != "canceled" {
		logger.L().Info("openai_codex_ticket probe miss", zap.Int64("account_id", account.ID), zap.String("model", model), zap.Int("harvest_proxy_index", proxyIndex), zap.String("reason", result.Code), zap.Int("http", status), zap.Int("len", len(state)), zap.Int("target_length", target))
	}
}

func codexTicketExponentialDelay(base time.Duration, failures int, cap time.Duration) time.Duration {
	for i := 1; i < failures && base < cap; i++ {
		base *= 2
	}
	return min(base, cap)
}
func codexTicketRetryDelay(result *openAICodexTicketProbeError, failures int, base time.Duration) (time.Duration, bool) {
	delay, paused := base, false
	switch result.Code {
	case "model_unsupported":
		delay, paused = 30*time.Minute, true
	case "auth", "forbidden", "quota":
		delay, paused = 5*time.Minute, true
	case "rate_limited":
		delay = codexTicketExponentialDelay(base, failures, 5*time.Minute)
		if delay < 30*time.Second {
			delay = 30 * time.Second
		}
	case "network", "timeout", "proxy_auth", "upstream_5xx", "invalid_response":
		delay = codexTicketExponentialDelay(base, failures, 2*time.Minute)
	}
	if result.RetryAfter > delay {
		delay = result.RetryAfter
	}
	return delay, paused
}

// EnrichOpenAICodexTicketDiagnostics is used only by administrator account DTOs.
// Cached valid tickets are also reflected before the scheduler snapshot catches up.
func (s *OpenAIGatewayService) EnrichOpenAICodexTicketDiagnostics(account *Account, statuses []OpenAICodexTicketStatus) {
	if s == nil || account == nil || len(statuses) == 0 {
		return
	}
	r := &s.openaiCodexTicketScheduler
	now := time.Now()
	for i := range statuses {
		status := &statuses[i]
		if ticket := s.lookupOpenAICodexTicket(account, status.Model); ticket.valid(now, status.TargetLength) {
			status.Ready = true
			status.Length = ticket.Length
			status.RemainingSeconds = int64(ticket.ExpiresAt.Sub(now) / time.Second)
			if status.RemainingSeconds < 0 {
				status.RemainingSeconds = 0
			}
			exp := ticket.ExpiresAt
			status.ExpiresAt = &exp
			status.Blocked = false
		}
		r.mu.Lock()
		if job := r.jobs[openAICodexTicketKey(account.ID, status.Model)]; job != nil {
			status.OpenAICodexTicketDiagnostics = job.OpenAICodexTicketDiagnostics
		}
		if telemetry := r.telemetry[openAICodexTicketKey(account.ID, status.Model)]; telemetry != nil {
			status.Successes, status.Failures = telemetry.successes, telemetry.failures
			status.InjectMisses, status.LastInjectMissAt = telemetry.injectMisses, telemetry.lastInjectMissAt
		}
		r.mu.Unlock()
		status.PlanKnown = codexTicketPlanKnown(account)
	}
}

// A 401 may invalidate a not-yet-expired access token. Reuse the normal refresh
// coordinator and its locks/CAS; never rotate credentials with a direct HTTP call.
type codexTicketRejectedTokenExecutor struct {
	OAuthRefreshExecutor
	rejected string
}

func (e codexTicketRejectedTokenExecutor) NeedsRefresh(account *Account, window time.Duration) bool {
	return account.GetOpenAIAccessToken() == e.rejected || e.OAuthRefreshExecutor.NeedsRefresh(account, window)
}
func (s *OpenAIGatewayService) refreshOpenAICodexTicketAuthentication(ctx context.Context, account *Account, rejected string) bool {
	p := s.openAITokenProvider
	if p == nil || p.refreshAPI == nil || p.executor == nil || account.Type != AccountTypeOAuth || account.IsOpenAIPersonalAccessToken() || strings.TrimSpace(account.GetOpenAIRefreshToken()) == "" {
		return false
	}
	r := &s.openaiCodexTicketScheduler
	r.mu.Lock()
	r.init()
	if time.Since(r.authRefreshAt[account.ID]) < 5*time.Minute {
		r.mu.Unlock()
		return false
	}
	r.authRefreshAt[account.ID] = time.Now()
	r.mu.Unlock()
	if p.tokenCache != nil {
		_ = p.tokenCache.DeleteAccessToken(ctx, OpenAITokenCacheKey(account))
	}
	result, err := p.refreshAPI.RefreshIfNeeded(withOAuthRefreshRequestPath(ctx), account, codexTicketRejectedTokenExecutor{p.executor, rejected}, openAITokenRefreshSkew)
	refreshed := err == nil && result != nil && (result.Refreshed || (result.Account != nil && result.Account.GetOpenAIAccessToken() != rejected))
	if refreshed && p.tokenCache != nil {
		// A business request may have refilled the old token while refresh ran.
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		defer cancel()
		_ = p.tokenCache.DeleteAccessToken(cleanupCtx, OpenAITokenCacheKey(account))
	}
	return refreshed
}
