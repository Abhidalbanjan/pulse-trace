#!/usr/bin/env node
// Seeds the running PulseTrace stack with realistic demo data so every page
// renders something meaningful. Idempotent-ish: server-side ON CONFLICT / IF
// NOT EXISTS handle re-runs. Requires the stack to be up (docker compose up).
//
//   node scripts/seed-demo-data.mjs
//
// Env overrides: GATEWAY_URL (default http://127.0.0.1:8080),
//                VECTOR_URL  (default http://127.0.0.1:8383)

const GATEWAY = process.env.GATEWAY_URL || 'http://127.0.0.1:8080';
const VECTOR = process.env.VECTOR_URL || 'http://127.0.0.1:8383';
const OTLP = `${GATEWAY}/v1/traces`; // gateway proxies to the otel-collector

const SERVICES = ['gateway-service', 'cart-service', 'payment-service', 'catalog-service', 'notification-service', 'postgres-primary'];
const OPS = {
  'gateway-service': ['GET /api/v1/products', 'POST /api/v1/checkout'],
  'cart-service': ['POST /checkout', 'validateCart', 'GET /cart'],
  'payment-service': ['POST /charge', 'chargeCard', 'refund'],
  'catalog-service': ['listProducts', 'searchProducts'],
  'notification-service': ['sendReceipt', 'sendEmail'],
  'postgres-primary': ['SELECT orders', 'UPDATE ledger', 'INSERT payments'],
};
const ERROR_MESSAGES = [
  'Failed to acquire DB connection: timeout after 5000ms',
  'NullPointerException at CartService.java:88',
  'Upstream timeout after 3 retries',
  'SMTP connection refused',
  'Elasticsearch query timeout',
];
const INFO_MESSAGES = [
  'Request completed successfully', 'Connection pool at 82% utilization',
  'Retrying upstream call (attempt 2/3)', 'Cache miss for key user:88213',
  'Published event order.created', 'Health check passed', 'Session refreshed for user 4471',
];

const randHex = (n) => Array.from({ length: n }, () => Math.floor(Math.random() * 16).toString(16)).join('');
const pick = (a) => a[Math.floor(Math.random() * a.length)];

async function login() {
  const res = await fetch(`${GATEWAY}/api/v1/auth/login`, {
    method: 'POST', headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ username: 'admin', password: 'admin' }),
  });
  const j = await res.json();
  if (!j.token) throw new Error('login failed: ' + JSON.stringify(j));
  return j.token;
}

async function authed(token, path, body, method = 'POST') {
  const res = await fetch(`${GATEWAY}${path}`, {
    method,
    headers: { 'Content-Type': 'application/json', Authorization: `Bearer ${token}` },
    body: body ? JSON.stringify(body) : undefined,
  });
  return res.status;
}

async function seedLogs() {
  let n = 0;
  for (const svc of SERVICES) {
    for (let i = 0; i < 12; i++) {
      await fetch(VECTOR, {
        method: 'POST', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ service: svc, level: pick(['INFO', 'INFO', 'INFO', 'WARNING', 'DEBUG']), message: pick(INFO_MESSAGES), trace_id: randHex(32), metadata: { seeded: 'true' } }),
      }); n++;
    }
  }
  // ERROR/FATAL bursts to trigger alert-service -> incidents
  for (const svc of ['payment-service', 'cart-service', 'notification-service']) {
    for (let i = 0; i < 3; i++) {
      await fetch(VECTOR, {
        method: 'POST', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ service: svc, level: i === 0 ? 'FATAL' : 'ERROR', message: pick(ERROR_MESSAGES), trace_id: randHex(32), metadata: { seeded: 'true' } }),
      }); n++;
    }
  }
  console.log(`  logs: sent ${n} entries via Vector`);
}

function buildTrace(rootService, hasError) {
  const tid = randHex(32);
  const rootSpanId = randHex(16);
  // Current-ish timestamps (last 5 min) so time-windowed Services/Metrics queries include them.
  const startNs = BigInt(Date.now()) * 1000000n - BigInt(Math.floor(Math.random() * 300)) * 1000000000n;
  const rootDurMs = 20 + Math.floor(Math.random() * 400);
  const mkSpan = (name, parentId, id, offMs, durMs, isErr) => ({
    traceId: tid, spanId: id, parentSpanId: parentId || undefined, name, kind: 'SPAN_KIND_SERVER',
    startTimeUnixNano: (startNs + BigInt(offMs) * 1000000n).toString(),
    endTimeUnixNano: (startNs + BigInt(offMs + durMs) * 1000000n).toString(),
    status: isErr ? { code: 2, message: pick(ERROR_MESSAGES) } : { code: 1 },
    attributes: [{ key: 'seeded', value: { boolValue: true } }],
  });
  const spans = [mkSpan(pick(OPS[rootService]), null, rootSpanId, 0, rootDurMs, hasError)];
  const downstream = pick(SERVICES);
  if (downstream !== rootService) spans.push(mkSpan(OPS[downstream][0], rootSpanId, randHex(16), 5, Math.floor(rootDurMs * 0.6), hasError && Math.random() > 0.4));
  return {
    resourceSpans: spans.map((s, i) => ({
      resource: { attributes: [{ key: 'service.name', value: { stringValue: i === 0 ? rootService : downstream } }] },
      scopeSpans: [{ scope: { name: 'seed' }, spans: [s] }],
    })),
  };
}

async function seedTraces() {
  let n = 0;
  for (const svc of SERVICES) {
    for (let i = 0; i < 10; i++) {
      const res = await fetch(OTLP, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(buildTrace(svc, Math.random() < 0.2)) });
      if (!res.ok) console.error('  otlp send failed', res.status, await res.text());
      n++;
    }
  }
  console.log(`  traces: sent ${n} via OTLP`);
}

async function seedDeployments(token) {
  let ok = 0;
  for (const svc of ['cart-service', 'payment-service', 'gateway-service']) {
    for (let i = 0; i < 2; i++) {
      if (await authed(token, '/api/v1/deployments', { service: svc, version: `v1.${i + 1}.0`, git_sha: randHex(7), environment: i ? 'staging' : 'production', deployed_by: 'admin', notes: 'Seeded deployment for demo data' }) < 300) ok++;
    }
  }
  console.log(`  deployments: ${ok} created`);
}

async function seedSynthetics(token) {
  let ok = 0;
  for (const url of ['https://api.acme.io/health', 'https://api.acme.io/v1/checkout', 'https://gateway.acme.io/status', 'https://api.acme.io/v1/catalog']) {
    if (await authed(token, '/api/v1/synthetics/tests', { url }) < 300) ok++;
  }
  console.log(`  synthetics: ${ok} targets registered`);
}

async function seedCatalog(token) {
  const entries = [
    { service_name: 'cart-service', team: 'Team Checkout', repo: 'github.com/acme/cart-service', slack: '#eng-checkout' },
    { service_name: 'payment-service', team: 'Team Payments', repo: 'github.com/acme/payment-service', slack: '#eng-payments' },
    { service_name: 'gateway-service', team: 'Team Platform', repo: 'github.com/acme/gateway-service', slack: '#eng-platform' },
    { service_name: 'catalog-service', team: 'Team Catalog', repo: 'github.com/acme/catalog-service', slack: '#eng-catalog' },
  ];
  let ok = 0;
  for (const e of entries) if (await authed(token, '/api/v1/topology/catalog', e) < 300) ok++;
  console.log(`  catalog: ${ok} entries registered`);
}

async function seedAdmin(token) {
  let ok = 0;
  if (await authed(token, '/api/v1/admin/roles', { name: 'support', description: 'Support engineers triaging customer-reported issues.', permissions: ['incidents:read', 'errors:read'] }) < 300) ok++;
  if (await authed(token, '/api/v1/admin/policies', { name: 'viewer-write-block', effect: 'deny', resource: '*', condition: 'subject.role == "viewer" && action != "read"', priority: 20 }) < 300) ok++;
  if (await authed(token, '/api/v1/admin/rate-limits', { name: 'search-burst-guard', path_prefixes: ['/api/v1/search'], limit_count: 200, window_seconds: 60, priority: 10 }) < 300) ok++;
  if (await authed(token, '/api/v1/admin/users', { username: 'sarah.oncall', password: 'sarah_secret_123', role: 'viewer' }) < 300) ok++;
  if (await authed(token, '/api/v1/admin/ingestion-keys', { name: 'production-agents', tier: 'standard', scope: 'ingest' }) < 300) ok++;
  if (await authed(token, '/api/v1/slo/definitions', { service_name: 'payment-service', slo_target: 99.9, sli_type: 'availability', window_days: 30 }) < 300) ok++;
  // A per-tenant delivery channel (F3). Secret is encrypted at rest server-side.
  if (await authed(token, '/api/v1/notification-channels', { name: 'demo-webhook', type: 'webhook', config: { url: 'https://example.com/pulsetrace-hook' }, enabled: true }) < 300) ok++;
  console.log(`  admin: ${ok}/7 (role/policy/rate-limit/user/ingestion-key/slo/channel) created`);

  // A shift-left deploy gate: POST a PR event to the (public) GitHub webhook so
  // the Deploy Gates screen has a recorded verdict to show.
  await fetch(`${GATEWAY}/api/v1/webhooks/github`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', 'X-GitHub-Event': 'pull_request' },
    body: JSON.stringify({
      action: 'opened',
      pull_request: { number: 128, title: 'Add caching layer to catalog-service', user: { login: 'demo-dev' }, html_url: 'https://github.com/acme/app/pull/128', head: { sha: 'a1b2c3d' } },
      repository: { full_name: 'acme/app' },
    }),
  }).catch(() => {});
}

async function main() {
  console.log(`Seeding PulseTrace demo data (gateway=${GATEWAY})...`);
  const token = await login();
  await seedLogs();
  await seedTraces();
  await seedDeployments(token);
  await seedSynthetics(token);
  await seedCatalog(token);
  await seedAdmin(token);
  console.log('Waiting 20s for async pipelines (Kafka->Quickwit index, alert->incident, synthetics worker)...');
  await new Promise((r) => setTimeout(r, 20000));
  console.log('Seed complete.');
}

main().catch((e) => { console.error(e); process.exit(1); });
