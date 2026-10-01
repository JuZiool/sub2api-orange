-- Orange 特有：为 api_keys 增加密钥级兜底分组。
-- 仅当主分组没有可用账号时，请求才会切换到兜底分组选号。
ALTER TABLE api_keys
    ADD COLUMN IF NOT EXISTS fallback_group_id BIGINT;

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname = 'api_keys_fallback_group_id_fkey'
          AND conrelid = 'api_keys'::regclass
    ) THEN
        ALTER TABLE api_keys
            ADD CONSTRAINT api_keys_fallback_group_id_fkey
            FOREIGN KEY (fallback_group_id) REFERENCES groups(id) ON DELETE SET NULL;
    END IF;
END $$;

CREATE INDEX IF NOT EXISTS idx_api_keys_fallback_group_id
    ON api_keys(fallback_group_id)
    WHERE deleted_at IS NULL AND fallback_group_id IS NOT NULL;

COMMENT ON COLUMN api_keys.fallback_group_id IS
    'Fallback group used only after the primary group has no available accounts';
