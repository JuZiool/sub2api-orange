-- Orange 特有：分组级「上游倍率」与利润统计。
--
-- 背景：费率倍数（groups.rate_multiplier）是卖给用户的价格倍率，
-- 上游倍率是我方向上游采购的成本倍率。二者之差即毛利。
--
-- 口径：
--   用户实付（收入） = SUM(usage_logs.actual_cost)          = SUM(total_cost * rate_multiplier)
--   我方成本         = SUM(usage_logs.total_cost) * upstream_rate_multiplier
--   毛利             = 收入 - 成本
--
-- 为什么不给 usage_logs 加快照列：上游倍率是分组级慢变配置，利润按查询时刻的
-- 分组配置重算即可满足"看赚多少"的需求；避免改动两处计费写入路径及其测试。
-- 日汇总表补一个 total_cost 列即可在 O(1) 聚合下算出成本，无需扫 usage_logs。

-- 1) 分组上游倍率。默认 1.0 = 按官方原价采购（中性值，不产生虚假利润）。
--    允许 0（免费上游），与「费率倍数」一致不做正数约束。
ALTER TABLE groups
    ADD COLUMN IF NOT EXISTS upstream_rate_multiplier DECIMAL(10, 4) NOT NULL DEFAULT 1.0;

COMMENT ON COLUMN groups.upstream_rate_multiplier IS
    'Orange 特有：上游成本倍率（我方采购价），仅用于利润统计，不影响用户计费与调度准入';

-- 2) 分组日汇总补官方原价合计，用于按当前上游倍率换算成本。
ALTER TABLE usage_group_daily_rollups
    ADD COLUMN IF NOT EXISTS total_cost DECIMAL(20, 10) NOT NULL DEFAULT 0;

COMMENT ON COLUMN usage_group_daily_rollups.total_cost IS
    'Orange 特有：当日官方原价合计（SUM(usage_logs.total_cost)），乘以分组上游倍率得到成本';

-- 3) 强制重建历史日桶：新增列在存量行上为 0，若不重建会让累计成本恒为 0、利润虚高。
--    退回到未发布状态，由后台聚合作业按持久水位全量回填（见 custom_group_usage_rollup_repo.go）。
UPDATE usage_group_rollup_state
SET closed_before = DATE '1970-01-01',
    updated_at = NOW()
WHERE id = 1;
