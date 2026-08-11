# Settings — Enhancement Spec

**Route:** `/settings` · **Component:** `frontend/src/components/Settings/*` · **Backend:** gateway auth/RBAC/ABAC/billing/audit/ingestion-keys, notification channels, anomaly config

## 1. Where it stands

Already deep (largely completed across Waves 2–4):
- **Users**, **Roles (RBAC)**, **Policies (ABAC)** with a guided builder + live validation, **Rate Limits**, **Audit Log** (tamper-evident verify + export), **API Keys** (ingestion), **SSO/SAML** status, **Alert Channels**, **Anomalies**, **Data & Privacy** (purge/close), **Billing & Usage** (plan compare, invoices, dunning), **Security (MFA)** (TOTP, recovery codes, session/device management, password change).

## 2. Market-ready gap

The admin surface is strong. What remains is the connective tissue an enterprise admin expects: a **usage/quota dashboard** with projections, an **integrations hub**, **programmatic API tokens** (distinct from ingestion keys), full **SCIM/SAML admin UX**, and per-user **notification preferences** — plus org-level polish (profile, retention, data residency).

## 3. Proposed enhancements

### E1. Usage & quota dashboard (F16 depth) · **M**
- **User value:** *"you're at 62% of your logs quota, projected to hit the cap on the 24th."*
- **What:** per-signal usage vs plan limit with quota bars + projected overage; ties to the plan catalog (F17) and quota enforcer.
- **Backend:** usage series + projection from metering (`/api/v1/usage` depth).
- **Frontend:** a Usage tab with bars, trend, and projected-overage callout.

### E2. Integrations hub · **M**
- **User value:** one place to connect/manage Slack, PagerDuty, Opsgenie, GitHub, Stripe, SSO, SCIM — with status.
- **What:** a grid of integrations showing connected/available + config, unifying today's scattered config.
- **Backend:** status reads from existing config; no new stores.
- **Frontend:** integrations grid tying into channels, SSO, billing, deploy-gates.

### E3. Programmatic API tokens · **M**
- **User value:** scripts/Terraform/CI can call the read/admin API with scoped, revocable tokens (not a user JWT, not an ingestion key).
- **What:** personal/service API tokens with scopes + expiry + revoke; audited.
- **Backend:** `api_tokens` (hashed) + a bearer path in AuthMiddleware; scope checks.
- **Frontend:** token management (create → show once → revoke).

### E4. SCIM / SAML admin UX · **M**
- **User value:** admins configure enterprise SSO/provisioning in-app, not via env vars.
- **What:** surface SCIM token issuance + SAML metadata/ACS URLs + IdP metadata upload and a connection test.
- **Backend:** expose SCIM token status + SAML SP metadata (F18 endpoints already exist).
- **Frontend:** an SSO/SCIM admin panel with copy-paste SP metadata + test.

### E5. Per-user notification preferences · **S**
- **User value:** each user chooses how/when they're notified; quiet hours.
- **What:** per-user channel routing + quiet hours, layered on the tenant channels.
- **Backend:** `user_notification_prefs`; notifier honors them.
- **Frontend:** a preferences panel.

### E6. Org profile, retention & data residency · **S**
- **User value:** set org name/logo, per-signal retention, and region — the enterprise procurement checklist.
- **What:** org profile + retention settings (drive ClickHouse TTLs) + documented residency.
- **Backend:** org settings store; retention applied to TTLs.
- **Frontend:** an Organization tab.

## 4. Market-ready DoD

- Admins see usage vs quota with projections, manage all integrations from one hub, issue scoped API tokens, and configure SSO/SCIM in-app.
- Users control their own notification preferences; org profile/retention/residency are configurable.

## 5. Suggested sequence

E1 (usage dashboard) → E3 (API tokens) → E2 (integrations hub) → E4 (SCIM/SAML UX) → E5 (notification prefs) → E6 (org/retention).
