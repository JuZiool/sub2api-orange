package service

import (
	"context"
	"strings"
)

// rateMultiplierInput 是倍率覆盖扩展点的入参，携带官方解析结果与请求上下文。
type rateMultiplierInput struct {
	Input   *OpenAIRecordUsageInput
	APIKey  *APIKey
	User    *User
	Result  *OpenAIForwardResult
	Current float64
}

// SetRateMultiplierOverride 注入可选的倍率覆盖实现。nil 表示使用官方默认行为。
func (s *OpenAIGatewayService) SetRateMultiplierOverride(fn func(ctx context.Context, in rateMultiplierInput) float64) {
	if s != nil {
		s.rateMultiplierOverride = fn
	}
}

// resolveModelRateMultiplierOverride 实现 Orange 的倍率优先级：
// 请求开始时刻冻结的快照 > 模型专属倍率 > 用户分组倍率 > 分组倍率 > 系统默认。
func resolveModelRateMultiplierOverride(_ context.Context, in rateMultiplierInput) float64 {
	if in.Input != nil && in.Input.RateResolution != nil {
		return in.Input.RateResolution.Multiplier
	}
	multiplier := in.Current
	if in.APIKey == nil || in.APIKey.Group == nil {
		return multiplier
	}
	requestedModel := ""
	if in.Input != nil {
		requestedModel = strings.TrimSpace(in.Input.OriginalModel)
	}
	if requestedModel == "" && in.Result != nil {
		requestedModel = strings.TrimSpace(in.Result.Model)
	}
	if modelMultiplier, _, matched := ResolveModelRateMultiplier(requestedModel, in.APIKey.Group.ModelRateMultipliers); matched {
		return modelMultiplier
	}
	return multiplier
}
