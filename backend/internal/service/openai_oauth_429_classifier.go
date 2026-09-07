package service

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	openAIOAuth429FiveHourFallback = 5 * time.Hour
	openAIOAuth429WeeklyFallback   = 7 * 24 * time.Hour
)

type openAIOAuth429Classification struct {
	Disposition openAIOAuth429Disposition
	ResetAt     *time.Time
	Window      string
	Source      string
	Code        string
}

func classifyOpenAIOAuth429At(headers http.Header, responseBody []byte, now time.Time) openAIOAuth429Classification {
	now = now.UTC()
	if snapshot := ParseCodexRateLimitHeaders(headers); snapshot != nil {
		if normalized := snapshot.Normalize(); normalized != nil {
			fiveExhausted := normalized.Used5hPercent != nil && *normalized.Used5hPercent >= 100
			sevenExhausted := normalized.Used7dPercent != nil && *normalized.Used7dPercent >= 100
			if fiveExhausted || sevenExhausted {
				fiveReset := resetTimeFromSeconds(now, normalized.Reset5hSeconds, openAIOAuth429FiveHourFallback)
				sevenReset := resetTimeFromSeconds(now, normalized.Reset7dSeconds, openAIOAuth429WeeklyFallback)
				switch {
				case fiveExhausted && sevenExhausted:
					resetAt := laterTime(fiveReset, sevenReset)
					return openAIOAuth429Classification{Disposition: openAIOAuth429QuotaReset, ResetAt: &resetAt, Window: "multiple", Source: "usage_headers"}
				case sevenExhausted:
					return openAIOAuth429Classification{Disposition: openAIOAuth429Quota7d, ResetAt: &sevenReset, Window: "7d", Source: "usage_headers"}
				default:
					return openAIOAuth429Classification{Disposition: openAIOAuth429Quota5h, ResetAt: &fiveReset, Window: "5h", Source: "usage_headers"}
				}
			}
		}
	}

	code, resetAt := findOpenAIHardQuotaSignal(responseBody, now)
	if code == "" {
		return openAIOAuth429Classification{Disposition: openAIOAuth429Transient, Window: "transient", Source: "transient_429"}
	}

	weekly := code == "weekly_limit_reached" || code == "monthly_limit_reached"
	window := "5h"
	disposition := openAIOAuth429Quota5h
	fallback := openAIOAuth429FiveHourFallback
	if weekly {
		window = "7d"
		disposition = openAIOAuth429Quota7d
		fallback = openAIOAuth429WeeklyFallback
	}
	if resetAt == nil {
		if snapshot := ParseCodexRateLimitHeaders(headers); snapshot != nil {
			if normalized := snapshot.Normalize(); normalized != nil {
				seconds := normalized.Reset5hSeconds
				if weekly {
					seconds = normalized.Reset7dSeconds
				}
				parsed := resetTimeFromSeconds(now, seconds, fallback)
				resetAt = &parsed
			}
		}
	}
	if resetAt == nil || !resetAt.After(now) {
		parsed := now.Add(fallback)
		resetAt = &parsed
	}
	return openAIOAuth429Classification{Disposition: disposition, ResetAt: resetAt, Window: window, Source: "structured_code", Code: code}
}

func resetTimeFromSeconds(now time.Time, seconds *int, fallback time.Duration) time.Time {
	if seconds != nil && *seconds > 0 {
		return now.Add(time.Duration(*seconds) * time.Second)
	}
	return now.Add(fallback)
}

func findOpenAIHardQuotaSignal(body []byte, now time.Time) (string, *time.Time) {
	for _, payload := range openAI429StructuredPayloads(body) {
		var value any
		decoder := json.NewDecoder(bytes.NewReader(payload))
		decoder.UseNumber()
		if decoder.Decode(&value) != nil {
			continue
		}
		if code, resetAt := findOpenAIHardQuotaSignalValue(value, now, 0); code != "" {
			return code, resetAt
		}
	}
	return "", nil
}

func openAI429StructuredPayloads(body []byte) [][]byte {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 {
		return nil
	}
	payloads := make([][]byte, 0, 4)
	if json.Valid(trimmed) {
		payloads = append(payloads, trimmed)
	}
	for _, line := range bytes.Split(trimmed, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if !bytes.HasPrefix(line, []byte("data:")) {
			continue
		}
		line = bytes.TrimSpace(bytes.TrimPrefix(line, []byte("data:")))
		if len(line) > 0 && !bytes.Equal(line, []byte("[DONE]")) && json.Valid(line) {
			payloads = append(payloads, line)
		}
	}
	return payloads
}

func findOpenAIHardQuotaSignalValue(value any, now time.Time, depth int) (string, *time.Time) {
	if depth > 8 {
		return "", nil
	}
	switch typed := value.(type) {
	case map[string]any:
		for _, key := range []string{"type", "code", "reason", "error_code"} {
			if raw, ok := typed[key].(string); ok {
				if code := normalizeOpenAIHardQuotaCode(raw); code != "" {
					return code, openAIHardQuotaResetFromObject(typed, now)
				}
			}
		}
		for _, raw := range typed {
			if code, resetAt := findOpenAIHardQuotaSignalValue(raw, now, depth+1); code != "" {
				return code, resetAt
			}
		}
	case []any:
		for _, raw := range typed {
			if code, resetAt := findOpenAIHardQuotaSignalValue(raw, now, depth+1); code != "" {
				return code, resetAt
			}
		}
	}
	return "", nil
}

func normalizeOpenAIHardQuotaCode(raw string) string {
	code := strings.NewReplacer("-", "_", " ", "_").Replace(strings.ToLower(strings.TrimSpace(raw)))
	switch code {
	case "usage_limit_reached", "weekly_limit_reached", "monthly_limit_reached", "quota_exhausted", "insufficient_quota", "billing_hard_limit_reached":
		return code
	case "gousagelimiterror":
		return "go_usage_limit_error"
	default:
		return ""
	}
}

func openAIHardQuotaResetFromObject(value map[string]any, now time.Time) *time.Time {
	if raw, ok := openAI429Int64(value["resets_at"]); ok {
		parsed := time.Unix(raw, 0).UTC()
		return &parsed
	}
	if raw, ok := openAI429Int64(value["reset_at"]); ok {
		parsed := time.Unix(raw, 0).UTC()
		return &parsed
	}
	if raw, ok := openAI429Int64(value["resets_in_seconds"]); ok && raw > 0 {
		parsed := now.Add(time.Duration(raw) * time.Second)
		return &parsed
	}
	if raw, ok := openAI429Int64(value["reset_after_seconds"]); ok && raw > 0 {
		parsed := now.Add(time.Duration(raw) * time.Second)
		return &parsed
	}
	return nil
}

func openAI429Int64(value any) (int64, bool) {
	switch typed := value.(type) {
	case json.Number:
		parsed, err := typed.Int64()
		return parsed, err == nil
	case float64:
		return int64(typed), true
	case string:
		parsed, err := strconv.ParseInt(strings.TrimSpace(typed), 10, 64)
		return parsed, err == nil
	default:
		return 0, false
	}
}
