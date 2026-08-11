# Synthetic Monitoring — Enhancement Spec

**Route:** `/synthetics` · **Component:** `frontend/src/components/Synthetics/SyntheticsView.tsx` · **Backend:** gateway synthetics (`/api/v1/synthetics/{tests,results}`), worker, ClickHouse `synthetic_results`

## 1. Where it stands

- **Multi-step checks** with a step/assertion builder, per-assertion evaluation, latency sparkline, last-failure column, and **edge-triggered failure paging** through the alert pipeline (F10).

## 2. Market-ready gap

The check engine is genuinely good. What's missing for a market-ready synthetics product (Datadog Synthetics, Checkly, Pingdom) is **multi-region probing**, **real browser checks**, **SSL/domain expiry**, an **uptime/SLA timeline**, and a **public status page** — the things buyers evaluate a synthetics tool on.

## 3. Proposed enhancements

### E1. Multi-region probing · **L**
- **User value:** *"up from US, failing from EU"* — catch regional and CDN issues.
- **What:** run each check from multiple named locations; per-region results + a latency-by-region view.
- **Backend:** region-tagged workers (or a location dimension) writing region into `synthetic_results`; assertions per region.
- **Frontend:** region selector + per-region status/latency.

### E2. Uptime / SLA timeline · **M**
- **User value:** the classic status timeline — 99.98% over 90 days, with incident bars.
- **What:** compute uptime % over ranges + a red/green availability strip per check.
- **Backend:** uptime aggregation over results.
- **Frontend:** availability timeline + SLA % tiles.

### E3. SSL cert & domain expiry monitoring · **S**
- **User value:** never get paged for an expired cert again.
- **What:** check TLS expiry + domain expiry for HTTP checks; warn N days ahead.
- **Backend:** cert inspection in the probe; expiry stored + alerted.
- **Frontend:** expiry column + warning badges.

### E4. Browser (headless) checks · **L**
- **User value:** test real user flows (login, checkout) in a real browser, not just HTTP.
- **What:** a headless-browser step type (Playwright) with actions + assertions; on failure capture a screenshot + trace/HAR.
- **Backend:** a browser-check runner; artifact storage (object store).
- **Frontend:** browser-step builder + failure screenshot viewer.

### E5. Public status page · **M**
- **User value:** a branded status page for customers — deflects support tickets.
- **What:** a public, tenant-scoped status page from check results (components, uptime, incidents).
- **Backend:** a public read endpoint (no auth, tenant-scoped by slug); reuse F19-style tenant scoping.
- **Frontend:** a standalone status page + Settings to configure it.

### E6. Scheduling & maintenance windows · **S**
- **User value:** control frequency; suppress alerts during known maintenance.
- **What:** per-check interval + maintenance windows (reuse the Alerts silences model).
- **Backend:** schedule/maintenance fields honored by the worker + notifier.
- **Frontend:** frequency + maintenance controls.

## 4. Market-ready DoD

- Checks run from multiple regions and (for flows) a real browser with failure artifacts.
- Uptime/SLA timelines, SSL/domain expiry monitoring, scheduling/maintenance, and a public status page all exist.

## 5. Suggested sequence

E2 (uptime timeline) → E3 (SSL expiry) → E1 (multi-region) → E6 (scheduling) → E5 (status page) → E4 (browser checks).
