-- Migration: 003_create_slo_tables
-- Stores SLO definitions, periodic SLI snapshots, and error budget breach alerts.

-- ── SLO Definitions ─────────────────────────────────────────────────────────
-- Configurable SLO targets per service (e.g. payment-service → 99.9%).
CREATE TABLE IF NOT EXISTS slo_definitions (
    id            TEXT           PRIMARY KEY,
    service_name  TEXT           NOT NULL UNIQUE,
    slo_target    NUMERIC(6,3)  NOT NULL DEFAULT 99.900,  -- e.g. 99.9%
    sli_type      TEXT           NOT NULL DEFAULT 'availability'
                                 CHECK (sli_type IN ('availability', 'latency')),
    window_days   INT            NOT NULL DEFAULT 30,
    created_at    TIMESTAMPTZ    NOT NULL DEFAULT NOW(),
    updated_at    TIMESTAMPTZ    NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_slo_definitions_service ON slo_definitions (service_name);

-- ── SLO Snapshots ───────────────────────────────────────────────────────────
-- Periodic SLI measurements computed by the background worker (~every 60s).
CREATE TABLE IF NOT EXISTS slo_snapshots (
    id            BIGSERIAL      PRIMARY KEY,
    service_name  TEXT           NOT NULL,
    sli_value     NUMERIC(8,4)  NOT NULL,  -- e.g. 99.8523%
    total_events  BIGINT         NOT NULL DEFAULT 0,
    error_events  BIGINT         NOT NULL DEFAULT 0,
    window_start  TIMESTAMPTZ    NOT NULL,
    window_end    TIMESTAMPTZ    NOT NULL,
    snapshot_at   TIMESTAMPTZ    NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_slo_snapshots_service     ON slo_snapshots (service_name);
CREATE INDEX IF NOT EXISTS idx_slo_snapshots_snapshot_at ON slo_snapshots (snapshot_at DESC);
CREATE INDEX IF NOT EXISTS idx_slo_snapshots_window      ON slo_snapshots (service_name, window_start, window_end);

-- ── SLO Budget Alerts ───────────────────────────────────────────────────────
-- Records of when burn rate thresholds were breached.
CREATE TABLE IF NOT EXISTS slo_budget_alerts (
    id                   TEXT           PRIMARY KEY,
    service_name         TEXT           NOT NULL,
    burn_rate            NUMERIC(8,2)   NOT NULL,
    budget_remaining_pct NUMERIC(6,3)   NOT NULL,
    severity             TEXT           NOT NULL CHECK (severity IN ('INFO', 'WARNING', 'ERROR', 'CRITICAL')),
    message              TEXT           NOT NULL,
    triggered_at         TIMESTAMPTZ    NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_slo_budget_alerts_service     ON slo_budget_alerts (service_name);
CREATE INDEX IF NOT EXISTS idx_slo_budget_alerts_triggered   ON slo_budget_alerts (triggered_at DESC);
CREATE INDEX IF NOT EXISTS idx_slo_budget_alerts_severity    ON slo_budget_alerts (severity);
