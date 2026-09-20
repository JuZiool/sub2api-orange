package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type ticketManualGateway struct {
	accountCodexTicketDiagnosticsStub
	harvestCalls []string
	stopCalls    []string
	result       service.OpenAICodexTicketManualResult
	stopErr      error
}

func (g *ticketManualGateway) HarvestOpenAICodexTicketNow(_ context.Context, id int64, model string) service.OpenAICodexTicketManualResult {
	g.harvestCalls = append(g.harvestCalls, model)
	result := g.result
	result.Model = model
	return result
}

func (g *ticketManualGateway) StopOpenAICodexTicketRenewal(_ context.Context, id int64, models []string) error {
	g.stopCalls = append(g.stopCalls, strings.Join(models, ","))
	return g.stopErr
}

func newTicketManualHandler(t *testing.T, gateway *ticketManualGateway) (*AccountHandler, *gin.Engine, *ticketHistoryAdminService) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	admin := &ticketHistoryAdminService{stubAdminService: newStubAdminService(), accountsByID: map[int64]*service.Account{
		41: {ID: 41, Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth, Extra: map[string]any{service.OpenAICodexTicketEnabledExtraKey: true}},
	}}
	cfg := &config.Config{Gateway: config.GatewayConfig{OpenAICodexTicket: config.OpenAICodexTicketConfig{Enabled: true, Models: []string{"gpt-6-astra"}}}}
	settings := service.NewSettingService(&settingHandlerRepoStub{values: map[string]string{service.SettingKeyOpenAICodexTicketEnabled: "true"}}, cfg)
	h := &AccountHandler{adminService: admin, cfg: cfg}
	h.SetCodexTicketSettings(settings)
	h.SetCodexTicketGateway(gateway)
	router := gin.New()
	router.POST("/accounts/:id/codex-ticket/harvest", h.HarvestCodexTicket)
	router.POST("/accounts/:id/codex-ticket/stop", h.StopCodexTicketRenewal)
	return h, router, admin
}

func TestHarvestCodexTicketEndpoint(t *testing.T) {
	gateway := &ticketManualGateway{result: service.OpenAICodexTicketManualResult{Success: true, Code: "ready", Length: 292, Target: 292}}
	_, router, _ := newTicketManualHandler(t, gateway)

	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/accounts/41/codex-ticket/harvest?model=gpt-6-astra", nil))
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Equal(t, []string{"gpt-6-astra"}, gateway.harvestCalls)
	var envelope struct {
		Data service.OpenAICodexTicketManualResult `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &envelope))
	require.True(t, envelope.Data.Success)

	// Unknown model must be rejected before any harvest.
	w = httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/accounts/41/codex-ticket/harvest?model=unconfigured", nil))
	require.Equal(t, http.StatusBadRequest, w.Code)
	require.Len(t, gateway.harvestCalls, 1)

	// Invalid account id.
	w = httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/accounts/0/codex-ticket/harvest?model=gpt-6-astra", nil))
	require.Equal(t, http.StatusBadRequest, w.Code)
}

func TestStopCodexTicketRenewalEndpoint(t *testing.T) {
	gateway := &ticketManualGateway{}
	_, router, _ := newTicketManualHandler(t, gateway)

	w := httptest.NewRecorder()
	body := strings.NewReader(`{"model":"gpt-6-astra"}`)
	req := httptest.NewRequest(http.MethodPost, "/accounts/41/codex-ticket/stop", body)
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Equal(t, []string{"gpt-6-astra"}, gateway.stopCalls)

	// No model stops the configured set.
	gateway.stopCalls = nil
	w = httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/accounts/41/codex-ticket/stop", nil))
	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, []string{""}, gateway.stopCalls)
}
