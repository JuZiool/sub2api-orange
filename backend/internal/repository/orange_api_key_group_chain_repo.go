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

const (
	// orangeAPIKeyGroupChainTable 是分组链表的表名。
	orangeAPIKeyGroupChainTable = "orange_api_key_group_chain"
	// orangeAPIKeyGroupChainMetaTable 记录分组链的展示模式（fallback / aggregate），
	// 见迁移 8003。它只表达"用户在界面上选的是兜底分组还是聚合密钥"，不参与选号。
	orangeAPIKeyGroupChainMetaTable = "orange_api_key_group_chain_meta"
)

// OrangeReplaceGroupChain 用 groupIDs 全量替换某个 API Key 的分组链候选。
// priority 按 groupIDs 顺序从 1 递增（1 = 链首之后的第一个候选）。
//
// groupIDs 为空表示清链：同一事务内一并删除模式元数据，避免残留"已无链却仍是聚合模式"的意图记录。
func (r *apiKeyRepository) OrangeReplaceGroupChain(ctx context.Context, apiKeyID int64, groupIDs []int64) error {
	if apiKeyID <= 0 {
		return service.ErrAPIKeyNotFound
	}
	return r.orangeRunInTx(ctx, func(exec sqlExecutor) error {
		if err := orangeReplaceGroupChainRows(ctx, exec, apiKeyID, groupIDs); err != nil {
			return err
		}
		if len(groupIDs) == 0 {
			return orangeUpsertGroupChainMode(ctx, exec, apiKeyID, "")
		}
		return nil
	})
}

// OrangeReplaceGroupChainAndPrimary 在同一事务内把主分组写回 api_keys.group_id、替换链候选，
// 并写入模式元数据（fallback / aggregate），避免"链首已改、候选未写、模式未记"的半成品状态。
//
// 只更新 api_keys.group_id 这一上游既有语义列（该列的语义就是"Key 属于哪个分组"），
// 不新增列、不写入 Orange 私有状态；模式元数据落在我方自建表。
// mode 为空表示删除模式元数据（调用方需要"清链"语义时使用）。
func (r *apiKeyRepository) OrangeReplaceGroupChainAndPrimary(ctx context.Context, apiKeyID, primaryGroupID int64, tail []int64, mode string) error {
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
		if err := orangeReplaceGroupChainRows(ctx, exec, apiKeyID, tail); err != nil {
			return err
		}
		return orangeUpsertGroupChainMode(ctx, exec, apiKeyID, mode)
	})
}

// OrangeSetGroupChainMode 单独写入某个 API Key 的分组链模式（upsert）；mode 为空表示删除记录。
// 主要供"只改模式、不改链"的场景使用；常规写链请用 OrangeReplaceGroupChainAndPrimary 保持同事务。
func (r *apiKeyRepository) OrangeSetGroupChainMode(ctx context.Context, apiKeyID int64, mode string) error {
	if apiKeyID <= 0 {
		return service.ErrAPIKeyNotFound
	}
	if mode != "" && mode != service.OrangeGroupChainModeFallback && mode != service.OrangeGroupChainModeAggregate {
		return fmt.Errorf("orange api key group chain: invalid mode %q", mode)
	}
	return r.orangeRunInTx(ctx, func(exec sqlExecutor) error {
		return orangeUpsertGroupChainMode(ctx, exec, apiKeyID, mode)
	})
}

// OrangeLoadGroupChainModes 批量装载多个 API Key 的分组链模式；无记录的 Key 不会出现在返回的 map 中。
func (r *apiKeyRepository) OrangeLoadGroupChainModes(ctx context.Context, apiKeyIDs []int64) (map[int64]string, error) {
	out := make(map[int64]string, len(apiKeyIDs))
	if len(apiKeyIDs) == 0 || r.sql == nil {
		return out, nil
	}
	rows, err := r.sql.QueryContext(ctx, `
		SELECT api_key_id, mode
		FROM `+orangeAPIKeyGroupChainMetaTable+`
		WHERE api_key_id = ANY($1)`, pq.Array(apiKeyIDs))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var apiKeyID int64
		var mode string
		if err := rows.Scan(&apiKeyID, &mode); err != nil {
			return nil, err
		}
		out[apiKeyID] = mode
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
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

// orangeUpsertGroupChainMode 在给定的执行器（事务内）上 upsert 模式元数据。
// mode 为空表示删除记录（清链语义）；非法取值由调用方负责校验。
func orangeUpsertGroupChainMode(ctx context.Context, exec sqlExecutor, apiKeyID int64, mode string) error {
	if mode == "" {
		_, err := exec.ExecContext(ctx,
			`DELETE FROM `+orangeAPIKeyGroupChainMetaTable+` WHERE api_key_id = $1`, apiKeyID)
		return err
	}
	_, err := exec.ExecContext(ctx, `
		INSERT INTO `+orangeAPIKeyGroupChainMetaTable+` (api_key_id, mode, updated_at)
		VALUES ($1, $2, NOW())
		ON CONFLICT (api_key_id) DO UPDATE
		SET mode = EXCLUDED.mode, updated_at = NOW()`, apiKeyID, mode)
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
