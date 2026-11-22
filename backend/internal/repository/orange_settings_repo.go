// Orange 特有：orange_settings（Orange 功能开关键值表）的读写仓储。
//
// 设计约束：
//   - 只读写 orange_settings 这一张 Orange 自建表；对上游 settings 表零读写
//     （上游 settings 表的 key 需要同步改 9 处注册面，属高冲突改动，故 Orange 开关全部落自建表）；
//   - 不新增 ent 实体，全部走裸 SQL，避免 go generate ./ent 造成生成代码漂移；
//   - 复用同包既有资产：*settingRepository（同包可访问其私有 client 字段）与 ProvideSQLDB；
//   - OrangeStore() 只做能力暴露，供 service 层用类型断言取用，不扩展上游 SettingRepository 接口。
package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

// orangeSettingsTable 是 Orange 功能开关键值表的表名（迁移 8000 创建）。
const orangeSettingsTable = "orange_settings"

// 编译期断言：*settingRepository 必须满足 service 层的 Orange 开关存储契约。
var _ service.OrangeSettingsStore = (*settingRepository)(nil)

// OrangeGetAll 读取 orange_settings 全表键值（键名 → 值文本）。
// 表为空时返回空 map（不返回 nil），由 service 层按白名单补默认值。
func (r *settingRepository) OrangeGetAll(ctx context.Context) (map[string]string, error) {
	db, err := r.orangeDB()
	if err != nil {
		return nil, err
	}

	rows, err := db.QueryContext(ctx, `SELECT key, value FROM `+orangeSettingsTable)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	values := make(map[string]string, 8)
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			return nil, err
		}
		values[key] = value
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return values, nil
}

// OrangeGet 读取单个 Orange 开关值；键不存在时返回 exists=false（err 为 nil）。
func (r *settingRepository) OrangeGet(ctx context.Context, key string) (value string, exists bool, err error) {
	key = strings.TrimSpace(key)
	if key == "" {
		return "", false, errors.New("orange_settings: key is required")
	}
	db, err := r.orangeDB()
	if err != nil {
		return "", false, err
	}

	err = db.QueryRowContext(ctx, `SELECT value FROM `+orangeSettingsTable+` WHERE key = $1`, key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return value, true, nil
}

// OrangeUpsert 写入单个 Orange 开关（key 冲突时更新值与审计字段）。
// updatedBy <= 0 表示拿不到当前管理员用户 ID，此时写 NULL（updated_by 仅审计用途）。
func (r *settingRepository) OrangeUpsert(ctx context.Context, key, value string, updatedBy int64) error {
	key = strings.TrimSpace(key)
	if key == "" {
		return errors.New("orange_settings: key is required")
	}
	db, err := r.orangeDB()
	if err != nil {
		return err
	}

	var updatedByArg any
	if updatedBy > 0 {
		updatedByArg = updatedBy
	}
	_, err = db.ExecContext(ctx, `
		INSERT INTO `+orangeSettingsTable+` (key, value, updated_at, updated_by)
		VALUES ($1, $2, NOW(), $3)
		ON CONFLICT (key) DO UPDATE
		SET value = EXCLUDED.value, updated_at = NOW(), updated_by = EXCLUDED.updated_by`,
		key, value, updatedByArg)
	if err != nil {
		return fmt.Errorf("orange_settings: upsert %q: %w", key, err)
	}
	return nil
}

// OrangeUpdatedAt 返回 orange_settings 表内最近一次更新时间；表内无行时返回 nil。
func (r *settingRepository) OrangeUpdatedAt(ctx context.Context) (*time.Time, error) {
	db, err := r.orangeDB()
	if err != nil {
		return nil, err
	}

	var updatedAt sql.NullTime
	if err := db.QueryRowContext(ctx, `SELECT MAX(updated_at) FROM `+orangeSettingsTable).Scan(&updatedAt); err != nil {
		return nil, err
	}
	if !updatedAt.Valid {
		return nil, nil
	}
	value := updatedAt.Time
	return &value, nil
}

// OrangeStore 把 *settingRepository 暴露为 service.OrangeSettingsStore。
// 第二个返回值表示能力是否可用（ent 客户端缺失或拿不到底层 *sql.DB 时返回 false）。
func (r *settingRepository) OrangeStore() (service.OrangeSettingsStore, bool) {
	if r == nil || r.client == nil {
		return nil, false
	}
	return r, true
}

// orangeDB 取 ent 客户端底层的 *sql.DB（复用同包 ProvideSQLDB，不新建连接池）。
func (r *settingRepository) orangeDB() (*sql.DB, error) {
	if r == nil || r.client == nil {
		return nil, errors.New("orange_settings: ent client is nil")
	}
	db, err := ProvideSQLDB(r.client)
	if err != nil {
		return nil, fmt.Errorf("orange_settings: resolve sql db: %w", err)
	}
	if db == nil {
		return nil, errors.New("orange_settings: sql db is nil")
	}
	return db, nil
}
