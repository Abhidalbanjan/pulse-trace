-- Rate limit rules, previously hardcoded Go struct literals in cmd/main.go
-- (any change required a code redeploy). Now DB-backed: gateway-service polls
-- this table every few seconds and pushes updates into the live RateLimiter,
-- so an admin can add/edit/disable a rule from Settings with no redeploy.
CREATE TABLE IF NOT EXISTS rate_limit_rules (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name VARCHAR(255) UNIQUE NOT NULL,
    path_prefixes JSONB NOT NULL DEFAULT '[]'::jsonb,
    limit_count INT NOT NULL,
    window_seconds INT NOT NULL,
    priority INT NOT NULL DEFAULT 100, -- lower = matched first
    enabled BOOLEAN NOT NULL DEFAULT true,
    created_at TIMESTAMP WITH TIME ZONE DEFAULT now(),
    updated_at TIMESTAMP WITH TIME ZONE DEFAULT now()
);

INSERT INTO rate_limit_rules (name, path_prefixes, limit_count, window_seconds, priority) VALUES
    ('auth-login', '["/api/v1/auth/login", "/api/v1/auth/register"]'::jsonb, 10, 60, 10),
    ('telemetry-ingest', '["/v1/traces", "/v1/metrics", "/v1/logs", "/api/v1/logs"]'::jsonb, 6000, 60, 20),
    ('default', '["/"]'::jsonb, 600, 60, 100)
ON CONFLICT (name) DO NOTHING;
