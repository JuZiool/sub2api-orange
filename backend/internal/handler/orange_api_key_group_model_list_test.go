// Orange 特有：聚合密钥模型目录合并的接入测试（/v1/models 与 Codex 清单）。
package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// orangeChainTestHandler 构造一个带"链首 + 1 个后续分组"的 handler 与 API Key。
func orangeChainTestHandler(t *testing.T, head, tail *service.Group, accounts map[int64][]service.Account) (*GatewayHandler, *service.APIKey) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	h := newGatewayModelsHandlerForTest(&gatewayModelsAccountRepoStub{byGroup: accounts})
	apiKey := &service.APIKey{
		ID:      1,
		GroupID: &head.ID,
		Group:   head,
		Groups: []*service.APIKeyGroupEntry{
			{Group: head, Priority: 1},
			{Group: tail, Priority: 2},
		},
	}
	return h, apiKey
}

func orangeChainTestGroup(id int64, platform string) *service.Group {
	return &service.Group{ID: id, Name: "orange-chain-group", Platform: platform, Status: service.StatusActive, Hydrated: true}
}

func TestGatewayModelsAggregatesGroupChainCatalogue(t *testing.T) {
	const headID, tailID int64 = 9100, 9101
	head := orangeChainTestGroup(headID, service.PlatformOpenAI)
	tail := orangeChainTestGroup(tailID, service.PlatformOpenAI)
	tail.AllowImageGeneration = true
	h, apiKey := orangeChainTestHandler(t, head, tail, map[int64][]service.Account{
		headID: {{ID: 1, Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey, Credentials: map[string]any{
			"base_url":      "https://head.example/v1",
			"model_mapping": map[string]any{"head-only-model": "gpt-5.6"},
		}}},
		tailID: {{ID: 2, Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey, Credentials: map[string]any{
			"base_url":      "https://tail.example/v1",
			"model_mapping": map[string]any{"tail-only-model": "gpt-6", "gpt-image-1": "gpt-image-1"},
		}}},
	})

	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	ctx.Set(string(middleware2.ContextKeyAPIKey), apiKey)
	h.Models(ctx)
	require.Equal(t, http.StatusOK, rec.Code)

	var response gatewayModelsResponseForTest
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &response))
	ids := modelIDsForTest(response.Data)
	require.Contains(t, ids, "head-only-model", "链首分组的模型必须保留")
	require.Contains(t, ids, "tail-only-model", "链上后续分组的模型必须出现在聚合目录里")
	require.Contains(t, ids, "gpt-image-1", "后续分组允许生图时，生图模型也应出现在聚合目录里")
}

func TestGatewayModelsAggregatedCatalogueSkipsDisallowedImageModels(t *testing.T) {
	const headID, tailID int64 = 9200, 9201
	head := orangeChainTestGroup(headID, service.PlatformOpenAI)
	tail := orangeChainTestGroup(tailID, service.PlatformOpenAI)
	h, apiKey := orangeChainTestHandler(t, head, tail, map[int64][]service.Account{
		headID: {{ID: 1, Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey, Credentials: map[string]any{
			"base_url":      "https://head.example/v1",
			"model_mapping": map[string]any{"head-only-model": "gpt-5.6"},
		}}},
		tailID: {{ID: 2, Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey, Credentials: map[string]any{
			"base_url":      "https://tail.example/v1",
			"model_mapping": map[string]any{"tail-only-model": "gpt-6", "gpt-image-1": "gpt-image-1"},
		}}},
	})

	ids, ok := h.orangeAggregateAvailableModels(context.Background(), apiKey, service.PlatformOpenAI)
	require.True(t, ok)
	require.Contains(t, ids, "tail-only-model")
	require.NotContains(t, ids, "gpt-image-1", "未开启生图的分组不得把生图模型列入目录")
}

func TestOrangeAggregateCodexModelIDsMergesChain(t *testing.T) {
	const headID, tailID int64 = 9300, 9301
	head := orangeChainTestGroup(headID, service.PlatformOpenAI)
	tail := orangeChainTestGroup(tailID, service.PlatformOpenAI)
	tail.AllowImageGeneration = true
	h, apiKey := orangeChainTestHandler(t, head, tail, map[int64][]service.Account{
		headID: {{ID: 1, Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey, Credentials: map[string]any{
			"base_url":      "https://head.example/v1",
			"model_mapping": map[string]any{"head-only-model": "gpt-5.6"},
		}}},
		tailID: {{ID: 2, Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey, Credentials: map[string]any{
			"base_url":      "https://tail.example/v1",
			"model_mapping": map[string]any{"tail-only-model": "gpt-6", "gpt-image-1": "gpt-image-1"},
		}}},
	})

	modelIDs, ok := h.orangeAggregateCodexModelIDs(context.Background(), apiKey, "")
	require.True(t, ok)
	require.Contains(t, modelIDs, "head-only-model")
	require.Contains(t, modelIDs, "tail-only-model")
	require.Contains(t, modelIDs, "gpt-image-1", "链上允许生图的分组应把生图模型带进 Codex 清单")

	// 普通 Key（无链）不合并，调用方沿用单分组结果。
	plainHeadID := headID
	plain := &service.APIKey{ID: 2, GroupID: &plainHeadID, Group: head}
	_, ok = h.orangeAggregateCodexModelIDs(context.Background(), plain, "")
	require.False(t, ok)
}
