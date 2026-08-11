-- Session tracking for revocation & device management (F18).
--
-- JWTs are stateless, so "log out this device" and "log out everywhere" need a
-- server-side record to revoke against. Each issued dashboard session gets a
-- row here keyed by the token's jti; AuthMiddleware rejects a token whose jti
-- has a revoked_at, and the Settings → Security UI lists and revokes them.
CREATE TABLE IF NOT EXISTS user_sessions (
    id           UUID PRIMARY KEY,           -- the JWT's jti claim
    username     VARCHAR(255) NOT NULL,
    tenant_id    VARCHAR(50) NOT NULL DEFAULT 'default',
    user_agent   TEXT,
    ip           VARCHAR(64),
    created_at   TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP,
    last_seen_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP,
    revoked_at   TIMESTAMP WITH TIME ZONE
);

CREATE INDEX IF NOT EXISTS idx_user_sessions_username ON user_sessions (username, created_at DESC);
-- The revocation cache only cares about recently-revoked sessions (older ones
-- are already past token expiry); this partial index keeps that refresh cheap.
CREATE INDEX IF NOT EXISTS idx_user_sessions_revoked ON user_sessions (revoked_at) WHERE revoked_at IS NOT NULL;
