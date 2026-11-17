-- Orange 特有：仅显式勾选的分组同步到费用计算器，已有与新分组默认关闭。
ALTER TABLE groups
    ADD COLUMN IF NOT EXISTS fee_calculator_sync_enabled BOOLEAN NOT NULL DEFAULT FALSE;

COMMENT ON COLUMN groups.fee_calculator_sync_enabled IS
    'Orange 特有：是否同步到费用计算器，默认关闭';
