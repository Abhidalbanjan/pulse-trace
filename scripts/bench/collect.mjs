// Executes every query class against both targets and emits a JSON run record
// on stdout. Measurement only — rendering lives in report.mjs so the formatting
// can be tested without standing up two stacks.
//
// Measurement decisions worth stating, because each is a way to get a
// flattering-but-wrong number:
//
//   * A warm-up iteration is discarded per query per target. The first call
//     pays connection setup and cache population on both sides; including it
//     measures the harness, not the engine.
//   * A query that errors is recorded as an error, never dropped. Silently
//     discarding failures turns "this engine cannot do it" into "no data".
//   * Sample count is reported alongside percentiles so a reader can see how
//     much precision the numbers actually support.

import { readdir, readFile } from 'node:fs/promises';
import { fileURLToPath } from 'node:url';
import { dirname, join } from 'node:path';
import { summarise } from './report.mjs';

const here = dirname(fileURLToPath(import.meta.url));
const ITERATIONS = Number(process.env.ITERATIONS || 20);
const PT = process.env.PT_GATEWAY || 'http://127.0.0.1:8080';
const OO = process.env.OO_URL || 'http://127.0.0.1:5080';
const OO_USER = process.env.OO_USER || 'bench@pulsetrace.local';
const OO_PASS = process.env.OO_PASS || 'benchpassword123';

async function ptToken() {
  const res = await fetch(`${PT}/api/v1/auth/login`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ username: process.env.PT_USER || 'admin', password: process.env.PT_PASS || 'admin' }),
  });
  if (!res.ok) throw new Error(`PulseTrace login failed: ${res.status}`);
  return (await res.json()).token;
}

/** One timed request. Returns {ms} or {error} — never throws past the caller. */
async function timed(fn) {
  const t0 = performance.now();
  try {
    const ok = await fn();
    const ms = performance.now() - t0;
    return ok ? { ms } : { error: 'non-2xx' };
  } catch (e) {
    return { error: e.message };
  }
}

async function runPulseTrace(spec, token, ctx) {
  if (spec.unsupported) return { unsupported: spec.unsupported };
  const path = spec.path.replaceAll('${TRACE_ID}', ctx.traceId ?? '');
  const call = async () => {
    const res = await fetch(`${PT}${path}`, {
      method: spec.method || 'GET',
      headers: { Authorization: `Bearer ${token}` },
    });
    return res.ok;
  };
  return sample(call);
}

async function runOpenObserve(spec, ctx) {
  if (spec.unsupported) return { unsupported: spec.unsupported };
  const auth = Buffer.from(`${OO_USER}:${OO_PASS}`).toString('base64');
  // Substitute the shared window so both sides query the same 48h span.
  const body = JSON.parse(
    JSON.stringify(spec.body ?? {})
      .replaceAll('${TRACE_ID}', ctx.traceId ?? '')
      .replaceAll('"${START_US}"', String(ctx.startUs))
      .replaceAll('"${END_US}"', String(ctx.endUs)),
  );
  const call = async () => {
    const res = await fetch(`${OO}${spec.path}`, {
      method: spec.method || 'POST',
      headers: { 'Content-Type': 'application/json', Authorization: `Basic ${auth}` },
      body: JSON.stringify(body),
    });
    return res.ok;
  };
  return sample(call);
}

async function sample(call) {
  await timed(call); // discarded warm-up
  const samples = [];
  const errors = [];
  for (let i = 0; i < ITERATIONS; i++) {
    const r = await timed(call);
    if (r.error) errors.push(r.error);
    else samples.push(r.ms);
  }
  const s = summarise(samples) ?? {};
  return { ...s, errors: errors.length, errorSample: errors[0] };
}

/** A real trace_id from the corpus, so the point-lookup class queries live data. */
async function pickTraceId(token) {
  try {
    const res = await fetch(`${PT}/api/v1/logs?since=48h&limit=1`, {
      headers: { Authorization: `Bearer ${token}` },
    });
    const rows = (await res.json())?.data ?? [];
    return rows[0]?.trace_id || null;
  } catch {
    return null;
  }
}

async function main() {
  const token = await ptToken();
  const endUs = Date.now() * 1000;
  const startUs = endUs - 48 * 3600 * 1_000_000; // mirrors PulseTrace's since=48h
  const ctx = { traceId: await pickTraceId(token), startUs, endUs };

  const files = (await readdir(join(here, 'queries'))).filter((f) => f.endsWith('.json')).sort();
  const queries = [];
  for (const f of files) {
    const spec = JSON.parse(await readFile(join(here, 'queries', f), 'utf8'));
    process.stderr.write(`    ${spec.name}\n`);
    queries.push({
      id: spec.id,
      name: spec.name,
      why: spec.why,
      pulsetrace: await runPulseTrace(spec.pulsetrace, token, ctx),
      openobserve: await runOpenObserve(spec.openobserve, ctx),
    });
  }

  const run = {
    timestamp: new Date().toISOString(),
    iterations: ITERATIONS,
    resourceCap: `${process.env.BENCH_CPUS || '?'} CPU / ${process.env.BENCH_MEMORY || '?'} memory`,
    openobserveImage: 'public.ecr.aws/zinclabs/openobserve:v0.14.4',
    corpus: {
      seed: process.env.SEED || null,
      sha256: process.env.CORPUS_SHA256 || '',
      sizeLabel: process.env.CORPUS_SIZE_LABEL || '',
    },
    queries,
    // Footprint sampling (bytes on disk, container count, RSS, cold start) is
    // collected by the shell layer where docker is available; left empty here
    // rather than guessed.
    footprint: {},
  };
  process.stdout.write(JSON.stringify(run, null, 2));
}

main().catch((e) => {
  process.stderr.write(`collect failed: ${e.message}\n`);
  process.exit(1);
});
