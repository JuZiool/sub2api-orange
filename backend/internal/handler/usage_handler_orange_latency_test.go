// Orange 特有：使用记录列表（用户端 GET /api/v1/usage）首字延迟拆分回填自检。
package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// orangeLatencyUsageRepoCapture 返回固定用量行，并实现 Orange 延迟拆分读取能力。
type orangeLatencyUsageRepoCapture struct {
	service.UsageLogRepository
	rows      []service.UsageLog
	latencies map[string]service.OrangeUsageLatencyRecord
	loadErr   error
	loaded    [][]string
}

func (s *orangeLatencyUsageRepoCapture) ListWithFilters(_ context.Context, params pagination.PaginationParams, _ usagestats.UsageLogFilters) ([]service.UsageLog, *pagination.PaginationResult, error) {
	return s.rows, &pagination.PaginationResult{Total: int64(len(s.rows)), Page: params.Page, PageSize: params.PageSize}, nil
}

func (s *orangeLatencyUsageRepoCapture) OrangeLoadUsageLatency(_ context.Context, requestIDs []string) (map[string]service.OrangeUsageLatencyRecord, error) {
	s.loaded = append(s.loaded, requestIDs)
	if s.loadErr != nil {
		return nil, s.loadErr
	}
	out := make(map[string]service.OrangeUsageLatencyRecord, len(requestIDs))
	for _, id := range requestIDs {
		if record, ok := s.latencies[id]; ok {
			out[id] = record
		}
	}
	return out, nil
}

func newOrangeLatencyUserUsageRouter(repo service.UsageLogRepository) *gin.Engine {
	gin.SetMode(gin.TestMode)
	usageSvc := service.NewUsageService(repo, nil, nil, nil)
	usageHandler := NewUsageHandler(usageSvc, nil, nil, nil)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set(string(middleware2.ContextKeyUser), middleware2.AuthSubject{UserID: 42})
		c.Next()
	})
	router.GET("/usage", usageHandler.List)
	return router
}

func decodeOrangeLatencyUsageItems(t *testing.T, rec *httptest.ResponseRecorder) []map[string]any {
	t.Helper()
	var body struct {
		Data struct {
			Items []map[string]any `json:"items"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	return body.Data.Items
}

func TestUserUsageListBackfillsOrangeUsageLatency(t *testing.T) {
	repo := &orangeLatencyUsageRepoCapture{
		rows: []service.UsageLog{
			{ID: 1, UserID: 42, RequestID: "local:req-1", Model: "gpt-5", Stream: true},
			{ID: 2, UserID: 42, RequestID: "local:req-2", Model: "gpt-5", Stream: true},
		},
		latencies: map[string]service.OrangeUsageLatencyRecord{
			"local:req-1": {GatewayMs: 30, UpstreamMs: 270},
		},
	}
	router := newOrangeLatencyUserUsageRouter(repo)

	req := httptest.NewRequest(http.MethodGet, "/usage", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	items := decodeOrangeLatencyUsageItems(t, rec)
	require.Len(t, items, 2)
	// 有采样记录：两段耗时回填为整数。
	require.Equal(t, float64(30), items[0]["gateway_ms"])
	require.Equal(t, float64(270), items[0]["upstream_ms"])
	// 无采样记录：保持 null（前端据此不显示额外行）。
	require.Nil(t, items[1]["gateway_ms"])
	require.Nil(t, items[1]["upstream_ms"])
	// 本页 request_id 一次批量查询（含去重语义交给 service 层）。
	require.Equal(t, [][]string{{"local:req-1", "local:req-2"}}, repo.loaded)
}

func TestUserUsageListKeepsNullWhenOrangeLatencyLoadFails(t *testing.T) {
	repo := &orangeLatencyUsageRepoCapture{
		rows:    []service.UsageLog{{ID: 1, UserID: 42, RequestID: "local:req-1", Model: "gpt-5", Stream: true}},
		loadErr: context.DeadlineExceeded,
	}
	router := newOrangeLatencyUserUsageRouter(repo)

	req := httptest.NewRequest(http.MethodGet, "/usage", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code, "查询失败不得影响列表本身")
	items := decodeOrangeLatencyUsageItems(t, rec)
	require.Len(t, items, 1)
	require.Nil(t, items[0]["gateway_ms"])
	require.Nil(t, items[0]["upstream_ms"])
}
