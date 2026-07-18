-- The RBAC engine now supports resource-scoped permissions ("<resourceType>:<action>",
-- e.g. "incidents:write") in addition to the legacy bare "read"/"write" form (which
-- matches every resource type - kept only for backward compatibility). The original
-- seed gave the "editor" role bare ["read","write"], which under resource scoping
-- would still mean "write access to literally everything" including admin/settings/
-- roles/policies/users - the exact over-broad-permission gap this migration closes.
-- "viewer" is left as bare ["read"] intentionally: read-only-everywhere is safe.
UPDATE roles
SET permissions = '[
    "incidents:*", "alerts:*", "logs:*", "errors:*", "deployments:*",
    "services:*", "topology:*", "search:*", "slo:*", "synthetics:*",
    "analytics:read", "profiler:read", "rum:read"
]'::jsonb,
    updated_at = now()
WHERE name = 'editor' AND is_system = true;
