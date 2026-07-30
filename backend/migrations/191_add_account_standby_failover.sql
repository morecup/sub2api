-- 主备账号自动接管配置。
-- schedulable 继续作为人工硬开关；备用账号仅在主账号命中任一 trigger 时进入调度池。
ALTER TABLE accounts
    ADD COLUMN IF NOT EXISTS standby_for_account_id BIGINT REFERENCES accounts(id) ON DELETE SET NULL;

ALTER TABLE accounts
    ADD COLUMN IF NOT EXISTS standby_trigger_types JSONB NOT NULL DEFAULT '[]'::jsonb;

CREATE INDEX IF NOT EXISTS idx_accounts_standby_for_account_id
    ON accounts (standby_for_account_id)
    WHERE deleted_at IS NULL AND standby_for_account_id IS NOT NULL;
