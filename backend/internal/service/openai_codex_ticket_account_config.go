package service

import (
	"context"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/config"
)

// Orange 特有：Codex 打票账号级开关与套餐识别。
// 参考 参考项目/sub2api-custom（f0b2c0dce）的账号策略，并按 Orange 方案修正
// custom「缺 key 默认启用」的缺陷：Orange 缺 key 即 false（显式 opt-in），
// 避免打开总开关瞬间让全部存量 OAuth/Setup Token 账号参与打票。
const (
	// OpenAICodexTicketEnabledExtraKey 账号级打票开关；缺失即 false。
	OpenAICodexTicketEnabledExtraKey = "codex_ticket_enabled"
	// OpenAICodexTicketFailClosedExtraKey 账号级 fail_closed 覆盖；缺失继承全局。
	OpenAICodexTicketFailClosedExtraKey = "codex_ticket_fail_closed"
	// OpenAICodexTicketHarvestProxyIDsExtraKey 仅用于丢弃已废弃的账号级打票代理池设置。
	OpenAICodexTicketHarvestProxyIDsExtraKey = "codex_ticket_harvest_proxy_ids"
)

// OpenAICodexTicketAccountConfig 暴露开关与期望票据长度，绝不包含票据材料。
// Enabled 是生效策略；AccountEnabled 保留账号自身选择（便于总开关关闭时回显）。
type OpenAICodexTicketAccountConfig struct {
	GatewayEnabled bool `json:"gateway_enabled"`
	AccountEnabled bool `json:"account_enabled"`
	Enabled        bool `json:"enabled"`
	FailClosed     bool `json:"fail_closed"`
	TargetLength   int  `json:"target_length"`
}

// ResolveOpenAICodexTicketAccountConfig 解析账号级打票策略。
// 与 custom 不同：缺失 codex_ticket_enabled 时按 false（显式 opt-in）。
func ResolveOpenAICodexTicketAccountConfig(account *Account, fallback config.OpenAICodexTicketConfig) OpenAICodexTicketAccountConfig {
	policy := OpenAICodexTicketAccountConfig{
		GatewayEnabled: fallback.Enabled,
		FailClosed:     fallback.FailClosed,
		TargetLength:   openAICodexTicketTargetLength(account, fallback),
	}
	if !isOpenAICodexTicketAccount(account) {
		return policy
	}
	if raw, ok := account.Extra[OpenAICodexTicketEnabledExtraKey]; ok {
		if enabled, ok := raw.(bool); ok {
			policy.AccountEnabled = enabled
		}
	}
	if value, ok := account.Extra[OpenAICodexTicketFailClosedExtraKey].(bool); ok {
		policy.FailClosed = value
	}
	policy.Enabled = policy.GatewayEnabled && policy.AccountEnabled
	return policy
}

// openAICodexTicketTargetLength 按套餐选择票据长度：Pro 292，Team/Business 332。
// 未知套餐回退配置默认值，不做猜测。
func openAICodexTicketTargetLength(account *Account, fallback config.OpenAICodexTicketConfig) int {
	plan := normalizedOpenAICodexTicketPlan(account)
	switch plan {
	case "team", "chatgptteam", "business", "chatgptbusiness":
		return 332
	case "pro", "chatgptpro", "prolite", "chatgptprolite":
		return 292
	}
	if strings.HasPrefix(plan, "selfservebusiness") {
		return 332
	}
	if fallback.TargetLength > 0 {
		return fallback.TargetLength
	}
	return 292
}

// codexTicketPlanKnown 报告账号套餐是否已识别（供前端对未知套餐告警）。
func codexTicketPlanKnown(account *Account) bool {
	plan := normalizedOpenAICodexTicketPlan(account)
	switch plan {
	case "team", "chatgptteam", "business", "chatgptbusiness",
		"pro", "chatgptpro", "prolite", "chatgptprolite":
		return true
	}
	return strings.HasPrefix(plan, "selfservebusiness")
}

// normalizedOpenAICodexTicketPlan 归一化 plan_type / chatgpt_plan_type：
// 去下划线/连字符/空格并转小写。
func normalizedOpenAICodexTicketPlan(account *Account) string {
	if account == nil {
		return ""
	}
	plan := strings.TrimSpace(account.GetCredential("plan_type"))
	if plan == "" {
		plan = account.GetCredential("chatgpt_plan_type")
	}
	return strings.NewReplacer("_", "", "-", "", " ", "").Replace(strings.ToLower(strings.TrimSpace(plan)))
}

// openAICodexTicketAccountConfig 返回账号生效策略（总开关取后台设置，可热更新）。
func (s *OpenAIGatewayService) openAICodexTicketAccountConfig(ctx context.Context, account *Account) OpenAICodexTicketAccountConfig {
	cfg := s.openAICodexTicketConfig()
	cfg.Enabled = s.openAICodexTicketEnabledContext(ctx)
	return ResolveOpenAICodexTicketAccountConfig(account, cfg)
}
