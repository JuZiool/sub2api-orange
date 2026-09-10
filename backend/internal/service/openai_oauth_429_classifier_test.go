//go:build unit

package service

import (
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestClassifyOpenAIOAuth429IncrementTransientHeadersDoNotCreateCooldown(t *testing.T) {
	now := time.Date(2026, time.September, 2, 12, 0, 0, 0, time.UTC)
	headers := http.Header{}
	headers.Set("x-codex-primary-used-percent", "31")
	headers.Set("x-codex-primary-reset-after-seconds", "3600")
	headers.Set("x-codex-primary-window-minutes", "300")
	headers.Set("x-codex-secondary-used-percent", "76")
	headers.Set("x-codex-secondary-reset-after-seconds", "518400")
	headers.Set("x-codex-secondary-window-minutes", "10080")

	classification := classifyOpenAIOAuth429At(headers, []byte(`{"error":{"type":"rate_limit_error","code":"rate_limit_exceeded"}}`), now)
	require.Equal(t, openAIOAuth429Transient, classification.Disposition)
	require.Nil(t, classification.ResetAt)
	require.Equal(t, "transient_429", classification.Source)
}

func TestClassifyOpenAIOAuth429IncrementExhaustedWindowsUseLaterReset(t *testing.T) {
	now := time.Date(2026, time.September, 2, 12, 0, 0, 0, time.UTC)
	headers := http.Header{}
	headers.Set("x-codex-primary-used-percent", "100")
	headers.Set("x-codex-primary-reset-after-seconds", "120")
	headers.Set("x-codex-primary-window-minutes", "300")
	headers.Set("x-codex-secondary-used-percent", "100")
	headers.Set("x-codex-secondary-reset-after-seconds", "7200")
	headers.Set("x-codex-secondary-window-minutes", "10080")

	classification := classifyOpenAIOAuth429At(headers, []byte(`{"error":{"type":"rate_limit_error"}}`), now)
	require.Equal(t, openAIOAuth429QuotaReset, classification.Disposition)
	require.Equal(t, "multiple", classification.Window)
	require.NotNil(t, classification.ResetAt)
	require.Equal(t, now.Add(2*time.Hour), *classification.ResetAt)
}

func TestClassifyOpenAIOAuth429IncrementStructuredCodesAcrossTransports(t *testing.T) {
	now := time.Date(2026, time.September, 2, 12, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		name       string
		body       string
		wantWindow string
		wantReset  time.Duration
	}{
		{name: "http json", body: `{"error":{"type":"usage_limit_reached"}}`, wantWindow: "5h", wantReset: 5 * time.Hour},
		{name: "sse response failed", body: "data: {\"type\":\"response.failed\",\"response\":{\"error\":{\"code\":\"weekly_limit_reached\"}}}\n\n", wantWindow: "7d", wantReset: 7 * 24 * time.Hour},
		{name: "websocket error", body: `{"type":"error","error":{"code":"quota_exhausted","resets_in_seconds":90}}`, wantWindow: "5h", wantReset: 90 * time.Second},
	} {
		t.Run(test.name, func(t *testing.T) {
			classification := classifyOpenAIOAuth429At(nil, []byte(test.body), now)
			require.NotEqual(t, openAIOAuth429Transient, classification.Disposition)
			require.Equal(t, test.wantWindow, classification.Window)
			require.NotNil(t, classification.ResetAt)
			require.Equal(t, now.Add(test.wantReset), *classification.ResetAt)
		})
	}
}

func TestClassifyOpenAIOAuth429IncrementMessageCannotUpgradeTransientCode(t *testing.T) {
	now := time.Date(2026, time.September, 2, 12, 0, 0, 0, time.UTC)
	classification := classifyOpenAIOAuth429At(nil, []byte(`{"error":{"type":"rate_limit_error","message":"weekly limit reached; quota exhausted"}}`), now)
	require.Equal(t, openAIOAuth429Transient, classification.Disposition)
	require.Nil(t, classification.ResetAt)
}
