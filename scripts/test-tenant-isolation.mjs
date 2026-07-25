#!/usr/bin/env node
// End-to-end cross-tenant isolation test against a running PulseTrace stack.
//
// Exercises the full Phase 1 isolation chain on the RUM pillar (chosen because
// the gateway writes rum_events synchronously, so there's no collector→ClickHouse
// lag to race):
//   1. mint a per-tenant ingestion key for tenant-a and tenant-b (admin API),
//   2. ingest a tenant-marked error event under each key (server resolves the
//      tenant from the key — never a client header),
//   3. read /api/v1/rum/errors as a user of each tenant and assert each sees ONLY
//      its own data.
//
// A leak (tenant A seeing tenant B's marker, or vice versa) fails the process
// with a non-zero exit. Prerequisites: the stack + gateway are up (same as
// scripts/run-e2e.sh). Usage: node scripts/test-tenant-isolation.mjs
const GATEWAY = process.env.GATEWAY_URL || 'http://127.0.0.1:8080';

const TENANT_A = 'tenant-a-' + Date.now();
const TENANT_B = 'tenant-b-' + Date.now();
const MARKER_A = 'ISO_MARKER_A_' + Date.now();
const MARKER_B = 'ISO_MARKER_B_' + Date.now();

function die(msg) {
  console.error('✗ FAIL: ' + msg);
  process.exit(1);
}

async function api(path, { method = 'GET', token, key, body } = {}) {
  const headers = { 'Content-Type': 'application/json' };
  if (token) headers['Authorization'] = 'Bearer ' + token;
  if (key) headers['Authorization'] = 'Bearer ' + key;
  const res = await fetch(GATEWAY + path, {
    method,
    headers,
    body: body ? JSON.stringify(body) : undefined,
  });
  return res;
}

async function adminToken() {
  const res = await api('/api/v1/auth/login', { method: 'POST', body: { username: 'admin', password: 'admin' } });
  if (!res.ok) die(`admin login returned ${res.status}`);
  const { token } = await res.json();
  if (!token) die('admin login returned no token');
  return token;
}

async function createIngestionKey(token, tenantID, scope = 'ingest') {
  const res = await api('/api/v1/admin/ingestion-keys', {
    method: 'POST',
    token,
    body: { name: `iso-test-${tenantID}-${scope}`, tenant_id: tenantID, scope },
  });
  if (res.status !== 201) die(`create ${scope} key for ${tenantID} returned ${res.status}`);
  const { key } = await res.json();
  if (!key) die(`create ${scope} key for ${tenantID} returned no plaintext key`);
  return key;
}

// registerAndLogin makes a viewer in the given tenant and returns their token.
async function registerAndLogin(tenantID) {
  const username = `iso-user-${tenantID}`;
  const password = 'iso-test-password';
  const reg = await api('/api/v1/auth/register', { method: 'POST', body: { username, password, tenant_id: tenantID } });
  // 201 created or 409 already-exists are both fine.
  if (reg.status !== 201 && reg.status !== 409) die(`register ${username} returned ${reg.status}`);
  const login = await api('/api/v1/auth/login', { method: 'POST', body: { username, password } });
  if (!login.ok) die(`login ${username} returned ${login.status}`);
  const { token } = await login.json();
  if (!token) die(`login ${username} returned no token`);
  return token;
}

async function ingestError(key, marker) {
  const res = await api('/api/v1/rum/ingest', {
    method: 'POST',
    key,
    body: [{ session_id: marker, type: 'error', path: '/iso', user_agent: 'iso-test', error_msg: marker }],
  });
  if (!res.ok) die(`rum ingest for ${marker} returned ${res.status}`);
}

// pollErrors reads /api/v1/rum/errors as the tenant's user until `want` appears
// (or times out), then returns the concatenated error messages seen.
async function pollErrors(token, want, timeoutMs = 20000) {
  const deadline = Date.now() + timeoutMs;
  let seen = '';
  while (Date.now() < deadline) {
    const res = await api('/api/v1/rum/errors', { token });
    if (res.ok) {
      const body = await res.json();
      const rows = Array.isArray(body.data) ? body.data : [];
      seen = rows.map((r) => r.error_msg || '').join('\n');
      if (seen.includes(want)) return seen;
    }
    await new Promise((r) => setTimeout(r, 1000));
  }
  return seen;
}

async function main() {
  console.log(`==> Cross-tenant isolation test (A=${TENANT_A}, B=${TENANT_B})`);
  const admin = await adminToken();

  const keyA = await createIngestionKey(admin, TENANT_A);
  const keyB = await createIngestionKey(admin, TENANT_B);
  const tokenA = await registerAndLogin(TENANT_A);
  const tokenB = await registerAndLogin(TENANT_B);

  await ingestError(keyA, MARKER_A);
  await ingestError(keyB, MARKER_B);
  console.log('    ingested one error marker per tenant');

  const seenByA = await pollErrors(tokenA, MARKER_A);
  if (!seenByA.includes(MARKER_A)) die(`tenant A could not see its OWN data (${MARKER_A})`);
  if (seenByA.includes(MARKER_B)) die(`LEAK: tenant A can see tenant B's data (${MARKER_B})`);
  console.log('    ✓ tenant A sees only its own errors');

  const seenByB = await pollErrors(tokenB, MARKER_B);
  if (!seenByB.includes(MARKER_B)) die(`tenant B could not see its OWN data (${MARKER_B})`);
  if (seenByB.includes(MARKER_A)) die(`LEAK: tenant B can see tenant A's data (${MARKER_A})`);
  console.log('    ✓ tenant B sees only its own errors');

  // Scope enforcement: a PUBLIC rum-scoped key can attribute RUM but must NOT be
  // usable to write server telemetry (/api/v1/logs).
  const rumKeyA = await createIngestionKey(admin, TENANT_A, 'rum');
  const logRes = await api('/api/v1/logs', {
    method: 'POST',
    key: rumKeyA,
    body: { service: 'iso', level: 'ERROR', message: 'should-be-rejected' },
  });
  if (logRes.status !== 403) die(`a rum-scoped key must be rejected (403) for server ingest, got ${logRes.status}`);
  const rumRes = await api('/api/v1/rum/ingest', {
    method: 'POST',
    key: rumKeyA,
    body: [{ session_id: 'scope', type: 'error', path: '/iso', user_agent: 'iso', error_msg: 'RUM_SCOPE_OK' }],
  });
  if (!rumRes.ok) die(`a rum-scoped key must be accepted for RUM ingest, got ${rumRes.status}`);
  console.log('    ✓ rum-scoped key rejected for server ingest, accepted for RUM');

  console.log('✓ PASS: cross-tenant isolation + scope enforcement hold');
}

main().catch((err) => die(err.stack || String(err)));
