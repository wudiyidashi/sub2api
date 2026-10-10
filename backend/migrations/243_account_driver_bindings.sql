-- Existing accounts retain native behavior. Bindings are created atomically
-- with their accounts; revisions are reserved for maintenance operations.
ALTER TABLE accounts
    ADD COLUMN IF NOT EXISTS account_driver_id VARCHAR(64) NOT NULL DEFAULT 'native',
    ADD COLUMN IF NOT EXISTS account_driver_revision BIGINT NOT NULL DEFAULT 1;

CREATE INDEX IF NOT EXISTS idx_accounts_account_driver_id
    ON accounts (account_driver_id) WHERE deleted_at IS NULL;
