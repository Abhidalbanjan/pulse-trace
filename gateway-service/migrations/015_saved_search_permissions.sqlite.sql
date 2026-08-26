-- SQLite counterpart of 015_saved_search_permissions.sql.
--
-- Same effect, different operators. The Postgres original uses two jsonb
-- operators SQLite has no equivalent for:
--
--   permissions || '[…]'::jsonb    append to a JSON array
--   permissions @> '[…]'::jsonb    containment test
--
-- SQLite's JSON1 expresses both, but not as operators: `json_insert(x,'$[#]',v)`
-- appends, and containment becomes an EXISTS over `json_each`. That is a
-- different enough shape that translating it with a regex would be guessing, so
-- this is written by hand — which is the escape hatch the dialect layer
-- deliberately keeps rather than widening its rules until they misfire on
-- something they only half understand.
--
-- The guard matters as much as the append: without it, re-running the migration
-- appends the same two permissions again, and `permissions` grows every time
-- the file is applied.

UPDATE roles
SET permissions = json_insert(
        json_insert(permissions, '$[#]', 'saved-searches:read'),
        '$[#]', 'saved-searches:write'),
    updated_at = CURRENT_TIMESTAMP
WHERE name = 'viewer'
  AND NOT EXISTS (
        SELECT 1 FROM json_each(roles.permissions)
        WHERE json_each.value = 'saved-searches:write'
      );

INSERT INTO abac_policies (name, effect, resource, condition, priority) VALUES
    ('saved-searches-self-service', 'allow', 'saved-searches', 'true', 5)
ON CONFLICT (name) DO NOTHING;
