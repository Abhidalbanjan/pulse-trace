-- Per-tenant alert delivery channels (ROAD_TO_100 · F3).
--
-- Moves channel configuration out of notification-service env vars into a
-- tenant-scoped, UI-manageable store. Secret fields inside `config` (webhook
-- URLs, SMTP passwords, PagerDuty/Opsgenie keys) are encrypted at rest with
-- AES-256-GCM before they land here (see notification-service/internal/channels
-- crypto); the plaintext is never stored and never returned by the API.
--
-- The env-var channels remain a valid global fallback for backward compatibility
-- and for the always-on log channel — a tenant with no rows here still gets the
-- env-configured delivery, so this is purely additive.
CREATE TABLE IF NOT EXISTS notification_channels (
    id         VARCHAR(64) PRIMARY KEY,
    tenant_id  VARCHAR(50) NOT NULL DEFAULT 'default',
    name       VARCHAR(255) NOT NULL,
    type       VARCHAR(32) NOT NULL, -- slack | email | pagerduty | opsgenie | webhook
    config     JSONB NOT NULL DEFAULT '{}'::jsonb, -- non-secret fields in clear; secrets AES-GCM-encrypted
    enabled    BOOLEAN NOT NULL DEFAULT true,
    created_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT now(),
    updated_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT now()
);

-- Delivery resolves a tenant's enabled channels on the hot notification path.
CREATE INDEX IF NOT EXISTS idx_notification_channels_tenant ON notification_channels (tenant_id, enabled);
