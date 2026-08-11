-- Password-reset tokens (F18).
--
-- A reset token is delivered out-of-band (email) and never stored in the clear:
-- only a bcrypt hash of its secret half is kept, keyed by id, so a database leak
-- doesn't hand an attacker working reset links. Rows are single-use (used_at)
-- and short-lived (expires_at).
CREATE TABLE IF NOT EXISTS password_resets (
    id         UUID PRIMARY KEY,
    username   VARCHAR(255) NOT NULL,
    token_hash VARCHAR(255) NOT NULL,
    expires_at TIMESTAMP WITH TIME ZONE NOT NULL,
    used_at    TIMESTAMP WITH TIME ZONE,
    created_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_password_resets_username ON password_resets (username);
