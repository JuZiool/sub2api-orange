package service

import (
	"fmt"
	"maps"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

func hasOpenAICodexTicketAccountPolicyKeys(extra map[string]any) bool {
	if extra == nil {
		return false
	}
	_, enabled := extra[OpenAICodexTicketEnabledExtraKey]
	_, failClosed := extra[OpenAICodexTicketFailClosedExtraKey]
	return enabled || failClosed
}

// normalizeOpenAICodexTicketAccountExtra validates the account ticket switches
// and retires the former account-scoped harvest proxy IDs. Ticket
// state itself is deliberately left untouched; MergeOpenAICodexTicketExtra
// remains responsible for protecting server-owned ticket material.
//
// When initialize is true (new account creation), both policy fields are
// written explicitly. This prevents an account created after the migration
// from unexpectedly inheriting the legacy gateway switch.
func normalizeOpenAICodexTicketAccountExtra(platform string, extra map[string]any, initialize bool) (map[string]any, error) {
	normalized := maps.Clone(extra)
	delete(normalized, OpenAICodexTicketHarvestProxyIDsExtraKey)
	if platform != PlatformOpenAI {
		return normalized, nil
	}
	if normalized == nil && !initialize {
		return nil, nil
	}
	if normalized == nil {
		normalized = make(map[string]any, 2)
	}

	if raw, ok := normalized[OpenAICodexTicketEnabledExtraKey]; ok {
		if _, valid := raw.(bool); !valid {
			return nil, invalidOpenAICodexTicketExtra(OpenAICodexTicketEnabledExtraKey, "must be a boolean")
		}
	}
	if raw, ok := normalized[OpenAICodexTicketFailClosedExtraKey]; ok {
		if _, valid := raw.(bool); !valid {
			return nil, invalidOpenAICodexTicketExtra(OpenAICodexTicketFailClosedExtraKey, "must be a boolean")
		}
	}
	if initialize {
		if _, ok := normalized[OpenAICodexTicketEnabledExtraKey]; !ok {
			normalized[OpenAICodexTicketEnabledExtraKey] = false
		}
		if _, ok := normalized[OpenAICodexTicketFailClosedExtraKey]; !ok {
			normalized[OpenAICodexTicketFailClosedExtraKey] = true
		}
	}
	return normalized, nil
}

// normalizeOpenAICodexTicketAccountUpdateExtra preserves policy keys omitted
// by a full account edit. An old account with no policy keys remains old and
// therefore continues to use the legacy gateway fallback until explicitly
// configured.
func normalizeOpenAICodexTicketAccountUpdateExtra(account *Account, extra map[string]any) (map[string]any, error) {
	if account == nil {
		return extra, nil
	}
	normalized, err := normalizeOpenAICodexTicketAccountExtra(account.Platform, extra, false)
	if err != nil || account.Platform != PlatformOpenAI {
		return normalized, err
	}
	if normalized == nil {
		normalized = make(map[string]any)
	}
	for _, key := range []string{
		OpenAICodexTicketEnabledExtraKey,
		OpenAICodexTicketFailClosedExtraKey,
	} {
		if _, provided := extra[key]; provided {
			continue
		}
		if value, exists := account.Extra[key]; exists {
			normalized[key] = value
		}
	}
	return normalized, nil
}

func invalidOpenAICodexTicketExtra(key, detail string) error {
	return infraerrors.BadRequest("INVALID_OPENAI_CODEX_TICKET_CONFIG", fmt.Sprintf("%s %s", key, detail))
}
