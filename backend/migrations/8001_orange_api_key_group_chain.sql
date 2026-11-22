-- Orange 特有：API Key 的分组优先级链（密钥级兜底分组 + 聚合分组共用）。
-- 只保存「链首（api_keys.group_id）之后」的候选分组：
--   priority = 1  表示主分组没有可用账号时第一个尝试的分组（单兜底）
--   priority = 1..N 表示聚合链（数值越小越先尝试）
-- 不新增/修改任何上游表的列；api_keys.group_id 仍是链首的权威来源。

CREATE TABLE IF NOT EXISTS orange_api_key_group_chain (
    id          BIGSERIAL PRIMARY KEY,
    api_key_id  BIGINT      NOT NULL,
    group_id    BIGINT      NOT NULL,
    priority    INT         NOT NULL CHECK (priority >= 1),
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

COMMENT ON TABLE  orange_api_key_group_chain IS
    'Orange 特有：API Key 分组优先级链（链首在 api_keys.group_id，本表存其后的候选）';
COMMENT ON COLUMN orange_api_key_group_chain.priority IS
    '1 = 链首之后的第一个候选；数值越小越先尝试';

-- 一个 Key 内分组不重复
CREATE UNIQUE INDEX IF NOT EXISTS uq_orange_key_group_chain_key_group
    ON orange_api_key_group_chain(api_key_id, group_id);
-- 同一 Key 内优先级唯一，保证顺序稳定
CREATE UNIQUE INDEX IF NOT EXISTS uq_orange_key_group_chain_key_priority
    ON orange_api_key_group_chain(api_key_id, priority);
-- 按分组反查（缓存失效扇出 / 分组删除清理）
CREATE INDEX IF NOT EXISTS idx_orange_key_group_chain_group
    ON orange_api_key_group_chain(group_id);
