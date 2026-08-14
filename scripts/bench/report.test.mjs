// Tests for the code that writes the published numbers.
//
// Run: node --test scripts/bench/report.test.mjs

import { test } from 'node:test';
import assert from 'node:assert/strict';
import {
  percentile,
  summarise,
  verdict,
  renderQueryTable,
  renderResultsBlock,
  spliceResults,
  RESULTS_BEGIN,
  RESULTS_END,
} from './report.mjs';

test('percentile uses nearest-rank and never interpolates', () => {
  const v = [10, 20, 30, 40, 50, 60, 70, 80, 90, 100];
  assert.equal(percentile(v, 50), 50);
  assert.equal(percentile(v, 95), 100);
  assert.equal(percentile(v, 99), 100);
  // Order of the input must not matter.
  assert.equal(percentile([...v].reverse(), 50), 50);
});

test('percentile is defined at the edges', () => {
  assert.equal(percentile([], 95), null);
  assert.equal(percentile([42], 50), 42);
  assert.equal(percentile([42], 99), 42);
  // p0 must not index -1.
  assert.equal(percentile([1, 2, 3], 0), 1);
});

test('summarise reports the sample size alongside the percentiles', () => {
  const s = summarise([5, 1, 3]);
  assert.equal(s.n, 3);
  assert.equal(s.min, 1);
  assert.equal(s.max, 5);
  assert.equal(s.p50, 3);
});

test('verdict is lower-is-better and refuses to compare a missing side', () => {
  assert.equal(verdict(50, 100).label, 'PulseTrace faster');
  assert.equal(verdict(200, 100).label, 'OpenObserve faster');
  assert.equal(verdict(100, 100).label, 'parity');
  // A comparison against a measurement that does not exist is not a comparison.
  assert.equal(verdict(null, 100), null);
  assert.equal(verdict(100, null), null);
  assert.equal(verdict(100, 0), null);
});

test('verdict ratio direction cannot silently invert', () => {
  // Guards the single most consequential bug this file could have: reporting
  // "PulseTrace faster" when it is in fact slower.
  const v = verdict(20, 200);
  assert.ok(v.ratio < 1, 'a faster PulseTrace must yield a ratio below 1');
  assert.equal(v.label, 'PulseTrace faster');
});

test('an unsupported query renders as a capability gap, never as a blank', () => {
  const table = renderQueryTable([
    {
      name: 'Full-scan aggregation',
      pulsetrace: { unsupported: 'no GROUP BY on the log API' },
      openobserve: { p50: 100, p95: 150 },
    },
  ]);
  assert.match(table, /not expressible/);
  assert.match(table, /OpenObserve only/);
  // It must not quietly become a dash, a zero, or a win.
  assert.doesNotMatch(table, /PulseTrace faster/);
});

test('results block surfaces capability gaps in their own section', () => {
  const block = renderResultsBlock({
    timestamp: '2026-01-01T00:00:00Z',
    iterations: 20,
    corpus: { sizeLabel: '10 GiB', seed: 42, sha256: 'abc123def456abc7' },
    queries: [
      { name: 'Needle', pulsetrace: { p50: 10, p95: 20 }, openobserve: { p50: 30, p95: 40 } },
      { name: 'Group-by', pulsetrace: { unsupported: 'no GROUP BY' }, openobserve: { p50: 5, p95: 9 } },
    ],
    footprint: {},
  });
  assert.match(block, /Capability gaps/);
  assert.match(block, /1 of 2 query classes/);
  assert.ok(block.startsWith(RESULTS_BEGIN));
  assert.ok(block.trimEnd().endsWith(RESULTS_END));
});

test('no capability-gap section when everything is expressible', () => {
  const block = renderResultsBlock({
    queries: [{ name: 'Needle', pulsetrace: { p50: 1, p95: 2 }, openobserve: { p50: 3, p95: 4 } }],
    footprint: {},
  });
  assert.doesNotMatch(block, /Capability gaps/);
});

test('splice replaces only the generated block and preserves prose', () => {
  const doc = `# Benchmark\n\nHand-written intro.\n\n${RESULTS_BEGIN}\nstale\n${RESULTS_END}\n\nHand-written outro.\n`;
  const out = spliceResults(doc, `${RESULTS_BEGIN}\nfresh\n${RESULTS_END}`);
  assert.match(out, /Hand-written intro/);
  assert.match(out, /Hand-written outro/);
  assert.match(out, /fresh/);
  assert.doesNotMatch(out, /stale/);
});

test('splice appends when the document has no block yet', () => {
  const out = spliceResults('# Benchmark\n\nIntro only.\n', `${RESULTS_BEGIN}\nfresh\n${RESULTS_END}`);
  assert.match(out, /Intro only/);
  assert.match(out, /fresh/);
});

test('a query that errored renders as failed, never as no-data', () => {
  const table = renderQueryTable([
    {
      name: 'Regex scan',
      pulsetrace: { errors: 5, errorSample: 'non-2xx' },
      openobserve: { p50: 1, p95: 2 },
    },
  ]);
  // A broken feature must not be able to hide behind an em dash.
  assert.match(table, /\*\*failed\*\*/);
  assert.doesNotMatch(table, /\| — \| 1 ms/);
});

test('partial failures keep their numbers but are flagged', () => {
  const table = renderQueryTable([
    { name: 'Needle', pulsetrace: { p50: 10, p95: 20, errors: 2 }, openobserve: { p50: 5, p95: 6 } },
  ]);
  assert.match(table, /10 ms \/ 20 ms ⚠ 2 errored/);
});

test('a side with neither samples nor errors is genuinely absent', () => {
  const table = renderQueryTable([{ name: 'X', pulsetrace: null, openobserve: { p50: 1, p95: 2 } }]);
  assert.match(table, /\| — \|/);
  assert.match(table, /no comparison/);
});
