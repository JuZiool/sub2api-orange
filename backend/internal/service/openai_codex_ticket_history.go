package service

import (
	"context"
	"sort"
	"strings"
	"time"
)

const OpenAICodexTicketHistoryLimit = 20

// OpenAICodexTicketEvent is an administrator-only, credential-free diagnostic.
// IDs increase for the lifetime of this process. No upstream text is retained.
type OpenAICodexTicketEvent struct {
	ID           uint64    `json:"id"`
	At           time.Time `json:"at"`
	Model        string    `json:"model"`
	Outcome      string    `json:"outcome"`
	ErrorCode    string    `json:"error_code"`
	HTTPStatus   int       `json:"http_status"`
	Length       int       `json:"length"`
	TargetLength int       `json:"target_length"`
	ProxyIndex   int       `json:"proxy_index"`
	DurationMs   int64     `json:"duration_ms"`
}

type codexTicketTelemetry struct {
	successes, failures, injectMisses int
	lastInjectMissAt                  *time.Time
	events                            []OpenAICodexTicketEvent
}

// Scheduler lock must be held. Telemetry never mutates job scheduling state.
func (r *codexTicketScheduler) measurements(accountID int64, model string) *codexTicketTelemetry {
	if r.telemetry == nil {
		r.telemetry = make(map[string]*codexTicketTelemetry)
	}
	key := openAICodexTicketKey(accountID, model)
	if r.telemetry[key] == nil {
		r.telemetry[key] = &codexTicketTelemetry{}
	}
	return r.telemetry[key]
}

func (s *OpenAIGatewayService) recordOpenAICodexTicketInjectMiss(accountID int64, model string) {
	if accountID <= 0 {
		return
	}
	r := &s.openaiCodexTicketScheduler
	r.mu.Lock()
	defer r.mu.Unlock()
	t := r.measurements(accountID, model)
	now := time.Now()
	t.injectMisses++
	t.lastInjectMissAt = &now
}

// Caller holds r.mu. One event per actual completed probe, including cancellation.
func (r *codexTicketScheduler) recordProbeEvent(accountID int64, model string, result *openAICodexTicketProbeError, status, length, target, proxyIndex int, started, finished time.Time) {
	t := r.measurements(accountID, model)
	r.nextEventID++
	event := OpenAICodexTicketEvent{
		ID: r.nextEventID, At: finished, Model: model, Outcome: "success",
		HTTPStatus: status, Length: length, TargetLength: target, ProxyIndex: proxyIndex,
		DurationMs: finished.Sub(started).Milliseconds(),
	}
	if result == nil {
		t.successes++
	} else {
		event.ErrorCode = safeCodexTicketEventErrorCode(result.Code)
		if result.Code == "canceled" {
			event.Outcome = "canceled"
		} else {
			event.Outcome = "failure"
			t.failures++
		}
	}
	if len(t.events) == OpenAICodexTicketHistoryLimit {
		copy(t.events, t.events[1:])
		t.events[len(t.events)-1] = event
	} else {
		t.events = append(t.events, event)
	}
}

func safeCodexTicketEventErrorCode(code string) string {
	switch code {
	case "auth", "forbidden", "model_unsupported", "quota", "rate_limited", "proxy_auth", "network", "timeout", "upstream_5xx", "invalid_response", "canceled", "length_mismatch", "invalid_ticket":
		return code
	default:
		return "invalid_response"
	}
}

func (s *OpenAIGatewayService) OpenAICodexTicketHistory(ctx context.Context, account *Account, model string) []OpenAICodexTicketEvent {
	events := make([]OpenAICodexTicketEvent, 0, OpenAICodexTicketHistoryLimit)
	if s == nil || account == nil || account.ID <= 0 || !s.openAICodexTicketAccountConfig(ctx, account).Enabled {
		return events
	}
	model = strings.TrimSpace(model)
	if model != "" && !s.openAICodexTicketGatedModel(model) {
		return events
	}
	cfg := s.openAICodexTicketConfig()
	r := &s.openaiCodexTicketScheduler
	r.mu.Lock()
	seen := make(map[string]bool)
	for _, configured := range cfg.Models {
		configured = strings.TrimSpace(configured)
		if configured == "" || seen[configured] || (model != "" && model != configured) {
			continue
		}
		seen[configured] = true
		if t := r.telemetry[openAICodexTicketKey(account.ID, configured)]; t != nil {
			events = append(events, t.events...)
		}
	}
	r.mu.Unlock()
	sort.Slice(events, func(i, j int) bool { return events[i].ID > events[j].ID })
	if len(events) > OpenAICodexTicketHistoryLimit {
		events = events[:OpenAICodexTicketHistoryLimit]
	}
	return events
}
