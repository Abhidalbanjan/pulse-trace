// Extractors for the backend↔frontend parity gate (F0.1).
//
// backendRoutes(): every HTTP route the Go services register, normalized.
// frontendCalls(): every gateway path literal the frontend references, normalized.
//
// Dependency-free (no npm installs) so it runs in CI as-is. Extraction is
// regex-based on source, which is deliberately conservative: it errs toward
// reporting a route as "present" rather than inventing one. If a route is
// registered in a way these patterns miss, add it to the registry's `extra`.

import { readdirSync, readFileSync, statSync } from 'node:fs';
import { join, extname } from 'node:path';

const REPO = new URL('../../', import.meta.url).pathname;

function walk(dir, exts, out = []) {
  let entries;
  try {
    entries = readdirSync(dir);
  } catch {
    return out;
  }
  for (const e of entries) {
    if (e === 'node_modules' || e === '.git' || e === '.next' || e === 'vendor') continue;
    const p = join(dir, e);
    const st = statSync(p);
    if (st.isDirectory()) walk(p, exts, out);
    else if (exts.includes(extname(p))) out.push(p);
  }
  return out;
}

// Collapse path params to a single token so backend {id}/frontend ${id}/:id all
// match, strip query strings and trailing slashes (except a bare "/").
export function normalizePath(path) {
  let p = path.split('?')[0];
  p = p.replace(/\$\{[^}]*\}/g, ':p'); // balanced ${id}
  p = p.replace(/\$\{[^/]*/g, ':p'); // unbalanced ${encodeURIComponent(name)} → to next '/'
  p = p.replace(/\{[^}]*\}/g, ':p'); // {id}
  p = p.replace(/:[A-Za-z_][A-Za-z0-9_]*/g, ':p'); // :id
  p = p.replace(/\/:p(\/:p)+/g, '/:p/:p'); // keep multi-param depth stable
  if (p.length > 1) p = p.replace(/\/+$/, '');
  return p;
}

// Every ".HandleFunc(\"METHOD /path\"" across the Go services.
export function backendRoutes() {
  const files = walk(join(REPO, ''), ['.go']).filter((f) => !f.endsWith('_test.go'));
  // Go 1.22 method routing: HandleFunc("METHOD /path", ...); and the older
  // method-less HandleFunc("/path", ...) form (chat, agent handlers) → method ANY.
  const reMethod = /\.HandleFunc\(\s*"([A-Z]+)\s+(\/[^"]*)"/g;
  const reAny = /\.HandleFunc\(\s*"(\/[^"]*)"/g;
  const routes = new Map(); // key: METHOD normPath -> {method, path, raw, file}
  const add = (method, raw, f) => {
    const norm = normalizePath(raw);
    const key = `${method} ${norm}`;
    if (!routes.has(key)) routes.set(key, { method, path: norm, raw, file: rel(f) });
  };
  for (const f of files) {
    const src = readFileSync(f, 'utf8');
    let m;
    while ((m = reMethod.exec(src))) add(m[1], m[2], f);
    // reAny's pattern requires the char after the quote to be '/', so it never
    // matches the "METHOD /path" form (which starts with an uppercase letter).
    while ((m = reAny.exec(src))) add('ANY', m[1], f);
  }
  return routes;
}

// Every "/api/..."-shaped literal referenced in the frontend source.
export function frontendCalls() {
  const files = walk(join(REPO, 'frontend/src'), ['.ts', '.tsx', '.js', '.jsx']);
  // Match a /api/... literal inside a string or template literal, capturing up to
  // the closing quote/backtick, whitespace, or a query '?'. Parens/braces are kept
  // so ${encodeURIComponent(x)} is captured whole, then collapsed by normalizePath.
  const re = /["'`](\/api\/[^"'`\s?]+)/g;
  const calls = new Map(); // normPath -> {path, raw, files:Set}
  for (const f of files) {
    const src = readFileSync(f, 'utf8');
    let m;
    while ((m = re.exec(src))) {
      const raw = m[1];
      const norm = normalizePath(raw);
      if (!calls.has(norm)) calls.set(norm, { path: norm, raw, files: new Set() });
      calls.get(norm).files.add(rel(f));
    }
  }
  return calls;
}

function rel(f) {
  return f.startsWith(REPO) ? f.slice(REPO.length) : f;
}
