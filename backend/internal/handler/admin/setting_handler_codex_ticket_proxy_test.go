package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestSettingCodexTicketProxyTestRejectsArbitraryURLAndMalformedInput(t *testing.T) {
	gin.SetMode(gin.TestMode)
	handler := &SettingHandler{}
	for _, body := range []string{
		`{"proxy_index":1,"proxy_url":"http://user:secret@127.0.0.1"}`,
		`{"proxy_index":"1"}`, `{"proxy_index":1} {}`, strings.Repeat("x", 2048),
	} {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
		handler.TestOpenAICodexTicketProxy(c)
		require.Equal(t, http.StatusBadRequest, w.Code)
		require.NotContains(t, w.Body.String(), "secret")
		require.NotContains(t, w.Body.String(), "127.0.0.1")
	}
}

func TestSettingCodexTicketProxyTestReturnsStructuredDiagnostic(t *testing.T) {
	gin.SetMode(gin.TestMode)
	handler := &SettingHandler{}
	handler.SetOpenAICodexTicketProxyTester(service.NewOpenAICodexTicketProxyTester(nil))
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"proxy_index":2}`))
	handler.TestOpenAICodexTicketProxy(c)
	require.Equal(t, http.StatusOK, w.Code)
	var result struct {
		Code int                                      `json:"code"`
		Data service.OpenAICodexTicketProxyTestResult `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &result))
	require.Zero(t, result.Code)
	require.Equal(t, 2, result.Data.ProxyIndex)
	require.False(t, result.Data.Success)
	require.Equal(t, "settings_unavailable", result.Data.ErrorCode)
}
