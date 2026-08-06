-- Per-tenant anomaly-detection tuning (ROAD_TO_100 · F14).
--
-- The EWMA anomaly detector's sensitivity was hardcoded constants. This table
-- makes the thresholds tunable per tenant from the UI: how far above baseline a
-- p99 spike, error-rate jump, or throughput drop must be before a service is
-- flagged PREDICTIVE_WARNING — and an on/off switch. A tenant with no row uses
-- the built-in defaults (the previous constants), so this is purely additive.
CREATE TABLE IF NOT EXISTS anomaly_config (
    tenant_id              VARCHAR(50) PRIMARY KEY,
    enabled                BOOLEAN NOT NULL DEFAULT true,
    p99_multiplier         DOUBLE PRECISION NOT NULL DEFAULT 1.6, -- fire when p99 >= this × baseline
    error_rate_jump_points DOUBLE PRECISION NOT NULL DEFAULT 5.0, -- absolute % points above baseline
    min_error_rate         DOUBLE PRECISION NOT NULL DEFAULT 5.0, -- floor: error rate must also clear this %
    throughput_drop_ratio  DOUBLE PRECISION NOT NULL DEFAULT 0.4, -- fire when throughput <= this × baseline
    updated_at             TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT now()
);
