-- Saved searches: a named, reusable log/trace query an operator can pin and
-- re-run instead of re-typing the same filter set during every incident. This
-- is table-stakes query depth (Datadog "Saved Views", Grafana "Saved queries").
--
-- Scoping is per (tenant, owner): a search belongs to the user who created it
-- (owner = the gateway-verified JWT `sub`). `shared` promotes it to the whole
-- tenant so a team can standardise on a common view; unshared searches stay
-- private to their owner. `query_params` is the opaque filter set the client
-- replays against /api/v1/logs (service, level, q, regex, since, ...) — stored
-- as JSONB so new query params need no schema change.
CREATE TABLE IF NOT EXISTS saved_searches (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id    VARCHAR(255) NOT NULL DEFAULT 'default',
    owner        VARCHAR(255) NOT NULL,               -- JWT sub of the creator
    name         VARCHAR(255) NOT NULL,
    kind         VARCHAR(20)  NOT NULL DEFAULT 'logs', -- logs | traces
    query_params JSONB        NOT NULL DEFAULT '{}',
    shared       BOOLEAN      NOT NULL DEFAULT false,  -- visible tenant-wide, not just to owner
    created_at   TIMESTAMP WITH TIME ZONE DEFAULT now(),
    updated_at   TIMESTAMP WITH TIME ZONE DEFAULT now(),
    -- A user can't have two searches with the same name; different users in the
    -- same tenant (and different tenants) can.
    UNIQUE (tenant_id, owner, name)
);

-- Backs the common "my searches + my tenant's shared searches" list query.
CREATE INDEX IF NOT EXISTS idx_saved_searches_tenant_owner ON saved_searches (tenant_id, owner);
