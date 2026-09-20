package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestCodexTicketProbeResponseClassification(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		code   string
		proxy  bool
	}{
		{"expired account", 401, `{"error":{"code":"invalid_api_key","message":"secret-access-token"}}`, "auth", false},
		{"forbidden", 403, `<html>blocked secret-proxy-password</html>`, "forbidden", false},
		{"model access code", 403, `{"error":{"code":"model_access_denied"}}`, "model_unsupported", false},
		{"model not found", 404, `{"error":{"code":"model_not_found"}}`, "model_unsupported", false},
		{"unsupported model code", 400, `{"error":{"code":"unsupported_model"}}`, "model_unsupported", false},
		{"model access text", 400, `{"error":{"message":"You do not have access to this model: secret-model-name"}}`, "model_unsupported", false},
		{"model absent text", 404, `{"error":{"message":"The model 'private-model' does not exist or you do not have access to it."}}`, "model_unsupported", false},
		{"unsupported parameter is not model access", 400, `{"error":{"message":"Model does not support the max_output_tokens parameter"}}`, "invalid_response", false},
		{"generic unsupported is not model access", 400, `{"error":{"message":"Unsupported value: model field"}}`, "invalid_response", false},
		{"temporary model failure", 503, `{"error":{"code":"model_not_found"}}`, "upstream_5xx", false},
		{"quota takes priority over rate limit", 429, `{"error":{"code":"insufficient_quota"}}`, "quota", false},
		{"quota type", 429, `{"error":{"type":"usage_limit_reached"}}`, "quota", false},
		{"payment required", 402, ``, "quota", false},
		{"rate limited", 429, `{"error":{"code":"rate_limit_exceeded"}}`, "rate_limited", false},
		{"proxy auth", 407, `secret-proxy-password`, "proxy_auth", true},
		{"upstream unavailable", 502, `<html>secret-access-token</html>`, "upstream_5xx", false},
		{"truncated json", 400, `{"error":{"code":"model_not_found"`, "invalid_response", false},
		{"unknown redirect", 302, ``, "invalid_response", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result := classifyOpenAICodexTicketProbeResponse(tc.status, http.Header{"Retry-After": {"15"}}, []byte(tc.body), time.Now())
			require.Equal(t, tc.code, result.Code)
			require.Equal(t, tc.status, result.HTTPStatus)
			require.Equal(t, tc.proxy, result.ProxyFailure)
			require.Equal(t, 15*time.Second, result.RetryAfter)
			require.NotEmpty(t, result.Error())
			require.NotContains(t, fmt.Sprintf("%+v", result), "secret")
			require.NotContains(t, result.Error(), "private-model")
		})
	}
}

func TestCodexTicketProbeTransportClassification(t *testing.T) {
	for _, tc := range []struct {
		name  string
		err   error
		code  string
		proxy bool
	}{
		{"canceled", fmt.Errorf("secret: %w", context.Canceled), "canceled", false},
		{"deadline", fmt.Errorf("secret: %w", context.DeadlineExceeded), "timeout", true},
		{"network timeout", &net.DNSError{Err: "secret", IsTimeout: true}, "timeout", true},
		{"proxy HTTP auth", errors.New("proxyconnect http://user:secret@proxy: Proxy Authentication Required"), "proxy_auth", true},
		{"proxy SOCKS auth", errors.New("socks connect: username/password authentication failed: secret"), "proxy_auth", true},
		{"connection refused", errors.New("proxyconnect tcp: user:secret@proxy connection refused"), "network", true},
		{"EOF", io.EOF, "network", true},
		{"nil response", nil, "invalid_response", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result := classifyOpenAICodexTicketProbeError(tc.err, 0)
			require.Equal(t, tc.code, result.Code)
			require.Equal(t, tc.proxy, result.ProxyFailure)
			require.NotContains(t, result.Error(), "secret")
			require.NotContains(t, fmt.Sprintf("%+v", result), "user:")
		})
	}
	typed := newOpenAICodexTicketProbeError("rate_limited", 429)
	typed.RetryAfter = 3 * time.Minute
	require.Same(t, typed, classifyOpenAICodexTicketProbeError(fmt.Errorf("wrapped: %w", typed), 429))
	require.Equal(t, "auth", classifyOpenAICodexTicketProbeError(nil, 401).Code)
}

func TestCodexTicketProbeRetryAfter(t *testing.T) {
	now := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		value string
		want  time.Duration
	}{
		{"60", time.Minute},
		{" 12 ", 12 * time.Second},
		{now.Add(45 * time.Second).Format(http.TimeFormat), 45 * time.Second},
		{now.Add(-time.Minute).Format(http.TimeFormat), 0},
		{"0", 0},
		{"-1", 0},
		{"1.5", 0},
		{"garbage", 0},
		{"9223372036854775807", 0},
		{"", 0},
	} {
		t.Run(tc.value, func(t *testing.T) {
			require.Equal(t, tc.want, parseOpenAICodexTicketRetryAfter(tc.value, now))
		})
	}
}

type codexTicketProbeTrackingBody struct {
	reader io.Reader
	read   int
	closed bool
}

func (b *codexTicketProbeTrackingBody) Read(p []byte) (int, error) {
	if b.reader == nil {
		panic("successful probe must not read response body")
	}
	n, err := b.reader.Read(p)
	b.read += n
	return n, err
}

func (b *codexTicketProbeTrackingBody) Close() error {
	b.closed = true
	return nil
}

func TestCodexTicketProbeBoundsErrorBodyAndDiscardsSensitiveHeaders(t *testing.T) {
	body := &codexTicketProbeTrackingBody{reader: strings.NewReader(strings.Repeat("secret", 4*1024))}
	header := http.Header{"Retry-After": {"30"}}
	header.Set(openAICodexTurnStateHeader, "secret-ticket")
	upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: 429, Header: header, Body: body}}
	svc := ticketTestService(t, config.OpenAICodexTicketConfig{}, upstream)
	state, status, err := svc.fireOpenAICodexTicketProbe(context.Background(), ticketTestAccount(42), "secret-token", "gpt-6-astra", "http://user:secret@proxy", time.Second)
	require.Empty(t, state)
	require.Equal(t, 429, status)
	var probeErr *openAICodexTicketProbeError
	require.ErrorAs(t, err, &probeErr)
	require.Equal(t, "rate_limited", probeErr.Code)
	require.Equal(t, 30*time.Second, probeErr.RetryAfter)
	require.Equal(t, openAICodexTicketProbeErrorBodyLimit, body.read)
	require.True(t, body.closed)
	require.NotContains(t, fmt.Sprintf("%+v", probeErr), "secret")
}

func TestCodexTicketProbeSuccessReadsOnlyHeaders(t *testing.T) {
	body := &codexTicketProbeTrackingBody{}
	header := http.Header{}
	state := fakeCodexTicketState(332)
	header.Set(openAICodexTurnStateHeader, state)
	upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: 200, Header: header, Body: body}}
	svc := ticketTestService(t, config.OpenAICodexTicketConfig{}, upstream)
	got, status, err := svc.fireOpenAICodexTicketProbe(context.Background(), ticketTestAccount(42), "token", "gpt-6-astra", "http://proxy", time.Second)
	require.NoError(t, err)
	require.Equal(t, 200, status)
	require.Equal(t, state, got)
	require.Zero(t, body.read)
	require.True(t, body.closed)
	require.True(t, upstream.lastReq.Close)
	require.Equal(t, HTTPUpstreamProfileOpenAIHarvest, HTTPUpstreamProfileFromContext(upstream.lastReq.Context()))
}

func TestCodexTicketProbeRoundTripErrorIsSafeAndClosesBody(t *testing.T) {
	body := &codexTicketProbeTrackingBody{}
	upstream := &codexTicketFuncUpstream{do: func(_ *http.Request) (*http.Response, error) {
		return &http.Response{Body: body}, errors.New("http://user:secret@proxy: connection refused")
	}}
	svc := ticketTestService(t, config.OpenAICodexTicketConfig{}, upstream)
	state, status, err := svc.fireOpenAICodexTicketProbe(context.Background(), ticketTestAccount(42), "token", "gpt-6-astra", "http://proxy", time.Second)
	require.Empty(t, state)
	require.Zero(t, status)
	var probeErr *openAICodexTicketProbeError
	require.ErrorAs(t, err, &probeErr)
	require.Equal(t, "network", probeErr.Code)
	require.NotContains(t, err.Error(), "secret")
	require.True(t, body.closed)
	require.Zero(t, body.read)
}
