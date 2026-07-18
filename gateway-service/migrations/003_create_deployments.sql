-- Deployment markers for Deployment Tracking. A row is created whenever a
-- service is released; the Service Page uses these to overlay "what changed
-- and when" against the RED metrics computed from ClickHouse otel_traces.
CREATE TABLE IF NOT EXISTS deployments (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    service VARCHAR(255) NOT NULL,
    version VARCHAR(255) NOT NULL,
    git_sha VARCHAR(64),
    environment VARCHAR(50) NOT NULL DEFAULT 'production',
    deployed_by VARCHAR(255),
    notes TEXT,
    deployed_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_deployments_service_time ON deployments (service, deployed_at DESC);
