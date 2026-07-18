-- Append-only audit trail for RBAC/ABAC/user mutations. Every role, policy, and
-- user change writes a row here with who did it and the before/after state, so
-- "who granted themselves admin" or "who deleted this deny policy" is answerable -
-- previously there was no record at all.
CREATE TABLE IF NOT EXISTS audit_log (
    id BIGSERIAL PRIMARY KEY,
    actor VARCHAR(255) NOT NULL DEFAULT 'unknown',
    action VARCHAR(50) NOT NULL,       -- create | update | delete
    target_type VARCHAR(50) NOT NULL,  -- role | policy | user
    target_id VARCHAR(255) NOT NULL,
    before_state JSONB,
    after_state JSONB,
    created_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_audit_log_created_at ON audit_log (created_at DESC);
