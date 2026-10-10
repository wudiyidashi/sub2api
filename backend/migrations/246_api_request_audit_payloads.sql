-- Raw HTTP payload audit storage. This table is intentionally separate from
-- ops_error_logs: payload bytes are captured only when the audit feature is
-- enabled and are retained as bounded chunks for request-id lookup.
CREATE TABLE IF NOT EXISTS api_request_audit_payloads (
    id                BIGSERIAL PRIMARY KEY,
    request_id        VARCHAR(128) NOT NULL,
    client_request_id VARCHAR(128) NOT NULL DEFAULT '',
    direction         VARCHAR(32) NOT NULL,
    content_type      VARCHAR(255) NOT NULL DEFAULT '',
    headers           JSONB NOT NULL DEFAULT '{}'::jsonb,
    body              BYTEA NOT NULL DEFAULT ''::bytea,
    body_bytes        BIGINT NOT NULL DEFAULT 0,
    original_bytes    BIGINT NOT NULL DEFAULT 0,
    chunk_index       INT NOT NULL DEFAULT 0,
    is_final          BOOLEAN NOT NULL DEFAULT TRUE,
    truncated         BOOLEAN NOT NULL DEFAULT FALSE,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT chk_api_request_audit_direction
        CHECK (direction IN ('client_request','client_response','upstream_request','upstream_response')),
    CONSTRAINT chk_api_request_audit_nonnegative
        CHECK (body_bytes >= 0 AND original_bytes >= 0 AND chunk_index >= 0)
);

CREATE INDEX IF NOT EXISTS idx_api_request_audit_payloads_request
    ON api_request_audit_payloads(request_id, created_at ASC, id ASC);
CREATE INDEX IF NOT EXISTS idx_api_request_audit_payloads_request_direction
    ON api_request_audit_payloads(request_id, direction, chunk_index, id);
CREATE INDEX IF NOT EXISTS idx_api_request_audit_payloads_created
    ON api_request_audit_payloads(created_at DESC, id DESC);
