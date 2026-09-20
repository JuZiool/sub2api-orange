//go:build unit

package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestAdminCodexTicketCreateInitializesExplicitPolicy(t *testing.T) {
	account, err := buildAccountForCreate(&CreateAccountInput{Platform: PlatformOpenAI, Type: AccountTypeOAuth}, nil)
	require.NoError(t, err)
	require.Equal(t, false, account.Extra[OpenAICodexTicketEnabledExtraKey])
	require.Equal(t, true, account.Extra[OpenAICodexTicketFailClosedExtraKey])
	require.NotContains(t, account.Extra, OpenAICodexTicketHarvestProxyIDsExtraKey)
}

func TestAdminCodexTicketRejectsMalformedPolicy(t *testing.T) {
	for _, extra := range []map[string]any{
		{OpenAICodexTicketEnabledExtraKey: "true"},
		{OpenAICodexTicketFailClosedExtraKey: nil},
	} {
		_, err := normalizeOpenAICodexTicketAccountExtra(PlatformOpenAI, extra, false)
		require.Error(t, err, "extra=%v", extra)
	}
}

func TestAdminCodexTicketLegacyProxyIDsAreIgnored(t *testing.T) {
	for _, raw := range []any{nil, "1,2", []any{0, -1, "1"}, []int64{404}} {
		extra := map[string]any{OpenAICodexTicketHarvestProxyIDsExtraKey: raw, OpenAICodexTicketEnabledExtraKey: true}
		normalized, err := normalizeOpenAICodexTicketAccountExtra(PlatformOpenAI, extra, false)
		require.NoError(t, err)
		require.NotContains(t, normalized, OpenAICodexTicketHarvestProxyIDsExtraKey)
		require.Equal(t, true, normalized[OpenAICodexTicketEnabledExtraKey])
		require.Contains(t, extra, OpenAICodexTicketHarvestProxyIDsExtraKey, "normalization must not mutate caller data")
	}
}

func TestAdminCodexTicketUpdatePreservesPolicyAndBusinessProxy(t *testing.T) {
	ctx := context.Background()
	proxyID := int64(17)
	ticket := &openAICodexTicket{Model: "gpt-6-astra", State: fakeCodexTicketState(292), Length: 292, ExpiresAt: time.Now().Add(time.Hour)}
	account := ticketTestAccount(41)
	account.ProxyID = &proxyID
	account.ProxyIDs = []int64{17, 19}
	account.Extra = map[string]any{
		OpenAICodexTicketEnabledExtraKey:         true,
		OpenAICodexTicketFailClosedExtraKey:      false,
		OpenAICodexTicketHarvestProxyIDsExtraKey: []int64{99},
		openAICodexTicketExtraKey(ticket.Model):  ticket,
	}
	repo := &upstreamBillingProbeAccountRepo{accounts: map[int64]*Account{41: account}}
	svc := &adminServiceImpl{accountRepo: repo}
	updated, err := svc.UpdateAccount(ctx, 41, &UpdateAccountInput{Name: "renamed"})
	require.NoError(t, err)
	require.NotContains(t, updated.Extra, OpenAICodexTicketHarvestProxyIDsExtraKey)
	updated.Extra[OpenAICodexTicketHarvestProxyIDsExtraKey] = []int64{99}
	updated, err = svc.UpdateAccount(ctx, 41, &UpdateAccountInput{Extra: map[string]any{"custom": true}})
	require.NoError(t, err)
	require.Equal(t, true, updated.Extra[OpenAICodexTicketEnabledExtraKey])
	require.Equal(t, false, updated.Extra[OpenAICodexTicketFailClosedExtraKey])
	require.NotContains(t, updated.Extra, OpenAICodexTicketHarvestProxyIDsExtraKey)
	require.Equal(t, ticket, updated.Extra[openAICodexTicketExtraKey(ticket.Model)])
	require.Equal(t, &proxyID, updated.ProxyID)
	require.Equal(t, []int64{17, 19}, updated.ProxyIDs)

	updated, err = svc.UpdateAccount(ctx, 41, &UpdateAccountInput{Extra: map[string]any{
		OpenAICodexTicketEnabledExtraKey:         false,
		OpenAICodexTicketHarvestProxyIDsExtraKey: []any{404},
	}})
	require.NoError(t, err)
	require.Equal(t, false, updated.Extra[OpenAICodexTicketEnabledExtraKey])
	require.Equal(t, false, updated.Extra[OpenAICodexTicketFailClosedExtraKey])
	require.NotContains(t, updated.Extra, OpenAICodexTicketHarvestProxyIDsExtraKey)
}

func TestAdminCodexTicketCreationIgnoresRetiredProxies(t *testing.T) {
	repo := &upstreamBillingProbeAccountRepo{}
	svc := &adminServiceImpl{accountRepo: repo}
	account, err := svc.CreateAccount(context.Background(), &CreateAccountInput{
		Platform: PlatformOpenAI, Type: AccountTypeOAuth, SkipDefaultGroupBind: true,
		Extra: map[string]any{OpenAICodexTicketEnabledExtraKey: true, OpenAICodexTicketHarvestProxyIDsExtraKey: []any{404}},
	})
	require.NoError(t, err)
	require.NotContains(t, account.Extra, OpenAICodexTicketHarvestProxyIDsExtraKey)
	require.Nil(t, account.ProxyID)
	require.Empty(t, account.ProxyIDs)
}

func TestAdminCodexTicketBulkValidatesBeforeWritingAndKeepsFalse(t *testing.T) {
	repo := &accountRepoStubForBulkUpdate{getByIDsAccounts: []*Account{{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth}}}
	svc := &adminServiceImpl{accountRepo: repo}
	_, err := svc.BulkUpdateAccounts(context.Background(), &BulkUpdateAccountsInput{
		AccountIDs: []int64{1}, Extra: map[string]any{OpenAICodexTicketEnabledExtraKey: "false"},
	})
	require.Error(t, err)
	require.Zero(t, repo.bulkUpdateCalls)
	_, err = svc.BulkUpdateAccounts(context.Background(), &BulkUpdateAccountsInput{
		AccountIDs: []int64{1}, Extra: map[string]any{OpenAICodexTicketEnabledExtraKey: false, OpenAICodexTicketFailClosedExtraKey: false, OpenAICodexTicketHarvestProxyIDsExtraKey: []any{404}},
	})
	require.NoError(t, err)
	require.Equal(t, false, repo.lastBulkUpdate.Extra[OpenAICodexTicketEnabledExtraKey])
	require.Equal(t, false, repo.lastBulkUpdate.Extra[OpenAICodexTicketFailClosedExtraKey])
	require.NotContains(t, repo.lastBulkUpdate.Extra, OpenAICodexTicketHarvestProxyIDsExtraKey)
	require.Nil(t, repo.lastBulkUpdate.ProxyID)
	require.Nil(t, repo.lastBulkUpdate.ProxyIDs)
}
