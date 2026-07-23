#!/usr/bin/env bash
# Seeds demo data and runs the frontend end-to-end (Playwright) suite against a
# running PulseTrace stack. This is the check that catches frontend<->backend
# contract drift (response shapes, field names, proxy paths) that unit tests
# can't see.
#
# Prerequisites (the caller is responsible for these being up and reachable):
#   - the backend stack:  docker compose up -d
#   - the frontend:       (cd frontend && npm run dev)  on http://localhost:3000
#
# Usage:  scripts/run-e2e.sh
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
GATEWAY_URL="${GATEWAY_URL:-http://127.0.0.1:8080}"
FRONTEND_URL="${FRONTEND_URL:-http://localhost:3000}"

echo "==> Waiting for gateway ($GATEWAY_URL/healthz)"
for i in $(seq 1 60); do
  if curl -sf "$GATEWAY_URL/healthz" >/dev/null 2>&1; then echo "    gateway up"; break; fi
  [ "$i" = 60 ] && { echo "    ERROR: gateway never became healthy"; exit 1; }
  sleep 2
done

echo "==> Waiting for frontend ($FRONTEND_URL)"
for i in $(seq 1 60); do
  if curl -sf "$FRONTEND_URL" >/dev/null 2>&1; then echo "    frontend up"; break; fi
  [ "$i" = 60 ] && { echo "    ERROR: frontend never became reachable"; exit 1; }
  sleep 2
done

echo "==> Seeding demo data"
node "$ROOT/scripts/seed-demo-data.mjs"

echo "==> Running Playwright e2e suite"
cd "$ROOT/frontend"
npx playwright test "$@"
