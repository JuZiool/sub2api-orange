package admin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type subscriptionGraceAdminService struct {
	*stubAdminService
	account service.Account
}

func (s *subscriptionGraceAdminService) GetAccount(context.Context, int64) (*service.Account, error) {
	return &s.account, nil
}

func TestAccountSubscriptionGraceHandler(t *testing.T) {
	gin.SetMode(gin.TestMode)
	adminSvc := &subscriptionGraceAdminService{account: service.Account{
		ID: 42, Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth,
		Credentials: map[string]any{"plan_type": "plus", "access_token": "secret-token", "subscription_expires_at": time.Now().Add(time.Hour).Format(time.RFC3339)},
	}}
	h := &AccountHandler{adminService: adminSvc, openaiOAuthService: &service.OpenAIOAuthService{}}
	router := gin.New()
	router.GET("/accounts/:id/subscription-grace", h.GetSubscriptionGrace)
	for _, tc := range []struct {
		id     string
		status int
	}{{"42", http.StatusOK}, {"0", http.StatusBadRequest}, {"-1", http.StatusBadRequest}, {"bad", http.StatusBadRequest}} {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/accounts/"+tc.id+"/subscription-grace", nil))
		require.Equal(t, tc.status, w.Code)
		require.NotContains(t, w.Body.String(), "secret-token")
	}
	adminSvc.account.Credentials["subscription_expires_at"] = time.Now().Add(-time.Hour).Format(time.RFC3339)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/accounts/42/subscription-grace", nil))
	require.Equal(t, http.StatusServiceUnavailable, w.Code)
	require.NotContains(t, w.Body.String(), "secret-token")
}
