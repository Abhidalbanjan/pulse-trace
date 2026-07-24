-- Per-tenant ingestion API keys.
--
-- Before this table existed, the telemetry ingestion endpoints (/v1/traces,
-- /v1/metrics, /v1/logs, /api/v1/logs, /api/v1/rum/ingest) trusted a
-- client-supplied X-Tenant-ID header verbatim, so any caller could write data
-- into any other tenant's stream simply by setting that header. Tenant identity
-- for ingestion now comes from a server-verifiable secret key instead: the agent
-- presents `Authorization: Bearer <key>`, the gateway hashes it, looks it up
-- here, and resolves tenant_id/tier from THIS row — never from a client header.
--
-- Only the SHA-256 hash of the key is stored, never the plaintext (same posture
-- as a password hash): the plaintext is shown to the operator exactly once at
-- creation time and is unrecoverable afterwards.
CREATE TABLE IF NOT EXISTS ingestion_keys (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name         VARCHAR(255) NOT NULL,
    -- Non-secret display prefix (e.g. "pt_ingest_A1b2") so operators can tell
    -- keys apart in the UI without ever seeing the full secret again.
    key_prefix   VARCHAR(32) NOT NULL,
    -- SHA-256 hex digest of the full plaintext key (64 chars).
    key_hash     CHAR(64) NOT NULL UNIQUE,
    tenant_id    VARCHAR(50) NOT NULL DEFAULT 'default',
    tier         VARCHAR(50) NOT NULL DEFAULT 'standard',
    created_at   TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP,
    last_used_at TIMESTAMP WITH TIME ZONE,
    -- Soft-delete: a revoked key stops resolving immediately but stays in the
    -- table for audit/history. Revocation is not a DELETE.
    revoked_at   TIMESTAMP WITH TIME ZONE
);

-- Hot-path lookup on every ingestion request: hash → tenant. Partial index on
-- live keys only keeps it small and makes the "is this key still valid?" check
-- an index-only scan.
CREATE INDEX IF NOT EXISTS idx_ingestion_keys_active_hash
    ON ingestion_keys (key_hash)
    WHERE revoked_at IS NULL;
