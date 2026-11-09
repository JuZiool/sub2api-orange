-- Orange 特有：移除 Codex 额度透支的设置、账号元数据与专属临时暂停。
-- 常规限流、其他暂停和实际业务用量保持原值；历史迁移与账本不修改。
DO $$
DECLARE
    account_row RECORD;
    cleaned_extra JSONB;
    pause_payload JSONB;
    clear_pause BOOLEAN;
BEGIN
    FOR account_row IN
        SELECT id, extra, temp_unschedulable_reason
        FROM accounts
        WHERE (
            jsonb_typeof(extra) = 'object'
            AND extra ?| ARRAY[
                'codex_quota_overdraft_disabled',
                'codex_quota_overdraft_enabled',
                'codex_quota_overdraft_probe'
            ]
        ) OR temp_unschedulable_reason IS NOT NULL
        FOR UPDATE
    LOOP
        cleaned_extra := account_row.extra;
        IF jsonb_typeof(cleaned_extra) = 'object' THEN
            cleaned_extra := cleaned_extra
                - 'codex_quota_overdraft_disabled'
                - 'codex_quota_overdraft_enabled'
                - 'codex_quota_overdraft_probe';
        END IF;

        clear_pause := FALSE;
        BEGIN
            pause_payload := account_row.temp_unschedulable_reason::jsonb;
            clear_pause := COALESCE(
                jsonb_typeof(pause_payload) = 'object'
                AND pause_payload ->> 'source' = 'codex_quota_overdraft',
                FALSE
            );
        EXCEPTION WHEN data_exception THEN
            -- 旧版和人工输入可能是纯文本；无法确认来源时保留暂停。
            clear_pause := FALSE;
        END;

        IF cleaned_extra IS DISTINCT FROM account_row.extra OR clear_pause THEN
            UPDATE accounts
            SET extra = cleaned_extra,
                temp_unschedulable_until = CASE
                    WHEN clear_pause THEN NULL ELSE temp_unschedulable_until END,
                temp_unschedulable_reason = CASE
                    WHEN clear_pause THEN NULL ELSE temp_unschedulable_reason END,
                updated_at = NOW()
            WHERE id = account_row.id;

            INSERT INTO scheduler_outbox (event_type, account_id)
            VALUES ('account_changed', account_row.id);
        END IF;
    END LOOP;
END $$;

DELETE FROM settings WHERE key = 'codex_quota_overdraft_enabled';
