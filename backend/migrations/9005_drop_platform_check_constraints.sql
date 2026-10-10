-- Orange 特有：在不可变的历史 Orange 迁移之后落实官方平台清单校验。
-- 保留渠道监控与模板的 provider CHECK，平台合法性由应用层清单负责。

ALTER TABLE user_platform_quotas
    DROP CONSTRAINT IF EXISTS user_platform_quotas_platform_check;

ALTER TABLE composite_model_routes
    DROP CONSTRAINT IF EXISTS composite_model_routes_target_platform_check;
