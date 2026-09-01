-- The service topology, as tables (P1.4).
--
-- This is the SQL backing for shared/graph. Neo4j holds the same data in the
-- cluster; a single-binary deployment holds it here, and both go through the
-- same port so neither is the special case.
--
-- Two tables, because the Cypher being replaced is an adjacency list: ten
-- statements, every one depth-1 or a whole-tenant scan, no traversal anywhere.
-- A graph database was what it was built on, not what it needed.

-- One service. Rows are created by whichever write sees the service first —
-- an edge write, a state update, or a catalog edit — so every column past the
-- key must have a default.
CREATE TABLE IF NOT EXISTS graph_nodes (
    tenant_id  VARCHAR(50)  NOT NULL,
    name       TEXT         NOT NULL,
    -- Empty means "never evaluated", which GetGraph presents as HEALTHY. The
    -- two are stored distinctly on purpose: "no signal yet" and "checked and
    -- fine" look the same to a user and should not look the same to us.
    state      TEXT         NOT NULL DEFAULT '',
    team       TEXT         NOT NULL DEFAULT '',
    repo       TEXT         NOT NULL DEFAULT '',
    slack      TEXT         NOT NULL DEFAULT '',
    -- Catalog · E3 metadata.
    tier       TEXT         NOT NULL DEFAULT '',
    lifecycle  TEXT         NOT NULL DEFAULT '',
    -- JSON object, encoded exactly as the Neo4j property was: node properties
    -- there cannot hold nested maps, so links have always been a JSON string.
    -- Keeping the encoding means a topology migrated between the two backends
    -- reads back identically instead of needing a conversion nobody wrote.
    links      TEXT         NOT NULL DEFAULT '{}',
    updated_at TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    -- tenant_id leads the key, so two tenants running a service of the same
    -- name cannot collide. Neo4j got this from every MERGE remembering to key
    -- on {name, tenant}; here the database enforces it.
    PRIMARY KEY (tenant_id, name)
);

-- One DEPENDS_ON dependency, source depends on target.
CREATE TABLE IF NOT EXISTS graph_edges (
    tenant_id      VARCHAR(50) NOT NULL,
    source         TEXT        NOT NULL,
    target         TEXT        NOT NULL,
    -- JSON array of "incidentID::reason". A list rather than a single
    -- is_causal/reason pair because two incidents analysed at the same time
    -- both legitimately implicate the same edge, and a scalar makes the second
    -- silently overwrite the first.
    causal_entries TEXT        NOT NULL DEFAULT '[]',
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (tenant_id, source, target)
);

-- Reverse lookup: "what depends on this service" is the downstream query, and
-- without this it is a scan of the tenant's every edge.
CREATE INDEX IF NOT EXISTS idx_graph_edges_target ON graph_edges (tenant_id, target);
