#!/usr/bin/env node
// Render the results section of PERF_BASELINE.md from a k6 summary + infra
// metrics (ROAD_TO_100 · F0.2). Only the block between the BASELINE markers is
// rewritten; the methodology prose around it is preserved.
//
//   node scripts/load/render-baseline.mjs <summary.json> [infra-metrics.json]

import { readFileSync, writeFileSync, existsSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { dirname, join } from 'node:path';

const HERE = dirname(fileURLToPath(import.meta.url));
const REPO = join(HERE, '..', '..');
const BASELINE = join(REPO, 'PERF_BASELINE.md');
const BEGIN = '<!-- BASELINE:BEGIN -->';
const END = '<!-- BASELINE:END -->';

const summaryPath = process.argv[2] || join(HERE, 'summary.json');
const infraPath = process.argv[3] || join(HERE, 'infra-metrics.json');

if (!existsSync(summaryPath)) {
  console.error(`render-baseline: no k6 summary at ${summaryPath}`);
  process.exit(1);
}
const s = JSON.parse(readFileSync(summaryPath, 'utf8'));
const infra = existsSync(infraPath) ? JSON.parse(readFileSync(infraPath, 'utf8')) : null;

const n = (v, unit = '') => (v == null || Number.isNaN(v) ? '—' : `${v}${unit}`);

const lines = [];
lines.push(BEGIN);
lines.push('');
lines.push(`_Last run: **${s.generatedAt}** · target \`${s.baseUrl}\` · ${s.duration} @ ${s.aggregateRate} req/s aggregate (${s.perProtocolRate}/s × ${s.protocols.length} protocols) · batch ${s.batch}._`);
lines.push('');
lines.push('### Gateway ingest latency (per protocol)');
lines.push('');
lines.push('| Protocol | requests | p50 ms | p95 ms | p99 ms | max ms |');
lines.push('| --- | ---: | ---: | ---: | ---: | ---: |');
for (const p of s.protocols) {
  const l = s.latencyMs[p];
  if (!l) {
    lines.push(`| \`${p}\` | — | — | — | — | — |`);
    continue;
  }
  lines.push(`| \`${p}\` | ${n(l.count)} | ${n(l.p50)} | ${n(l.p95)} | ${n(l.p99)} | ${n(l.max)} |`);
}
lines.push('');
lines.push(`**Records accepted:** ${n(s.recordsAccepted)} · **HTTP failure rate:** ${n(s.httpReqFailedRate)}`);
lines.push('');

if (infra && !infra.error) {
  lines.push('### Downstream back-pressure (sampled during the run)');
  lines.push('');
  lines.push('| Signal | Peak | End-of-run |');
  lines.push('| --- | ---: | ---: |');
  if (infra.kafka) {
    lines.push(`| Kafka consumer-group lag (all groups) | ${n(infra.kafka.peakTotalLag)} | ${n(infra.kafka.endTotalLag)} |`);
  }
  if (infra.clickhouse) {
    lines.push(`| ClickHouse active parts | ${n(infra.clickhouse.peakActiveParts)} | — |`);
    lines.push(`| ClickHouse concurrent merges | ${n(infra.clickhouse.peakMerges)} | — |`);
    lines.push(`| ClickHouse rows (active parts) | — | ${n(infra.clickhouse.endRows)} |`);
  }
  lines.push('');
  if (infra.containers && Object.keys(infra.containers).length) {
    lines.push('| Container | peak CPU % | peak mem MiB |');
    lines.push('| --- | ---: | ---: |');
    for (const [ct, m] of Object.entries(infra.containers)) {
      lines.push(`| \`${ct}\` | ${n(m.peakCpuPct)} | ${n(m.peakMemMiB)} |`);
    }
    lines.push('');
  }
  const kafkaHealthy = infra.kafka && (infra.kafka.endTotalLag == null || infra.kafka.endTotalLag <= (infra.kafka.peakTotalLag || 0));
  lines.push(
    `> **Read:** end-of-run Kafka lag ${kafkaHealthy ? 'did not exceed' : 'exceeded'} the peak, i.e. the log topic ${kafkaHealthy ? 'drained as fast as it filled' : 'fell behind'} at this rate.`
  );
  lines.push('');
} else {
  lines.push('### Downstream back-pressure');
  lines.push('');
  lines.push('_No infra metrics captured for this run (sampler unavailable or stack not reachable from the runner)._');
  lines.push('');
}

lines.push(END);

let doc = readFileSync(BASELINE, 'utf8');
const startIdx = doc.indexOf(BEGIN);
const endIdx = doc.indexOf(END);
if (startIdx === -1 || endIdx === -1) {
  console.error('render-baseline: BASELINE markers not found in PERF_BASELINE.md');
  process.exit(1);
}
doc = doc.slice(0, startIdx) + lines.join('\n') + doc.slice(endIdx + END.length);
writeFileSync(BASELINE, doc);
console.log(`render-baseline: updated ${BASELINE}`);
