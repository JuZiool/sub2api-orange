//go:build unit

package service

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/stretchr/testify/require"
)

type fallbackBillingRateRepo struct {
	UserGroupRateRepository
	rates map[int64]float64
}

func (r *fallbackBillingRateRepo) GetByUserAndGroup(_ context.Context, _, groupID int64) (*float64, error) {
	if rate, ok := r.rates[groupID]; ok {
		return &rate, nil
	}
	return nil, nil
}

func TestAPIKeyFallbackRateSnapshotBilling(t *testing.T) {
	for _, platform := range []string{PlatformOpenAI, PlatformAnthropic} {
		for _, tc := range []struct {
			name                string
			primaryAvailable    bool
			fallbackUnavailable bool
			noFallback          bool
			userRate            bool
			modelRate           bool
			otherModel          bool
			free                bool
			want                float64
		}{
			{name: "primary_available", primaryAvailable: true, want: 1.5},
			{name: "not_configured", noFallback: true, primaryAvailable: true, want: 1.5},
			{name: "fallback_default", want: 2.5},
			{name: "fallback_user_override", userRate: true, want: 3.5},
			{name: "fallback_model_override", userRate: true, modelRate: true, want: 4.5},
			{name: "unmatched_model_uses_fallback_user_rate", userRate: true, modelRate: true, otherModel: true, want: 3.5},
			{name: "fallback_free", free: true, want: 0},
			{name: "both_unavailable", fallbackUnavailable: true, want: 1.5},
		} {
			t.Run(platform+"/"+tc.name, func(t *testing.T) {
				primary := &Group{ID: 101, Platform: platform, Status: StatusActive, Hydrated: true, RateMultiplier: 1.5}
				fallback := &Group{ID: 102, Platform: platform, Status: StatusActive, Hydrated: true, RateMultiplier: 2.5}
				if tc.free {
					fallback.RateMultiplier = 0
				}
				// The client model is intentionally different from the forwarded model.
				requestedModel := "client-alias"
				if tc.modelRate {
					fallback.ModelRateMultipliers = []ModelRateMultiplierRule{{Model: requestedModel, Multiplier: 4.5}}
				}
				if tc.otherModel {
					requestedModel = "other-alias"
				}
				user := &User{ID: 103}
				key := &APIKey{ID: 104, UserID: user.ID, User: user, GroupID: i64p(primary.ID), Group: primary}
				if !tc.noFallback {
					key.FallbackGroupID = i64p(fallback.ID)
					key.FallbackGroup = fallback
				}
				ctx := WithAPIKeyGroupFallbackRouting(context.WithValue(context.Background(), ctxkey.Group, primary), key)
				rates := &fallbackBillingRateRepo{rates: map[int64]float64{}}
				if tc.userRate {
					rates.rates[primary.ID] = 0.5
					rates.rates[fallback.ID] = 3.5
				}
				usageRepo := &openAIRecordUsageLogRepoStub{inserted: true}
				userRepo := &openAIRecordUsageUserRepoStub{}
				account := &Account{ID: 105, Platform: platform, Type: AccountTypeAPIKey}
				var resolution *RateResolution
				var record func() error
				if platform == PlatformOpenAI {
					svc := newOpenAIRecordUsageServiceForTest(usageRepo, userRepo, &openAIRecordUsageSubRepoStub{}, rates)
					svc.resolver = NewModelPricingResolver(nil, svc.billingService)
					resolution = svc.ResolveRateResolution(ctx, user.ID, primary, requestedModel)
					record = func() error {
						return svc.RecordUsage(context.Background(), &OpenAIRecordUsageInput{
							APIKey: key, User: user, Account: account, RateResolution: resolution,
							ChannelUsageFields: ChannelUsageFields{OriginalModel: requestedModel},
							Result:             &OpenAIForwardResult{RequestID: "fallback-rate", Model: "gpt-5.4", Usage: OpenAIUsage{InputTokens: 100, OutputTokens: 50}},
						})
					}
				} else {
					svc := newGatewayRecordUsageServiceForTest(usageRepo, userRepo, &openAIRecordUsageSubRepoStub{})
					svc.userGroupRateResolver = newUserGroupRateResolver(rates, nil, time.Minute, nil, "test")
					resolution = svc.ResolveRateResolution(ctx, user.ID, primary, requestedModel)
					record = func() error {
						return svc.RecordUsage(context.Background(), &RecordUsageInput{
							APIKey: key, User: user, Account: account, RateResolution: resolution,
							ChannelUsageFields: ChannelUsageFields{OriginalModel: requestedModel},
							Result:             &ForwardResult{RequestID: "fallback-rate", Model: "claude-sonnet-4", Usage: ClaudeUsage{InputTokens: 100, OutputTokens: 50}},
						})
					}
				}
				originalRate := resolution.Multiplier
				// Admin edits after capture must not reprice either route's in-flight request.
				primary.RateMultiplier = 9
				fallback.RateMultiplier = 9
				if tc.modelRate {
					fallback.ModelRateMultipliers[0].Multiplier = 9
				}
				rates.rates[101], rates.rates[102] = 9, 9
				attempts := 0
				_, err := selectAccountWithAPIKeyGroupFallback(ctx, key.GroupID, requestedModel, func(_ context.Context, groupID *int64) (*Account, error) {
					attempts++
					if (*groupID == 101 && !tc.primaryAvailable) || (*groupID == 102 && tc.fallbackUnavailable) {
						return nil, ErrNoAvailableAccounts
					}
					return account, nil
				})
				if tc.fallbackUnavailable {
					require.ErrorIs(t, err, ErrNoAvailableAccounts)
					require.EqualValues(t, 101, *key.GroupID)
					require.Equal(t, originalRate, resolution.Multiplier)
					require.Equal(t, 2, attempts)
					require.Zero(t, usageRepo.calls)
					return
				}
				require.NoError(t, err)
				wantGroup, wantAttempts := int64(102), 2
				if tc.primaryAvailable {
					wantGroup, wantAttempts = 101, 1
				}
				require.Equal(t, wantAttempts, attempts)
				require.Equal(t, wantGroup, *key.GroupID)
				require.NoError(t, record())
				require.NotNil(t, usageRepo.lastLog)
				require.Equal(t, wantGroup, *usageRepo.lastLog.GroupID)
				require.Equal(t, tc.want, usageRepo.lastLog.RateMultiplier)
				require.Positive(t, usageRepo.lastLog.TotalCost)
				require.InDelta(t, usageRepo.lastLog.TotalCost*tc.want, usageRepo.lastLog.ActualCost, 1e-12)
				require.InDelta(t, usageRepo.lastLog.ActualCost, userRepo.lastAmount, 1e-12)
				require.Equal(t, originalRate, resolution.Multiplier, "route selection must not mutate the captured snapshot")
			})
		}
	}
}
