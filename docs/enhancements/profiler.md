# Continuous Profiler — Enhancement Spec

**Route:** `/profiler` · **Component:** `frontend/src/components/Profiler/ContinuousProfilerView.tsx` · **Backend:** gateway profiler (`/api/v1/profiler/functions`, `/api/v1/profiler/diff`) over Pyroscope

## 1. Where it stands

- Native **flat profile** (top functions) and a **regression diff** vs the preceding window (F12) — the Pyroscope iframe was removed in favor of first-party rendering.

## 2. Market-ready gap

Top-functions + diff is a great start, but profiling buyers (Pyroscope/Grafana, Datadog Continuous Profiler, Polar Signals) expect an **interactive flame graph**, a **diff flame graph** (before/after a deploy), more **profile types** (memory/alloc, goroutine, lock/block), and **cost attribution**. A flat table alone doesn't sell profiling.

## 3. Proposed enhancements

### E1. Interactive flame graph · **M**
- **User value:** the iconic profiling view — click to zoom, hover for self/total, search frames.
- **What:** render the flamebearer (already fetched from Pyroscope) as an interactive SVG/canvas flame graph.
- **Backend:** none new (reuse the flamebearer response).
- **Frontend:** flame graph component with zoom/hover/search.

### E2. Diff flame graph · **M**
- **User value:** *"this function got 3× hotter after v1.4.2"* — visual regression, not just a table.
- **What:** a red/green diff flame between two windows (reuse the F12 diff data, rendered as a flame).
- **Backend:** the diff endpoint already computes per-frame deltas.
- **Frontend:** diff flame with ▲/▼ coloring + threshold.

### E3. Multiple profile types · **M**
- **User value:** CPU is table stakes; memory leaks and lock contention need alloc/goroutine/mutex profiles.
- **What:** a profile-type selector (cpu, alloc_space, inuse_space, goroutine, mutex/block).
- **Backend:** parameterize the Pyroscope query by profile type.
- **Frontend:** type dropdown feeding the flame/table.

### E4. CPU-over-time timeline with drill-in · **S**
- **User value:** see when the profile changed and jump to that window.
- **What:** a timeline of total CPU/allocations; select a window to profile it.
- **Backend:** time-series of profile totals.
- **Frontend:** timeline → sets the profile window.

### E5. Per-endpoint / labeled profiling · **M**
- **User value:** *"profile only the /checkout handler"* — profiling scoped to what matters.
- **What:** filter profiles by labels (endpoint, span) where Pyroscope tags allow.
- **Backend:** pass label selectors to Pyroscope.
- **Frontend:** label filter bar.

### E6. Cost attribution · **S**
- **User value:** translate CPU share into $ — the CFO-friendly view.
- **What:** map self-CPU share to an estimated cost per function/service (configurable $/core-hr).
- **Backend:** compute from profile share + a rate setting.
- **Frontend:** "$ / month" column on top functions.

## 4. Market-ready DoD

- An interactive flame graph and a diff flame graph are the default views.
- CPU, memory, and contention profiles are all available; profiles can be scoped by label and attributed to cost.

## 5. Suggested sequence

E1 (flame graph) → E2 (diff flame) → E3 (profile types) → E4 (timeline) → E5 (labels) → E6 (cost).
