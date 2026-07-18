-- Dynamic RBAC: roles are rows, not hardcoded Go strings. permissions is a JSON
-- array of permission strings; "*" grants everything (used by the built-in admin role).
CREATE TABLE IF NOT EXISTS roles (
    name VARCHAR(100) PRIMARY KEY,
    description TEXT,
    permissions JSONB NOT NULL DEFAULT '[]'::jsonb,
    is_system BOOLEAN NOT NULL DEFAULT false, -- built-in roles can't be deleted
    created_at TIMESTAMPTZ DEFAULT now(),
    updated_at TIMESTAMPTZ DEFAULT now()
);

-- ABAC: attribute-based policies evaluated on top of the RBAC permission check.
-- condition is an expr-lang boolean expression over subject/resource/action attributes,
-- e.g. `subject.role == "viewer" && action == "delete"`. Effect is applied when the
-- condition evaluates true; policies are evaluated in priority order (lowest first),
-- first match wins. No match = allow (RBAC already gated the coarse permission).
CREATE TABLE IF NOT EXISTS abac_policies (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name VARCHAR(255) UNIQUE NOT NULL,
    effect VARCHAR(10) NOT NULL DEFAULT 'deny', -- 'allow' | 'deny'
    resource VARCHAR(100) NOT NULL DEFAULT '*', -- resource type this policy applies to, or '*'
    condition TEXT NOT NULL,
    priority INT NOT NULL DEFAULT 100,
    enabled BOOLEAN NOT NULL DEFAULT true,
    created_at TIMESTAMPTZ DEFAULT now()
);

INSERT INTO roles (name, description, permissions, is_system) VALUES
    ('admin', 'Full access to all resources and settings', '["*"]'::jsonb, true),
    ('editor', 'Can read and modify telemetry/incidents, no admin access', '["read","write"]'::jsonb, true),
    ('viewer', 'Read-only access to telemetry and dashboards', '["read"]'::jsonb, true)
ON CONFLICT (name) DO NOTHING;

INSERT INTO abac_policies (name, effect, resource, condition, priority) VALUES
    ('no-non-admin-deletes', 'deny', '*', 'action == "delete" && subject.role != "admin"', 10),
    ('viewer-strictly-read-only', 'deny', '*', 'subject.role == "viewer" && action != "read"', 20),
    ('premium-only-raw-search', 'deny', 'search', 'subject.tier != "premium" && subject.role != "admin"', 30)
ON CONFLICT (name) DO NOTHING;
