-- Error Tracking workflow state. Error occurrences themselves live in ClickHouse
-- (otel_traces, grouped by fingerprint at query time); this table only tracks the
-- triage state (open/resolved/muted) a human has applied to a given error group.
CREATE TABLE IF NOT EXISTS error_groups (
    fingerprint VARCHAR(32) PRIMARY KEY,
    service VARCHAR(255) NOT NULL,
    operation VARCHAR(512) NOT NULL,
    message TEXT NOT NULL,
    status VARCHAR(20) NOT NULL DEFAULT 'open', -- open | resolved | muted
    resolved_by VARCHAR(255),
    resolved_at TIMESTAMP WITH TIME ZONE,
    created_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP
);
