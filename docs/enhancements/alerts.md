# Alerts — Enhancement Spec

**Route:** `/alerts` · **Component:** `frontend/src/components/Alerts/AlertsView.tsx` · **Backend:** gateway alert-rule CRUD (`/api/v1/admin/alert-rules`), alert-service, correlation `AlertRuleEvaluator`

## 1. Where it stands

- Alerts list (`/api/v1/alerts`, `/api/v1/alerts/{id}`) and user-defined alert rules (DB-backed, evaluated by correlation-service).
- Per-tenant **anomaly-detection config** exists (F14) but is a separate Settings tab, not an alert-rule type.
- Delivery via Slack/SMTP/PagerDuty/Opsgenie/webhook channels (F3).

## 2. Market-ready gap

The list is a firehose. The #1 complaint about every alerting product is **noise**. A market-ready alerting surface is defined by what it *suppresses*: grouping, dedup, silences, dependency-aware muting, and composite conditions — plus routing so the right team gets the right alert.

## 3. Proposed enhancements

### E1. Alert grouping & deduplication · **M**
- **User value:** 500 identical alerts become one card with a count.
- **What:** group by fingerprint (service + rule + labels); collapse duplicates; show count + first/last seen + sparkline.
- **Backend:** grouping key on alerts; `GET /api/v1/alerts?group=true`.
- **Frontend:** grouped list with expand-to-instances.

### E2. Silences & maintenance windows · **M**
- **User value:** deploying? mute the noise for 30 min without deleting rules.
- **What:** create a silence by matcher (service/label/rule) for a time window; scheduled recurring maintenance windows suppress + annotate.
- **Backend:** `alert_silences` table; the evaluator/notifier checks active silences before paging; `CRUD /api/v1/alerts/silences`.
- **Frontend:** "Silence" action on any alert/group; a silences manager.

### E3. Composite / multi-condition rules · **M**
- **User value:** *"page only if error-rate > 5% AND latency p99 > 1s for 5m"* — fewer false alarms.
- **What:** rule builder combining conditions (AND/OR), `for` duration, and severity; reuse the guided-builder pattern from ABAC policies.
- **Backend:** extend the alert-rule model + evaluator with condition trees + `for`.
- **Frontend:** guided condition builder (mirror `PoliciesPanel`).

### E4. Anomaly-based alert rules · **M**
- **User value:** alert on "unusual," not just static thresholds — the EWMA detector already exists.
- **What:** expose "anomaly" as a rule type (metric + sensitivity from F14 config) so it's a first-class alert, not a hidden setting.
- **Backend:** bridge `anomaly_config` + detector into the alert-rule evaluator.
- **Frontend:** anomaly rule type in the builder with a sensitivity slider.

### E5. Routing & escalation policies · **M**
- **User value:** the payments team gets payments alerts; unacked alerts escalate.
- **What:** route by matcher → channel/team; escalation chains with timeouts.
- **Backend:** `alert_routes` + escalation policy; notification-service honors them.
- **Frontend:** routing table editor.

### E6. Alert history, flap detection & test-fire · **S**
- **User value:** trust the rule before it pages you; catch flapping rules.
- **What:** per-rule fire history + flap score; a "test notification" button; per-alert runbook URL field.
- **Backend:** history query; test-send reuses channel test path.
- **Frontend:** rule detail with history sparkline + Test button + runbook link.

## 4. Market-ready DoD

- Duplicate alerts are grouped; silences/maintenance windows suppress noise on demand and on schedule.
- Rules support multi-condition + `for` + anomaly types via a guided builder.
- Alerts route to the right team with escalation; every rule is testable and shows history.

## 5. Suggested sequence

E1 (grouping) → E2 (silences) → E3 (composite builder) → E4 (anomaly type) → E6 (history/test) → E5 (routing).
