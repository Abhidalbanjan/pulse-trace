# Metrics — Enhancement Spec

**Route:** `/metrics` · **Component:** `frontend/src/components/Metrics/MetricsView.tsx` · **Backend:** gateway metrics (`/api/v1/metrics`, `/api/v1/metrics/query`) over ClickHouse

## 1. Where it stands

- Query functions (rate / quantile / avg / min / max / sum) and **unit-aware charts** (F8).

## 2. Market-ready gap

You can query one metric with a function. The market bar (Datadog metrics, Grafana, Chronosphere) is a **metric explorer** to discover what exists, **saveable dashboards** with layout + template variables, and **math across series** — plus alerting straight from a query. Without dashboards, metrics can't be a daily home.

## 3. Proposed enhancements

### E1. Metric explorer / browser · **M**
- **User value:** discover available metrics + their labels without knowing names.
- **What:** a searchable list of metric names with label keys/values and a quick preview.
- **Backend:** `GET /api/v1/metrics/catalog` (distinct names + labels from ClickHouse).
- **Frontend:** explorer sidebar → click to graph.

### E2. Saveable dashboards · **L**
- **User value:** build once, revisit daily; the core of any metrics product.
- **What:** create/save/arrange panels (grid), each a metric query; per-tenant dashboards, shareable.
- **Backend:** `dashboards` + `dashboard_panels` (migration); CRUD endpoints.
- **Frontend:** dashboard grid with add/edit/drag panels + a shareable URL.

### E3. Template variables · **M**
- **User value:** one dashboard, any service/env via a dropdown.
- **What:** `$service`, `$env` variables bound to label values, applied across panels.
- **Backend:** variable value resolution from the metric catalog.
- **Frontend:** variable bar at the top of a dashboard.

### E4. Multi-series & math across metrics · **M**
- **User value:** `errors / requests` error-ratio, `a - b` deltas — real analysis.
- **What:** multiple series per panel + arithmetic between queries.
- **Backend:** extend `/metrics/query` to accept multiple series + an expression.
- **Frontend:** multi-query panel editor.

### E5. Alert from a metric query · **S**
- **User value:** graph it, then alert on it, in one place.
- **What:** "Create alert" from any panel → an alert rule pre-filled with the query + threshold.
- **Backend:** hook into the alert-rule model.
- **Frontend:** "Alert on this" affordance.

### E6. Anomaly & change overlay · **S**
- **User value:** see anomalies (F14 detector) and deploy markers on the same chart.
- **What:** overlay anomaly bands + deploy markers on metric panels.
- **Frontend:** reuse the anomaly + `<DeployMarkers>` overlays.

## 4. Market-ready DoD

- Users can discover metrics, build and save multi-panel dashboards with template variables, do math across series, and alert from any query.
- Anomalies and deploys are visible on the same charts.

## 5. Suggested sequence

E1 (explorer) → E2 (dashboards) → E3 (variables) → E4 (math) → E5 (alert-from-query) → E6 (overlays).
