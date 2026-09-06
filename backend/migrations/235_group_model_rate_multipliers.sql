ALTER TABLE groups
    ADD COLUMN IF NOT EXISTS model_rate_multipliers JSONB NOT NULL DEFAULT '[]'::jsonb;
