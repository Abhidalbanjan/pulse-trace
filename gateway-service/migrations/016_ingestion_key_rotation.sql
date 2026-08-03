-- Ingestion-key rotation lineage.
--
-- Rotation mints a replacement key and revokes the old one after a grace window,
-- so a public RUM token embedded in already-served browser pages keeps working
-- long enough for clients to pick up the new one instead of breaking the moment
-- the key changes. Two things support that:
--
--  1. replaced_by links a rotated-out key to its successor, so the admin UI can
--     show "rotated → <new key>" lineage instead of an orphaned revoked row.
--  2. Grace-window revocation sets revoked_at to a *future* timestamp. The
--     resolver now treats a key as valid while revoked_at IS NULL OR is still in
--     the future, so the old token stays live until the grace expires. Those
--     grace-window keys aren't in the "revoked_at IS NULL" partial index, but the
--     UNIQUE index on key_hash still makes the hot-path lookup index-only, so no
--     index change is needed (a partial predicate can't use the non-immutable
--     now() anyway).
ALTER TABLE ingestion_keys
    ADD COLUMN IF NOT EXISTS replaced_by UUID REFERENCES ingestion_keys(id);
