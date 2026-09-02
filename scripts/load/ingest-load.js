// Multi-protocol k6 load test for the telemetry ingestion hot path (ROAD_TO_100 · F0.2).
//
// The ingestion path (gateway → log-service buffered queue → Kafka → ClickHouse)
// is the first thing that falls over under real customer traffic. This drives it
// through *every* log-ingest protocol PulseTrace accepts, so a regression in any
// one decoder (native, OTLP, the Datadog/Splunk "Trojan Horse" migration paths)
// is caught, not just the native path.
//
// All enabled protocols converge on the same downstream (Kafka → ClickHouse) but
// exercise different gateway decoders:
//   logs      POST /api/v1/logs        native JSON batch          → 201
//   otlp_logs POST /v1/logs            OTLP/HTTP JSON logs         → 200
//   dd_logs   POST /api/v2/logs        Datadog logs migration      → 200
//   splunk    POST /services/collector Splunk HEC migration        → 200
// Opt-in (forward to the OTLP collector, not the Kafka log path — enable only
// against a stack with a collector; see PROTOCOLS below):
//   otlp_metrics POST /v1/metrics      OTLP/HTTP JSON metrics      → 200
//
// Run locally against the docker-compose stack:
//   k6 run scripts/load/ingest-load.js
//   PROTOCOLS=logs,otlp_logs,dd_logs,splunk RATE=120 DURATION=2m k6 run scripts/load/ingest-load.js
//
// Tunables (env vars):
//   BASE_URL    gateway base URL                       (default http://localhost:8080)
//   INGEST_KEY  ingestion key; sent in each protocol's native auth header
//   RATE        AGGREGATE requests/sec across all enabled protocols (default 80)
//   DURATION    hold time at that rate                 (default 30s)
//   BATCH       records per request                    (default 50)
//   PROTOCOLS   comma list of protocols to drive       (default "logs")
//   SUMMARY_OUT path to write the JSON summary         (default scripts/load/summary.json)
//
// RATE is the *aggregate* arrival rate; it is split evenly across the enabled
// protocols. The gateway's `telemetry-ingest` limiter caps every ingestion path
// at 100 req/s per tenant (6000/60s), so keep the aggregate under that on the
// default (keyless → 'default' tenant) stack.
//
// This comment used to say dd_logs and splunk were "on separate paths and not
// covered by that rule", which was true and the wrong conclusion: uncovered
// meant they fell through to the `default` rule at 600/60s — 10 req/s, a tenth
// of the native path — not that they were unlimited. The scheduled job drives
// each protocol at 30 req/s, so both vendor paths were rejected at roughly two
// thirds and the run failed its threshold every week from the day it was added.
// Fixed in gateway migration 028; the paths are now on the ingest limiter where
// they always belonged.
//
// CI's fast gate runs PROTOCOLS=logs only (unchanged behaviour); the scheduled
// scale job runs the full profile.
import http from 'k6/http';
import { check } from 'k6';
import { Counter } from 'k6/metrics';

const BASE_URL = (__ENV.BASE_URL || 'http://localhost:8080').replace(/\/$/, '');
const INGEST_KEY = __ENV.INGEST_KEY || '';
const RATE = Math.max(1, parseInt(__ENV.RATE || '80', 10));
const DURATION = __ENV.DURATION || '30s';
const BATCH = Math.max(1, parseInt(__ENV.BATCH || '50', 10));
const SUMMARY_OUT = __ENV.SUMMARY_OUT || 'scripts/load/summary.json';
const ENABLED = (__ENV.PROTOCOLS || 'logs')
  .split(',')
  .map((s) => s.trim())
  .filter(Boolean);

// Records actually accepted, tagged per protocol, so the summary reports
// end-to-end ingestion throughput (records/s), not just HTTP requests/s.
const acceptedRecords = new Counter('ingest_records_accepted');

const SERVICES = ['checkout', 'payments-api', 'cart', 'inventory', 'search', 'auth'];
const LEVELS = ['INFO', 'INFO', 'INFO', 'DEBUG', 'WARNING', 'ERROR']; // weighted toward INFO
const MESSAGES = [
  'request completed',
  'cache miss, falling back to db',
  'upstream latency elevated',
  'retrying idempotent operation',
  'payment authorized',
  'connection reset by peer',
];

function pick(arr) {
  return arr[Math.floor(Math.random() * arr.length)];
}

// severityNumber per the OTLP spec: INFO=9, WARN=13, ERROR=17, DEBUG=5.
const OTLP_SEVERITY = { INFO: 9, DEBUG: 5, WARNING: 13, ERROR: 17 };

// ── Payload builders (one per protocol) ─────────────────────────────────────────
function nativeLogs(n) {
  const out = new Array(n);
  for (let i = 0; i < n; i++) {
    out[i] = { service: pick(SERVICES), level: pick(LEVELS), message: pick(MESSAGES) };
  }
  return JSON.stringify(out);
}

function otlpLogs(n) {
  const nowNano = `${Date.now()}000000`;
  const records = new Array(n);
  for (let i = 0; i < n; i++) {
    const level = pick(LEVELS);
    records[i] = {
      timeUnixNano: nowNano,
      severityNumber: OTLP_SEVERITY[level] || 9,
      severityText: level,
      body: { stringValue: pick(MESSAGES) },
    };
  }
  return JSON.stringify({
    resourceLogs: [
      {
        resource: {
          attributes: [{ key: 'service.name', value: { stringValue: pick(SERVICES) } }],
        },
        scopeLogs: [{ logRecords: records }],
      },
    ],
  });
}

function ddLogs(n) {
  const out = new Array(n);
  for (let i = 0; i < n; i++) {
    out[i] = {
      message: pick(MESSAGES),
      ddsource: 'nodejs',
      service: pick(SERVICES),
      status: pick(LEVELS).toLowerCase(),
    };
  }
  return JSON.stringify(out);
}

function splunkHEC(n) {
  // A HEC body is newline-delimited JSON events (not a JSON array).
  const lines = new Array(n);
  for (let i = 0; i < n; i++) {
    lines[i] = JSON.stringify({
      time: Date.now() / 1000,
      host: pick(SERVICES),
      source: 'app',
      sourcetype: '_json',
      event: { message: pick(MESSAGES), level: pick(LEVELS) },
    });
  }
  return lines.join('\n');
}

function otlpMetrics(n) {
  const nowNano = `${Date.now()}000000`;
  const points = new Array(n);
  for (let i = 0; i < n; i++) {
    points[i] = {
      timeUnixNano: nowNano,
      asDouble: Math.random() * 1000,
      attributes: [{ key: 'service.name', value: { stringValue: pick(SERVICES) } }],
    };
  }
  return JSON.stringify({
    resourceMetrics: [
      {
        resource: { attributes: [{ key: 'service.name', value: { stringValue: pick(SERVICES) } }] },
        scopeMetrics: [
          {
            metrics: [
              { name: 'http.server.duration', gauge: { dataPoints: points } },
            ],
          },
        ],
      },
    ],
  });
}

// Protocol registry: path, payload builder, success status, and how the ingestion
// key (if any) is carried in that protocol's native auth header.
const PROTOCOLS = {
  logs: {
    path: '/api/v1/logs',
    build: nativeLogs,
    ok: 201,
    contentType: 'application/json',
    auth: (k) => ({ Authorization: `Bearer ${k}` }),
  },
  otlp_logs: {
    path: '/v1/logs',
    build: otlpLogs,
    ok: 200,
    contentType: 'application/json',
    auth: (k) => ({ Authorization: `Bearer ${k}` }),
  },
  dd_logs: {
    path: '/api/v2/logs',
    build: ddLogs,
    ok: 200,
    contentType: 'application/json',
    auth: (k) => ({ 'DD-API-KEY': k }),
  },
  splunk: {
    path: '/services/collector',
    build: splunkHEC,
    ok: 200,
    contentType: 'application/json',
    auth: (k) => ({ Authorization: `Splunk ${k}` }),
  },
  otlp_metrics: {
    path: '/v1/metrics',
    build: otlpMetrics,
    ok: 200,
    contentType: 'application/json',
    auth: (k) => ({ Authorization: `Bearer ${k}` }),
  },
};

const SELECTED = ENABLED.filter((p) => {
  if (!PROTOCOLS[p]) {
    // Fail loudly at init on an unknown protocol rather than silently skipping it.
    throw new Error(`unknown PROTOCOLS entry "${p}"; valid: ${Object.keys(PROTOCOLS).join(', ')}`);
  }
  return true;
});

// Split the aggregate RATE evenly across the enabled protocols (min 1/s each).
const PER_PROTOCOL_RATE = Math.max(1, Math.floor(RATE / SELECTED.length));

// One arrival-rate scenario per protocol, each calling its exec function below.
const scenarios = {};
const thresholds = {
  // Ingestion is a fire-and-forget enqueue: effectively no failures, and batches
  // must actually be accepted (not merely non-5xx).
  http_req_failed: ['rate<0.02'],
  checks: ['rate>0.98'],
};
for (const p of SELECTED) {
  scenarios[p] = {
    executor: 'constant-arrival-rate',
    rate: PER_PROTOCOL_RATE,
    timeUnit: '1s',
    duration: DURATION,
    preAllocatedVUs: Math.max(10, PER_PROTOCOL_RATE),
    maxVUs: Math.max(30, PER_PROTOCOL_RATE * 3),
    exec: `run_${p}`,
    tags: { endpoint: p },
  };
  // Per-protocol latency budgets. Declaring the threshold also materialises the
  // per-endpoint sub-metric so handleSummary can report p50/p95/p99 per protocol.
  thresholds[`http_req_duration{endpoint:${p}}`] = ['p(95)<800', 'p(99)<1500'];

  // Per-protocol failure budgets, same rate as the aggregate.
  //
  // These exist to make a breach *legible*, not to tighten it. The aggregate
  // `http_req_failed` says 37% of requests failed and nothing about which of
  // four protocols did it — that number stood in a weekly job for three runs
  // while two vendor paths were being rejected at two thirds and the other two
  // were fine. A per-protocol threshold names the culprit in the failure line
  // itself.
  thresholds[`http_req_failed{endpoint:${p}}`] = ['rate<0.02'];

  // No budget, purely to materialise the sub-metric: k6 only surfaces a tagged
  // sub-metric in handleSummary when a threshold references it, and `count>=0`
  // is always true. Without this the accepted-record count is aggregate-only,
  // so a protocol accepting nothing is invisible next to three that are fine.
  thresholds[`ingest_records_accepted{endpoint:${p}}`] = ['count>=0'];
}

export const options = {
  scenarios,
  thresholds,
  // Materialise the stats handleSummary reports per protocol. Without this, k6
  // omits p(99) and count from a Trend metric's `.values`, so the baseline table
  // would show nulls for them.
  summaryTrendStats: ['med', 'p(95)', 'p(99)', 'max', 'avg', 'count'],
};

// ── Per-protocol exec functions (k6 requires a named export per scenario) ───────
function drive(name) {
  const spec = PROTOCOLS[name];
  const headers = { 'Content-Type': spec.contentType };
  if (INGEST_KEY) Object.assign(headers, spec.auth(INGEST_KEY));
  const res = http.post(`${BASE_URL}${spec.path}`, spec.build(BATCH), {
    headers,
    tags: { endpoint: name },
  });
  const ok = check(res, { [`${name} accepted`]: (r) => r.status === spec.ok }, { endpoint: name });
  if (ok) acceptedRecords.add(BATCH, { endpoint: name });
}

export function run_logs() {
  drive('logs');
}
export function run_otlp_logs() {
  drive('otlp_logs');
}
export function run_dd_logs() {
  drive('dd_logs');
}
export function run_splunk() {
  drive('splunk');
}
export function run_otlp_metrics() {
  drive('otlp_metrics');
}

// ── Summary: machine-readable JSON (for PERF_BASELINE.md) + human text ──────────
export function handleSummary(data) {
  const dur = (endpoint) => {
    const m = data.metrics[`http_req_duration{endpoint:${endpoint}}`];
    if (!m || !m.values) return null;
    const v = m.values;
    return {
      count: v.count,
      p50: round(v.med),
      p95: round(v['p(95)']),
      p99: round(v['p(99)']),
      avg: round(v.avg),
      max: round(v.max),
    };
  };
  const perProtocol = {};
  for (const p of SELECTED) perProtocol[p] = dur(p);

  // Accepted records and failure rate per protocol. `dur` above reports latency
  // for every request, successful or not, so a protocol being refused outright
  // still shows a healthy p99 — these two are what distinguish "fast" from
  // "working".
  const accepted = (endpoint) => {
    const m = data.metrics[`ingest_records_accepted{endpoint:${endpoint}}`];
    return m && m.values ? m.values.count : 0;
  };
  const failedRate = (endpoint) => {
    const m = data.metrics[`http_req_failed{endpoint:${endpoint}}`];
    return m && m.values ? round4(m.values.rate) : null;
  };
  const perProtocolAccepted = {};
  const perProtocolFailed = {};
  for (const p of SELECTED) {
    perProtocolAccepted[p] = accepted(p);
    perProtocolFailed[p] = failedRate(p);
  }

  const summary = {
    generatedAt: new Date().toISOString(),
    baseUrl: BASE_URL,
    duration: DURATION,
    batch: BATCH,
    aggregateRate: RATE,
    perProtocolRate: PER_PROTOCOL_RATE,
    protocols: SELECTED,
    recordsAccepted: data.metrics.ingest_records_accepted
      ? data.metrics.ingest_records_accepted.values.count
      : 0,
    httpReqFailedRate: data.metrics.http_req_failed
      ? round4(data.metrics.http_req_failed.values.rate)
      : null,
    latencyMs: perProtocol,
    acceptedByProtocol: perProtocolAccepted,
    failureRateByProtocol: perProtocolFailed,
  };

  const out = {};
  out[SUMMARY_OUT] = JSON.stringify(summary, null, 2);
  // Hermetic (no remote jslib import): print our own compact markdown summary.
  // k6 still evaluates thresholds and sets the exit code independently of this.
  out.stdout = markdownTable(summary);
  return out;
}

function markdownTable(s) {
  const rows = [
    '',
    `Ingestion baseline — ${s.protocols.join(', ')} @ ${s.aggregateRate} req/s aggregate, batch ${s.batch}, ${s.duration}`,
    '',
    '| Protocol | reqs | p50 ms | p95 ms | p99 ms | max ms |',
    '| --- | ---: | ---: | ---: | ---: | ---: |',
  ];
  for (const p of s.protocols) {
    const l = s.latencyMs[p];
    if (!l) {
      rows.push(`| ${p} | — | — | — | — | — |`);
      continue;
    }
    rows.push(`| ${p} | ${l.count} | ${l.p50} | ${l.p95} | ${l.p99} | ${l.max} |`);
  }
  rows.push('');
  rows.push(`Records accepted: ${s.recordsAccepted} · HTTP failure rate: ${s.httpReqFailedRate}`);
  rows.push('');
  return rows.join('\n');
}

function round(n) {
  return n == null ? null : Math.round(n * 100) / 100;
}
function round4(n) {
  return n == null ? null : Math.round(n * 10000) / 10000;
}
