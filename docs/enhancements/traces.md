# Distributed Traces — Enhancement Spec

**Route:** `/traces` · **Component:** `frontend/src/components/Traces/TraceWaterfall.tsx` · **Backend:** ClickHouse `otel_traces`, gateway; today the view reaches trace/log data via the search path

## 1. Where it stands

- A **trace waterfall** with an "Open in Explorer" pivot (F7).
- Trace data lives in ClickHouse (`otel_traces`); logs carry `trace_id` for correlation.

## 2. Market-ready gap

There's a waterfall, but no first-class **trace search** (find traces by service/operation/duration/tags/error), no **service map from traces**, and no **latency analysis** (distributions, flame graph, critical path). APM buyers (Datadog APM, Honeycomb, Tempo) live in these. This is the biggest depth gap of the tracing pillar.

## 3. Proposed enhancements

### E1. First-class trace search & explorer · **L**
- **User value:** *"show me error traces on checkout over 2s in the last hour"* — the core APM query.
- **What:** a trace search UI over ClickHouse: filter by service, operation, duration range, status/error, and span attributes; results table → waterfall.
- **Backend:** `GET /api/v1/traces?service=&op=&minDuration=&status=&tag=…` (tenant-scoped ClickHouse query).
- **Frontend:** search bar + results list + existing waterfall on select.

### E2. Latency distribution & percentile heatmap · **M**
- **User value:** find the slow tail, not just the average; click a bucket → exemplar traces.
- **What:** duration histogram + p50/p95/p99 per operation; heatmap over time; click-through to traces in that bucket.
- **Backend:** ClickHouse quantile/histogram aggregation per operation.
- **Frontend:** distribution chart with exemplar drill-in.

### E3. Span detail & events panel · **S**
- **User value:** see attributes, span events, and errors inline while reading a trace.
- **What:** a span inspector (attributes, events/logs on span, error stack) in the waterfall.
- **Backend:** include span attributes/events in the trace fetch.
- **Frontend:** side panel on span click.

### E4. Service map from traces · **M**
- **User value:** the real call graph with latency/error/throughput per edge — derived from actual spans, complementing Topology.
- **What:** aggregate parent→child service edges from traces with RED per edge.
- **Backend:** ClickHouse edge aggregation; `GET /api/v1/traces/service-map`.
- **Frontend:** a trace-derived map (can feed Topology's live view).

### E5. Critical-path & flame view · **M**
- **User value:** instantly see which spans dominate a request's latency.
- **What:** highlight the critical path; a flame-style aggregate across many traces of one operation.
- **Backend:** critical-path computation server-side or client from spans.
- **Frontend:** critical-path highlighting + aggregate flame.

### E6. Metric→trace exemplars · **S**
- **User value:** from a latency spike on a metric chart, jump to the traces behind it.
- **What:** link metric time buckets to exemplar trace ids.
- **Backend:** exemplar association (trace ids per bucket).
- **Frontend:** "view traces" from metric points.

## 4. Market-ready DoD

- Users can search traces by the dimensions APM users expect and land in a rich waterfall with span details.
- Latency distributions expose the slow tail with exemplar drill-in; a trace-derived service map and critical-path view exist.

## 5. Suggested sequence

E1 (trace search — the keystone) → E3 (span detail) → E2 (latency distribution) → E4 (service map) → E5 (critical path) → E6 (exemplars).
