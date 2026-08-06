-- F11: scope error-group triage rows to a tenant so the regression worker can
-- enumerate tenants and page per-tenant (fingerprint already embeds the tenant,
-- but the worker needs a queryable column to fan out over). Existing rows
-- backfill to 'default', matching the pre-multi-tenant value.
ALTER TABLE error_groups ADD COLUMN IF NOT EXISTS tenant_id VARCHAR(50) NOT NULL DEFAULT 'default';
CREATE INDEX IF NOT EXISTS idx_error_groups_tenant ON error_groups(tenant_id);
