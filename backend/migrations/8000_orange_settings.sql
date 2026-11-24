-- Orange 特有：Orange 自有功能开关键值表。
-- 与上游 settings 表完全隔离：本表只由 Orange 模块读写，上游代码零依赖、零 ALTER。
-- 迁移编号从 8000 起，远离上游当前最大编号，避免与上游后续新增迁移撞号。
-- 幂等：可重复执行（runner 记录 checksum，并要求已应用的迁移文件不再被修改）。

CREATE TABLE IF NOT EXISTS orange_settings (
    id         BIGSERIAL PRIMARY KEY,
    key        VARCHAR(100) NOT NULL,
    value      TEXT NOT NULL DEFAULT '',
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_by BIGINT NULL,
    CONSTRAINT orange_settings_key_key UNIQUE (key)
);

COMMENT ON TABLE  orange_settings IS 'Orange 特有功能开关（键值对）。上游 settings 表禁止写入，Orange 开关全部落在此表。';
COMMENT ON COLUMN orange_settings.key        IS '开关键名，白名单由后端常量与校验维护';
COMMENT ON COLUMN orange_settings.value      IS '开关值；布尔开关统一存 true/false 文本';
COMMENT ON COLUMN orange_settings.updated_by IS '最后一次修改的管理员 users.id（可空，仅审计用途）';

-- 初始开关：用量排行（Token 排行榜）默认开启，与旧 Orange「无开关 = 始终可见」的行为保持一致。
INSERT INTO orange_settings (key, value) VALUES
    ('token_ranking_enabled', 'true')
ON CONFLICT (key) DO NOTHING;
