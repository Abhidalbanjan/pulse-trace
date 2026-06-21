-- Migration: 004_add_tenant_fields
-- Adds tenant_id field to incidents and alerts.

ALTER TABLE alerts ADD COLUMN IF NOT EXISTS tenant_id VARCHAR(50) DEFAULT 'default';
ALTER TABLE incidents ADD COLUMN IF NOT EXISTS tenant_id VARCHAR(50) DEFAULT 'default';

CREATE INDEX IF NOT EXISTS idx_alerts_tenant_id ON alerts (tenant_id);
CREATE INDEX IF NOT EXISTS idx_incidents_tenant_id ON incidents (tenant_id);
