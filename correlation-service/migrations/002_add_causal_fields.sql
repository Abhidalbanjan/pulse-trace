-- Migration: 002_add_causal_fields
-- Adds AI-augmented causal root-cause analysis to incidents.
-- The causal column holds a CausalAnalysis JSON document populated
-- asynchronously by the causal-AI analyzer after incident upsert.

ALTER TABLE incidents
    ADD COLUMN IF NOT EXISTS causal             JSONB,
    ADD COLUMN IF NOT EXISTS causal_analyzed_at TIMESTAMPTZ;

CREATE INDEX IF NOT EXISTS idx_incidents_causal_analyzed_at
    ON incidents (causal_analyzed_at DESC NULLS LAST);
