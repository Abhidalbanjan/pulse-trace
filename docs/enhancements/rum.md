# Real User Monitoring — Enhancement Spec

**Route:** `/rum` · **Component:** `frontend/src/components/RUM/RUMView.tsx` · **Backend:** gateway RUM (`/api/v1/rum/{ingest,analytics,trends,sessions,devices,errors}`) over ClickHouse `rum_events`

## 1. Where it stands

- **Web-vitals trends** (p75), **session stitching**, **device breakdown**, RUM errors, and time-ranged analytics (F9).

## 2. Market-ready gap

Strong aggregate RUM. What converts frontend teams (Datadog RUM, LogRocket, Sentry) is the **individual session view** (what did the user actually experience), **frustration signals** (rage/dead clicks), **RUM→backend-trace linking**, and **user-journey funnels**. Aggregates tell you *that* it's slow; these tell you *why* for a real user.

## 3. Proposed enhancements

### E1. Session timeline (lightweight replay) · **L**
- **User value:** step through one user's session — page loads, route changes, resources, errors, timing.
- **What:** a per-session waterfall of events (not pixel replay): navigations, long tasks, XHR/fetch, errors, web-vitals per view.
- **Backend:** session-detail endpoint assembling ordered events for a session id.
- **Frontend:** session timeline view linked from the sessions table.

### E2. Frustration signals · **M**
- **User value:** surface rage clicks, dead clicks, and error clicks — where users struggle.
- **What:** detect frustration patterns from RUM events; list the worst pages.
- **Backend:** capture click/interaction events; detection query.
- **Frontend:** frustration panel + per-session markers.

### E3. RUM → backend trace linking · **M**
- **User value:** from a slow page load, jump to the backend trace that made it slow — the full-stack story.
- **What:** correlate RUM resource/XHR events to `trace_id` and deep-link to Traces.
- **Backend:** propagate/join trace ids on RUM events.
- **Frontend:** "view backend trace" from a session/resource.

### E4. Core Web Vitals by page / geo / device · **M**
- **User value:** CWV percentiles sliced by URL, country, and device — the SEO/UX report that matters.
- **What:** per-dimension CWV (LCP/INP/CLS) with p75 and pass/fail vs Google thresholds.
- **Backend:** grouped CWV aggregation.
- **Frontend:** breakdown tables + a geo map.

### E5. User-journey funnels · **M**
- **User value:** *"40% drop between cart and checkout"* — connect performance to conversion.
- **What:** define an ordered step funnel over RUM view events; show conversion + drop-off.
- **Backend:** funnel aggregation over sessions.
- **Frontend:** funnel builder + chart.

### E6. Real-time active users · **S**
- **User value:** a live pulse of who's on the app right now.
- **What:** rolling count of active sessions + top live pages.
- **Backend:** recent-window session count.
- **Frontend:** live tile.

## 4. Market-ready DoD

- Any session can be opened as a timeline; frustration signals surface struggling pages.
- RUM links to backend traces; CWV slices by page/geo/device; funnels tie performance to conversion.

## 5. Suggested sequence

E1 (session timeline) → E3 (RUM→trace) → E2 (frustration) → E4 (CWV breakdowns) → E5 (funnels) → E6 (live users).
