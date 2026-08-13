-- Migration: 002_create_alert_silences
-- Alert silences / maintenance windows (Alerts · E2). A silence suppresses
-- alerts matching a matcher (any of service / level / message substring) during
-- an active [starts_at, ends_at] window — so a known-noisy deploy or a planned
-- maintenance doesn't page or flood the stream. Matching is evaluated at read
-- time (see silenceMatches); an expired silence simply stops matching.
CREATE TABLE IF NOT EXISTS alert_silences (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id    VARCHAR(50)  NOT NULL DEFAULT 'default',
    matcher      JSONB        NOT NULL,  -- {service?, level?, message_contains?}
    starts_at    TIMESTAMPTZ  NOT NULL,
    ends_at      TIMESTAMPTZ  NOT NULL,
    created_by   VARCHAR(255),
    created_at   TIMESTAMPTZ  NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_alert_silences_tenant ON alert_silences (tenant_id);
CREATE INDEX IF NOT EXISTS idx_alert_silences_active ON alert_silences (tenant_id, ends_at);
