package admin

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

type ticketHistoryAdminService struct {
	*stubAdminService
	accountsByID map[int64]*service.Account
}

func (s *ticketHistoryAdminService) GetAccount(_ context.Context, id int64) (*service.Account, error) {
	if a := s.accountsByID[id]; a != nil {
		return a, nil
	}
	return nil, service.ErrAccountNotFound
}

type ticketHistoryGateway struct {
	accountCodexTicketDiagnosticsStub
	calls []string
}

func (s *ticketHistoryGateway) OpenAICodexTicketHistory(_ context.Context, account *service.Account, model string) []service.OpenAICodexTicketEvent {
	s.calls = append(s.calls, fmt.Sprintf("%d:%s", account.ID, model))
	return []service.OpenAICodexTicketEvent{{ID: uint64(account.ID), Model: model, Outcome: "success", Length: 292}}
}

func TestAccountCodexTicketHistoryIsolationAndValidation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	admin := &ticketHistoryAdminService{stubAdminService: newStubAdminService(), accountsByID: map[int64]*service.Account{}}
	for _, id := range []int64{41, 42} {
		// Orange: 打票为账号级显式 opt-in（缺 key 即 false），测试账号需显式开启。
		admin.accountsByID[id] = &service.Account{ID: id, Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth, Credentials: map[string]any{"access_token": "secret-token"}, Extra: map[string]any{service.OpenAICodexTicketEnabledExtraKey: true}}
	}
	cfg := &config.Config{Gateway: config.GatewayConfig{OpenAICodexTicket: config.OpenAICodexTicketConfig{Enabled: true}}}
	settingsRepo := &settingHandlerRepoStub{values: map[string]string{service.SettingKeyOpenAICodexTicketEnabled: "true"}}
	settings := service.NewSettingService(settingsRepo, cfg)
	gateway := &ticketHistoryGateway{}
	h := &AccountHandler{adminService: admin, cfg: cfg}
	h.SetCodexTicketSettings(settings)
	h.SetCodexTicketGateway(gateway)
	router := gin.New()
	router.GET("/accounts/:id/codex-ticket-history", h.GetCodexTicketHistory)
	request := func(path string, status int) string {
		t.Helper()
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		require.Equal(t, status, w.Code, w.Body.String())
		require.NotContains(t, w.Body.String(), "secret-token")
		return w.Body.String()
	}
	for _, id := range []int64{41, 42} {
		body := request(fmt.Sprintf("/accounts/%d/codex-ticket-history?model=gpt-6-astra", id), http.StatusOK)
		require.Equal(t, id, gjson.Get(body, "data.events.0.id").Int())
		require.Equal(t, int64(20), gjson.Get(body, "data.limit").Int())
	}
	require.Equal(t, []string{"41:gpt-6-astra", "42:gpt-6-astra"}, gateway.calls)
	request("/accounts/41/codex-ticket-history", http.StatusOK)
	require.Equal(t, "41:", gateway.calls[2])
	for _, tc := range []struct {
		path string
		code int
	}{
		{"/accounts/no/codex-ticket-history", http.StatusBadRequest},
		{"/accounts/0/codex-ticket-history", http.StatusBadRequest},
		{"/accounts/999/codex-ticket-history", http.StatusNotFound},
		{"/accounts/41/codex-ticket-history?model=unconfigured", http.StatusBadRequest},
	} {
		request(tc.path, tc.code)
	}
	require.Len(t, gateway.calls, 3)
	admin.accountsByID[41].Extra = map[string]any{service.OpenAICodexTicketEnabledExtraKey: false}
	body := request("/accounts/41/codex-ticket-history", http.StatusOK)
	require.Equal(t, "[]", gjson.Get(body, "data.events").Raw)
	settingsRepo.values[service.SettingKeyOpenAICodexTicketEnabled] = "false"
	settings.InvalidateOpenAICodexTicketEnabledCache()
	body = request("/accounts/42/codex-ticket-history", http.StatusOK)
	require.Equal(t, "[]", gjson.Get(body, "data.events").Raw)
	require.Len(t, gateway.calls, 3, "disabled policies must not read historical records")
}
