-- Orange 特有：聚合密钥（一个 API Key 按优先级绑定多个分组）。
-- api_keys.group_id 仍保存链上优先级最高的分组，本表保存完整候选链；
-- 请求调度在主分组无可用账号时按 priority 升序依次尝试后续分组。
CREATE TABLE IF NOT EXISTS api_key_groups (
    id         BIGSERIAL PRIMARY KEY,
    api_key_id BIGINT NOT NULL REFERENCES api_keys(id) ON DELETE CASCADE,
    group_id   BIGINT NOT NULL REFERENCES groups(id) ON DELETE CASCADE,
    priority   INT NOT NULL DEFAULT 1,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_api_key_groups_key_group
    ON api_key_groups(api_key_id, group_id);

CREATE INDEX IF NOT EXISTS idx_api_key_groups_key_priority
    ON api_key_groups(api_key_id, priority);

CREATE INDEX IF NOT EXISTS idx_api_key_groups_group
    ON api_key_groups(group_id);

COMMENT ON TABLE api_key_groups IS
    'Orange 特有：聚合密钥的分组优先级链；priority 越小越先尝试，1 为主分组';
