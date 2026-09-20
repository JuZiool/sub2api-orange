package service

import (
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

// Orange 特有：账号级打票开关默认关闭（显式 opt-in），修正 custom 缺 key 即启用的缺陷。
func TestOrangeCodexTicketAccountPolicyDefaultsOff(t *testing.T) {
	cfg := config.OpenAICodexTicketConfig{Enabled: true, FailClosed: true, TargetLength: 292}

	// 存量账号（无 codex_ticket_enabled）不得参与，即使总开关打开。
	legacy := &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	policy := ResolveOpenAICodexTicketAccountConfig(legacy, cfg)
	require.False(t, policy.AccountEnabled)
	require.False(t, policy.Enabled)

	// 显式 opt-in 后才参与。
	opted := &Account{ID: 2, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Extra: map[string]any{OpenAICodexTicketEnabledExtraKey: true}}
	policy = ResolveOpenAICodexTicketAccountConfig(opted, cfg)
	require.True(t, policy.AccountEnabled)
	require.True(t, policy.Enabled)

	// 总开关关闭时即使账号 opt-in 也不生效。
	policy = ResolveOpenAICodexTicketAccountConfig(opted, config.OpenAICodexTicketConfig{Enabled: false, TargetLength: 292})
	require.True(t, policy.AccountEnabled)
	require.False(t, policy.Enabled)
}

// Orange 特有：账号级 fail_closed 覆盖全局。
func TestOrangeCodexTicketAccountPolicyFailClosedOverride(t *testing.T) {
	cfg := config.OpenAICodexTicketConfig{Enabled: true, FailClosed: true, TargetLength: 292}
	account := &Account{ID: 3, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Extra: map[string]any{
		OpenAICodexTicketEnabledExtraKey:    true,
		OpenAICodexTicketFailClosedExtraKey: false,
	}}
	policy := ResolveOpenAICodexTicketAccountConfig(account, cfg)
	require.False(t, policy.FailClosed)
}

// Orange 特有：按套餐识别票据长度，Pro 292 / Team、Business 332，未知回退默认值。
func TestOrangeCodexTicketTargetLengthByPlan(t *testing.T) {
	fallback := config.OpenAICodexTicketConfig{TargetLength: 292}
	cases := []struct {
		plan  string
		want  int
		known bool
	}{
		{"pro", 292, true},
		{"chatgpt_pro", 292, true},
		{"prolite", 292, true},
		{"team", 332, true},
		{"chatgpt_team", 332, true},
		{"business", 332, true},
		{"self_serve_business_2024", 332, true},
		{"enterprise", 292, false},
		{"", 292, false},
	}
	for _, tc := range cases {
		account := &Account{ID: 4, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{"plan_type": tc.plan}}
		require.Equal(t, tc.want, openAICodexTicketTargetLength(account, fallback), "plan=%q", tc.plan)
		require.Equal(t, tc.known, codexTicketPlanKnown(account), "plan=%q", tc.plan)
	}

	// chatgpt_plan_type 作为 plan_type 缺失时的兜底。
	account := &Account{ID: 5, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{"chatgpt_plan_type": "Team"}}
	require.Equal(t, 332, openAICodexTicketTargetLength(account, fallback))

	// 配置默认长度作为未知套餐的兜底。
	account = &Account{ID: 6, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	require.Equal(t, 332, openAICodexTicketTargetLength(account, config.OpenAICodexTicketConfig{TargetLength: 332}))
}

// Orange 特有：状态接口暴露 target_length / plan_known，未知套餐可被前端告警。
func TestOrangeCodexTicketStatusesExposePlan(t *testing.T) {
	account := &Account{
		ID: 7, Platform: PlatformOpenAI, Type: AccountTypeOAuth,
		Credentials: map[string]any{"plan_type": "team"},
		Extra:       map[string]any{OpenAICodexTicketEnabledExtraKey: true},
	}
	cfg := config.OpenAICodexTicketConfig{Enabled: true, Models: []string{"gpt-6-astra"}, FailClosed: true}
	statuses := OpenAICodexTicketStatuses(account, cfg, time.Now())
	require.Len(t, statuses, 1)
	require.Equal(t, 332, statuses[0].TargetLength)
	require.True(t, statuses[0].PlanKnown)

	unknown := &Account{
		ID: 8, Platform: PlatformOpenAI, Type: AccountTypeOAuth,
		Extra: map[string]any{OpenAICodexTicketEnabledExtraKey: true},
	}
	statuses = OpenAICodexTicketStatuses(unknown, cfg, time.Now())
	require.Len(t, statuses, 1)
	require.Equal(t, 292, statuses[0].TargetLength)
	require.False(t, statuses[0].PlanKnown)
}
