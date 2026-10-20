package service

import "maps"

// Orange 特有：仅在普通新建路径补齐配置，历史账号、复制及完整配置导入保留原语义。
// 调用方仅传入无父账号的新建请求，影子账号不经过此函数。
func applyOpenAIAccountCreateDefaults(platform, accountType string, extra map[string]any) map[string]any {
	if platform != PlatformOpenAI {
		return extra
	}
	defaults := map[string]any{
		"auto_pause_5h_disabled": true,
		"auto_pause_7d_disabled": true,
	}
	if accountType == AccountTypeOAuth {
		defaults[OpenAIAutoResetCreditEnabledExtraKey] = false
		defaults[OpenAIAutoResetCredit5hDisabledExtraKey] = true
		defaults[OpenAIAutoResetCredit5hThresholdExtraKey] = openAIAutoResetCreditDefaultThreshold
		defaults[OpenAIAutoResetCredit7dThresholdExtraKey] = openAIAutoResetCreditDefaultThreshold
	}
	prepared := maps.Clone(extra)
	if prepared == nil {
		prepared = make(map[string]any, len(defaults))
	}
	for key, value := range defaults {
		if _, exists := prepared[key]; !exists {
			prepared[key] = value
		}
	}
	return prepared
}

func resolveAccountAutoPauseOnExpiredForCreate(platform string, configured *bool, skipOpenAIAccountDefaults bool) bool {
	if configured != nil {
		return *configured
	}
	return platform != PlatformOpenAI || skipOpenAIAccountDefaults
}
