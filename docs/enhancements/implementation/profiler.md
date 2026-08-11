# Continuous Profiler — Implementation Plan

Spec: [../profiler.md](../profiler.md) · Service: **gateway** (Pyroscope) · View: `frontend/src/components/Profiler/ContinuousProfilerView.tsx`

## Current state (grounded)
- Native flat profile + regression diff (F12): `/api/v1/profiler/functions`, `/api/v1/profiler/diff`; pure `aggregateSelf`, `topFunctions`, `diffProfiles`, `buildProfilerQuery`; reads Pyroscope flamebearer JSON.

## E1 — Interactive flame graph · M  *(recommended first slice — FE only)*
- Render the already-fetched flamebearer as an interactive flame graph (zoom/hover self-vs-total/search). New FE `<FlameGraph>` component; backend unchanged (`/api/v1/profiler/functions` already returns the tree data or extend it to return the flamebearer levels).
- Tests: pure `flattenFlamebearer(levels)`/layout helper (FE unit or Go if server-side); e2e flame renders + zoom.

## E2 — Diff flame graph · M
- Render the F12 diff (`diffProfiles` per-frame deltas) as a red/green diff flame. FE reuses `<FlameGraph>` with delta coloring.

## E3 — Multiple profile types · M
- Parameterize the Pyroscope query by `type` (cpu|alloc_space|inuse_space|goroutine|mutex); FE type dropdown. Extend `buildProfilerQuery(service, type, spanID)`.

## E4 — CPU-over-time timeline · S
- `GET /api/v1/profiler/timeline?service=&type=&range=` → totals over time; FE timeline → sets the profile window.

## E5 — Per-endpoint / labeled profiling · M
- Pass Pyroscope label selectors; FE label filter bar.

## E6 — Cost attribution · S
- Pure `costPerFunction(selfShare, ratePerCoreHr)`; FE "$/month" column.

## Sequencing & gates
E1 → E2 → E3 → E4 → E5 → E6. Per slice: gateway build/vet/test, FE gates, parity, govulncheck, e2e; commit `feat(profiler): …`.
