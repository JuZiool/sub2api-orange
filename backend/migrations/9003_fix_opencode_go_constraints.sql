-- 9003: Orange 特有 —— 修正迁移排序造成的 opencode_go 白名单丢失。
--
-- 背景：迁移执行器按文件名排序（见 internal/repository/migrations_runner.go 的
-- sort.Strings(files)），实际顺序为
--   237/238（上游 opencode_go） -> 9000 -> 9001（Orange minimax） -> 9002 -> 9003
-- 其中 9001 对 user_platform_quotas 与 composite_model_routes 是「无条件 DROP + 重建」，
-- 且平台列表不含 opencode_go，因此会把 238 刚加入的 opencode_go 从这两个约束里抹掉，
-- 导致 OpenCode 分组与配额写入被 CHECK 拒绝。
-- （channel_monitors 两段有 position('minimax' IN ...) 守卫，会跳过重建，故不受影响。）
--
-- 9001 已在实际部署中应用并记录 checksum，不可修改（改则启动时校验失败）。
-- 因此这里排在 9001 之后，把四处约束统一重建为同时包含 minimax 与 opencode_go 的
-- 超集，作为最终权威状态：无论全新安装还是从 0.2.4-9 升级，结果都确定一致。
--
-- 幂等：DROP IF EXISTS + 重建；重复执行结果相同。

ALTER TABLE user_platform_quotas
    DROP CONSTRAINT IF EXISTS user_platform_quotas_platform_check;

ALTER TABLE user_platform_quotas
    ADD CONSTRAINT user_platform_quotas_platform_check
    CHECK (platform IN ('anthropic', 'openai', 'gemini', 'antigravity', 'grok',
                        'kimi', 'zhipu', 'deepseek', 'minimax', 'opencode_go'));

ALTER TABLE composite_model_routes
    DROP CONSTRAINT IF EXISTS composite_model_routes_target_platform_check;

ALTER TABLE composite_model_routes
    ADD CONSTRAINT composite_model_routes_target_platform_check
    CHECK (target_platform IN ('anthropic', 'openai', 'gemini', 'antigravity', 'grok',
                               'kimi', 'zhipu', 'deepseek', 'minimax', 'opencode_go'));

DO $$
DECLARE
    monitor_constraint_def TEXT;
    template_constraint_def TEXT;
BEGIN
    SELECT pg_get_constraintdef(c.oid)
      INTO monitor_constraint_def
      FROM pg_constraint c
      JOIN pg_class t ON t.oid = c.conrelid
     WHERE t.relname = 'channel_monitors'
       AND c.conname = 'channel_monitors_provider_check';

    IF monitor_constraint_def IS NULL
       OR position('minimax' IN monitor_constraint_def) = 0
       OR position('opencode_go' IN monitor_constraint_def) = 0 THEN
        ALTER TABLE channel_monitors
            DROP CONSTRAINT IF EXISTS channel_monitors_provider_check;
        ALTER TABLE channel_monitors
            ADD CONSTRAINT channel_monitors_provider_check
            CHECK (provider IN ('openai', 'anthropic', 'gemini', 'grok',
                                'antigravity', 'kimi', 'zhipu', 'deepseek', 'minimax', 'opencode_go'));
    END IF;

    SELECT pg_get_constraintdef(c.oid)
      INTO template_constraint_def
      FROM pg_constraint c
      JOIN pg_class t ON t.oid = c.conrelid
     WHERE t.relname = 'channel_monitor_request_templates'
       AND c.conname = 'channel_monitor_request_templates_provider_check';

    IF template_constraint_def IS NULL
       OR position('minimax' IN template_constraint_def) = 0
       OR position('opencode_go' IN template_constraint_def) = 0 THEN
        ALTER TABLE channel_monitor_request_templates
            DROP CONSTRAINT IF EXISTS channel_monitor_request_templates_provider_check;
        ALTER TABLE channel_monitor_request_templates
            ADD CONSTRAINT channel_monitor_request_templates_provider_check
            CHECK (provider IN ('openai', 'anthropic', 'gemini', 'grok',
                                'antigravity', 'kimi', 'zhipu', 'deepseek', 'minimax', 'opencode_go'));
    END IF;
END $$;
