-- Error Tracking · E1 (triage workflow completion). The base table already
-- tracks open|resolved|muted plus who resolved it; this adds the two fields a
-- real triage queue needs:
--   * assignee     — who owns driving this error to resolution (ownership is the
--                    difference between a backlog and a to-do list).
--   * snoozed_until — a "come back to me later" that auto-expires: a group set to
--                     status 'snoozed' silently reads as open again once this
--                     timestamp passes (computed at read time; see effectiveStatus).
-- The status vocabulary therefore extends to: open | resolved | muted | snoozed.
-- Existing rows are unaffected (both columns nullable, no default status change).
ALTER TABLE error_groups ADD COLUMN IF NOT EXISTS assignee VARCHAR(255);
ALTER TABLE error_groups ADD COLUMN IF NOT EXISTS snoozed_until TIMESTAMP WITH TIME ZONE;

-- Partial index: the regression worker and list view only ever look up live
-- snoozes ("is this one still hidden?"), never the null majority.
CREATE INDEX IF NOT EXISTS idx_error_groups_snoozed_until
    ON error_groups(snoozed_until) WHERE snoozed_until IS NOT NULL;
