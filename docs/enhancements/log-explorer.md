# Log Explorer — Enhancement Spec

**Route:** `/explorer` · **Component:** `frontend/src/components/Explorer/ExplorerView.tsx` · **Backend:** gateway logs + Quickwit search (`/api/v1/logs`, `/api/v1/logs/{id}/context`, `/api/v1/search/pulsetrace-logs/search`, saved searches)

## 1. Where it stands

- Full-text/regex search over Quickwit, **saved searches**, a **surrounding-context view**, and **shareable query URLs** (F6). Log→trace pivot (F7).

## 2. Market-ready gap

Search + context is solid, but at real cardinality users drown. The market bar (Datadog Logs, Grafana Loki, Coralogix) is defined by **facets**, **pattern clustering**, **live tail**, and turning logs into **metrics and alerts** — the tools that turn millions of lines into a handful of insights.

## 3. Proposed enhancements

### E1. Pattern clustering · **L**
- **User value:** *"10,432 logs collapse into 12 patterns"* — see the shape of your logs instantly.
- **What:** cluster similar messages (Drain-style tokenization) into patterns with counts + trend; click to expand instances.
- **Backend:** pattern extraction over the result set; `GET /api/v1/logs/patterns?q=…`.
- **Frontend:** a "Patterns" tab beside raw results.

### E2. Facet / field sidebar · **M**
- **User value:** filter by service/level/host with value counts, no query syntax.
- **What:** a left rail of fields with top values + counts; click to add to the query.
- **Backend:** facet aggregation from Quickwit; `GET /api/v1/logs/facets?q=…`.
- **Frontend:** collapsible facet sidebar wired to the query.

### E3. Live tail · **M**
- **User value:** `tail -f` for production, filtered.
- **What:** stream new matching logs in real time; pause/resume; highlight.
- **Backend:** SSE/poll tail endpoint honoring the current query + tenant.
- **Frontend:** "Live" toggle with auto-scroll.

### E4. Log-to-metric & alert-from-search · **M**
- **User value:** turn "count of ERROR from payment" into a metric + an alert, from the search bar.
- **What:** save a query as a generated metric (count/rate) and/or create an alert rule from it.
- **Backend:** log-metric definition evaluated on a schedule → metrics store; hook into the alert-rule model.
- **Frontend:** "Create metric" / "Alert on this" from a saved search.

### E5. Histogram with brush-to-zoom · **S**
- **User value:** see volume over time; drag to zoom the range.
- **What:** a per-bucket count histogram above results; brushing sets the time range.
- **Backend:** date-histogram aggregation.
- **Frontend:** histogram bound to the range picker.

### E6. Export · **S**
- **User value:** hand logs to an auditor or a notebook.
- **What:** export the current result set as NDJSON/CSV (capped/streamed).
- **Backend:** streaming export endpoint.
- **Frontend:** "Export" button.

## 4. Market-ready DoD

- Millions of logs are navigable via patterns + facets, not scrolling.
- Live tail works; a search can become a metric and an alert; results export.

## 5. Suggested sequence

E2 (facets) → E5 (histogram) → E1 (patterns) → E3 (live tail) → E4 (log-to-metric) → E6 (export).
