# Settings — Implementation Plan

Spec: [../settings.md](../settings.md) · Service: **gateway** · Views: `frontend/src/components/Settings/*`

## Current state (grounded)
- Deep already: Users, Roles (RBAC), Policies (ABAC guided builder), Rate Limits, Audit (verify/export), API Keys (ingestion), SSO/SAML status, Alert Channels, Anomalies, Data & Privacy, Billing (plans/invoices/dunning), Security (MFA/sessions/password).

## E1 — Usage & quota dashboard · M  *(recommended first slice)*
- `GET /api/v1/usage/series?from=&to=` (per-signal usage series from metering) + reuse plan limits (`quota.LimitsForPlan`). Pure `projectOverage(series, limit, now)`. FE Usage tab: quota bars + trend + projected-overage callout (ties to F17 plan catalog). Parity: route consumed.
- Tests: `projectOverage`; e2e usage bars render.

## E3 — Programmatic API tokens · M
- **Data (gateway migration 025):** `api_tokens(id, tenant_id, name, token_hash, scopes[], created_by, expires_at, revoked_at)`. Bearer path in `AuthMiddleware` (constant-time, scope check, resolves tenant server-side) distinct from JWT/ingest key. CRUD `/api/v1/admin/api-tokens` (show-once). Audited. FE token panel.
- Tests: token hash/verify + scope check; e2e create→show-once→revoke.

## E2 — Integrations hub · M
- FE grid unifying channels/SSO/billing/deploy-gate status (reads existing config). Minimal backend status endpoint.

## E4 — SCIM / SAML admin UX · M
- Surface SCIM token status + SAML SP metadata/ACS URLs + IdP metadata upload + connection test (F18 endpoints exist). FE SSO/SCIM admin panel.

## E5 — Per-user notification preferences · S
- `user_notification_prefs` (channels + quiet hours); notifier honors them. FE preferences panel.

## E6 — Org profile, retention & residency · S
- Org settings store + per-signal retention → ClickHouse TTLs; FE Organization tab.

## Sequencing & gates
E1 → E3 → E2 → E4 → E5 → E6. Per slice: gateway build/vet/test (+ AuthMiddleware tests for E3), FE gates, parity, govulncheck, e2e; commit `feat(settings): …`.
