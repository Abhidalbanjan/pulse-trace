-- AI-drafted incident postmortems (Incidents · E1).
--
-- One editable postmortem per incident. It is generated on demand from the
-- incident's evidence (alerts + timeline + causal analysis) — by the LLM when a
-- provider is configured, or a deterministic template otherwise — and can then
-- be edited and re-saved. Tenant-scoped so a postmortem is only ever readable by
-- the incident's own tenant.
CREATE TABLE IF NOT EXISTS incident_postmortems (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    incident_id  VARCHAR(255) NOT NULL UNIQUE,
    tenant_id    VARCHAR(50) NOT NULL DEFAULT 'default',
    content      TEXT NOT NULL,
    model        VARCHAR(255) NOT NULL DEFAULT '', -- analyzer/provider that drafted it, or "template"
    generated_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP,
    edited_at    TIMESTAMP WITH TIME ZONE
);

CREATE INDEX IF NOT EXISTS idx_incident_postmortems_tenant ON incident_postmortems (tenant_id, incident_id);
