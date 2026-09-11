package repository

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestProxyPoolPersistenceOrderingAndRollback(t *testing.T) {
	_, client := newUserEntRepo(t)
	ctx := context.Background()
	ids := []int64{}
	for range 3 {
		proxy, err := client.Proxy.Create().SetName("proxy").SetProtocol("http").SetHost("127.0.0.1").SetPort(8080).Save(ctx)
		require.NoError(t, err)
		ids = append(ids, proxy.ID)
	}
	account := &service.Account{Name: "pool", Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth, Credentials: map[string]any{}, Extra: map[string]any{}, Status: service.StatusActive, Concurrency: 2, ProxyIDs: []int64{ids[2], ids[0], ids[1]}}
	tx, err := client.Tx(ctx)
	require.NoError(t, err)
	require.NoError(t, createAccountRecord(ctx, tx.Client(), account))
	require.NoError(t, tx.Commit())
	repo := newAccountRepositoryWithSQL(client, nil, nil)
	got, err := repo.GetByID(ctx, account.ID)
	require.NoError(t, err)
	require.Equal(t, account.ProxyIDs, got.ProxyIDs)
	require.Len(t, got.Proxies, 3)
	batch, err := repo.GetByIDs(ctx, []int64{account.ID})
	require.NoError(t, err)
	require.Equal(t, account.ProxyIDs, batch[0].ProxyIDs)
	tx, err = client.Tx(ctx)
	require.NoError(t, err)
	require.Error(t, replaceAccountProxyPool(ctx, tx.Client(), account.ID, []int64{ids[1], 999}))
	require.NoError(t, tx.Rollback())
	got, err = repo.GetByID(ctx, account.ID)
	require.NoError(t, err)
	require.Equal(t, account.ProxyIDs, got.ProxyIDs)
	tx, err = client.Tx(ctx)
	require.NoError(t, err)
	require.NoError(t, replaceAccountProxyPool(ctx, tx.Client(), account.ID, []int64{ids[0]}))
	require.NoError(t, tx.Commit())
	got, err = repo.GetByID(ctx, account.ID)
	require.NoError(t, err)
	require.Empty(t, got.ProxyIDs)
}
