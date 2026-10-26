package service

import (
	"context"
	"errors"
	"strings"
)

// ErrImageGenerationNotAllowed 表示聚合/兜底链上没有任何分组允许执行本次生图请求。
// 它与 ErrNoAvailableAccounts 严格区分：前者是权限拒绝，后者是容量不足。
var ErrImageGenerationNotAllowed = errors.New("image generation is not enabled for any group of this API key")

// ImageGenerationGroupPolicy 是请求级生图分组能力筛选结果（Orange 特有）。
//
// 聚合密钥（以及旧式“主分组 + 单兜底分组”）的模型目录合并整条链，但生图开关与
// 分组模型白名单都是分组级策略：请求必须按优先级尝试“允许生图、且自己白名单允许
// 该模型”的分组。本策略由 handler 在入口按请求计算并写入 context，调度器只在这些
// 分组内选号——既不会把请求送进禁用生图的组，也不会借其他组的权限在该组执行。
//
// 策略是请求级的：不修改 API Key 的分组链，不写回认证缓存，也不影响其他请求。
type ImageGenerationGroupPolicy struct {
	// Groups 是按优先级升序、已通过生图开关与分组模型白名单筛选的链上分组。
	Groups []*Group
	// Mappings 是各分组自己的渠道映射；命中后组时必须用它转发与计费，
	// 不能继续沿用首组解析出的映射。
	Mappings map[int64]ChannelMappingResult
	// Capabilities 记录各分组自己的生图能力要求（渠道映射可能把模型改写成
	// 仅 APIKey 账号可用的形态）。
	Capabilities map[int64]OpenAIImagesCapability
}

// MappingFor 返回该分组预解析的渠道映射；未预解析时返回零值。
func (p ImageGenerationGroupPolicy) MappingFor(groupID int64) (ChannelMappingResult, bool) {
	if p.Mappings == nil {
		return ChannelMappingResult{}, false
	}
	mapping, ok := p.Mappings[groupID]
	return mapping, ok
}

// CapabilityFor 返回该分组应使用的生图能力要求；未预解析时回退调用方传入值。
func (p ImageGenerationGroupPolicy) CapabilityFor(groupID int64, fallback OpenAIImagesCapability) OpenAIImagesCapability {
	if capability, ok := p.Capabilities[groupID]; ok && capability != "" {
		return capability
	}
	return fallback
}

type imageGenerationGroupPolicyContextKey struct{}

// WithImageGenerationGroupPolicy 把请求级生图分组策略写入 context。
func WithImageGenerationGroupPolicy(ctx context.Context, policy ImageGenerationGroupPolicy) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, imageGenerationGroupPolicyContextKey{}, policy)
}

// ImageGenerationGroupPolicyFromContext 读取请求级生图分组策略。
func ImageGenerationGroupPolicyFromContext(ctx context.Context) (ImageGenerationGroupPolicy, bool) {
	if ctx == nil {
		return ImageGenerationGroupPolicy{}, false
	}
	policy, ok := ctx.Value(imageGenerationGroupPolicyContextKey{}).(ImageGenerationGroupPolicy)
	return policy, ok
}

// APIKeyGroupRoutingChain 返回该 Key 真正会用于请求级兜底的分组链（按优先级升序）。
//
// 校验条件与 WithAPIKeyGroupFallbackRouting 保持一致，避免“准入阶段认为链可用、
// 调度阶段却按普通密钥处理”的不一致。非链路密钥返回 nil。
func APIKeyGroupRoutingChain(apiKey *APIKey) []*Group {
	chain := apiKeyRoutingChain(apiKey)
	if len(chain) < 2 {
		return nil
	}
	primary := chain[0].Group
	if primary == nil || apiKey == nil || apiKey.GroupID == nil || *apiKey.GroupID != primary.ID {
		return nil
	}
	if !primary.IsActive() || !IsGroupContextValid(primary) {
		return nil
	}
	groups := make([]*Group, 0, len(chain))
	groups = append(groups, primary)
	for _, entry := range chain[1:] {
		if entry.Group == nil || !entry.Group.IsActive() || !IsGroupContextValid(entry.Group) {
			return nil
		}
		// 网关的分发与协议转换在中间件阶段已按主分组平台确定，跨平台链不可路由。
		if entry.Group.Platform != primary.Platform {
			return nil
		}
		groups = append(groups, entry.Group)
	}
	return groups
}

// ChainAllowsImageGeneration 报告聚合/兜底链上是否至少有一个分组允许生图。
//
// 非链路密钥沿用单分组语义；未绑定分组的密钥保持既有“未分组即放行”行为。
func ChainAllowsImageGeneration(apiKey *APIKey) bool {
	groups := APIKeyGroupRoutingChain(apiKey)
	if len(groups) == 0 {
		return GroupAllowsImageGeneration(apiKeyGroup(apiKey))
	}
	for _, group := range groups {
		if GroupAllowsImageGeneration(group) {
			return true
		}
	}
	return false
}

// ChainModelAllowlistAllows 报告聚合/兜底链上是否至少有一个分组允许该模型。
//
// 只用于入口候选判断（避免首组白名单提前堵死后组支持的模型）。链上各分组仍是
// 各自白名单的唯一裁决者：真正执行请求的分组必须自己允许该模型。生图模型额外
// 要求该分组开启生图，否则“白名单允许但组禁止生图”会被误判为可执行。
func ChainModelAllowlistAllows(apiKey *APIKey, model string) bool {
	groups := APIKeyGroupRoutingChain(apiKey)
	if len(groups) == 0 {
		group := apiKeyGroup(apiKey)
		if group == nil {
			return true
		}
		if !GroupAllowsImageGenerationModel(group, model) {
			return false
		}
		return !group.ModelAllowlistEnabled() || group.ModelAllowlist.Allows(model)
	}
	for _, group := range groups {
		if !GroupAllowsImageGenerationModel(group, model) {
			continue
		}
		if !group.ModelAllowlistEnabled() || group.ModelAllowlist.Allows(model) {
			return true
		}
	}
	return false
}

// ResolveImageGenerationGroupPolicy 在聚合/兜底链上按优先级筛选可执行本次生图请求的分组。
//
// publicModel 用于分组自己的模型白名单判定（与入口白名单一致，只看客户端书写的
// 模型名）。prepare 可选，用于为每个候选分组预解析渠道映射与生图能力；
// 它必须无副作用，且只在该分组真正进入候选时才调用。
//
// 返回的第二个值为 false 表示该 Key 不是可用链路（普通密钥或链路失效），调用方
// 必须沿用原有单分组语义。
func ResolveImageGenerationGroupPolicy(
	apiKey *APIKey,
	publicModel string,
	prepare func(*Group) (ChannelMappingResult, OpenAIImagesCapability),
) (ImageGenerationGroupPolicy, bool) {
	groups := APIKeyGroupRoutingChain(apiKey)
	if len(groups) == 0 {
		return ImageGenerationGroupPolicy{}, false
	}
	model := strings.TrimSpace(publicModel)
	policy := ImageGenerationGroupPolicy{Groups: make([]*Group, 0, len(groups))}
	for _, group := range groups {
		if !GroupAllowsImageGeneration(group) {
			continue
		}
		// 白名单并集只用于入口候选判断：候选分组必须自己允许该模型，
		// 才能在它里面执行；否则跳过，绝不借其他组的权限代跑。
		if group.ModelAllowlistEnabled() && !group.ModelAllowlist.Allows(model) {
			continue
		}
		policy.Groups = append(policy.Groups, group)
		if prepare == nil {
			continue
		}
		mapping, capability := prepare(group)
		if policy.Mappings == nil {
			policy.Mappings = make(map[int64]ChannelMappingResult, len(groups))
		}
		policy.Mappings[group.ID] = mapping
		if capability == "" {
			continue
		}
		if policy.Capabilities == nil {
			policy.Capabilities = make(map[int64]OpenAIImagesCapability, len(groups))
		}
		policy.Capabilities[group.ID] = capability
	}
	return policy, true
}

// apiKeyImagePolicyChainStep 返回请求级生图策略内、当前分组之后的下一个候选分组。
//
// currentGroupID 为 nil 或不在策略内时从策略首位开始；策略已走完返回 false。
// 该函数是纯请求级计算，不读写共享状态。
func apiKeyImagePolicyChainStep(policy ImageGenerationGroupPolicy, currentGroupID *int64) (*Group, bool) {
	if len(policy.Groups) == 0 {
		return nil, false
	}
	start := 0
	if currentGroupID != nil && *currentGroupID > 0 {
		for i, group := range policy.Groups {
			if group != nil && group.ID == *currentGroupID {
				start = i + 1
				break
			}
		}
	}
	for _, group := range policy.Groups[start:] {
		if group != nil && group.ID > 0 && group.IsActive() {
			return group, true
		}
	}
	return nil, false
}

// imagePolicyCandidateGroups 返回策略内可尝试的分组序列（含主分组，按优先级）。
func imagePolicyCandidateGroups(policy ImageGenerationGroupPolicy) []*Group {
	out := make([]*Group, 0, len(policy.Groups))
	for _, group := range policy.Groups {
		if group != nil && group.ID > 0 && group.IsActive() {
			out = append(out, group)
		}
	}
	return out
}

// GroupAllowsImageGenerationModel 报告分组是否允许把该模型用于生图。
//
// 目录展示与入口准入共用该判定：生图开关关闭的分组不得把生图模型宣称为可调用，
// 也不能让请求借它的账号执行生图。非生图模型恒为 true。
func GroupAllowsImageGenerationModel(group *Group, model string) bool {
	if !isOpenAIImageGenerationModel(model) {
		return true
	}
	return GroupAllowsImageGeneration(group)
}

// AggregateImageAdmissionAllowed 报告聚合/兜底链是否应当放行这次生图请求的入口准入。
//
// 分组模型白名单中间件挂在 handler 之前，只看链首主分组会提前堵死“仅后组允许”
// 的生图模型。这里用与调度一致的候选规则（允许生图 + 自己的白名单允许该模型）
// 判断链上是否存在可执行分组；它只决定入口是否放行，最终执行分组仍由各自的
// 白名单与生图开关裁决，因此不会绕过任何分组策略。
//
// 生效范围严格限定为生图入口：/v1/images/*（含 /async）与显式生图的 /v1/responses。
// 其他入口（例如 chat/completions 携带图片模型名）保持原有首组白名单语义。
func AggregateImageAdmissionAllowed(apiKey *APIKey, endpoint, model string, body []byte) bool {
	if apiKey == nil || len(apiKey.Groups) == 0 {
		return false
	}
	if !isImageGenerationAdmissionEndpoint(endpoint, model, body) {
		return false
	}
	policy, ok := ResolveImageGenerationGroupPolicy(apiKey, model, nil)
	if !ok {
		// 非链路密钥：沿用单分组语义，由调用方按主分组白名单裁决。
		return false
	}
	return len(policy.Groups) > 0
}

// isImageGenerationAdmissionEndpoint 判断入口是否属于“生图请求”。
func isImageGenerationAdmissionEndpoint(endpoint, model string, body []byte) bool {
	path := normalizeImageGenerationEndpoint(endpoint)
	if IsImageGenerationEndpoint(path) || strings.HasPrefix(path, "/v1/images/") || strings.HasPrefix(path, "/images/") {
		return true
	}
	if path != openAIResponsesEndpoint {
		return false
	}
	return IsExplicitImageGenerationIntent(path, model, body)
}
