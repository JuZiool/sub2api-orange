-- Orange 特有：分组上游成本倍率与利润统计。
-- 9008 曾在汇总表缺失时直接 ALTER 并中止启动；本迁移允许旧库暂缺可选汇总表。

ALTER TABLE groups
    ADD COLUMN IF NOT EXISTS upstream_rate_multiplier DECIMAL(10, 4) NOT NULL DEFAULT 1.0;

COMMENT ON COLUMN groups.upstream_rate_multiplier IS
    'Orange 特有：分组上游成本倍率，仅用于利润统计';

DO $$
DECLARE
    total_cost_exists BOOLEAN;
BEGIN
    IF to_regclass('public.usage_group_daily_rollups') IS NULL THEN
        RAISE NOTICE 'usage_group_daily_rollups 不存在；利润统计将使用原始用量数据';
    ELSE
        SELECT EXISTS (
            SELECT 1
            FROM information_schema.columns
            WHERE table_schema = 'public'
              AND table_name = 'usage_group_daily_rollups'
              AND column_name = 'total_cost'
        ) INTO total_cost_exists;

        ALTER TABLE usage_group_daily_rollups
            ADD COLUMN IF NOT EXISTS total_cost DECIMAL(20, 10) NOT NULL DEFAULT 0;

        COMMENT ON COLUMN usage_group_daily_rollups.total_cost IS
            'Orange 特有：按分组和自然日汇总的上游原价';

        IF NOT total_cost_exists
           AND to_regclass('public.usage_group_rollup_state') IS NOT NULL THEN
            UPDATE usage_group_rollup_state
            SET closed_before = DATE '1970-01-01', updated_at = NOW()
            WHERE id = 1;
        END IF;
    END IF;

    IF to_regclass('public.usage_group_rollup_state') IS NOT NULL THEN
        EXECUTE 'DROP TRIGGER IF EXISTS usage_logs_group_rollup_invalidate_total_cost_update ON public.usage_logs';
        EXECUTE $trigger$
            CREATE TRIGGER usage_logs_group_rollup_invalidate_total_cost_update
            AFTER UPDATE OF total_cost ON public.usage_logs
            FOR EACH ROW
            WHEN (
                OLD.total_cost IS DISTINCT FROM NEW.total_cost
                AND (OLD.group_id IS NOT NULL OR NEW.group_id IS NOT NULL)
            )
            EXECUTE FUNCTION public.invalidate_group_usage_rollup_state()
        $trigger$;
    END IF;
END $$;
