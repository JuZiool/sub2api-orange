package service

import "strings"

func normalizeGroupModelsListConfig(cfg GroupModelsListConfig) GroupModelsListConfig {
	out := GroupModelsListConfig{Enabled: cfg.Enabled}
	if len(cfg.Models) > 0 {
		seen := make(map[string]struct{}, len(cfg.Models))
		out.Models = make([]string, 0, len(cfg.Models))
		for _, model := range cfg.Models {
			model = strings.TrimSpace(model)
			if model == "" {
				continue
			}
			if _, ok := seen[model]; ok {
				continue
			}
			seen[model] = struct{}{}
			out.Models = append(out.Models, model)
		}
		if len(out.Models) == 0 {
			out.Models = nil
		}
	}
	if len(cfg.HiddenModels) > 0 {
		seen := make(map[string]struct{}, len(cfg.HiddenModels))
		out.HiddenModels = make([]string, 0, len(cfg.HiddenModels))
		for _, model := range cfg.HiddenModels {
			model = strings.TrimSpace(model)
			if model == "" {
				continue
			}
			if _, ok := seen[model]; ok {
				continue
			}
			seen[model] = struct{}{}
			out.HiddenModels = append(out.HiddenModels, model)
		}
		if len(out.HiddenModels) == 0 {
			out.HiddenModels = nil
		}
	}
	return out
}

// IsGroupModelHidden supports exact names and a trailing-* prefix rule. Gemini
// callers may use either "foo" or "models/foo"; both forms are equivalent.
func IsGroupModelHidden(cfg GroupModelsListConfig, model string) bool {
	model = strings.TrimSpace(model)
	if model == "" {
		return false
	}
	normalizedModel := strings.TrimPrefix(model, "models/")
	for _, rawRule := range cfg.HiddenModels {
		rule := strings.TrimSpace(rawRule)
		prefix := strings.TrimSuffix(rule, "*")
		if strings.HasSuffix(rule, "*") {
			if strings.HasPrefix(model, prefix) || strings.HasPrefix(normalizedModel, strings.TrimPrefix(prefix, "models/")) {
				return true
			}
			continue
		}
		if strings.TrimPrefix(rule, "models/") == normalizedModel {
			return true
		}
	}
	return false
}

func FilterGroupHiddenModels(cfg GroupModelsListConfig, models []string) []string {
	filtered := make([]string, 0, len(models))
	for _, model := range models {
		if !IsGroupModelHidden(cfg, model) {
			filtered = append(filtered, model)
		}
	}
	return filtered
}

func (g *Group) CustomModelsListEnabled() bool {
	return g != nil && g.ModelsListConfig.Enabled && len(g.ModelsListConfig.Models) > 0
}
