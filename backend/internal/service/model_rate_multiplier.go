package service

import (
	"fmt"
	"math"
	"strings"
)

const maxModelRateMultiplier = 1000.0

// NormalizeModelRateMultiplierRules validates and preserves administrator order.
func NormalizeModelRateMultiplierRules(rules []ModelRateMultiplierRule) ([]ModelRateMultiplierRule, error) {
	if len(rules) == 0 {
		return nil, nil
	}
	out := make([]ModelRateMultiplierRule, 0, len(rules))
	seen := make(map[string]struct{}, len(rules))
	for _, rule := range rules {
		model := strings.TrimSpace(rule.Model)
		if model == "" {
			return nil, fmt.Errorf("model_rate_multipliers model must not be empty")
		}
		if strings.Contains(model, "*") {
			return nil, fmt.Errorf("model_rate_multipliers only supports exact model names")
		}
		if math.IsNaN(rule.Multiplier) || math.IsInf(rule.Multiplier, 0) || rule.Multiplier <= 0 || rule.Multiplier > maxModelRateMultiplier {
			return nil, fmt.Errorf("model_rate_multipliers multiplier must be > 0 and <= %g", maxModelRateMultiplier)
		}
		if _, ok := seen[model]; ok {
			return nil, fmt.Errorf("duplicate model_rate_multipliers model: %s", model)
		}
		seen[model] = struct{}{}
		out = append(out, ModelRateMultiplierRule{Model: model, Multiplier: rule.Multiplier})
	}
	return out, nil
}

type RateResolution struct {
	RequestedModel string
	Multiplier     float64
	MatchedModel   string
	Source         string
}

func ResolveModelRateMultiplier(model string, rules []ModelRateMultiplierRule) (float64, string, bool) {
	model = strings.TrimSpace(model)
	for _, rule := range rules {
		if rule.Model == model {
			return rule.Multiplier, rule.Model, true
		}
	}
	return 0, "", false
}

func FormatModelRateMultiplierRules(rules []ModelRateMultiplierRule) string {
	parts := make([]string, 0, len(rules))
	for _, rule := range rules {
		parts = append(parts, fmt.Sprintf("%s: %.4gx", rule.Model, rule.Multiplier))
	}
	return strings.Join(parts, "；")
}
