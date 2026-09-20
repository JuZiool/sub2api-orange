package admin

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestAccountDataCodexTicketProxiesAreRetired(t *testing.T) {
	router, source := setupAccountDataRouter()
	businessID := int64(11)
	source.proxies = []service.Proxy{
		{ID: 11, Name: "business", Protocol: "http", Host: "business.example", Port: 8080, Status: service.StatusActive},
		{ID: 22, Name: "harvest A", Protocol: "http", Host: "harvest-a.example", Port: 8080, Status: service.StatusActive},
		{ID: 33, Name: "harvest B", Protocol: "socks5", Host: "harvest-b.example", Port: 1080, Status: service.StatusActive},
	}
	source.accounts = []service.Account{{
		ID: 1, Name: "account", Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth,
		Credentials: map[string]any{"access_token": "test-token"}, ProxyID: &businessID,
		Extra: map[string]any{
			service.OpenAICodexTicketEnabledExtraKey:         true,
			service.OpenAICodexTicketFailClosedExtraKey:      true,
			service.OpenAICodexTicketHarvestProxyIDsExtraKey: []int64{33, 22},
			"codex_turn_ticket:gpt-6-astra":                  map[string]any{"state": "private-state"},
		},
	}}
	for _, include := range []bool{true, false} {
		t.Run(map[bool]string{true: "include proxies", false: "exclude proxies"}[include], func(t *testing.T) {
			url := "/api/v1/admin/accounts/data"
			if !include {
				url += "?include_proxies=false"
			}
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, url, nil))
			require.Equal(t, http.StatusOK, rec.Code)
			var exported struct {
				Data DataPayload `json:"data"`
			}
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &exported))
			require.Len(t, exported.Data.Accounts, 1)
			account := exported.Data.Accounts[0]
			require.NotContains(t, rec.Body.String(), "codex_ticket_proxy_keys")
			require.NotContains(t, account.Extra, service.OpenAICodexTicketHarvestProxyIDsExtraKey)
			require.NotContains(t, rec.Body.String(), "private-state")
			if include {
				require.Len(t, exported.Data.Proxies, 1, "only business proxies are exported")
				require.Equal(t, "business.example", exported.Data.Proxies[0].Host)
			} else {
				require.Empty(t, exported.Data.Proxies)
			}
			targetRouter, target := setupAccountDataRouter()
			target.proxies = append([]service.Proxy(nil), source.proxies...)
			for i := range target.proxies {
				target.proxies[i].ID += 100
			}
			body, err := json.Marshal(DataImportRequest{Data: exported.Data})
			require.NoError(t, err)
			rec = httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/accounts/data", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			targetRouter.ServeHTTP(rec, req)
			require.Equal(t, http.StatusOK, rec.Code)
			require.Len(t, target.createdAccounts, 1, rec.Body.String())
			created := target.createdAccounts[0]
			require.Equal(t, true, created.Extra[service.OpenAICodexTicketEnabledExtraKey])
			require.NotContains(t, created.Extra, service.OpenAICodexTicketHarvestProxyIDsExtraKey)
			if include {
				require.Equal(t, int64(111), *created.ProxyID)
			} else {
				require.Nil(t, created.ProxyID)
			}
		})
	}
	require.Equal(t, []int64{33, 22}, source.accounts[0].Extra[service.OpenAICodexTicketHarvestProxyIDsExtraKey], "export must not mutate source routing")
}

func TestImportCodexTicketProxiesIgnoresRetiredConfiguration(t *testing.T) {
	router, target := setupAccountDataRouter()
	body := []byte(`{"data":{"accounts":[{"name":"legacy", "platform":"openai", "type":"oauth", "credentials":{"access_token":"test-token"}, "extra":{"codex_ticket_enabled":true,"codex_ticket_harvest_proxy_ids":[404]}, "codex_ticket_proxy_keys":["http|missing.example|8080|user|secret"]}],"proxies":[]}}`)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/accounts/data", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Len(t, target.createdAccounts, 1, rec.Body.String())
	require.Equal(t, true, target.createdAccounts[0].Extra[service.OpenAICodexTicketEnabledExtraKey])
	require.NotContains(t, target.createdAccounts[0].Extra, service.OpenAICodexTicketHarvestProxyIDsExtraKey)
	require.Empty(t, target.createdProxies)
}
