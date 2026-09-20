package admin

import (
	"maps"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

// Ticket credentials and retired per-account proxy assignments are never exported.
func exportCodexTicketExtra(account *service.Account) map[string]any {
	extra := service.RedactOpenAICodexTicketExtra(account.Extra)
	delete(extra, service.OpenAICodexTicketHarvestProxyIDsExtraKey)
	return extra
}

// Older backups may still contain local proxy IDs. Ignore them without requiring
// the old proxy records: harvesting now uses the gateway's dedicated proxy pool.
func stripLegacyCodexTicketProxyExtra(extra map[string]any) map[string]any {
	result := maps.Clone(extra)
	delete(result, service.OpenAICodexTicketHarvestProxyIDsExtraKey)
	return result
}
