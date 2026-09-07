package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
	"github.com/stretchr/testify/require"
)

type tokenRankingServiceRepoStub struct {
	UsageLogRepository
	rows      []usagestats.TokenRankingRow
	err       error
	weekStart time.Time
	dayStart  time.Time
	dayEnd    time.Time
}

func (s *tokenRankingServiceRepoStub) GetGlobalTokenRanking(_ context.Context, weekStart, dayStart, dayEnd time.Time) ([]usagestats.TokenRankingRow, error) {
	s.weekStart = weekStart
	s.dayStart = dayStart
	s.dayEnd = dayEnd
	return s.rows, s.err
}

func TestUsageServiceGetGlobalTokenRankingDelegatesToRepository(t *testing.T) {
	weekStart := time.Date(2026, 9, 7, 0, 0, 0, 0, time.UTC)
	dayStart := weekStart.AddDate(0, 0, 2)
	dayEnd := dayStart.AddDate(0, 0, 1)
	repo := &tokenRankingServiceRepoStub{rows: []usagestats.TokenRankingRow{{Period: "daily", Rank: 1, UserID: 9}}}
	svc := NewUsageService(repo, nil, nil, nil)

	rows, err := svc.GetGlobalTokenRanking(context.Background(), weekStart, dayStart, dayEnd)

	require.NoError(t, err)
	require.Equal(t, repo.rows, rows)
	require.Equal(t, weekStart, repo.weekStart)
	require.Equal(t, dayStart, repo.dayStart)
	require.Equal(t, dayEnd, repo.dayEnd)
}

func TestUsageServiceGetGlobalTokenRankingWrapsRepositoryError(t *testing.T) {
	repo := &tokenRankingServiceRepoStub{err: errors.New("database unavailable")}
	svc := NewUsageService(repo, nil, nil, nil)

	_, err := svc.GetGlobalTokenRanking(context.Background(), time.Time{}, time.Time{}, time.Time{})

	require.Error(t, err)
	require.ErrorContains(t, err, "get global token ranking")
}
