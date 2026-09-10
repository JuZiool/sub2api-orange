//go:build unit

package service

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestCodexQuotaOverdraftSchedulingHooksPreserveThresholdAndNotifierBoundaries(t *testing.T) {
	t.Cleanup(func() { SetCodexQuotaOverdraftEnabled(false) })
	SetCodexQuotaOverdraftEnabled(true)

	settingsRepo := newMockSettingRepo()
	settingsRepo.data[SettingKeyAccountSchedulingThresholds] = `{"openai":80}`
	accountRepo := &rateLimitAccountRepoStub{}
	runtimeBlocker := &runtimeBlockRecorder{}
	rl := NewRateLimitService(accountRepo, nil, &config.Config{}, nil, nil)
	rl.SetSettingService(NewSettingService(settingsRepo, &config.Config{}))
	rl.SetAccountRuntimeBlocker(runtimeBlocker)
	installCodexQuotaOverdraftSchedulingHooks(rl)

	now := time.Now().UTC().Add(time.Hour)
	account := &Account{
		ID:          9104,
		Platform:    PlatformOpenAI,
		Type:        AccountTypeOAuth,
		Status:      StatusActive,
		Schedulable: true,
		Extra: map[string]any{
			"codex_7d_used_percent": 100,
			"codex_7d_reset_at":     now.Format(time.RFC3339),
		},
	}
	ctx := WithCodexQuotaOverdraftScheduling(context.Background())
	require.False(t, rl.ApplyAccountSchedulingThreshold(ctx, account))
	require.Empty(t, runtimeBlocker.reasons, "透支阈值旁路不能通过 notifier 写普通 runtime block")

	normalAccount := &Account{ID: 9105, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true}
	require.False(t, rl.ApplyAccountSchedulingThreshold(context.Background(), normalAccount))
}
