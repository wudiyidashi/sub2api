CREATE TABLE IF NOT EXISTS account_driver_maintenance (
    account_id BIGINT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    operation_id UUID PRIMARY KEY,
    source_driver_id VARCHAR(64) NOT NULL,
    target_driver_id VARCHAR(64) NOT NULL,
    expected_binding_revision BIGINT NOT NULL,
    actor_id BIGINT NOT NULL DEFAULT 0,
    previous_schedulable BOOLEAN NOT NULL,
    state VARCHAR(20) NOT NULL,
    started_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    expires_at TIMESTAMPTZ NOT NULL,
    error TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS account_driver_operation_leases (
    lease_id UUID PRIMARY KEY,
    account_id BIGINT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    kind VARCHAR(32) NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_account_driver_operation_leases_active
    ON account_driver_operation_leases(account_id, expires_at);
CREATE UNIQUE INDEX IF NOT EXISTS idx_account_driver_maintenance_active_account
    ON account_driver_maintenance(account_id)
    WHERE state IN ('preparing','draining','committing');
CREATE INDEX IF NOT EXISTS idx_account_driver_maintenance_account_started
    ON account_driver_maintenance(account_id, started_at DESC);
