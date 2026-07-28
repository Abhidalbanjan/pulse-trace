-- Let every authenticated user manage their OWN saved searches, including
-- read-only viewers. Saved searches are personal productivity items (a pinned
-- filter set), not tenant configuration, so the usual "viewers can't write /
-- only admins can delete" posture is wrong for this one resource. Ownership is
-- still enforced in SQL (owner = the JWT subject), so this only ever lets a user
-- touch their own rows — never a teammate's.
--
-- Two gates have to open, because RBAC and ABAC are independent layers:
--
-- 1. RBAC (coarse read/write per resource): grant the viewer role explicit
--    saved-searches:read / saved-searches:write so a viewer's write isn't
--    rejected before ABAC even runs. Editors/admins already have write.
UPDATE roles
SET permissions = permissions || '["saved-searches:read","saved-searches:write"]'::jsonb,
    updated_at = now()
WHERE name = 'viewer'
  AND NOT (permissions @> '["saved-searches:write"]'::jsonb);

-- 2. ABAC (attribute policies on top of RBAC): the seeded 'no-non-admin-deletes'
--    (priority 10) and 'viewer-strictly-read-only' (priority 20) policies would
--    otherwise deny a viewer's create and anyone-but-admin's delete. A higher-
--    priority (lower number) ALLOW scoped to the saved-searches resource wins
--    first for this resource only, leaving those global denies intact everywhere
--    else.
INSERT INTO abac_policies (name, effect, resource, condition, priority) VALUES
    ('saved-searches-self-service', 'allow', 'saved-searches', 'true', 5)
ON CONFLICT (name) DO NOTHING;
