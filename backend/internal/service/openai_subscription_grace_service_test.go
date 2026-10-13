package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/imroc/req/v3"
	"github.com/stretchr/testify/require"
)

func TestParseChatGPTSubscriptionTimestamp(t *testing.T) {
	for _, tc := range []struct {
		raw, want string
		invalid   bool
	}{
		{`"2026-10-18T08:30:00+08:00"`, "2026-10-18T00:30:00Z", false},
		{`1792283400`, "2026-10-18T00:30:00Z", false},
		{`1792283400000`, "2026-10-18T00:30:00Z", false},
		{`"1792283400"`, "2026-10-18T00:30:00Z", false},
		{`1792283400.5`, "2026-10-18T00:30:00Z", false},
		{`null`, "", false}, {``, "", false}, {`""`, "", false}, {`0`, "", false},
		{`"not-a-date"`, "", true}, {`true`, "", true}, {`{}`, "", true},
		{`-1`, "", true}, {`"NaN"`, "", true}, {`"Inf"`, "", true}, {`1e30`, "", true},
	} {
		t.Run(tc.raw, func(t *testing.T) {
			got, err := parseChatGPTSubscriptionTimestamp(json.RawMessage(tc.raw))
			if tc.invalid {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
				require.Equal(t, tc.want, got)
			}
		})
	}
}

type subscriptionGraceProxyRepo struct {
	ProxyRepository
	proxy *Proxy
	err   error
}

func (r subscriptionGraceProxyRepo) GetByID(_ context.Context, id int64) (*Proxy, error) {
	if r.proxy != nil && id != r.proxy.ID {
		return nil, errors.New("unexpected proxy ID")
	}
	return r.proxy, r.err
}

func TestGetAccountSubscriptionGrace_ReadsPersonalAccountThroughProxy(t *testing.T) {
	past := time.Now().Add(-24 * time.Hour).UTC().Format(time.RFC3339)
	end := time.Now().Add(48 * time.Hour).UTC().Truncate(time.Second)
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		require.Equal(t, http.MethodGet, r.Method)
		require.Equal(t, "/backend-api/subscriptions", r.URL.Path)
		require.Equal(t, "personal-account", r.URL.Query().Get("account_id"))
		require.Equal(t, "Bearer secret-token", r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"plan_type": "plus", "active_until": past,
			"became_delinquent_timestamp": past, "grace_period_end_timestamp": end.UnixMilli(),
		})
	}))
	defer server.Close()
	oldURL := chatGPTSubscriptionsURL
	chatGPTSubscriptionsURL = server.URL + "/backend-api/subscriptions"
	t.Cleanup(func() { chatGPTSubscriptionsURL = oldURL })
	proxy := &Proxy{ID: 7, Protocol: "http", Host: "127.0.0.1", Port: 8089, Status: StatusActive}
	svc := &OpenAIOAuthService{
		proxyRepo: subscriptionGraceProxyRepo{proxy: proxy},
		privacyClientFactory: func(proxyURL string) (*req.Client, error) {
			require.Equal(t, proxy.URL(), proxyURL)
			return req.C().SetTimeout(time.Second), nil
		},
	}
	account := &Account{ID: 42, Platform: PlatformOpenAI, Type: AccountTypeOAuth, ProxyID: &proxy.ID, Credentials: map[string]any{
		"access_token": "secret-token", "chatgpt_account_id": "personal-account", "organization_id": "other-workspace",
		"plan_type": "plus", "subscription_expires_at": past,
	}}
	before, err := json.Marshal(account.Credentials)
	require.NoError(t, err)
	info, err := svc.GetAccountSubscriptionGrace(context.Background(), account)
	require.NoError(t, err)
	require.Equal(t, past, info.ActiveUntil)
	require.Equal(t, past, info.BecameDelinquentAt)
	require.Equal(t, end.Format(time.RFC3339), info.GracePeriodEndsAt)
	require.Equal(t, 1, calls)
	after, err := json.Marshal(account.Credentials)
	require.NoError(t, err)
	require.Equal(t, string(before), string(after), "查询不应修改账号凭据")

	account.Credentials["subscription_expires_at"] = end.Format(time.RFC3339)
	info, err = svc.GetAccountSubscriptionGrace(context.Background(), account)
	require.NoError(t, err)
	require.Empty(t, info.GracePeriodEndsAt)
	require.Equal(t, 1, calls, "未到期账号不得查询上游")

	account.Credentials["subscription_expires_at"] = past
	svc.proxyRepo = subscriptionGraceProxyRepo{err: errors.New("proxy unavailable")}
	_, err = svc.GetAccountSubscriptionGrace(context.Background(), account)
	require.Error(t, err)
	require.Equal(t, 1, calls, "代理不可用时不得静默直连")

	delete(account.Credentials, "chatgpt_account_id")
	_, err = svc.GetAccountSubscriptionGrace(context.Background(), account)
	require.Error(t, err, "不能改用另一工作区 ID 查询")
}

func TestSubscriptionGrace_EmptyAndFailedResponses(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
		fail       bool
	}{
		{"null fields", `{"active_until":"2026-01-01T00:00:00Z","grace_period_end_timestamp":null}`, 200, false},
		{"missing fields", `{}`, 200, false},
		{"invalid timestamp", `{"active_until":"2026-01-01T00:00:00Z","grace_period_end_timestamp":"invalid"}`, 200, true},
		{"invalid json", `{`, 200, true},
		{"html success", `<html>challenge</html>`, 200, true},
		{"null response", `null`, 200, true},
		{"unauthorized", `{"error":"token expired"}`, 401, true},
		{"cloudflare", `<html>challenge</html>`, 403, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			oldURL := chatGPTSubscriptionsURL
			chatGPTSubscriptionsURL = server.URL
			t.Cleanup(func() { chatGPTSubscriptionsURL = oldURL })
			svc := &OpenAIOAuthService{privacyClientFactory: func(string) (*req.Client, error) { return req.C(), nil }}
			account := &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{
				"access_token": "secret-token", "chatgpt_account_id": "personal", "plan_type": "plus",
				"subscription_expires_at": time.Now().Add(-time.Hour).Format(time.RFC3339),
			}}
			info, err := svc.GetAccountSubscriptionGrace(context.Background(), account)
			if tc.fail {
				require.Error(t, err)
				require.NotContains(t, err.Error(), "secret-token")
			} else {
				require.NoError(t, err)
				require.Empty(t, info.GracePeriodEndsAt)
			}
			if tc.name == "invalid timestamp" {
				require.Equal(t, "2026-01-01T00:00:00Z", fetchChatGPTSubscriptionExpiresAt(context.Background(), svc.privacyClientFactory, "secret-token", "", "personal"), "非法挽留字段不能破坏原有到期补全")
			}
		})
	}
}

func TestSubscriptionGrace_RejectsIneligibleAccounts(t *testing.T) {
	svc := &OpenAIOAuthService{}
	for _, account := range []*Account{
		nil,
		{Platform: PlatformAnthropic, Type: AccountTypeOAuth},
		{Platform: PlatformOpenAI, Type: AccountTypeAPIKey},
		{Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{"plan_type": "free"}},
		{Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{"plan_type": "plus", "subscription_expires_at": "bad"}},
	} {
		_, err := svc.GetAccountSubscriptionGrace(context.Background(), account)
		require.Error(t, err)
	}
}
