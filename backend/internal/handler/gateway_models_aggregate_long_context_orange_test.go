package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// Orange 特有：聚合目录先按各组账号展开长上下文，再按各组白名单过滤。
func TestGatewayModelsAggregatedAnthropicLongContext(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, test := range []struct {
		name          string
		contextWindow int64
		allowlist     []string
		routing       map[string][]int64
		want          []string
	}{
		{
			name:          "secondary group can expose only its long-context variant",
			contextWindow: 1_000_000,
			allowlist:     []string{"secondary[1m]"},
			want:          []string{"shared", "secondary[1m]"},
		},
		{
			name:          "primary metadata does not expand secondary group capabilities",
			contextWindow: 200_000,
			allowlist:     []string{"shared[1m]", "secondary"},
			want:          []string{"shared", "secondary"},
		},
		{
			name:          "routing excludes the account with long-context metadata",
			contextWindow: 1_000_000,
			allowlist:     []string{"secondary", "secondary[1m]"},
			routing:       map[string][]int64{"secondary": {999}},
			want:          []string{"shared", "secondary"},
		},
		{
			name:          "routing includes the account with long-context metadata",
			contextWindow: 900_000,
			allowlist:     []string{"secondary", "secondary[1m]"},
			routing:       map[string][]int64{"secondary": {2}},
			want:          []string{"shared", "secondary", "secondary[1m]"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			primary := &service.Group{
				ID: 8810, Platform: service.PlatformAnthropic,
				ModelAllowlist: service.GroupModelAllowlist{Enabled: true, Models: []string{"shared"}},
			}
			secondary := &service.Group{
				ID: 8811, Platform: service.PlatformAnthropic,
				ModelAllowlist:      service.GroupModelAllowlist{Enabled: true, Models: test.allowlist},
				ModelRoutingEnabled: test.routing != nil, ModelRouting: test.routing,
			}
			primaryAccount := service.Account{
				ID: 1, Platform: service.PlatformAnthropic, Type: service.AccountTypeOAuth,
				Credentials: map[string]any{"model_mapping": map[string]any{"shared": "primary-upstream"}},
			}
			primaryAccount.SetUpstreamModelMetadataSnapshot(service.UpstreamModelMetadataSnapshot{Models: map[string]service.UpstreamModelMetadata{
				"primary-upstream": {ContextWindow: 1_000_000},
			}})
			secondaryAccount := service.Account{
				ID: 2, Platform: service.PlatformAnthropic, Type: service.AccountTypeOAuth,
				Credentials: map[string]any{"model_mapping": map[string]any{"shared": "secondary-upstream", "secondary": "secondary-upstream"}},
			}
			secondaryAccount.SetUpstreamModelMetadataSnapshot(service.UpstreamModelMetadataSnapshot{Models: map[string]service.UpstreamModelMetadata{
				"secondary-upstream": {ContextWindow: test.contextWindow},
			}})
			handler := newGatewayModelsHandlerForTest(&gatewayModelsAccountRepoStub{byGroup: map[int64][]service.Account{
				primary.ID: {primaryAccount}, secondary.ID: {secondaryAccount},
			}})
			recorder := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(recorder)
			ctx.Request = httptest.NewRequest(http.MethodGet, "/v1/models", nil)
			ctx.Set(string(middleware2.ContextKeyAPIKey), &service.APIKey{
				GroupID: &primary.ID, Group: primary,
				Groups: []*service.APIKeyGroupEntry{{Group: primary, Priority: 1}, {Group: secondary, Priority: 2}},
			})
			handler.Models(ctx)
			require.Equal(t, http.StatusOK, recorder.Code)
			var response gatewayModelsResponseForTest
			require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
			require.Equal(t, test.want, modelIDsForTest(response.Data))
		})
	}
}
