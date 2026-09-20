package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"go.uber.org/zap"
)

// Orange 特有：Codex 打票生命周期（T5）。
// 参考 参考项目/sub2api-HTExplicit 的 codex_ticket_lifecycle.go（只读对照）设计，
// 但 Orange 不引入独立存储/DB 事务：生命周期状态与票据一起写入 accounts.extra，
// 由进程内调度器在单账号单模型的 job 上串行执行，因而崩溃语义可安全收紧为
// 「未确认即停止，绝不自动重复发送」。
const (
	// OpenAICodexTicketRuntimeExtraPrefix 生命周期状态键前缀（同票据一样属私有键）。
	OpenAICodexTicketRuntimeExtraPrefix = "codex_ticket_runtime:"

	codexTicketRenewLead          = time.Minute
	codexTicketMaxPendingAttempts = 3
)

// CodexTicketLifecycle 是账号单模型打票的生命周期状态；不含票据材料。
type CodexTicketLifecycle struct {
	AccountID     int64      `json:"account_id"`
	Model         string     `json:"model"`
	Phase         string     `json:"phase"`
	NextAt        *time.Time `json:"next_attempt_at,omitempty"`
	ExpiresAt     *time.Time `json:"expires_at,omitempty"`
	LeaseID       string     `json:"lease_id,omitempty"`
	LeaseUntil    *time.Time `json:"lease_until,omitempty"`
	Pending       bool       `json:"pending,omitempty"`
	Attempts      int        `json:"attempts,omitempty"`
	AutoRenew     bool       `json:"auto_renew,omitempty"`
	LastAttemptAt *time.Time `json:"last_attempt_at,omitempty"`
	LastCode      string     `json:"last_code,omitempty"`
	LastMessage   string     `json:"last_message,omitempty"`
}

var (
	ErrCodexTicketNotDue       = errors.New("ticket renewal is not due")
	ErrCodexTicketBusy         = errors.New("ticket operation already running")
	ErrCodexTicketAlreadyTried = errors.New("ticket attempt unconfirmed after restart")
	ErrCodexTicketStale        = errors.New("ticket lifecycle state is stale")
)

// CodexTicketAccountIdentity 标识票据归属主体。token 刷新不改主体；重新授权到
// 另一个主体（换号/换工作区）必须让旧票据作废。凭证缺失时回退 email。
func CodexTicketAccountIdentity(a *Account) string {
	if a == nil {
		return ""
	}
	parts := []string{a.Platform, a.Type, a.GetCredential("chatgpt_account_id"), a.GetCredential("chatgpt_user_id"), a.GetCredential("organization_id")}
	if parts[2] == "" && parts[3] == "" {
		parts = append(parts, a.GetCredential("email"))
	}
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(sum[:])
}

func codexTicketNewLeaseID() string {
	var b [16]byte
	seed := sha256.Sum256([]byte(strconv.FormatInt(time.Now().UnixNano(), 10)))
	copy(b[:], seed[:16])
	return hex.EncodeToString(b[:])
}

func codexTicketRuntimeExtraKey(model string) string {
	return OpenAICodexTicketRuntimeExtraPrefix + strings.TrimSpace(model)
}

func isOpenAICodexTicketRuntimeExtraKey(key string) bool {
	return strings.HasPrefix(key, OpenAICodexTicketRuntimeExtraPrefix)
}

func parseCodexTicketLifecycle(account *Account, model string) *CodexTicketLifecycle {
	if account == nil || account.Extra == nil {
		return nil
	}
	raw, ok := account.Extra[codexTicketRuntimeExtraKey(model)]
	if !ok || raw == nil {
		return nil
	}
	b, err := json.Marshal(raw)
	if err != nil {
		return nil
	}
	var state CodexTicketLifecycle
	if json.Unmarshal(b, &state) != nil || strings.TrimSpace(state.Phase) == "" {
		return nil
	}
	state.Model = normalizeOpenAICodexTicketModel(model)
	if state.AccountID == 0 {
		state.AccountID = account.ID
	}
	return &state
}

// persistCodexTicketLifecycle 把状态写入账号 extra（同票据 key 一起，服务端独占）。
func (s *OpenAIGatewayService) persistCodexTicketLifecycle(ctx context.Context, account *Account, state *CodexTicketLifecycle) {
	if s == nil || account == nil || state == nil || account.ID <= 0 {
		return
	}
	state.AccountID = account.ID
	state.Model = normalizeOpenAICodexTicketModel(state.Model)
	if account.Extra == nil {
		account.Extra = make(map[string]any)
	}
	account.Extra[codexTicketRuntimeExtraKey(state.Model)] = state
	if s.accountRepo == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := s.accountRepo.UpdateExtra(ctx, account.ID, map[string]any{
		codexTicketRuntimeExtraKey(state.Model): state,
	}); err != nil {
		logger.L().Warn("openai_codex_ticket lifecycle persist failed",
			zap.Int64("account_id", account.ID), zap.String("model", state.Model), zap.Error(err))
	}
}

// begin 消耗一个自动阶段；必须在任何网络 IO 之前调用。崩溃中断的尝试不会自动重发。
func (state *CodexTicketLifecycle) begin(now time.Time, leaseID string) error {
	if state == nil {
		return ErrCodexTicketStale
	}
	if state.Pending {
		return ErrCodexTicketAlreadyTried
	}
	if state.NextAt == nil || now.Before(*state.NextAt) || state.ExpiresAt == nil {
		return ErrCodexTicketNotDue
	}
	switch state.Phase {
	case "ready":
		if !now.Before(*state.ExpiresAt) {
			due := state.ExpiresAt.Add(codexTicketRenewLead)
			state.Phase = "retry"
			state.NextAt = &due
			if now.Before(due) {
				return ErrCodexTicketNotDue
			}
			state.Phase = "post_running"
		} else {
			state.Phase = "pre_running"
		}
	case "retry":
		state.Phase = "post_running"
	default:
		return ErrCodexTicketNotDue
	}
	state.LeaseID = leaseID
	leaseUntil := now.Add(2 * time.Minute)
	state.LeaseUntil = &leaseUntil
	state.Pending = true
	state.Attempts++
	return nil
}

// beginManual 消耗一次人工阶段；manual 打票不受「未到期」限制，但仍拒绝并发。
func (state *CodexTicketLifecycle) beginManual(now time.Time, leaseID string) error {
	if state == nil {
		return ErrCodexTicketStale
	}
	if state.Pending {
		return ErrCodexTicketBusy
	}
	state.LeaseID = leaseID
	leaseUntil := now.Add(2 * time.Minute)
	state.LeaseUntil = &leaseUntil
	state.Pending = true
	state.Attempts++
	state.Phase = "manual_running"
	return nil
}

// complete 结算一次尝试。失败时保留可用旧票，绝不删除既有票据。
func (state *CodexTicketLifecycle) complete(now time.Time, ticket *openAICodexTicket, code, message string, success bool) {
	if state == nil {
		return
	}
	prior := state.Phase
	at := now
	state.LastAttemptAt = &at
	state.LastCode, state.LastMessage = code, message
	state.LeaseID, state.LeaseUntil, state.Pending = "", nil, false
	// 有限续期：连续未确认到上限即停止，避免无限重试。
	if !success && state.Attempts >= codexTicketMaxPendingAttempts {
		state.Phase = "stopped"
		state.NextAt, state.AutoRenew = nil, false
		return
	}
	if success && ticket != nil {
		expiry := ticket.ExpiresAt
		due := expiry.Add(-codexTicketRenewLead)
		state.ExpiresAt = &expiry
		state.NextAt = &due
		state.Phase = "ready"
		return
	}
	if (prior == "pre_running" || prior == "manual_running") && state.ExpiresAt != nil {
		if prior == "manual_running" && now.Before(*state.ExpiresAt) {
			due := state.ExpiresAt.Add(-codexTicketRenewLead)
			if !now.Before(due) {
				due = state.ExpiresAt.Add(codexTicketRenewLead)
				state.Phase = "retry"
			} else {
				state.Phase = "ready"
			}
			state.NextAt = &due
			return
		}
		due := state.ExpiresAt.Add(codexTicketRenewLead)
		state.NextAt = &due
		state.Phase = "retry"
		return
	}
	state.Phase = "stopped"
	state.NextAt = nil
}

// dueForRenewal 报告是否到自动续期时间。
func (state *CodexTicketLifecycle) dueForRenewal(now time.Time) bool {
	if state == nil || state.NextAt == nil {
		return false
	}
	return !now.Before(*state.NextAt)
}
