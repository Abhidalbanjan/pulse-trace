-- P3.2 · the SQL endpoint's control plane.
--
-- query_audit exists because opening SQL to users changes what "who read what"
-- means. Until now every read was shaped by a Go handler, so the route name
-- described the access. A user-authored statement does not: the same endpoint
-- can read one row or a million, from one relation or six, and the only record
-- of which is the statement itself. Auditing the route would record nothing
-- worth having.
CREATE TABLE IF NOT EXISTS query_audit (
    id              BIGSERIAL PRIMARY KEY,
    tenant_id       VARCHAR(50)  NOT NULL,
    actor           VARCHAR(255) NOT NULL,
    -- The statement as the user wrote it. Stored verbatim rather than
    -- normalised: an investigation needs to see what was actually sent,
    -- including the parts that were refused.
    statement       TEXT         NOT NULL,
    -- Relations the validator resolved. Denormalised on purpose — reconstructing
    -- this later would mean re-parsing historical SQL with a parser that has
    -- since changed, and getting a different answer than the one that was acted
    -- on at the time.
    relations       TEXT[]       NOT NULL DEFAULT '{}',
    rows_scanned    BIGINT       NOT NULL DEFAULT 0,
    rows_returned   BIGINT       NOT NULL DEFAULT 0,
    duration_ms     INTEGER      NOT NULL DEFAULT 0,
    -- outcome is 'ok', 'rejected' (policy refused it) or 'error'. Refusals are
    -- recorded, not just successes: a run of rejections against system schemas
    -- is the signal you most want and the one a success-only log discards.
    outcome         VARCHAR(20)  NOT NULL,
    -- The machine-readable rejection reason from sqlq, when there was one.
    reason          VARCHAR(64),
    created_at      TIMESTAMPTZ  NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_query_audit_tenant_time
    ON query_audit (tenant_id, created_at DESC);
-- Supports "show me everything this account ran", the first question asked
-- during an investigation.
CREATE INDEX IF NOT EXISTS idx_query_audit_actor_time
    ON query_audit (tenant_id, actor, created_at DESC);
-- Supports "what is being refused, and is it the same person each time".
CREATE INDEX IF NOT EXISTS idx_query_audit_outcome
    ON query_audit (tenant_id, outcome, created_at DESC)
    WHERE outcome <> 'ok';

-- Per-role budget overrides. Absent row means the compiled default applies, so
-- the table is an escape hatch rather than a required configuration step —
-- a query surface that only works after someone populates a settings table is
-- one that silently does not work.
CREATE TABLE IF NOT EXISTS query_budgets (
    tenant_id             VARCHAR(50) NOT NULL,
    role                  VARCHAR(50) NOT NULL,
    max_rows_per_relation INTEGER,
    max_total_rows        INTEGER,
    max_wall_clock_ms     INTEGER,
    updated_at            TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (tenant_id, role)
);
