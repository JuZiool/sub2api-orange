//go:build unit

package repository

import (
	"context"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
	"github.com/stretchr/testify/require"
)

func TestGetGlobalTokenRankingUsesActiveUsersAndStableDailyRanking(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := &usageLogRepository{sql: db}
	weekStart := time.Date(2026, 9, 7, 0, 0, 0, 0, time.UTC)
	dayStart := time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC)
	dayEnd := dayStart.AddDate(0, 0, 1)

	mock.ExpectQuery(regexp.QuoteMeta("JOIN users u ON u.id = ul.user_id AND u.deleted_at IS NULL")).
		WithArgs(weekStart, dayStart, dayEnd).
		WillReturnRows(sqlmock.NewRows([]string{
			"period", "rank", "user_id", "email", "requests", "input_tokens", "output_tokens", "cache_tokens", "total_tokens",
		}).
			AddRow("weekly", 1, 7, "a@example.com", 2, 100, 200, 30, 330).
			AddRow("daily", 1, 7, "a@example.com", 1, 50, 100, 10, 160))

	rows, err := repo.GetGlobalTokenRanking(context.Background(), weekStart, dayStart, dayEnd)

	require.NoError(t, err)
	require.Len(t, rows, 2)
	require.Equal(t, int64(330), rows[0].TotalTokens)
	require.Equal(t, int64(160), rows[1].TotalTokens)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestGetGlobalTokenRankingQueryContainsDailyRequestFilterAndTokenFormula(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := &usageLogRepository{sql: db}
	start := time.Date(2026, 9, 7, 0, 0, 0, 0, time.UTC)
	day := start.AddDate(0, 0, 2)
	end := day.AddDate(0, 0, 1)

	mock.ExpectQuery(regexp.QuoteMeta("WHERE daily_requests > 0")).
		WithArgs(start, day, end).
		WillReturnRows(sqlmock.NewRows([]string{
			"period", "rank", "user_id", "email", "requests", "input_tokens", "output_tokens", "cache_tokens", "total_tokens",
		}))

	_, err := repo.GetGlobalTokenRanking(context.Background(), start, day, end)

	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

var _ = usagestats.TokenRankingRow{}
