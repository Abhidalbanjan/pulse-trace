-- Migration: 001_create_log_entries
-- Creates the core table for storing structured log events.

CREATE EXTENSION IF NOT EXISTS "pgcrypto"; -- for gen_random_uuid() fallback

CREATE TABLE IF NOT EXISTS log_entries (
    id          TEXT        PRIMARY KEY,
    service_name TEXT       NOT NULL,
    level       TEXT        NOT NULL CHECK (level IN ('DEBUG', 'INFO', 'WARNING', 'ERROR', 'FATAL')),
    message     TEXT        NOT NULL,
    trace_id    TEXT        DEFAULT '',
    span_id     TEXT        DEFAULT '',
    metadata    TEXT        DEFAULT '',   -- JSON blob
    timestamp   TIMESTAMPTZ NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Indexes for the most common query patterns.
CREATE INDEX IF NOT EXISTS idx_log_entries_service    ON log_entries (service_name);
CREATE INDEX IF NOT EXISTS idx_log_entries_level      ON log_entries (level);
CREATE INDEX IF NOT EXISTS idx_log_entries_timestamp  ON log_entries (timestamp DESC);
CREATE INDEX IF NOT EXISTS idx_log_entries_trace_id   ON log_entries (trace_id) WHERE trace_id != '';
