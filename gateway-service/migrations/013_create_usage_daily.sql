-- Per-tenant daily ingestion volume, the substrate both billing (2.3) and quota
-- enforcement (2.2) read. Live counters accumulate in Redis on the hot ingest
-- path (no per-request DB write); a background flusher mirrors them here, so this
-- table is the durable, queryable record of what each tenant ingested.
--
-- `signal` is one of: traces | metrics | logs | rum. count is the running total
-- for that (tenant, day, signal); the flusher SETs it to the current Redis value
-- (idempotent — Postgres mirrors Redis, it doesn't double-count).
CREATE TABLE IF NOT EXISTS usage_daily (
    tenant_id  VARCHAR(50) NOT NULL,
    day        DATE NOT NULL,
    signal     VARCHAR(20) NOT NULL,
    count      BIGINT NOT NULL DEFAULT 0,
    updated_at TIMESTAMP WITH TIME ZONE DEFAULT now(),
    PRIMARY KEY (tenant_id, day, signal)
);

-- Backs the common "usage for tenant X this billing period" and quota rollup queries.
CREATE INDEX IF NOT EXISTS idx_usage_daily_tenant_day ON usage_daily (tenant_id, day);
