-- User-defined alert rules, replacing "the only alert trigger is any ERROR
-- log" with real threshold/composite/anomaly-based rules.
--
-- `condition` is an expr-lang boolean expression (same engine already used
-- for ABAC policies — see gateway-service/internal/auth/rbac.go), evaluated
-- by correlation-service's AlertRuleEvaluator against a per-service metrics
-- env: error_rate, p99_latency_ms, p90_latency_ms, p50_latency_ms,
-- request_count, error_count, baseline_ratio (current/EWMA-baseline p99,
-- from the same anomaly detector baseline used for PREDICTIVE_WARNING).
--
-- Examples:
--   threshold:  "error_rate > 5"
--   composite:  "error_rate > 2 && p99_latency_ms > 800"
--   anomaly:    "baseline_ratio > 2.5"
CREATE TABLE IF NOT EXISTS alert_rules (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id VARCHAR(255) NOT NULL DEFAULT 'default',
    name VARCHAR(255) NOT NULL,
    description TEXT,
    service_name VARCHAR(255) NOT NULL DEFAULT '*', -- '*' = every service
    condition TEXT NOT NULL,
    severity VARCHAR(20) NOT NULL DEFAULT 'WARNING', -- DEBUG|INFO|WARNING|ERROR|FATAL — matches shared.LogLevel
    cooldown_seconds INT NOT NULL DEFAULT 900,       -- avoid alert storms; default 15 min between repeats
    enabled BOOLEAN NOT NULL DEFAULT true,
    created_at TIMESTAMP WITH TIME ZONE DEFAULT now(),
    updated_at TIMESTAMP WITH TIME ZONE DEFAULT now(),
    UNIQUE(tenant_id, name)
);

INSERT INTO alert_rules (tenant_id, name, description, service_name, condition, severity) VALUES
    ('default', 'high-error-rate', 'Error rate above 5% of requests', '*', 'error_rate > 5', 'CRITICAL'),
    ('default', 'high-p99-latency', 'p99 latency above 1000ms', '*', 'p99_latency_ms > 1000', 'WARNING'),
    ('default', 'latency-anomaly', 'p99 latency more than 2.5x its recent baseline', '*', 'baseline_ratio > 2.5', 'WARNING')
ON CONFLICT (tenant_id, name) DO NOTHING;
