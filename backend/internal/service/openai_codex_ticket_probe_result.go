package service

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const openAICodexTicketProbeErrorBodyLimit = 8 * 1024

// openAICodexTicketProbeError contains only diagnostics safe to persist or show
// in an account DTO. Never retain the response body or underlying transport error:
// either may contain a ticket, account credential or proxy password.
type openAICodexTicketProbeError struct {
	Code         string
	Message      string
	HTTPStatus   int
	RetryAfter   time.Duration
	ProxyFailure bool
}

func (e *openAICodexTicketProbeError) Error() string {
	if e == nil {
		return ""
	}
	return e.Message
}

func newOpenAICodexTicketProbeError(code string, status int) *openAICodexTicketProbeError {
	e := &openAICodexTicketProbeError{Code: code, HTTPStatus: status}
	switch code {
	case "auth":
		e.Message = "Account authentication expired or was rejected"
	case "forbidden":
		e.Message = "Upstream denied this request"
	case "model_unsupported":
		e.Message = "This account does not have access to the requested model"
	case "quota":
		e.Message = "Upstream account quota or balance is exhausted"
	case "rate_limited":
		e.Message = "Upstream rate limit reached"
	case "proxy_auth":
		e.Message = "Harvest proxy authentication failed"
		e.ProxyFailure = true
	case "network":
		e.Message = "Harvest connection failed"
		e.ProxyFailure = true
	case "timeout":
		e.Message = "Harvest request timed out"
		e.ProxyFailure = true
	case "upstream_5xx":
		e.Message = "Upstream service temporarily failed"
	case "canceled":
		e.Message = "Harvest request was canceled"
	default:
		e.Code = "invalid_response"
		e.Message = "Upstream returned an unexpected response"
	}
	return e
}

// classifyOpenAICodexTicketProbeError is also used by the scheduler for failures
// before response headers arrive. It deliberately drops raw error messages.
func classifyOpenAICodexTicketProbeError(err error, status int) *openAICodexTicketProbeError {
	var probeErr *openAICodexTicketProbeError
	if errors.As(err, &probeErr) {
		return probeErr
	}
	if status != 0 {
		return classifyOpenAICodexTicketProbeResponse(status, nil, nil, time.Now())
	}
	if err == nil {
		return newOpenAICodexTicketProbeError("invalid_response", 0)
	}
	if errors.Is(err, context.Canceled) {
		return newOpenAICodexTicketProbeError("canceled", 0)
	}
	var netErr net.Error
	if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &netErr) && netErr.Timeout()) {
		return newOpenAICodexTicketProbeError("timeout", 0)
	}
	// CONNECT and SOCKS handshake errors commonly have no structured status.
	// Matching is only used for classification; the original string is discarded.
	message := strings.ToLower(err.Error())
	if strings.Contains(message, "proxy authentication required") ||
		strings.Contains(message, "username/password authentication failed") ||
		strings.Contains(message, "socks authentication failed") ||
		strings.Contains(message, "no acceptable authentication methods") {
		return newOpenAICodexTicketProbeError("proxy_auth", http.StatusProxyAuthRequired)
	}
	return newOpenAICodexTicketProbeError("network", 0)
}

func openAICodexTicketProbeResponseError(resp *http.Response) *openAICodexTicketProbeError {
	if resp == nil {
		return newOpenAICodexTicketProbeError("invalid_response", 0)
	}
	var body []byte
	if resp.Body != nil {
		// Read no more than the limit, including for HTML errors and endless streams.
		// The request context provides the time bound; this provides the byte bound.
		body, _ = io.ReadAll(io.LimitReader(resp.Body, openAICodexTicketProbeErrorBodyLimit))
	}
	return classifyOpenAICodexTicketProbeResponse(resp.StatusCode, resp.Header, body, time.Now())
}

func classifyOpenAICodexTicketProbeResponse(status int, header http.Header, body []byte, now time.Time) *openAICodexTicketProbeError {
	code := "invalid_response"
	switch {
	case status == http.StatusUnauthorized:
		code = "auth"
	case status == http.StatusProxyAuthRequired:
		code = "proxy_auth"
	case status >= 500 && status <= 599:
		code = "upstream_5xx"
	case status >= 400 && status <= 499:
		upstreamCode, upstreamType, message := openAICodexTicketProbeErrorFields(body)
		switch {
		case openAICodexTicketProbeQuotaCode(upstreamCode) || openAICodexTicketProbeQuotaCode(upstreamType):
			code = "quota"
		case openAICodexTicketProbeModelUnavailable(upstreamCode, message):
			code = "model_unsupported"
		case status == http.StatusTooManyRequests:
			code = "rate_limited"
		case status == http.StatusPaymentRequired:
			code = "quota"
		case status == http.StatusForbidden:
			code = "forbidden"
		}
	}
	e := newOpenAICodexTicketProbeError(code, status)
	e.RetryAfter = parseOpenAICodexTicketRetryAfter(header.Get("Retry-After"), now)
	return e
}

func openAICodexTicketProbeErrorFields(body []byte) (code, errorType, message string) {
	// Require complete JSON. A truncated payload must not turn a coincidental
	// string or HTML page into a permanent model-access diagnosis.
	var envelope struct {
		Error struct {
			Code    string `json:"code"`
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &envelope) != nil {
		return "", "", ""
	}
	return strings.ToLower(strings.TrimSpace(envelope.Error.Code)),
		strings.ToLower(strings.TrimSpace(envelope.Error.Type)),
		strings.ToLower(strings.TrimSpace(envelope.Error.Message))
}

func openAICodexTicketProbeQuotaCode(code string) bool {
	switch code {
	case "insufficient_quota", "quota_exceeded", "usage_limit_reached", "billing_hard_limit_reached", "billing_not_active", "insufficient_balance", "credit_balance_too_low":
		return true
	default:
		return false
	}
}

func openAICodexTicketProbeModelUnavailable(code, message string) bool {
	switch code {
	case "model_not_found", "unsupported_model", "model_not_supported", "model_access_denied", "model_not_available_for_account":
		return true
	}
	// Do not infer missing model access from generic "unsupported" or "model"
	// text: the error may instead concern a request parameter or temporary outage.
	for _, phrase := range []string{
		"you do not have access to this model",
		"you do not have access to the model",
		"you do not have access to model ",
		"this model is not available for your account",
		"this model is not supported for your account",
		"this model is not supported with your current plan",
	} {
		if strings.Contains(message, phrase) {
			return true
		}
	}
	return strings.Contains(message, "model") && strings.Contains(message, "does not exist or you do not have access")
}

func parseOpenAICodexTicketRetryAfter(raw string, now time.Time) time.Duration {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0
	}
	if seconds, err := strconv.ParseInt(raw, 10, 64); err == nil {
		// Ignore negatives and overflow instead of wrapping them into immediate retries.
		if seconds <= 0 || seconds > int64((time.Duration(1<<63-1))/time.Second) {
			return 0
		}
		return time.Duration(seconds) * time.Second
	}
	if until, err := http.ParseTime(raw); err == nil && until.After(now) {
		return until.Sub(now)
	}
	return 0
}
