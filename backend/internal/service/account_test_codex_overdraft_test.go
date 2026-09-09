//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestAccountTestCodexQuotaOverdraftRequestGuards(t *testing.T) {
	t.Cleanup(func() { SetCodexQuotaOverdraftEnabled(false) })
	SetCodexQuotaOverdraftEnabled(true)
	payload := []byte(`{"model":"gpt-5.4","input":[{"type":"message","role":"user"}]}`)
	cfg := &config.Config{Gateway: config.GatewayConfig{CodexQuotaOverdraftEnabled: true}}
	svc := &AccountTestService{cfg: cfg}

	for _, test := range []struct {
		name    string
		account *Account
		want    bool
	}{
		{
			name:    "oauth without coordinator retains old behavior",
			account: &Account{ID: 11, Platform: PlatformOpenAI, Type: AccountTypeOAuth},
			want:    false,
		},
		{
			name:    "disabled account skips injection",
			account: &Account{ID: 12, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Extra: map[string]any{CodexQuotaOverdraftDisabledExtraKey: true}},
			want:    false,
		},
		{
			name:    "api key skips injection",
			account: &Account{ID: 13, Platform: PlatformOpenAI, Type: AccountTypeAPIKey},
			want:    false,
		},
		{
			name:    "shadow skips injection",
			account: &Account{ID: 14, Platform: PlatformOpenAI, Type: AccountTypeOAuth, ParentAccountID: int64PtrForCodexQuotaOverdraftTest(1)},
			want:    false,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			svc.codexQuotaOverdraft = nil
			if test.name == "oauth without coordinator retains old behavior" {
				ctx, updated, injected := svc.prepareCodexQuotaOverdraftTestRequest(context.Background(), test.account, payload)
				require.False(t, injected)
				require.Equal(t, string(payload), string(updated))
				require.False(t, CodexQuotaOverdraftSchedulingEnabled(ctx))
				return
			}

			svc.SetCodexQuotaOverdraftCoordinator(&CodexQuotaOverdraftCoordinator{cfg: cfg})
			ctx, updated, injected := svc.prepareCodexQuotaOverdraftTestRequest(context.Background(), test.account, payload)
			require.Equal(t, test.want, injected)
			if test.want {
				require.NotEqual(t, string(payload), string(updated))
				require.True(t, CodexQuotaOverdraftSchedulingEnabled(ctx))
			} else {
				require.Equal(t, string(payload), string(updated))
				require.False(t, CodexQuotaOverdraftSchedulingEnabled(ctx))
			}
		})
	}
}

func TestAccountTestCodexQuotaOverdraftRequestFailOpen(t *testing.T) {
	t.Cleanup(func() { SetCodexQuotaOverdraftEnabled(false) })
	SetCodexQuotaOverdraftEnabled(true)
	svc := &AccountTestService{
		cfg:                 &config.Config{Gateway: config.GatewayConfig{CodexQuotaOverdraftEnabled: true}},
		codexQuotaOverdraft: &CodexQuotaOverdraftCoordinator{},
	}
	account := &Account{ID: 15, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	invalid := []byte(`{"input":`)

	ctx, updated, injected := svc.prepareCodexQuotaOverdraftTestRequest(context.Background(), account, invalid)
	require.False(t, injected)
	require.Equal(t, string(invalid), string(updated))
	require.True(t, CodexQuotaOverdraftSchedulingEnabled(ctx), "eligible endpoint remains marked even when payload mutation fails open")

	_, unchanged, injected := (&AccountTestService{}).prepareCodexQuotaOverdraftTestRequest(context.Background(), account, invalid)
	require.False(t, injected)
	require.Equal(t, string(invalid), string(unchanged), "nil coordinator construction must retain old behavior")
}
