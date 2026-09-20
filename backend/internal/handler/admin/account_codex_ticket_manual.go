package admin

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

// Orange 特有：Codex 人工打票与停止续期（T8）。

// codexTicketManualHarvester 由网关实现，提供人工打票与停止续期。
type codexTicketManualHarvester interface {
	HarvestOpenAICodexTicketNow(context.Context, int64, string) service.OpenAICodexTicketManualResult
	StopOpenAICodexTicketRenewal(context.Context, int64, []string) error
}

func (h *AccountHandler) codexTicketManual(c *gin.Context, id int64) (codexTicketManualHarvester, *service.Account, bool) {
	if h == nil || h.adminService == nil || h.codexTicketGateway == nil {
		response.Error(c, 503, "Codex ticket gateway unavailable")
		return nil, nil, false
	}
	harvester, ok := h.codexTicketGateway.(codexTicketManualHarvester)
	if !ok {
		response.Error(c, 503, "Codex ticket gateway unavailable")
		return nil, nil, false
	}
	account, err := h.adminService.GetAccount(c.Request.Context(), id)
	if err != nil {
		response.ErrorFrom(c, err)
		return nil, nil, false
	}
	if account == nil || account.ID != id {
		response.NotFound(c, "Account not found")
		return nil, nil, false
	}
	return harvester, account, true
}

func (h *AccountHandler) codexTicketConfiguredModel(c *gin.Context, account *service.Account, model string) (string, bool) {
	model = strings.TrimSpace(model)
	if model == "" {
		response.BadRequest(c, "Model is required")
		return "", false
	}
	cfg := h.codexTicketConfig(c.Request.Context())
	for _, status := range service.OpenAICodexTicketStatuses(account, cfg, time.Now()) {
		if status.Model == model {
			return model, true
		}
	}
	response.BadRequest(c, "Model is not configured for Codex tickets")
	return "", false
}

func (h *AccountHandler) codexTicketConfig(ctx context.Context) (cfg config.OpenAICodexTicketConfig) {
	if h != nil && h.cfg != nil {
		cfg = h.cfg.Gateway.OpenAICodexTicket
	}
	if h != nil && h.codexTicketSettings != nil {
		cfg.Enabled = h.codexTicketSettings.GetOpenAICodexTicketEnabled(ctx, cfg.Enabled)
	}
	return cfg
}

// HarvestCodexTicket triggers one immediate ticket harvest for the account/model.
// POST /api/v1/admin/accounts/:id/codex-ticket/harvest
func (h *AccountHandler) HarvestCodexTicket(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		response.BadRequest(c, "Invalid account ID")
		return
	}
	harvester, account, ok := h.codexTicketManual(c, id)
	if !ok {
		return
	}
	model := strings.TrimSpace(c.Query("model"))
	if model == "" {
		model = strings.TrimSpace(c.PostForm("model"))
	}
	model, ok = h.codexTicketConfiguredModel(c, account, model)
	if !ok {
		return
	}
	result := harvester.HarvestOpenAICodexTicketNow(c.Request.Context(), id, model)
	response.Success(c, result)
}

// StopCodexTicketRenewal stops automatic renewal for the account (optionally one model).
// POST /api/v1/admin/accounts/:id/codex-ticket/stop
func (h *AccountHandler) StopCodexTicketRenewal(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		response.BadRequest(c, "Invalid account ID")
		return
	}
	harvester, account, ok := h.codexTicketManual(c, id)
	if !ok {
		return
	}
	var body struct {
		Model  string   `json:"model"`
		Models []string `json:"models"`
	}
	_ = c.ShouldBindJSON(&body)
	models := body.Models
	if model := strings.TrimSpace(body.Model); model != "" {
		models = append(models, model)
	}
	if len(models) > 0 {
		validated := make([]string, 0, len(models))
		for _, raw := range models {
			model, ok := h.codexTicketConfiguredModel(c, account, raw)
			if !ok {
				return
			}
			validated = append(validated, model)
		}
		models = validated
	}
	if err := harvester.StopOpenAICodexTicketRenewal(c.Request.Context(), id, models); err != nil {
		response.Error(c, 500, "Failed to stop Codex ticket renewal")
		return
	}
	response.Success(c, gin.H{"status": "stopped"})
}
