-- Tenants as a first-class entity.
--
-- Until now `tenant_id` was a free-form string stamped on users and telemetry
-- with nothing owning it — so there was nothing to bill, meter, or apply a plan
-- to. This table makes a tenant a real row: an organization with a plan, a
-- status, and (for SaaS) a link to its billing customer/subscription.
--
-- id is the slug used everywhere as tenant_id (VARCHAR to match the existing
-- users.tenant_id / telemetry tenant.id values), so existing data keeps working.
CREATE TABLE IF NOT EXISTS tenants (
    id                     VARCHAR(50) PRIMARY KEY,
    name                   VARCHAR(255) NOT NULL,
    -- Billing plan / entitlement tier. 'free' by default; billing (2.3) moves a
    -- tenant between plans, and quota enforcement (2.2) reads this.
    plan                   VARCHAR(50) NOT NULL DEFAULT 'free',
    -- 'active' | 'suspended' (e.g. non-payment) | 'deleted' (offboarded).
    status                 VARCHAR(50) NOT NULL DEFAULT 'active',
    -- SaaS billing links (null on enterprise/on-prem, where the manual billing
    -- provider sets the plan directly).
    stripe_customer_id     VARCHAR(255),
    stripe_subscription_id VARCHAR(255),
    created_at             TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP,
    updated_at             TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_tenants_stripe_customer ON tenants (stripe_customer_id) WHERE stripe_customer_id IS NOT NULL;

-- The built-in 'default' tenant that all pre-multi-tenant data belongs to.
INSERT INTO tenants (id, name, plan) VALUES ('default', 'Default', 'enterprise')
ON CONFLICT (id) DO NOTHING;

-- Backfill a tenant row for every tenant_id that already exists on a user, so
-- existing accounts map to a real tenant. Their name defaults to the id; an
-- admin can rename later.
INSERT INTO tenants (id, name)
SELECT DISTINCT tenant_id, tenant_id FROM users
ON CONFLICT (id) DO NOTHING;
