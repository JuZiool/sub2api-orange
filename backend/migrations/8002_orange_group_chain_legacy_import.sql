-- Orange 特有：把旧 Orange 版本（迁移 9004/9006 时期）遗留在库里的分组配置一次性导入新结构。
-- 仅当旧结构存在时才执行；纯官方库或新库上等价于空操作。
-- 旧列/旧表本身保留不动（不 ALTER、不 DROP 上游表结构），仅做只读 SELECT。

DO $$
BEGIN
    -- 1) 旧单兜底列 api_keys.fallback_group_id → 链上 priority = 1
    IF EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_schema = 'public'
          AND table_name = 'api_keys'
          AND column_name = 'fallback_group_id'
    ) THEN
        INSERT INTO orange_api_key_group_chain (api_key_id, group_id, priority)
        SELECT k.id, k.fallback_group_id, 1
        FROM api_keys k
        WHERE k.fallback_group_id IS NOT NULL
          AND k.deleted_at IS NULL
          AND k.fallback_group_id IS DISTINCT FROM k.group_id
        ON CONFLICT DO NOTHING;
    END IF;

    -- 2) 旧聚合链表 api_key_groups（含链首）→ 跳过链首，其余按原 priority 顺延
    IF to_regclass('public.api_key_groups') IS NOT NULL THEN
        INSERT INTO orange_api_key_group_chain (api_key_id, group_id, priority)
        SELECT ag.api_key_id,
               ag.group_id,
               ROW_NUMBER() OVER (PARTITION BY ag.api_key_id ORDER BY ag.priority, ag.id)
        FROM api_key_groups ag
        JOIN api_keys k ON k.id = ag.api_key_id AND k.deleted_at IS NULL
        WHERE ag.group_id IS DISTINCT FROM k.group_id
        ON CONFLICT DO NOTHING;
    END IF;
END $$;
