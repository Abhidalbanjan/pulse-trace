-- Migration: 001_create_incidents
-- Stores correlated incidents and their constituent alerts.

CREATE TABLE IF NOT EXISTS incidents (
    id            TEXT           PRIMARY KEY,
    title         TEXT           NOT NULL,
    root_cause    TEXT           NOT NULL DEFAULT '',
    status        TEXT           NOT NULL DEFAULT 'OPEN' CHECK (status IN ('OPEN', 'RESOLVED')),
    severity      TEXT           NOT NULL CHECK (severity IN ('DEBUG', 'INFO', 'WARNING', 'ERROR', 'FATAL')),
    alert_count   INT            NOT NULL DEFAULT 0,
    started_at    TIMESTAMPTZ    NOT NULL,
    resolved_at   TIMESTAMPTZ,
    created_at    TIMESTAMPTZ    NOT NULL DEFAULT NOW(),
    updated_at    TIMESTAMPTZ    NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_incidents_status     ON incidents (status);
CREATE INDEX IF NOT EXISTS idx_incidents_severity   ON incidents (severity);
CREATE INDEX IF NOT EXISTS idx_incidents_started_at ON incidents (started_at DESC);

-- Join table: which alerts belong to which incident.
CREATE TABLE IF NOT EXISTS incident_alerts (
    incident_id  TEXT        NOT NULL REFERENCES incidents(id) ON DELETE CASCADE,
    alert_id     TEXT        NOT NULL,
    service_name TEXT        NOT NULL,
    level        TEXT        NOT NULL,
    message      TEXT        NOT NULL,
    triggered_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (incident_id, alert_id)
);

CREATE INDEX IF NOT EXISTS idx_incident_alerts_incident ON incident_alerts (incident_id);
CREATE INDEX IF NOT EXISTS idx_incident_alerts_alert    ON incident_alerts (alert_id);
