// The catalog as the engine describes it, plus the suggestion logic built on it.
//
// Nothing here hardcodes a relation or a column. The whole point of fetching
// `/api/v1/query/schema` rather than embedding a copy is that an editor which
// offers a name the validator would reject is worse than one that offers
// nothing: it invites the user to write a query and then refuses it.

export interface SchemaRelation {
  name: string;
  store: string;
  columns: string[];
  attr_prefix?: string;
}

export interface Suggestion {
  /** Text inserted into the statement. */
  value: string;
  /** What it is, shown beside the value. */
  detail: string;
  kind: 'relation' | 'column' | 'attribute' | 'keyword';
}

// The keywords worth offering: the ones this engine actually accepts. It is
// SELECT-only, so suggesting INSERT or CREATE would be advertising a refusal.
const KEYWORDS = [
  'SELECT', 'FROM', 'WHERE', 'GROUP BY', 'ORDER BY', 'LIMIT', 'AND', 'OR',
  'AS', 'JOIN', 'ON', 'DESC', 'ASC', 'count(*)', 'BETWEEN', 'NOT', 'NULL',
];

/**
 * The token under the caret — what the user is part-way through typing.
 *
 * Backquotes and dots count as part of the token because an attribute column is
 * spelled `` `metadata.customer_id` ``: treating the dot as a boundary would
 * make the half-typed attribute impossible to complete, which is exactly the
 * name a user is least likely to remember.
 */
export function tokenAt(text: string, caret: number): { word: string; start: number } {
  let start = caret;
  while (start > 0 && /[A-Za-z0-9_.`*()]/.test(text[start - 1])) start--;
  return { word: text.slice(start, caret), start };
}

/** Case-insensitive subsequence match, so `mcid` finds `metadata.customer_id`. */
function matches(candidate: string, query: string): boolean {
  if (!query) return true;
  const c = candidate.toLowerCase();
  const q = query.toLowerCase();
  let i = 0;
  for (const ch of c) {
    if (ch === q[i]) i++;
    if (i === q.length) return true;
  }
  return false;
}

/**
 * Rank and filter the catalog against what is being typed.
 *
 * Relations come before columns because a statement names a relation first and
 * an unqualified column list is ambiguous until it does. Exact prefix matches
 * outrank subsequence ones so that typing a full name does not bury it under
 * fuzzier hits.
 */
export function suggestionsFor(
  relations: SchemaRelation[],
  word: string,
  limit = 12,
): Suggestion[] {
  const bare = word.replace(/`/g, '');
  const pool: Suggestion[] = [];

  for (const rel of relations) {
    pool.push({ value: rel.name, detail: `relation · ${rel.store}`, kind: 'relation' });
  }
  for (const rel of relations) {
    for (const col of rel.columns) {
      pool.push({ value: col, detail: `${rel.name} column`, kind: 'column' });
    }
    if (rel.attr_prefix) {
      // The keys are the customer's and cannot be enumerated, so what is
      // offered is the shape: enough to show that attributes exist and how they
      // are spelled, without inventing a key that may not be there.
      pool.push({
        value: '`' + rel.attr_prefix + '.`',
        detail: `${rel.name} attributes — type a key inside the backquotes`,
        kind: 'attribute',
      });
    }
  }
  for (const kw of KEYWORDS) {
    pool.push({ value: kw, detail: 'keyword', kind: 'keyword' });
  }

  const seen = new Set<string>();
  const scored = pool
    .filter((s) => {
      if (seen.has(s.value + s.kind)) return false;
      seen.add(s.value + s.kind);
      return matches(s.value, bare);
    })
    .map((s) => {
      const v = s.value.toLowerCase().replace(/`/g, '');
      const q = bare.toLowerCase();
      const prefix = q && v.startsWith(q) ? 0 : 1;
      const kindRank = { relation: 0, column: 1, attribute: 2, keyword: 3 }[s.kind];
      return { s, prefix, kindRank };
    });

  scored.sort((a, b) =>
    a.prefix - b.prefix || a.kindRank - b.kindRank || a.s.value.localeCompare(b.s.value));
  return scored.slice(0, limit).map((x) => x.s);
}
