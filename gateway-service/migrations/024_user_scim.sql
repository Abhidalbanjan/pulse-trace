-- SCIM 2.0 provisioning support (F18).
--
-- Enterprise IdPs (Okta, Azure AD, OneLogin) push user lifecycle over SCIM.
-- active supports deprovisioning without deleting history (a deactivated user
-- can't log in but their audit trail and incidents stay attributable);
-- external_id links a row back to the IdP's stable user id for idempotent sync.
ALTER TABLE users ADD COLUMN IF NOT EXISTS active BOOLEAN NOT NULL DEFAULT true;
ALTER TABLE users ADD COLUMN IF NOT EXISTS external_id VARCHAR(255);

CREATE INDEX IF NOT EXISTS idx_users_external_id ON users (external_id);
