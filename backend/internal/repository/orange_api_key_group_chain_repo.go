// Orange 特有：API Key 分组优先级链（密钥级兜底分组 + 聚合分组）的裸 SQL 仓储。
//
// 设计约束（见 文档/功能分析/2026-11-22-密钥分组兜底与聚合分组恢复分析.md）：
//   - 链首（主分组）仍以上游既有列 api_keys.group_id 为权威来源，本表只存链首之后的候选；
//   - 不新增 ent 实体，因此全部走裸 SQL，避免 go generate ./ent 造成生成代码漂移；
//   - 对上游表只读；唯一的写操作是设置链时更新 api_keys.group_id（上游既有语义列）。
package repository

import (
	"context"
	"fmt"
	"strings"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/lib/pq"
)

// orangeAPIKeyGroupChainTable 是分组链表的表名。
const orangeAPIKeyGroupChainTable = "orange_api_key_group_chain"

// OrangeReplaceGroupChain 用 groupIDs 全量替换某个 API Key 的分组链候选。
// priority 按 groupIDs 顺序从 1 递增（1 = 链首之后的第一个候选）。
func (r *apiKeyRepository) OrangeReplaceGroupChain(ctx context.Context, apiKeyID int64, groupIDs []int64) error {
	if apiKeyID <= 0 {
		return service.ErrAPIKeyNotFound
	}
	return r.orangeRunInTx(ctx, func(exec sqlExecutor) error {
		return orangeReplaceGroupChainRows(ctx, exec, apiKeyID, groupIDs)
	})
}

// OrangeReplaceGroupChainAndPrimary 在同一事务内把主分组写回 api_keys.group_id 并替换链候选，
// 避免"链首已改、候选未写"的半成品状态。
//
// 只更新 api_keys.group_id 这一上游既有语义列（该列的语义就是"Key 属于哪个分组"），
// 不新增列、不写入 Orange 私有状态。
func (r *apiKeyRepository) OrangeReplaceGroupChainAndPrimary(ctx context.Context, apiKeyID, primaryGroupID int64, tail []int64) error {
	if apiKeyID <= 0 {
		return service.ErrAPIKeyNotFound
	}
	if primaryGroupID <= 0 {
		return service.ErrGroupNotFound
	}
	return r.orangeRunInTx(ctx, func(exec sqlExecutor) error {
		res, err := exec.ExecContext(ctx, `
			UPDATE api_keys
			SET group_id = $1, updated_at = NOW()
			WHERE id = $2 AND deleted_at IS NULL`, primaryGroupID, apiKeyID)
		if err != nil {
			return err
		}
		affected, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if affected == 0 {
			return service.ErrAPIKeyNotFound
		}
		return orangeReplaceGroupChainRows(ctx, exec, apiKeyID, tail)
	})
}

// OrangeLoadGroupChains 批量装载多个 API Key 的分组链候选，按 priority 升序。
// 不存在链的 Key 不会出现在返回的 map 中。
func (r *apiKeyRepository) OrangeLoadGroupChains(ctx context.Context, apiKeyIDs []int64) (map[int64][]service.OrangeGroupChainMember, error) {
	out := make(map[int64][]service.OrangeGroupChainMember, len(apiKeyIDs))
	if len(apiKeyIDs) == 0 {
		return out, nil
	}
	if r.sql == nil {
		return out, nil
	}

	rows, err := r.sql.QueryContext(ctx, `
		SELECT api_key_id, group_id, priority
		FROM `+orangeAPIKeyGroupChainTable+`
		WHERE api_key_id = ANY($1)
		ORDER BY api_key_id, priority, id`, pq.Array(apiKeyIDs))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var apiKeyID int64
		var member service.OrangeGroupChainMember
		if err := rows.Scan(&apiKeyID, &member.GroupID, &member.Priority); err != nil {
			return nil, err
		}
		out[apiKeyID] = append(out[apiKeyID], member)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// OrangeListAPIKeyIDsByChainGroup 反查把 groupID 作为候选（链首之后的任意位置）的 API Key ID。
// 供缓存失效扇出使用：分组配置（倍率/白名单/停用）变更时，链上引用该分组的 Key 也必须失效。
func (r *apiKeyRepository) OrangeListAPIKeyIDsByChainGroup(ctx context.Context, groupID int64) ([]int64, error) {
	if groupID <= 0 || r.sql == nil {
		return nil, nil
	}
	rows, err := r.sql.QueryContext(ctx, `
		SELECT DISTINCT api_key_id
		FROM `+orangeAPIKeyGroupChainTable+`
		WHERE group_id = $1
		ORDER BY api_key_id`, groupID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	out := make([]int64, 0, 4)
	for rows.Next() {
		var apiKeyID int64
		if err := rows.Scan(&apiKeyID); err != nil {
			return nil, err
		}
		out = append(out, apiKeyID)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// orangeReplaceGroupChainRows 在给定的执行器（事务内）上先删后插。
func orangeReplaceGroupChainRows(ctx context.Context, exec sqlExecutor, apiKeyID int64, groupIDs []int64) error {
	if _, err := exec.ExecContext(ctx, `DELETE FROM `+orangeAPIKeyGroupChainTable+` WHERE api_key_id = $1`, apiKeyID); err != nil {
		return err
	}
	if len(groupIDs) == 0 {
		return nil
	}

	values := make([]string, 0, len(groupIDs))
	args := make([]any, 0, len(groupIDs)*3)
	for i, groupID := range groupIDs {
		values = append(values, fmt.Sprintf("($%d, $%d, $%d)", i*3+1, i*3+2, i*3+3))
		args = append(args, apiKeyID, groupID, i+1)
	}
	_, err := exec.ExecContext(ctx,
		`INSERT INTO `+orangeAPIKeyGroupChainTable+` (api_key_id, group_id, priority) VALUES `+strings.Join(values, ", "),
		args...)
	return err
}

// orangeRunInTx 在事务内执行 fn；若 ctx 已携带事务则直接复用，避免 errTxStarted。
func (r *apiKeyRepository) orangeRunInTx(ctx context.Context, fn func(exec sqlExecutor) error) error {
	if tx := dbent.TxFromContext(ctx); tx != nil {
		return fn(tx.Client())
	}
	tx, err := r.client.Tx(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := fn(tx.Client()); err != nil {
		return err
	}
	return tx.Commit()
}
