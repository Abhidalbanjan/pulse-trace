# Error Tracking — Enhancement Spec

**Route:** `/errors` · **Component:** `frontend/src/components/Errors/ErrorTrackingView.tsx` · **Backend:** gateway error-tracking (`/api/v1/errors/groups/{fingerprint}/timeline`, regression worker), Postgres `error_groups`

## 1. Where it stands

- Error groups with an **occurrence timeline** and **regression alerting** (new / regression detection, auto-reopen) (F11).

## 2. Market-ready gap

Grouping + regression is a strong core, but Sentry-class buyers expect a **workflow** (assign / resolve / ignore / snooze), **release health** (errors by version), **stack-trace source context** (and source maps for JS), and **impact** (users/sessions affected). Without these it's a list, not an error-management tool.

## 3. Proposed enhancements

### E1. Triage workflow: assign / resolve / ignore / snooze · **M**
- **User value:** errors move through states instead of piling up; noisy ones get muted.
- **What:** per-group status (unresolved/resolved/ignored/snoozed-until), assignee, and auto-reopen on regression (already partly there).
- **Backend:** extend `error_groups` with status/assignee/snooze; state-transition endpoints.
- **Frontend:** status controls + assignee + filters on the list.

### E2. Release health · **M**
- **User value:** *"v1.4.2 introduced 3 new error types and a 40% error-rate spike"* — catch bad releases fast.
- **What:** errors bucketed by release/version; new/regressed/resolved-in-release; crash-free rate.
- **Backend:** capture release on error events; per-release aggregation.
- **Frontend:** release health panel + regression-in-release badges.

### E3. Stack-trace source context & grouping detail · **M**
- **User value:** see the exact frames and code around the error, and why events grouped.
- **What:** structured stack frames with in-app vs library, and the grouping fingerprint explained.
- **Backend:** store/parse frames; expose them on the group detail.
- **Frontend:** stack-trace viewer with frame expansion.

### E4. Impact scoring (users / sessions affected) · **S**
- **User value:** prioritize by blast radius, not raw count.
- **What:** join errors to RUM sessions/users and tenants affected; show impact on the list.
- **Backend:** correlate error events to RUM/session ids.
- **Frontend:** "users affected" column + sort.

### E5. Issue creation & Slack · **S**
- **User value:** turn an error group into a Jira/GitHub issue or a Slack thread in one click.
- **What:** create an issue/notification from a group via the notification-channel integrations.
- **Backend:** reuse notification-service channels; issue-create adapter.
- **Frontend:** "Create issue" / "Send to Slack" on the group.

### E6. Similar-errors clustering · **S**
- **User value:** collapse near-duplicate groups; see families of related failures.
- **What:** cluster groups by message/stack similarity.
- **Backend:** similarity over fingerprints/messages.
- **Frontend:** "similar" section on the group.

## 4. Market-ready DoD

- Error groups have a full triage workflow, release health, source-context stack traces, and impact scoring.
- A group becomes an issue/Slack thread in one click.

## 5. Suggested sequence

E1 (workflow) → E2 (release health) → E3 (source context) → E4 (impact) → E5 (issue creation) → E6 (similar).
