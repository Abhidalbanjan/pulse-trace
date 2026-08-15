// Splices a run record into BENCHMARK.md, preserving the hand-written prose.
import { readFile, writeFile } from 'node:fs/promises';
import { renderResultsBlock, spliceResults } from './report.mjs';

const [, , runPath, docPath] = process.argv;
if (!runPath || !docPath) {
  console.error('usage: write-report.mjs <run.json> <BENCHMARK.md>');
  process.exit(2);
}

const run = JSON.parse(await readFile(runPath, 'utf8'));
let doc = '';
try {
  doc = await readFile(docPath, 'utf8');
} catch {
  doc = '';
}
await writeFile(docPath, spliceResults(doc, renderResultsBlock(run)));
console.log(`wrote ${docPath}`);
