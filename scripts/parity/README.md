# Backend ↔ Frontend Parity Gate (ROAD_TO_100 · F0.1)

Enforces the rule that **every backend capability is usable from the UI** — the
keystone of keeping backend and frontend in sync. Runs as a CI job
(`.github/workflows/ci.yml` → `parity`) and locally.

```bash
node scripts/parity/check-parity.mjs          # fail (exit 1) on drift
node scripts/parity/check-parity.mjs --report # just regenerate PARITY_REPORT.md
```

## What it does

1. **Extracts backend routes** from Go `HandleFunc` registrations across all services
   (`extract.mjs`), normalizing path params to `:p`.
2. **Extracts frontend calls** — every `/api/...` literal referenced in `frontend/src`.
3. **Cross-references** them (segment-wise, so a dynamic `${action}` matches a
   literal `resolve`/`mute`/`reopen`), classifying each backend route as:
   - **consumed** — the UI calls it ✅
   - **no-UI-by-design** — listed in `registry.json → uiNone` (ingest, webhooks,
     health, agent/control-plane internals) ✅
   - **known orphan** — listed in `registry.json → knownOrphans`: a real capability
     with no UI *yet* (the Wave-2 backlog), allowed but frozen 🟡
   - **new orphan** — none of the above ❌ **fails the build**

## The ratchet (why it can't regress)

- Add a backend route with no UI and no registry entry → **build fails**. You must
  either ship the UI, mark it `uiNone`, or add it to `knownOrphans` with its feature.
- Ship the UI for a known orphan but forget to delete its `knownOrphans` entry →
  **build fails** ("stale entry"). The backlog can only shrink.
- Frontend calls a path no backend serves (typo/dead call) → **build fails**.

So parity coverage only moves up. Today: **74%** (see `PARITY_REPORT.md`).

## Working with it

- **Shipping a new feature?** Add the endpoint *and* its UI in the same PR — the gate
  passes automatically (the UI call marks the route consumed).
- **Closing a Wave-2 orphan?** Build the UI, then delete that line from
  `knownOrphans`. Re-run; coverage ticks up.
- **New agent/ingest/webhook endpoint (no UI)?** Add it to `uiNone` with a reason.

`PARITY_REPORT.md` (repo root, generated) is the live burn-down: it groups the
remaining orphans by their ROAD_TO_100 feature (F1 self-healing, F2 SLO, …).

## Known extraction limits

Regex-based, so it can miss a route registered in an unusual way or a URL built by
opaque string concatenation. When the UI genuinely calls an endpoint the extractor
can't see, prefer fixing the call to use a readable literal; the segment-wise matcher
already handles template literals like `` `/api/v1/x/${id}` `` and `` `${base}/${action}` ``.
