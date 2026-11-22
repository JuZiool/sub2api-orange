// Orange 特有：API Key 分组链（密钥级兜底 / 聚合）的模型策略。
//
// 本文件解决两个用户可见问题，全部是"只读分组配置 + 只读账号映射"的纯判定：
//
//  1. 链级模型可见性（列表合并）：聚合密钥的模型目录必须是"链上所有分组可用模型的
//     并集"，而不是链首分组的目录。这里的助手给出链上分组序列（按 priority 升序）
//     与"某分组在配置层面是否支持该模型"的统一口径，供 /v1/models、Codex 清单、
//     Gemini models 列表与分组白名单准入中间件复用。
//
//  2. 请求前按模型预选分组：链首先行是"当前分组支持该模型"；只有当当前分组在配置
//     层面不支持（白名单不允许，或生图模型而该分组未开生图）时，才按链优先级预跳到
//     第一个支持它的分组，再交给既有选号流程。因此"A、B 都有该模型"仍然先走 A。
//
// 约定：本文件不写库、不改计费/用量、不发起上游请求，也不会跨请求共享可变状态。
package service

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"slices"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
)

// orangeAggregatedListModelCreated 是合并出的 /v1/models 条目的 created 字段，
// 与 handler.writeOpenAIModelsList 的兜底值保持一致。
const orangeAggregatedListModelCreated = 1704067200

// OrangeChainGroups 返回该 Key 的分组链（含链首，按 priority 升序，去重、过滤停用）。
//
// 返回值长度 < 2 表示该 Key 没有可用的兜底/聚合链：调用方必须沿用上游的单分组语义。
// 链成员来自鉴权快照（orange_api_key_group_chain_loader.go），这里只做只读筛选。
func OrangeChainGroups(apiKey *APIKey) []*Group {
	chain := apiKey.GroupChain()
	if len(chain) == 0 {
		return nil
	}
	groups := make([]*Group, 0, len(chain))
	seen := make(map[int64]struct{}, len(chain))
	for _, entry := range chain {
		group := entry.Group
		if group == nil || group.ID <= 0 {
			continue
		}
		if _, dup := seen[group.ID]; dup {
			continue
		}
		if !group.IsActive() || !IsGroupContextValid(group) {
			continue
		}
		seen[group.ID] = struct{}{}
		groups = append(groups, group)
	}
	return groups
}

// OrangeChainAllowsModel 报告该 Key 是否应因"链上任一分组允许该模型"而放行。
//
// 只在 Key 真的配置了可用链（≥2 组）时参与判定；普通 Key 恒为 false，调用方据此
// 保持上游原有语义（链首白名单是唯一裁决者）。
func OrangeChainAllowsModel(apiKey *APIKey, model string) bool {
	groups := OrangeChainGroups(apiKey)
	if len(groups) < 2 {
		return false
	}
	for _, group := range groups {
		if OrangeModelSupportedByGroup(group, model, group.Platform) {
			return true
		}
	}
	return false
}

// OrangeChainAllowsImageGeneration 报告聚合/兜底链上是否至少有一个分组允许生图。
//
// 非链路密钥沿用单分组语义（GroupAllowsImageGeneration 对未分组 Key 返回 true，
// 保持上游"未分组即放行"的行为）。用于替换 handler 里只看链首分组的生图门控。
func OrangeChainAllowsImageGeneration(apiKey *APIKey) bool {
	groups := OrangeChainGroups(apiKey)
	if len(groups) < 2 {
		return GroupAllowsImageGeneration(apiKeyGroup(apiKey))
	}
	for _, group := range groups {
		if GroupAllowsImageGeneration(group) {
			return true
		}
	}
	return false
}

// OrangeGeminiImageGenerationDenied 是 GeminiImageGenerationDenied 的链感知版本。
//
// 聚合/兜底密钥下，只要链上存在一个"不拒绝该生图请求"的分组，入口就放行；真正执行
// 请求的分组仍由自己的生图开关裁决（调度器会按模型把请求预选到允许生图的分组）。
// 非链路密钥与上游行为完全一致。
func OrangeGeminiImageGenerationDenied(apiKey *APIKey, geminiBody []byte, models ...string) bool {
	groups := OrangeChainGroups(apiKey)
	if len(groups) < 2 {
		return GeminiImageGenerationDenied(apiKeyGroup(apiKey), geminiBody, models...)
	}
	for _, group := range groups {
		if !GeminiImageGenerationDenied(group, geminiBody, models...) {
			return false
		}
	}
	return true
}

// OrangeModelSupportedByGroup 报告分组在配置层面是否支持该模型（能否为它选号/展示）。
//
// 判定口径与调度、准入保持一致，只读分组配置：
//   - 生图模型要求该分组开启生图（复用 GroupAllowsImageGeneration），否则"白名单允许
//     但组禁止生图"会被误判为可执行；
//   - 分组模型白名单开启时必须命中白名单（复用 GroupModelAllowlist.Allows）。
//
// platform 为空时回退 group.Platform，只用于生图模型判定。
func OrangeModelSupportedByGroup(group *Group, model, platform string) bool {
	if group == nil || group.ID <= 0 {
		return false
	}
	model = strings.TrimSpace(model)
	if model == "" {
		return true
	}
	if platform = strings.TrimSpace(platform); platform == "" {
		platform = group.Platform
	}
	if OrangeModelIsImageGenerationForPlatform(model, platform) && !GroupAllowsImageGeneration(group) {
		return false
	}
	if !group.ModelAllowlistEnabled() {
		return true
	}
	return group.ModelAllowlist.Allows(model)
}

// OrangeModelIsImageGenerationForPlatform 判断模型名在该平台上是否属于生图模型。
//
// 覆盖三类既有判定：OpenAI 原生生图模型（gpt-image-*、grok-imagine*）、
// Gemini/Antigravity 以图片为输出的模型（gemini-*-image*）以及兼容图片模型。
func OrangeModelIsImageGenerationForPlatform(model, platform string) bool {
	model = strings.TrimSpace(model)
	if model == "" {
		return false
	}
	if isOpenAIImageGenerationModel(model) {
		return true
	}
	switch strings.TrimSpace(platform) {
	case "", PlatformOpenAI, PlatformGemini, PlatformAntigravity, PlatformComposite:
		return isImageGenerationModel(model) || isGeminiCompatibleImageModel(model)
	default:
		return isImageGenerationModel(model)
	}
}

// OrangeCodexModelIDsForGroup 是 FilterCodexModelIDsForGroup 的聚合密钥版本。
//
// 与上游一致地剔除通配路由键、Codex 自动模式与未显式启用的 auto 模式，并按分组白名单
// 过滤；唯一差异是生图模型：上游 Codex 目录不列专用生图模型，而聚合密钥必须能看到
// "链上其它分组可调用的生图模型"，因此当该分组开启生图（且白名单允许）时保留它们。
func OrangeCodexModelIDsForGroup(modelIDs []string, group *Group) []string {
	if group == nil {
		return FilterCodexModelIDsForGroup(modelIDs, nil)
	}
	platform := group.Platform
	rest := make([]string, 0, len(modelIDs))
	media := make([]string, 0, 2)
	for _, modelID := range modelIDs {
		modelID = strings.TrimSpace(modelID)
		if modelID == "" {
			continue
		}
		if !isCodexDedicatedMediaModel(modelID) {
			rest = append(rest, modelID)
			continue
		}
		if OrangeModelSupportedByGroup(group, codexProviderQualifiedModelID(modelID), platform) {
			media = append(media, modelID)
		}
	}
	filtered := FilterCodexModelIDsForGroup(rest, group)
	if len(media) == 0 {
		return filtered
	}
	if group.ModelAllowlistEnabled() {
		media = group.ModelAllowlist.FilterForListing(media)
	}
	for _, modelID := range media {
		if slices.Contains(filtered, modelID) {
			continue
		}
		filtered = append(filtered, modelID)
	}
	return filtered
}

// orangePreselectChainGroupForModel 在真正选号前按"分组配置层面是否支持该模型"预选分组。
//
// 语义（用户要求）：A、B 都支持该模型则仍从当前分组（通常是链首）开始；只有当前分组
// 不支持——白名单不允许，或是生图模型而该分组未开生图——才按链优先级找第一个支持的
// 分组，激活请求级路由（复用 activateOrangeKeyRouting 的指针替换与缓存清理）并从它
// 开始选号。找不到支持的分组时保持原分组与原有失败路径。
//
// 返回的 *int64 是本次选号应使用的分组 ID（未预选时原样返回）。
func orangePreselectChainGroupForModel(ctx context.Context, groupID *int64, requestedModel string) (context.Context, *int64) {
	model := strings.TrimSpace(requestedModel)
	if ctx == nil || groupID == nil || *groupID <= 0 || model == "" {
		return ctx, groupID
	}
	// 强制平台路由（/antigravity 等）的分组在中间件阶段就已按平台锁定，与链推进一样
	// 不做换组，避免把请求送进语义不同的入口。
	if forcePlatform, _ := ctx.Value(ctxkey.ForcePlatform).(string); forcePlatform != "" {
		return ctx, groupID
	}
	routing, ok := orangeKeyRoutingFromContext(ctx)
	if !ok || routing.apiKey == nil || len(routing.chain) < 2 {
		return ctx, groupID
	}
	current := orangeChainGroupByID(routing.chain, *groupID)
	if current == nil {
		return ctx, groupID
	}
	if OrangeModelSupportedByGroup(current, model, current.Platform) {
		return ctx, groupID
	}
	for i := range routing.chain {
		candidate := routing.chain[i].Group
		if candidate == nil || candidate.ID <= 0 || candidate.ID == current.ID {
			continue
		}
		if !candidate.IsActive() || !IsGroupContextValid(candidate) {
			continue
		}
		if !OrangeModelSupportedByGroup(candidate, model, candidate.Platform) {
			continue
		}
		if !activateOrangeKeyRouting(ctx, candidate.ID) {
			return ctx, groupID
		}
		preselected := candidate.ID
		slog.Info("orange_key_group_chain_model_preselect",
			"from_group_id", current.ID,
			"to_group_id", preselected,
			"model", model)
		return ctx, &preselected
	}
	return ctx, groupID
}

// OrangeMergeAggregatedModelEntries 把链上"链首之外"各分组的模型条目并入上游目录。
//
// codex=true 处理 Codex 清单（models[].slug），false 处理 /v1/models（data[].id）。
// 只读账号映射与分组白名单，向目录追加当前缺失的条目；形状无法识别时保持上游原样。
// 返回 nil 表示"没有可并入的内容"或"不是链路密钥"，调用方无需区分。
func (s *OpenAIGatewayService) OrangeMergeAggregatedModelEntries(
	ctx context.Context,
	groups []*Group,
	headGroupID int64,
	response *OpenAIModelsResponse,
	codex bool,
	ifNoneMatch string,
) error {
	if s == nil || s.accountRepo == nil || response == nil || response.NotModified || len(response.Body) == 0 {
		return nil
	}
	if len(groups) < 2 {
		return nil
	}
	field, idField := "data", "id"
	if codex {
		field, idField = "models", "slug"
	}
	envelope, entries, err := modelCatalogEntries(response.Body, field)
	if err != nil {
		// 目录形状不认识（例如上游返回了非预期结构）时保持原样，绝不破坏已有清单。
		return nil
	}
	seen := make(map[string]struct{}, len(entries))
	for _, raw := range entries {
		var entry map[string]json.RawMessage
		if err := json.Unmarshal(raw, &entry); err != nil {
			continue
		}
		var id string
		if err := json.Unmarshal(entry[idField], &id); err != nil {
			continue
		}
		if id = strings.TrimSpace(id); id != "" {
			seen[strings.ToLower(id)] = struct{}{}
		}
	}

	accounts := make([]Account, 0)
	appended := make([]json.RawMessage, 0)
	for _, group := range groups {
		if group == nil || group.ID <= 0 {
			continue
		}
		visible, catalog, loadErr := loadCodexGroupCatalogAccounts(ctx, s.accountRepo, group.ID)
		if loadErr != nil {
			return fmt.Errorf("load group configured models: %w", loadErr)
		}
		accounts = append(accounts, catalog...)
		if group.ID == headGroupID {
			// 链首目录已由上游既有逻辑写入，不重复追加。
			continue
		}
		if group.CodexModelsManifestConfig.Enabled {
			// 固定账号发现的目录需要单独向上游拉取；列表接口不额外发起上游请求。
			continue
		}
		modelIDs := openAIConfiguredCodexModelIDsForGroup(visible, group)
		if group.ModelAllowlistEnabled() {
			modelIDs = group.ModelAllowlist.FilterForListing(modelIDs)
		}
		for _, modelID := range modelIDs {
			modelID = strings.TrimSpace(modelID)
			if modelID == "" || strings.Contains(modelID, "*") {
				continue
			}
			if !OrangeModelSupportedByGroup(group, modelID, group.Platform) {
				continue
			}
			key := strings.ToLower(modelID)
			if _, dup := seen[key]; dup {
				continue
			}
			raw, marshalErr := orangeConfiguredModelEntry(modelID, codex)
			if marshalErr != nil {
				return marshalErr
			}
			seen[key] = struct{}{}
			appended = append(appended, raw)
		}
	}
	if len(appended) == 0 {
		return nil
	}

	merged, err := json.Marshal(append(entries, appended...))
	if err != nil {
		return fmt.Errorf("merge aggregated model entries: %w", err)
	}
	envelope[field] = merged
	body, err := json.Marshal(envelope)
	if err != nil {
		return fmt.Errorf("merge aggregated model entries: %w", err)
	}
	// 与上游单分组路径一致地剔除已退役模型（账号集合取自链上全部分组）。
	if filtered, changed, filterErr := filterRetiredOpenAIPublicModelsBody(body, accounts, codex); filterErr == nil && changed {
		body = filtered
	}
	response.Body = body
	response.ETag = codexModelsManifestBodyETag(body)
	if codexModelsManifestETagMatches(ifNoneMatch, response.ETag) {
		response.Body = nil
		response.NotModified = true
	}
	return nil
}

// orangeConfiguredModelEntry 按目录形状生成"仅配置别名"的模型条目。
func orangeConfiguredModelEntry(modelID string, codex bool) (json.RawMessage, error) {
	if codex {
		return json.Marshal(newConfiguredCodexModelDescriptor(modelID))
	}
	return json.Marshal(openai.Model{
		ID:          modelID,
		Object:      "model",
		Created:     orangeAggregatedListModelCreated,
		OwnedBy:     "openai",
		Type:        "model",
		DisplayName: modelID,
	})
}
