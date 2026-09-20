-- 9004: Orange 特有 —— Codex 打票账号级开关显式回填为「关闭」。
--
-- 背景：打票为账号级 opt-in（缺 codex_ticket_enabled 即视为 false）。为便于管理端
-- 明确展示与后续批量操作，这里把 OpenAI OAuth / Setup Token 账号中缺失该键的记录
-- 显式写为 false；已显式设置（true/false）的账号一律不动，保证幂等且不覆盖用户选择。
--
-- 说明：该键只是账号策略开关，不含票据材料；票据材料存放于 codex_turn_ticket:<model>，
-- 由服务端管理并在导出/复制/导入时脱敏，不在本迁移范围内。

UPDATE accounts
SET extra = extra || jsonb_build_object('codex_ticket_enabled', false)
WHERE platform = 'openai'
  AND type IN ('oauth', 'setup-token')
  AND NOT (extra ? 'codex_ticket_enabled');
