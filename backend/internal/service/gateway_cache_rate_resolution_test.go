//go:build unit

package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestGatewayCacheQuoteMatchesRecordedRate(t *testing.T) {
	for _, storageOnly := range []bool{false, true} {
		for _, tc := range []struct {
			name     string
			model    float64
			user     float64
			group    float64
			snapshot bool
			want     float64
		}{
			{name: "model_double", model: 2, user: 3, group: 1, snapshot: true, want: 2},
			{name: "model_half", model: 0.5, group: 1, snapshot: true, want: 0.5},
			{name: "free_group", group: 0, snapshot: true, want: 0},
			{name: "user_override", user: 1.5, group: 2, snapshot: true, want: 1.5},
			{name: "group_default", group: 1.8, snapshot: true, want: 1.8},
			{name: "legacy_without_snapshot", model: 2.5, group: 1, want: 2.5},
		} {
			kind := "create/"
			if storageOnly {
				kind = "extend/"
			}
			t.Run(kind+tc.name, func(t *testing.T) {
				ctx := context.Background()
				usage := &openAIRecordUsageLogRepoStub{inserted: true}
				users := &openAIRecordUsageUserRepoStub{}
				svc := newGatewayRecordUsageServiceForTest(usage, users, &openAIRecordUsageSubRepoStub{})
				group := &Group{ID: 42, Platform: PlatformGemini, RateMultiplier: tc.group}
				if tc.model > 0 {
					group.ModelRateMultipliers = []ModelRateMultiplierRule{{Model: "client-alias", Multiplier: tc.model}}
				}
				rates := &fallbackBillingRateRepo{rates: map[int64]float64{}}
				if tc.user > 0 {
					rates.rates[group.ID] = tc.user
				}
				svc.userGroupRateResolver = newUserGroupRateResolver(rates, nil, time.Minute, nil, "test")
				user := &User{ID: 43}
				key := &APIKey{ID: 44, UserID: user.ID, User: user, GroupID: &group.ID, Group: group}
				input := &RecordUsageInput{
					APIKey: key, User: user, Account: &Account{ID: 45, Platform: PlatformGemini},
					Result:             &ForwardResult{RequestID: "cache-quote-rate", Model: "gemini-2.5-pro", CacheStorageTokenHours: 1_000_000},
					ChannelUsageFields: ChannelUsageFields{OriginalModel: "client-alias"},
				}
				if !storageOnly {
					input.Result.Usage.InputTokens = 1000
				}
				if tc.snapshot {
					input.RateResolution = svc.ResolveRateResolution(ctx, user.ID, group, "client-alias")
				}
				quote := svc.QuoteUsageCost(ctx, input)
				if tc.snapshot {
					group.RateMultiplier = 9
					group.ModelRateMultipliers = []ModelRateMultiplierRule{{Model: "client-alias", Multiplier: 9}}
				}
				require.NoError(t, svc.RecordUsage(ctx, input))
				require.NotNil(t, usage.lastLog)
				require.Positive(t, usage.lastLog.TotalCost)
				require.Equal(t, tc.want, usage.lastLog.RateMultiplier)
				require.InDelta(t, quote, usage.lastLog.ActualCost, 1e-12)
				require.InDelta(t, quote, users.lastAmount, 1e-12)
			})
		}
	}
}
