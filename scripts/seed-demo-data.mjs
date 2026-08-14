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

// buildMetrics emits an OTLP metrics payload for one service: a gauge
// (queue_depth), a monotonic cumulative counter (requests_total, so rate()
// renders a real per-second slope), and a byte-unit gauge (memory_bytes, so the
// unit-aware axis has something to format). The collector writes these into
// otel_metrics_gauge / otel_metrics_sum with a tenant.id resource dimension,
// exactly like the trace path above.
function buildMetrics(service) {
  const nowMs = Date.now();
  const N = 8; // one point per bucket over the recent window
  const dp = (val, kFromNow) => ({
    asDouble: val,
    timeUnixNano: (BigInt(nowMs - kFromNow * 60000) * 1000000n).toString(),
    startTimeUnixNano: (BigInt(nowMs - N * 60000) * 1000000n).toString(),
  });
  const gaugePoints = (base, jitter) => Array.from({ length: N }, (_, k) => dp(base + Math.random() * jitter, N - k));
  const counterPoints = (start, step) => {
    let v = start;
    return Array.from({ length: N }, (_, k) => { v += step + Math.random() * step; return dp(Math.round(v), N - k); });
  };
  return {
    resourceMetrics: [{
      resource: { attributes: [{ key: 'service.name', value: { stringValue: service } }] },
      scopeMetrics: [{
        scope: { name: 'seed' },
        metrics: [
          { name: 'queue_depth', unit: '1', description: 'Pending items in the work queue', gauge: { dataPoints: gaugePoints(20, 40) } },
          { name: 'requests_total', unit: '1', description: 'Total requests handled', sum: { isMonotonic: true, aggregationTemporality: 2, dataPoints: counterPoints(1000, 60) } },
          { name: 'memory_bytes', unit: 'By', description: 'Resident set size', gauge: { dataPoints: gaugePoints(2e8, 5e7) } },
        ],
      }],
    }],
  };
}

async function seedMetrics() {
  const OTLP_METRICS = `${GATEWAY}/v1/metrics`; // gateway proxies to the otel-collector
  let n = 0;
  for (const svc of SERVICES) {
    const res = await fetch(OTLP_METRICS, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(buildMetrics(svc)) });
    if (!res.ok) console.error('  otlp metrics send failed', res.status, await res.text());
    else n++;
  }
  console.log(`  metrics: sent ${n} services via OTLP (gauge + counter + bytes)`);
}

// A spread of real-world User-Agents so the device/browser/OS breakdown and
// per-session device labels have variety to classify.
const USER_AGENTS = [
  'Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0 Safari/537.36',
  'Mozilla/5.0 (iPhone; CPU iPhone OS 17_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.0 Mobile/15E148 Safari/604.1',
  'Mozilla/5.0 (Macintosh; Intel Mac OS X 10.15; rv:121.0) Gecko/20100101 Firefox/121.0',
  'Mozilla/5.0 (Linux; Android 13; Pixel 7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0 Mobile Safari/537.36',
  'Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 Chrome/120.0 Safari/537.36 Edg/120.0',
  'Mozilla/5.0 (iPad; CPU OS 17_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.0 Safari/604.1',
];
const RUM_PATHS = ['/', '/checkout', '/catalog', '/cart', '/account'];

// seedRUM emits browser telemetry (page views, Core Web Vitals, occasional JS
// errors) across several sessions, timestamped over the last ~6h so the Web
// Vitals trend renders a real slope rather than a single spike.
async function seedRUM(token) {
  const now = Date.now();
  let sessions = 0, events = 0;
  for (let s = 0; s < 10; s++) {
    const sessionId = randHex(16);
    const ua = pick(USER_AGENTS);
    const entry = pick(RUM_PATHS);
    // Spread this session's events across a random recent point in the window.
    const base = now - Math.floor(Math.random() * 6 * 3600 * 1000);
    const batch = [];
    const ev = (type, extra) => ({ session_id: sessionId, type, path: extra.path || entry, user_agent: ua, timestamp: base + (batch.length * 1500), ...extra });
    batch.push(ev('page_view', {}));
    batch.push(ev('web_vitals', { metric_name: 'LCP', metric_value: 1600 + Math.random() * 2200 }));
    batch.push(ev('web_vitals', { metric_name: 'CLS', metric_value: +(0.02 + Math.random() * 0.18).toFixed(3) }));
    batch.push(ev('web_vitals', { metric_name: 'FID', metric_value: 15 + Math.random() * 140 }));
    // ~1 in 3 sessions also visits a second page.
    if (Math.random() < 0.34) {
      const p2 = pick(RUM_PATHS);
      batch.push(ev('page_view', { path: p2 }));
      batch.push(ev('web_vitals', { metric_name: 'LCP', metric_value: 1600 + Math.random() * 2600, path: p2 }));
    }
    // ~1 in 4 sessions hits a JS error, correlatable to a backend trace.
    if (Math.random() < 0.25) {
      batch.push(ev('error', { error_msg: pick(['TypeError: cannot read properties of undefined', 'NetworkError: failed to fetch', 'Unhandled promise rejection']), error_stack: 'at checkout (app.js:214)', trace_id: randHex(32) }));
    }
    await authed(token, '/api/v1/rum/ingest', batch);
    sessions++; events += batch.length;
  }
  console.log(`  rum: sent ${events} events across ${sessions} sessions`);
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
  // A multi-step check with assertions (status / latency SLA / body-contains)
  // that pages on failure — exercises the full F10 capability.
  if (await authed(token, '/api/v1/synthetics/tests', {
    name: 'Checkout journey',
    steps: [
      { method: 'GET', url: 'https://api.acme.io/health', assert: { status: 200, max_latency_ms: 2000 } },
      { method: 'GET', url: 'https://api.acme.io/v1/catalog', assert: { max_latency_ms: 3000, body_contains: 'products' } },
    ],
  }) < 300) ok++;
  console.log(`  synthetics: ${ok} checks registered (incl. 1 multi-step)`);
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
  await seedMetrics();
  await seedRUM(token);
  await seedDeployments(token);
  await seedSynthetics(token);
  await seedCatalog(token);
  await seedAdmin(token);
  await waitForLogsSearchable(token);
  console.log('Seed complete.');
}

// Wait until the seeded logs are actually *searchable*, not for a fixed guess.
//
// Logs travel gateway -> Kafka -> Quickwit, and Quickwit only publishes a split
// once its commit timeout elapses — which defaults to 60s and is not overridden
// in quickwit/logs-index.yaml. The previous fixed 20s sleep was therefore
// shorter than the pipeline's own latency: locally it looked fine because
// minutes passed before anyone ran the suite, but in CI the tests start
// immediately and the Log Explorer specs failed against an empty index.
//
// Polling the real query path is both faster in the common case and correct in
// the slow one. If it never becomes searchable we warn rather than fail — the
// suite should report which specs broke, not die here with less context.
async function waitForLogsSearchable(token, timeoutMs = 120000) {
  const started = Date.now();
  process.stdout.write('Waiting for seeded logs to become searchable');
  while (Date.now() - started < timeoutMs) {
    try {
      const res = await fetch(`${GATEWAY}/api/v1/logs?since=1h&limit=1`, {
        headers: { Authorization: `Bearer ${token}` },
      });
      if (res.ok) {
        const body = await res.json();
        if ((body.data || []).length > 0) {
          console.log(` ok (${Math.round((Date.now() - started) / 1000)}s)`);
          return;
        }
      }
    } catch {
      // gateway momentarily unavailable — keep polling
    }
    process.stdout.write('.');
    await new Promise((r) => setTimeout(r, 2000));
  }
  console.log(`\nWARNING: logs still not searchable after ${timeoutMs / 1000}s — log-dependent specs will fail.`);
}

main().catch((e) => { console.error(e); process.exit(1); });
