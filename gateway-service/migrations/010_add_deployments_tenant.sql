-- Deployment markers were previously global: ListDeployments filtered only by
-- service, so any tenant could read (and RecordDeployment write) another tenant's
-- deployment history for a same-named service. Scope them per tenant.
ALTER TABLE deployments ADD COLUMN IF NOT EXISTS tenant_id VARCHAR(50) NOT NULL DEFAULT 'default';

-- ListDeployments filters by (tenant_id, service) newest-first, so this index
-- backs the exact lookup.
CREATE INDEX IF NOT EXISTS idx_deployments_tenant_service
    ON deployments (tenant_id, service, deployed_at DESC);
