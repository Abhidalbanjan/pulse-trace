// k6 load test for the telemetry ingestion hot path.
//
// The ingestion path (gateway → log-service buffered queue → Kafka → ClickHouse)
// is the first thing that falls over under real customer traffic, so this drives
// it with a realistic batched-log workload and fails the build if latency or
// error rate regress past the thresholds below.
//
// Run locally against the docker-compose stack:
//   k6 run scripts/load/ingest-load.js
//
// Tunables (env vars):
//   BASE_URL    gateway base URL              (default http://localhost:8080)
//   INGEST_KEY  ingestion key, if the stack has REQUIRE_INGESTION_KEY=true
//   RATE        requests/sec (arrival rate)   (default 80)
//   DURATION    hold time at that rate        (default 30s)
//   BATCH       log entries per request       (default 50)
//
// The workload is a fixed *arrival rate*, not an open VU ramp, deliberately kept
// under the gateway's telemetry-ingest rate limit (6000/min = 100/s) so the test
// measures the ingestion path's latency at a sustained, permitted throughput
// rather than just how fast it starts returning 429s. At the defaults that's
// 80 req/s × 50 = ~4,000 log records/sec sustained.
//
// With REQUIRE_INGESTION_KEY unset (the dev/CI default) ingestion is attributed
// to the 'default' tenant and needs no auth, so the script runs key-less there.
import http from 'k6/http';
import { check } from 'k6';
import { Counter } from 'k6/metrics';

const BASE_URL = __ENV.BASE_URL || 'http://localhost:8080';
const INGEST_KEY = __ENV.INGEST_KEY || '';
const RATE = parseInt(__ENV.RATE || '80', 10);
const DURATION = __ENV.DURATION || '30s';
const BATCH = parseInt(__ENV.BATCH || '50', 10);

// Count the log records we actually got accepted, so the summary reports
// end-to-end ingestion throughput, not just HTTP requests.
const acceptedRecords = new Counter('ingest_records_accepted');

export const options = {
  scenarios: {
    ingest: {
      executor: 'constant-arrival-rate',
      rate: RATE,
      timeUnit: '1s',
      duration: DURATION,
      // Enough VUs to sustain the rate even if a few requests run long; k6 warns
      // (doesn't fail) if it needs more, which itself signals a latency problem.
      preAllocatedVUs: Math.max(20, RATE),
      maxVUs: Math.max(50, RATE * 3),
    },
  },
  thresholds: {
    // Ingestion is a fire-and-forget enqueue, so it must stay fast even at peak.
    http_req_duration: ['p(95)<800', 'p(99)<1500'],
    // Effectively no failures tolerated on the ingestion path (429s would show
    // up here, which is why the arrival rate stays under the limiter).
    http_req_failed: ['rate<0.01'],
    // The endpoint must actually accept batches (201), not just not-error.
    checks: ['rate>0.99'],
  },
};

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

function makeBatch(n) {
  const batch = new Array(n);
  for (let i = 0; i < n; i++) {
    batch[i] = {
      service: pick(SERVICES),
      level: pick(LEVELS),
      message: pick(MESSAGES),
    };
  }
  return batch;
}

export default function () {
  const headers = { 'Content-Type': 'application/json' };
  if (INGEST_KEY) headers['Authorization'] = `Bearer ${INGEST_KEY}`;

  const res = http.post(`${BASE_URL}/api/v1/logs`, JSON.stringify(makeBatch(BATCH)), {
    headers,
    tags: { endpoint: 'log_ingest' },
  });

  const ok = check(res, {
    'status is 201': (r) => r.status === 201,
  });
  if (ok) acceptedRecords.add(BATCH);
}
