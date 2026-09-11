package repository

import (
	"context"
	dbent "github.com/Wei-Shaw/sub2api/ent"
	dbaccountproxy "github.com/Wei-Shaw/sub2api/ent/accountproxy"
)

// Called inside the transaction that saves the account and its outbox event.
func replaceAccountProxyPool(ctx context.Context, client *dbent.Client, accountID int64, ids []int64) error {
	if _, err := client.AccountProxy.Delete().Where(dbaccountproxy.AccountIDEQ(accountID)).Exec(ctx); err != nil {
		return err
	}
	if len(ids) < 2 {
		return nil
	}
	builders := make([]*dbent.AccountProxyCreate, 0, len(ids))
	for position, id := range ids {
		builders = append(builders, client.AccountProxy.Create().SetAccountID(accountID).SetProxyID(id).SetPosition(position))
	}
	return client.AccountProxy.CreateBulk(builders...).Exec(ctx)
}

func (r *accountRepository) loadAccountProxyPools(ctx context.Context, accountIDs []int64) (map[int64][]int64, error) {
	out := make(map[int64][]int64)
	for start := 0; start < len(accountIDs); start += postgresParameterBatchSize {
		end := start + postgresParameterBatchSize
		if end > len(accountIDs) {
			end = len(accountIDs)
		}
		entries, err := r.client.AccountProxy.Query().Where(dbaccountproxy.AccountIDIn(accountIDs[start:end]...)).Order(dbaccountproxy.ByAccountID(), dbaccountproxy.ByPosition()).All(ctx)
		if err != nil {
			return nil, err
		}
		for _, entry := range entries {
			out[entry.AccountID] = append(out[entry.AccountID], entry.ProxyID)
		}
	}
	return out, nil
}
