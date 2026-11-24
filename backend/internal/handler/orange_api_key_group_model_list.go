// Orange 特有：聚合密钥（分组链）的模型目录合并。
//
// 背景：链首分组的模型目录只代表链首自己。聚合密钥必须让客户端看到"链上所有分组
// 可用模型的并集"，否则只有后续分组支持（或支持生图）的模型在 Codex 等客户端里
// 完全不可见，用户会认为"聚合分组没生效"。
//
// 合并规则（与 service/orange_api_key_group_model_policy.go 的判定同源）：
//   - 按 priority 升序（链首在前），保持首次出现位置；
//   - 每个分组先套自己的口径：账号映射/默认列表 → 分组模型白名单 → 生图策略；
//   - 最后整体按模型名（小写）去重；
//   - 合并结果不再套链首白名单（否则链首白名单会重新抹掉其它分组的模型）。
//
// 全部只读：不写库、不改用量/计费、不写认证缓存。
package handler

import (
	"context"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/gemini"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

// orangeAggregateAvailableModels 合并链上各分组的 /v1/models 目录。
//
// 返回 (models, true) 表示已拿到合并结果，调用方直接输出即可；返回 (nil, false)
// 表示不是可用链路（普通 Key 或链失效），调用方沿用上游"单分组 + 默认列表"逻辑。
func (h *GatewayHandler) orangeAggregateAvailableModels(ctx context.Context, apiKey *service.APIKey, platform string) ([]string, bool) {
	if h == nil || h.gatewayService == nil {
		return nil, false
	}
	groups := service.OrangeChainGroups(apiKey)
	if len(groups) < 2 {
		return nil, false
	}
	merged := make([]string, 0)
	seen := make(map[string]struct{})
	for _, group := range groups {
		if group == nil || group.ID <= 0 {
			continue
		}
		groupPlatform := platform
		if group.Platform != "" {
			groupPlatform = group.Platform
		}
		available := h.gatewayService.GetAvailableModels(ctx, &group.ID, groupPlatform)
		source := modelListingSource(groupPlatform, available, defaultModelIDsForPlatform(groupPlatform))
		if groupPlatform == service.PlatformAnthropic {
			source = h.gatewayService.ExpandAnthropicLongContextModels(ctx, group, source)
		}
		if group.ModelAllowlistEnabled() {
			source = group.ModelAllowlist.FilterForListing(source)
		}
		for _, model := range source {
			model = strings.TrimSpace(model)
			if model == "" {
				continue
			}
			// 生图模型要求该分组开启生图：目录不得宣称组级策略禁止调用的模型。
			if !service.OrangeModelSupportedByGroup(group, model, groupPlatform) {
				continue
			}
			key := strings.ToLower(model)
			if _, dup := seen[key]; dup {
				continue
			}
			seen[key] = struct{}{}
			merged = append(merged, model)
		}
	}
	if len(merged) == 0 {
		return nil, false
	}
	return merged, true
}

// orangeAggregateCodexModelIDs 合并链上各分组的 Codex 模型清单 ID。
//
// 每个分组独立按上游口径生成目录（账号映射 + 默认列表 + 白名单 + Codex 过滤），
// 生图模型按"该分组允许生图"保留（见 service.OrangeCodexModelIDsForGroup）。
func (h *GatewayHandler) orangeAggregateCodexModelIDs(ctx context.Context, apiKey *service.APIKey, platformOverride string) ([]string, bool) {
	if h == nil || h.gatewayService == nil {
		return nil, false
	}
	groups := service.OrangeChainGroups(apiKey)
	if len(groups) < 2 {
		return nil, false
	}
	merged := make([]string, 0)
	seen := make(map[string]struct{})
	for _, group := range groups {
		if group == nil || group.ID <= 0 {
			continue
		}
		modelIDs := h.codexModelIDsForGroup(ctx, group, platformOverride)
		modelIDs = service.OrangeCodexModelIDsForGroup(modelIDs, group)
		for _, modelID := range modelIDs {
			modelID = strings.TrimSpace(modelID)
			if modelID == "" {
				continue
			}
			key := strings.ToLower(modelID)
			if _, dup := seen[key]; dup {
				continue
			}
			seen[key] = struct{}{}
			merged = append(merged, modelID)
		}
	}
	if len(merged) == 0 {
		return nil, false
	}
	return merged, true
}

// orangeAggregateGeminiModels 合并链上各分组的 Gemini models 目录（含生图模型）。
func (h *GatewayHandler) orangeAggregateGeminiModels(ctx context.Context, apiKey *service.APIKey, platform string) ([]gemini.Model, bool) {
	ids, ok := h.orangeAggregateAvailableModels(ctx, apiKey, platform)
	if !ok {
		return nil, false
	}
	models := make([]gemini.Model, 0, len(ids))
	for _, id := range ids {
		models = append(models, gemini.FallbackModel(id))
	}
	return models, true
}

// orangeBlockedModelAllowlistCandidate 是 WS 帧内白名单校验的链感知版本。
//
// 与 HTTP 准入中间件同口径：聚合/兜底密钥下链上任一分组允许该候选即放行，避免链首
// 白名单拒掉只有后续分组支持的模型。普通 Key（无链）完全走上游实现，拒绝值不变。
func orangeBlockedModelAllowlistCandidate(apiKey *service.APIKey, candidates []string) string {
	if apiKey == nil || apiKey.Group == nil || !apiKey.Group.ModelAllowlistEnabled() {
		return ""
	}
	remaining := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		if service.OrangeChainAllowsModel(apiKey, candidate) {
			continue
		}
		remaining = append(remaining, candidate)
	}
	return blockedModelAllowlistCandidate(apiKey.Group, remaining)
}

// orangeMergeAggregatedCatalog 把链上其余分组的模型并入上游目录（包级实现，两个 handler 共用）。
//
// codex=true 用于 Codex 清单（models[].slug），false 用于 /v1/models（data[].id）。
// 链首分组由上游既有逻辑写入，这里只补链首之外的成员；任何失败都返回错误交给调用方
// 按各自入口的既有错误格式处理。
func orangeMergeAggregatedCatalog(
	ctx context.Context,
	svc *service.OpenAIGatewayService,
	apiKey *service.APIKey,
	manifest *service.OpenAIModelsResponse,
	codex bool,
	ifNoneMatch string,
) error {
	if svc == nil || apiKey == nil || apiKey.Group == nil || manifest == nil {
		return nil
	}
	groups := service.OrangeChainGroups(apiKey)
	if len(groups) < 2 {
		return nil
	}
	return svc.OrangeMergeAggregatedModelEntries(ctx, groups, apiKey.Group.ID, manifest, codex, ifNoneMatch)
}

// orangeMergeAggregatedCodexManifest 是 orangeMergeAggregatedCatalog 在 GatewayHandler 上的入口。
func (h *GatewayHandler) orangeMergeAggregatedCodexManifest(
	ctx context.Context,
	apiKey *service.APIKey,
	manifest *service.OpenAIModelsResponse,
	codex bool,
	ifNoneMatch string,
) error {
	if h == nil {
		return nil
	}
	return orangeMergeAggregatedCatalog(ctx, h.openAIGatewayService, apiKey, manifest, codex, ifNoneMatch)
}

// orangeMergeAggregatedCodexManifest 是 orangeMergeAggregatedCatalog 在 OpenAIGatewayHandler 上的入口。
func (h *OpenAIGatewayHandler) orangeMergeAggregatedCodexManifest(
	ctx context.Context,
	apiKey *service.APIKey,
	manifest *service.OpenAIModelsResponse,
	codex bool,
	ifNoneMatch string,
) error {
	if h == nil {
		return nil
	}
	return orangeMergeAggregatedCatalog(ctx, h.gatewayService, apiKey, manifest, codex, ifNoneMatch)
}
