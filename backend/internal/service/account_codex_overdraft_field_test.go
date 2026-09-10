//go:build unit

package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCodexQuotaOverdraftAccountFieldSemanticsRemainOrangeCompatible(t *testing.T) {
	cases := []struct {
		name    string
		extra   map[string]any
		enabled bool
	}{
		{name: "missing inherits enabled", extra: nil, enabled: true},
		{name: "orange disabled bool", extra: map[string]any{CodexQuotaOverdraftDisabledExtraKey: true}, enabled: false},
		{name: "orange enabled bool", extra: map[string]any{CodexQuotaOverdraftDisabledExtraKey: false}, enabled: true},
		{name: "invalid value fails open", extra: map[string]any{CodexQuotaOverdraftDisabledExtraKey: 123}, enabled: true},
		{name: "invalid string fails open", extra: map[string]any{CodexQuotaOverdraftDisabledExtraKey: "not-a-bool"}, enabled: true},
		{name: "legacy HT field is ignored without migration data", extra: map[string]any{"codex_quota_overdraft_enabled": false}, enabled: true},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			account := &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth, Extra: test.extra}
			require.Equal(t, test.enabled, account.ResolveCodexQuotaOverdraftEnabled(true))
		})
	}
}
