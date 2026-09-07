package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type tokenRankingHandlerRepoStub struct {
	service.UsageLogRepository
	rows      []usagestats.TokenRankingRow
	err       error
	weekStart time.Time
	dayStart  time.Time
	dayEnd    time.Time
	called    bool
}

func (s *tokenRankingHandlerRepoStub) GetGlobalTokenRanking(_ context.Context, weekStart, dayStart, dayEnd time.Time) ([]usagestats.TokenRankingRow, error) {
	s.called = true
	s.weekStart = weekStart
	s.dayStart = dayStart
	s.dayEnd = dayEnd
	return s.rows, s.err
}

func newTokenRankingHandlerRouter(repo *tokenRankingHandlerRepoStub, authenticated bool) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	usageSvc := service.NewUsageService(repo, nil, nil, nil)
	handler := NewUsageHandler(usageSvc, nil, nil, nil)
	if authenticated {
		router.Use(func(c *gin.Context) {
			c.Set(string(middleware2.ContextKeyUser), middleware2.AuthSubject{UserID: 42})
			c.Next()
		})
	}
	router.GET("/usage/ranking", handler.TokenRanking)
	return router
}

func TestTokenRankingRequiresAuthentication(t *testing.T) {
	repo := &tokenRankingHandlerRepoStub{}
	router := newTokenRankingHandlerRouter(repo, false)
	req := httptest.NewRequest(http.MethodGet, "/usage/ranking", nil)
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusUnauthorized, rec.Code)
	require.False(t, repo.called)
}

func TestTokenRankingBuildsResponseAndMasksEmail(t *testing.T) {
	repo := &tokenRankingHandlerRepoStub{rows: []usagestats.TokenRankingRow{
		{Period: "weekly", Rank: 1, UserID: 7, Email: "abcdef@example.com", Requests: 3, TotalTokens: 900},
		{Period: "daily", Rank: 1, UserID: 7, Email: "abcdef@example.com", Requests: 2, InputTokens: 100, OutputTokens: 200, CacheTokens: 30, TotalTokens: 330},
	}}
	router := newTokenRankingHandlerRouter(repo, true)
	req := httptest.NewRequest(http.MethodGet, "/usage/ranking?timezone=UTC", nil)
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var payload struct {
		Data struct {
			Weekly struct {
				Items []tokenRankingItem `json:"items"`
			} `json:"weekly"`
			Daily struct {
				Items []tokenRankingItem `json:"items"`
			} `json:"daily"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	require.Len(t, payload.Data.Weekly.Items, 1)
	require.Equal(t, "abc***ef@example.com", payload.Data.Weekly.Items[0].Email)
	require.Len(t, payload.Data.Daily.Items, 1)
	require.Equal(t, int64(330), payload.Data.Daily.Items[0].TotalTokens)
	require.True(t, repo.called)
	require.Equal(t, repo.dayStart.AddDate(0, 0, 1), repo.dayEnd)
	require.True(t, !repo.weekStart.After(repo.dayStart))
}

func TestTokenRankingReturnsEmptyArraysOnEmptyRepositoryResult(t *testing.T) {
	router := newTokenRankingHandlerRouter(&tokenRankingHandlerRepoStub{rows: []usagestats.TokenRankingRow{}}, true)
	req := httptest.NewRequest(http.MethodGet, "/usage/ranking", nil)
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), `"items":[]`)
}

func TestTokenRankingPropagatesRepositoryError(t *testing.T) {
	router := newTokenRankingHandlerRouter(&tokenRankingHandlerRepoStub{err: errors.New("database unavailable")}, true)
	req := httptest.NewRequest(http.MethodGet, "/usage/ranking", nil)
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusInternalServerError, rec.Code)
}

func TestMaskRankingEmail(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "single", input: "a@example.com", want: "a***@example.com"},
		{name: "short", input: "ab@example.com", want: "a***b@example.com"},
		{name: "long", input: "abcdef@example.com", want: "abc***ef@example.com"},
		{name: "unicode", input: "用户甲@example.com", want: "用***甲@example.com"},
		{name: "invalid", input: "not-an-email", want: "***"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, maskRankingEmail(tt.input))
		})
	}
}
