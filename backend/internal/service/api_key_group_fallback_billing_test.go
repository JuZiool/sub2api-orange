//go:build unit

package service

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/gin-gonic/gin"
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

// Combine the official mixed-TTL response fixture with Orange's real group-chain
// activation, so TTL prices and the actual routed group's frozen rate are billed
// together even when an administrator changes both group rates in flight.
func TestAPIKeyFallbackRateSnapshotBillingWithCacheWriteTTL(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		name         string
		fallbackRate float64
	}{
		{name: "fallback_default", fallbackRate: 2.5},
		{name: "fallback_free", fallbackRate: 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const (
				primaryID      int64 = 101
				fallbackID     int64 = 102
				channelID      int64 = 201
				requestedModel       = "client-alias"
				forwardedModel       = "gpt-6-luna"
			)
			primary := &Group{ID: primaryID, Platform: PlatformOpenAI, Status: StatusActive, Hydrated: true, RateMultiplier: 1.5}
			fallback := &Group{ID: fallbackID, Platform: PlatformOpenAI, Status: StatusActive, Hydrated: true, RateMultiplier: tc.fallbackRate}
			user := &User{ID: 103}
			key := &APIKey{
				ID: 104, UserID: user.ID, User: user, GroupID: i64p(primaryID), Group: primary,
				Groups: []*APIKeyGroupEntry{{Group: primary, Priority: 1}, {Group: fallback, Priority: 2}},
			}
			ctx := WithAPIKeyGroupFallbackRouting(context.WithValue(context.Background(), ctxkey.Group, primary), key)
			rates := &fallbackBillingRateRepo{rates: map[int64]float64{}}
			usageRepo := &openAIRecordUsageLogRepoStub{inserted: true}
			userRepo := &openAIRecordUsageUserRepoStub{}
			svc := newOpenAIRecordUsageServiceForTest(usageRepo, userRepo, &openAIRecordUsageSubRepoStub{}, rates)
			var pricing ChannelModelPricing
			require.NoError(t, json.Unmarshal([]byte(`{"platform":"openai","models":["gpt-6-luna"],"billing_mode":"token","input_price":0.00000065,"output_price":0.00000325,"cache_write_price":0.0000008125,"cache_write_12h_price":0.000001625,"cache_read_price":0.000000065}`), &pricing))
			channel := Channel{
				ID: channelID, Status: StatusActive, GroupIDs: []int64{fallbackID},
				ModelPricing: []ChannelModelPricing{pricing}, ApplyPricingToAccountStats: true,
				BillingModelSource: BillingModelSourceChannelMapped,
				ModelMapping:       map[string]map[string]string{PlatformOpenAI: {requestedModel: forwardedModel}},
			}
			svc.channelService = &ChannelService{}
			svc.channelService.cache.Store(populateChannelCache([]Channel{channel}, map[int64]string{fallbackID: PlatformOpenAI}))
			svc.resolver = NewModelPricingResolver(svc.channelService, svc.billingService)
			svc.cfg.Gateway.MaxLineSize = defaultMaxLineSize
			resolution := svc.ResolveRateResolution(ctx, user.ID, primary, requestedModel)
			require.Equal(t, 1.5, resolution.Multiplier)

			// Neither the routed group object nor the rate repository can reprice
			// this request after its primary and fallback snapshots were captured.
			primary.RateMultiplier, fallback.RateMultiplier = 9, 9
			rates.rates[primaryID], rates.rates[fallbackID] = 9, 9
			account := &Account{ID: 105, Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
			var visited []int64
			selected, err := selectAccountWithAPIKeyGroupFallback(ctx, key.GroupID, requestedModel, func(_ context.Context, groupID *int64) (*Account, error) {
				visited = append(visited, *groupID)
				if *groupID == primaryID {
					return nil, ErrNoAvailableAccounts
				}
				return account, nil
			})
			require.NoError(t, err)
			require.Same(t, account, selected)
			require.Equal(t, []int64{primaryID, fallbackID}, visited)
			require.Equal(t, fallbackID, *key.GroupID)
			mapping := svc.channelService.ResolveChannelMapping(ctx, *key.GroupID, requestedModel)
			require.True(t, mapping.Mapped)
			require.Equal(t, forwardedModel, mapping.MappedModel)
			require.Equal(t, channelID, mapping.ChannelID)

			response := `{"id":"resp_ttl_fallback","object":"response","model":"gpt-6-luna","status":"completed","output":[],"usage":{"input_tokens":2402,"input_tokens_details":{"cached_tokens":0,"cache_write_tokens":2399,"cache_write_tokens_details":{"ttl_30m_tokens":9,"ttl_12h_tokens":2390}},"output_tokens":5,"total_tokens":2407}}`
			resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(response))}
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
			parsed, err := svc.handleNonStreamingResponse(ctx, resp, c, account, requestedModel, mapping.MappedModel)
			require.NoError(t, err)
			require.NotNil(t, parsed.usage)
			require.NoError(t, svc.RecordUsage(context.Background(), &OpenAIRecordUsageInput{
				APIKey: key, User: user, Account: account, RateResolution: resolution,
				ChannelUsageFields: mapping.ToUsageFields(requestedModel, mapping.MappedModel),
				Result: &OpenAIForwardResult{
					RequestID: "resp_ttl_fallback", Model: mapping.MappedModel,
					UpstreamModel: mapping.MappedModel, Usage: *parsed.usage,
				},
			}))

			log := usageRepo.lastLog
			require.NotNil(t, log)
			require.Equal(t, fallbackID, *log.GroupID)
			require.NotNil(t, log.ChannelID)
			require.Equal(t, channelID, *log.ChannelID)
			require.NotNil(t, log.ModelMappingChain)
			require.Equal(t, requestedModel+"→"+forwardedModel, *log.ModelMappingChain)
			require.Equal(t, 3, log.InputTokens)
			require.Equal(t, 5, log.OutputTokens)
			require.Equal(t, 2399, log.CacheCreationTokens)
			require.Equal(t, 9, log.CacheCreation30mTokens)
			require.Equal(t, 2390, log.CacheCreation12hTokens)
			require.Zero(t, log.CacheCreation5mTokens)
			require.Zero(t, log.CacheCreation1hTokens)
			cacheCost := (9*0.8125 + 2390*1.625) / 1e6
			totalCost := cacheCost + 3*0.65/1e6 + 5*3.25/1e6
			require.InDelta(t, cacheCost, log.CacheCreationCost, 1e-12)
			require.InDelta(t, totalCost, log.TotalCost, 1e-12)
			require.Equal(t, tc.fallbackRate, log.RateMultiplier)
			require.InDelta(t, totalCost*tc.fallbackRate, log.ActualCost, 1e-12)
			require.InDelta(t, totalCost*tc.fallbackRate, userRepo.lastAmount, 1e-12)
			require.NotNil(t, log.AccountStatsCost)
			require.InDelta(t, totalCost, *log.AccountStatsCost, 1e-12)
			require.Equal(t, 1.5, resolution.Multiplier, "actual fallback billing must preserve the primary snapshot")
		})
	}
}
