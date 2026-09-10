//go:build unit

package service

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestOpenAIRuntimeBlock_PersistedCooldownKeepsIndependentOwner(t *testing.T) {
	svc := &OpenAIGatewayService{}
	account := &Account{ID: 9101, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	independentUntil := time.Now().Add(20 * time.Minute).UTC()
	persistedUntil := time.Now().Add(10 * time.Minute).UTC()

	svc.BlockAccountScheduling(account, independentUntil, "oauth_401")
	svc.BlockAccountSchedulingFromPersistedCooldown(account, persistedUntil, codexQuotaOverdraftPauseSource)
	require.True(t, svc.isOpenAIAccountRuntimeBlocked(account))

	svc.ClearAccountSchedulingBlockFromPersistedCooldown(account.ID, persistedUntil)
	require.True(t, svc.isOpenAIAccountRuntimeBlocked(account), "independent owner must remain after persisted recovery")
	raw, ok := svc.openaiAccountRuntimeBlockSources.Load(account.ID)
	require.True(t, ok)
	sources, ok := raw.(openAIAccountRuntimeBlockSources)
	require.True(t, ok)
	require.False(t, sources.hasPersisted)
	require.True(t, sources.hasIndependent)
}

func TestOpenAIRuntimeBlock_PersistedCleanupRejectsChangedDeadline(t *testing.T) {
	svc := &OpenAIGatewayService{}
	account := &Account{ID: 9102, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	persistedUntil := time.Now().Add(10 * time.Minute).UTC()
	newerUntil := persistedUntil.Add(time.Minute)

	svc.BlockAccountSchedulingFromPersistedCooldown(account, persistedUntil, codexQuotaOverdraftPauseSource)
	svc.BlockAccountSchedulingFromPersistedCooldown(account, newerUntil, codexQuotaOverdraftPauseSource)
	svc.ClearAccountSchedulingBlockFromPersistedCooldown(account.ID, persistedUntil)
	require.True(t, svc.isOpenAIAccountRuntimeBlocked(account), "stale cleanup must not remove newer persisted owner")
}

func TestOpenAIRuntimeBlock_PersistedNotificationFallsBackToLegacyBlocker(t *testing.T) {
	blocker := &runtimeBlockRecorder{}
	account := &Account{ID: 9103, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	until := time.Now().Add(time.Minute)

	notifyPersistedAccountSchedulingCooldown(blocker, account, until, codexQuotaOverdraftPauseSource)
	require.Equal(t, 1, len(blocker.accounts))
	require.Equal(t, codexQuotaOverdraftPauseSource, blocker.reasons[0])

	clearPersistedAccountSchedulingCooldown(blocker, account.ID, until)
	require.Equal(t, []int64{account.ID}, blocker.clearedIDs)
}
