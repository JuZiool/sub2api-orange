package service

import (
	"context"
	"errors"
	"time"
)

// Orange 特有：Codex 人工打票与停止续期（T8）。
// 参考 参考项目/sub2api-HTExplicit 的 codex_ticket_manual.go（只读对照）；Orange 无
// admin_account_jobs 子系统，因此在这里做轻量同步实现：复用同一调度器与三级并发
// 上限，不额外引入后台任务表。
//
// 结果只包含固定文案与标量，绝不回显票据材料、上游正文或代理凭据。

// OpenAICodexTicketManualResult 是一次人工打票的结果摘要。
type OpenAICodexTicketManualResult struct {
	Model      string     `json:"model"`
	Success    bool       `json:"success"`
	Code       string     `json:"code"`
	Message    string     `json:"message"`
	HTTPStatus int        `json:"http_status,omitempty"`
	Length     int        `json:"length,omitempty"`
	Target     int        `json:"target_length,omitempty"`
	DurationMS int64      `json:"duration_ms,omitempty"`
	ExpiresAt  *time.Time `json:"expires_at,omitempty"`
}

func manualCodexTicketMessage(code string) string {
	switch code {
	case "ready":
		return "已取得有效门票"
	case "disabled":
		return "打票总开关未开启"
	case "ineligible":
		return "仅 OpenAI OAuth / Setup Token 非影子账号可打票"
	case "invalid_model":
		return "模型不在打票配置范围内"
	case "no_proxy":
		return "网关未配置打票代理"
	case "busy":
		return "该账号与模型已有进行中的打票任务"
	case "stopped":
		return "无可用票据且续期已停止"
	case "canceled":
		return "打票请求已取消"
	default:
		return "打票失败，请查看采票历史或服务日志"
	}
}

func manualFailure(model, code string) OpenAICodexTicketManualResult {
	return OpenAICodexTicketManualResult{Model: model, Success: false, Code: code, Message: manualCodexTicketMessage(code)}
}

// HarvestOpenAICodexTicketNow 对指定账号与模型立即打一发；force 语义在 Orange 中
// 等价于「必须尝试一发」，是否已有有效门票由调用方决定（本实现总会尝试）。
func (s *OpenAIGatewayService) HarvestOpenAICodexTicketNow(ctx context.Context, accountID int64, model string) OpenAICodexTicketManualResult {
	if ctx == nil {
		ctx = context.Background()
	}
	model = normalizeOpenAICodexTicketModel(model)
	started := time.Now()
	if s == nil || s.accountRepo == nil {
		return manualFailure(model, "internal")
	}
	if !s.openAICodexTicketEnabledContext(ctx) {
		return manualFailure(model, "disabled")
	}
	if model == "" || !s.openAICodexTicketConfiguredModel(model) {
		return manualFailure(model, "invalid_model")
	}
	account, err := s.accountRepo.GetByID(ctx, accountID)
	if err != nil || account == nil {
		return manualFailure(model, "ineligible")
	}
	if !isOpenAICodexTicketAccount(account) || account.IsShadow() {
		return manualFailure(model, "ineligible")
	}
	cfg := s.openAICodexTicketConfig()
	if !s.openAICodexTicketAccountConfig(ctx, account).Enabled {
		return manualFailure(model, "disabled")
	}
	proxies := s.openAICodexTicketHarvestProxies(ctx)
	if len(proxies) == 0 {
		return manualFailure(model, "no_proxy")
	}
	r := &s.openaiCodexTicketScheduler
	r.mu.Lock()
	r.init()
	job := r.job(account, model, cfg, proxies)
	if job.InProgress || job.manual {
		r.mu.Unlock()
		return manualFailure(model, "busy")
	}
	job.manual = true
	job.manualCode = ""
	job.identity = CodexTicketAccountIdentity(account)
	job.NextRetryAt, job.Paused = nil, false
	r.mu.Unlock()

	// 同步执行一发：async=false 让探针在本次调用内完成。
	// T5 的租约与「未确认」保护由 startOpenAICodexTicketProbe 在真正发起前统一预占。
	startedProbe := s.startOpenAICodexTicketProbe(ctx, account, model, cfg, proxies, time.Now(), false)

	r.mu.Lock()
	code := job.manualCode
	job.manual, job.manualCode = false, ""
	r.mu.Unlock()

	result := OpenAICodexTicketManualResult{Model: model, Code: code, Message: manualCodexTicketMessage(code)}
	if !startedProbe {
		if code == "" {
			return manualFailure(model, "busy")
		}
		return result
	}
	if code == "" {
		code = "interrupted"
		result.Code, result.Message = code, manualCodexTicketMessage(code)
	}
	result.Success = code == "ready"
	if state := parseCodexTicketLifecycle(account, model); state != nil {
		result.ExpiresAt = state.ExpiresAt
		result.Target = openAICodexTicketTargetLength(account, cfg)
		result.Length = 0
	}
	if ticket := s.lookupOpenAICodexTicket(account, model); ticket != nil {
		result.Length = ticket.Length
		exp := ticket.ExpiresAt
		result.ExpiresAt = &exp
	}
	result.DurationMS = time.Since(started).Milliseconds()
	return result
}

// StopOpenAICodexTicketRenewal 停止指定账号（可选模型）的自动续期，标记 stopped。
// 既有有效票据保留，仍可被出站注入使用，只是不再自动续期。
func (s *OpenAIGatewayService) StopOpenAICodexTicketRenewal(ctx context.Context, accountID int64, models []string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if s == nil || s.accountRepo == nil {
		return errors.New("ticket store unavailable")
	}
	account, err := s.accountRepo.GetByID(ctx, accountID)
	if err != nil || account == nil {
		return errors.New("account not found")
	}
	cfg := s.openAICodexTicketConfig()
	targets := make([]string, 0, len(models))
	for _, raw := range models {
		model := normalizeOpenAICodexTicketModel(raw)
		if model == "" {
			continue
		}
		targets = append(targets, model)
	}
	if len(targets) == 0 {
		for _, raw := range cfg.Models {
			if model := normalizeOpenAICodexTicketModel(raw); model != "" {
				targets = append(targets, model)
			}
		}
	}
	now := time.Now()
	for _, model := range targets {
		state := parseCodexTicketLifecycle(account, model)
		if state == nil {
			state = &CodexTicketLifecycle{AccountID: account.ID, Model: model}
		}
		state.Phase = "stopped"
		state.NextAt = nil
		state.AutoRenew = false
		state.Pending = false
		state.LeaseID, state.LeaseUntil = "", nil
		state.LastMessage = "自动续期已停止"
		state.LastAttemptAt = &now
		s.persistCodexTicketLifecycle(ctx, account, state)

		r := &s.openaiCodexTicketScheduler
		r.mu.Lock()
		r.init()
		if job := r.jobs[openAICodexTicketKey(account.ID, model)]; job != nil {
			job.Paused = true
			job.NextRetryAt = nil
		}
		r.mu.Unlock()
	}
	return nil
}
