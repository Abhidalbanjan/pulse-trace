// "Open in SQL" — handing a Log Explorer search to the workbench.
//
// The Explorer searches Quickwit's query language; the workbench speaks SQL.
// These are different languages, and the temptation is to translate the whole
// string. That is the same mistake the push-down matcher refuses to make: a
// filter that means *almost* the same thing produces a plausible wrong answer,
// and a user who arrived by clicking a button has no reason to doubt it.
//
// So this translates only the shapes that map exactly — `field:value` on a
// catalog column, joined by AND — and reports anything it dropped, so the
// caller can say so rather than let it look carried over.

/** Columns of the `logs` relation that a term can safely become a filter on. */
const FILTERABLE = new Set(['service_name', 'level', 'trace_id']);

const BASE_COLUMNS = 'timestamp, service_name, level, message';

export interface Translation {
  sql: string;
  /** Parts of the original search that could not be expressed and were dropped. */
  dropped: string[];
}

/**
 * Build a statement from an Explorer query string.
 *
 * A search this cannot express still yields a valid, useful statement over the
 * same relation — the point of the button is to get a user into SQL — but the
 * unexpressed parts are returned rather than silently discarded.
 */
export function sqlFromLogSearch(query: string, limit = 100): Translation {
  const dropped: string[] = [];
  const filters: string[] = [];

  const trimmed = (query ?? '').trim();
  if (trimmed && trimmed !== '*') {
    // Split on whitespace-delimited AND, the only combinator translated. Terms
    // may carry a quoted value, so quotes are respected while splitting.
    const parts = trimmed.match(/(?:[^\s"]+|"[^"]*")+/g) ?? [];
    for (const part of parts) {
      if (/^AND$/i.test(part)) continue;
      const m = /^([A-Za-z_][A-Za-z0-9_]*):"?([^"]*)"?$/.exec(part);
      if (m && FILTERABLE.has(m[1]) && m[2]) {
        // The value is escaped for SQL by doubling quotes — the statement is
        // then validated and parsed server-side like any other.
        filters.push(`${m[1]} = '${m[2].replace(/'/g, "''")}'`);
      } else {
        dropped.push(part);
      }
    }
  }

  const where = filters.length ? `\nWHERE ${filters.join('\n  AND ')}` : '';
  return {
    sql: `SELECT ${BASE_COLUMNS}\nFROM logs${where}\nORDER BY timestamp DESC\nLIMIT ${limit}`,
    dropped,
  };
}

/** The workbench URL for a search, ready to navigate to. */
export function queryHrefFromLogSearch(query: string): { href: string; dropped: string[] } {
  const { sql, dropped } = sqlFromLogSearch(query);
  return { href: `/query?sql=${encodeURIComponent(sql)}`, dropped };
}
