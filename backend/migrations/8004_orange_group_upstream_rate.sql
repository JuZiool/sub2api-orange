-- Orange 特有：分组上游成本倍率（用于利润统计）。
-- 只新建自己的表，不修改上游 groups 表；上游 groups 表仅只读。
-- 上游原价取 usage_logs.total_cost（上游已有列），上游成本 = 上游原价 * 本表倍率。

CREATE TABLE IF NOT EXISTS orange_group_upstream_rate (
    group_id                BIGINT PRIMARY KEY,
    upstream_rate_multiplier NUMERIC(10, 4) NOT NULL DEFAULT 1.0,
    updated_at              TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT orange_group_upstream_rate_non_negative CHECK (upstream_rate_multiplier >= 0)
);

COMMENT ON TABLE  orange_group_upstream_rate IS
    'Orange 特有：分组上游成本倍率（仅用于利润统计，不影响用户计费）';
COMMENT ON COLUMN orange_group_upstream_rate.upstream_rate_multiplier IS
    '上游成本倍率：上游成本 = SUM(usage_logs.total_cost) * 本值；0 表示免费上游';

CREATE INDEX IF NOT EXISTS idx_orange_group_upstream_rate_group
    ON orange_group_upstream_rate(group_id);

-- 旧 Orange 版本曾在 groups 表加过 upstream_rate_multiplier 列；若存在则只读导入一次（幂等）。
DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_schema = 'public'
          AND table_name = 'groups'
          AND column_name = 'upstream_rate_multiplier'
    ) THEN
        INSERT INTO orange_group_upstream_rate (group_id, upstream_rate_multiplier)
        SELECT g.id, g.upstream_rate_multiplier
        FROM groups g
        WHERE g.deleted_at IS NULL
          AND g.upstream_rate_multiplier IS NOT NULL
          AND g.upstream_rate_multiplier <> 1.0
        ON CONFLICT (group_id) DO NOTHING;
    END IF;
END $$;
