-- Tamper-evidence for the audit trail (F20).
--
-- Each audit row is chained to its predecessor by SHA-256: entry_hash =
-- SHA256(prev_hash || canonical(row fields)). Deleting, reordering, or editing
-- any row breaks entry_hash for that row and every row after it, so a verifier
-- replaying the chain can prove the log has not been altered since it was
-- written — the bar a SOC2/ISO auditor asks for.
--
-- Columns are nullable so the migration applies without a rewrite; existing
-- pre-tamper-evidence rows are back-filled once at startup (see
-- auth.BackfillAuditChain), after which every row carries a hash.
ALTER TABLE audit_log ADD COLUMN IF NOT EXISTS prev_hash  CHAR(64);
ALTER TABLE audit_log ADD COLUMN IF NOT EXISTS entry_hash CHAR(64);

-- entry_hash is unique once populated: two rows sharing a hash would mean a
-- collision or a copied row, either of which is a tamper signal.
CREATE UNIQUE INDEX IF NOT EXISTS idx_audit_log_entry_hash ON audit_log (entry_hash);
