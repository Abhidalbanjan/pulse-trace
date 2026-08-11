-- Time-based one-time-password MFA for dashboard users (F18).
--
-- mfa_secret holds the TOTP shared secret encrypted at rest with AES-256-GCM
-- (never the raw base32), so a database leak alone does not let an attacker
-- mint valid codes. mfa_recovery_codes is a JSON array of bcrypt hashes of
-- single-use backup codes, so a user who loses their authenticator can still
-- get in without the plaintext codes ever being stored.
ALTER TABLE users ADD COLUMN IF NOT EXISTS mfa_enabled BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE users ADD COLUMN IF NOT EXISTS mfa_secret TEXT;
ALTER TABLE users ADD COLUMN IF NOT EXISTS mfa_recovery_codes JSONB;
