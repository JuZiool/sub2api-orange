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

// Optional extension keeps the existing lightweight diagnostics interface usable.
type codexTicketHistoryReader interface {
	OpenAICodexTicketHistory(context.Context, *service.Account, string) []service.OpenAICodexTicketEvent
}

// GetCodexTicketHistory is registered only under the administrator route group.
// GET /api/v1/admin/accounts/:id/codex-ticket-history?model=...
func (h *AccountHandler) GetCodexTicketHistory(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		response.BadRequest(c, "Invalid account ID")
		return
	}
	account, err := h.adminService.GetAccount(c.Request.Context(), id)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	if account == nil || account.ID != id {
		response.NotFound(c, "Account not found")
		return
	}
	cfg := config.OpenAICodexTicketConfig{}
	if h.cfg != nil {
		cfg = h.cfg.Gateway.OpenAICodexTicket
	}
	if h.codexTicketSettings != nil {
		cfg.Enabled = h.codexTicketSettings.GetOpenAICodexTicketEnabled(c.Request.Context(), cfg.Enabled)
	}
	events := make([]service.OpenAICodexTicketEvent, 0)
	if service.ResolveOpenAICodexTicketAccountConfig(account, cfg).Enabled {
		model := strings.TrimSpace(c.Query("model"))
		if model != "" {
			valid := false
			for _, status := range service.OpenAICodexTicketStatuses(account, cfg, time.Now()) {
				if status.Model == model {
					valid = true
					break
				}
			}
			if !valid {
				response.BadRequest(c, "Model is not configured for Codex tickets")
				return
			}
		}
		if reader, ok := h.codexTicketGateway.(codexTicketHistoryReader); ok {
			if history := reader.OpenAICodexTicketHistory(c.Request.Context(), account, model); history != nil {
				events = history
			}
		}
	}
	response.Success(c, gin.H{"events": events, "limit": service.OpenAICodexTicketHistoryLimit})
}
