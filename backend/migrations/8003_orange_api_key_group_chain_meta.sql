-- Orange 特有：API Key 分组链的展示模式（兜底 / 聚合）。
-- 分组链本身一律存 orange_api_key_group_chain；本表只记录"用户配置意图"，
-- 用于前端区分「兜底分组」与「聚合密钥」两种交互（两者路由语义相同，只是链长度与展示不同）。
-- 不关联、不修改任何上游表。

CREATE TABLE IF NOT EXISTS orange_api_key_group_chain_meta (
    api_key_id BIGINT      PRIMARY KEY,
    mode       VARCHAR(16) NOT NULL DEFAULT 'fallback',
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT orange_api_key_group_chain_meta_mode_check CHECK (mode IN ('fallback', 'aggregate'))
);

COMMENT ON TABLE  orange_api_key_group_chain_meta IS
    'Orange 特有：API Key 分组链的展示模式（fallback=兜底分组，aggregate=聚合密钥）';
COMMENT ON COLUMN orange_api_key_group_chain_meta.mode IS
    'fallback=兜底（通常 1 个候选）；aggregate=聚合（多个候选按优先级依次尝试）';

-- 旧版遗留数据：旧 api_key_groups 表存在且某 Key 有 >=2 个候选时，视为聚合模式。
DO $$
BEGIN
    IF to_regclass('public.orange_api_key_group_chain') IS NOT NULL THEN
        INSERT INTO orange_api_key_group_chain_meta (api_key_id, mode)
        SELECT api_key_id, CASE WHEN count(*) >= 2 THEN 'aggregate' ELSE 'fallback' END
        FROM orange_api_key_group_chain
        GROUP BY api_key_id
        ON CONFLICT (api_key_id) DO NOTHING;
    END IF;
END $$;
