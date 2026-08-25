// Streaming client for POST /api/v1/query/sql (P3.6).
//
// The endpoint answers NDJSON — one `{"columns":…}` line, then zero or more
// `{"row":…}` lines, then one `{"stats":…}` line. This reads it incrementally
// rather than buffering, so the grid can paint the first rows while the rest
// are still arriving.
//
// The important detail is the *last* line. `stats` is what says the result is
// complete; a stream that ends without it delivered a prefix of the answer, not
// the answer. Callers are given that distinction (`complete`) instead of being
// left to assume the rows they hold are all of them — a truncated result that
// looks whole is the one failure mode a query tool must not have.

import { fetchWithAuth } from '../api';

export interface SqlStats {
  rows_returned: number;
  rows_scanned: number;
  duration_ms: number;
}

export type SqlCell = string | number | boolean | null;

export interface SqlOutcome {
  columns: string[];
  rows: SqlCell[][];
  stats: SqlStats | null;
  /** True only when the server sent its terminating stats line. */
  complete: boolean;
}

/**
 * A query the server refused, carrying the reason it gave.
 *
 * Refusals are not failures of the tool — they are the engine telling the user
 * something specific about their statement, and the reason code is the most
 * useful thing on the screen. It is kept as a field rather than folded into the
 * message so the UI can explain the category as well as quote the detail.
 */
export class SqlRefusal extends Error {
  readonly reason: string;
  readonly status: number;
  constructor(message: string, reason: string, status: number) {
    super(message);
    this.name = 'SqlRefusal';
    this.reason = reason;
    this.status = status;
  }
}

export interface SqlStreamHandlers {
  onColumns?: (columns: string[]) => void;
  /** Called per batch rather than per row so React re-renders once per chunk. */
  onRows?: (rows: SqlCell[][]) => void;
}

export async function runSql(
  sql: string,
  handlers: SqlStreamHandlers = {},
  signal?: AbortSignal,
): Promise<SqlOutcome> {
  const res = await fetchWithAuth('/api/v1/query/sql', {
    method: 'POST',
    body: JSON.stringify({ sql }),
    signal,
  });

  if (!res.ok) {
    const text = await res.text();
    let error = text || `HTTP ${res.status}`;
    let reason = '';
    try {
      const parsed = JSON.parse(text) as { error?: string; reason?: string };
      if (parsed.error) error = parsed.error;
      if (parsed.reason) reason = parsed.reason;
    } catch {
      // A non-JSON body (a proxy error page, say) still has to surface; the raw
      // text is more use than a generic message.
    }
    throw new SqlRefusal(error, reason, res.status);
  }

  const out: SqlOutcome = { columns: [], rows: [], stats: null, complete: false };
  if (!res.body) return out;

  const reader = res.body.getReader();
  const decoder = new TextDecoder();
  let buffer = '';

  const consume = (line: string) => {
    const trimmed = line.trim();
    if (!trimmed) return;
    let msg: { columns?: string[]; row?: SqlCell[]; stats?: SqlStats };
    try {
      msg = JSON.parse(trimmed);
    } catch {
      return; // a line we cannot read is not a line we can act on
    }
    if (msg.columns) {
      out.columns = msg.columns;
      handlers.onColumns?.(msg.columns);
    } else if (msg.row) {
      out.rows.push(msg.row);
      pending.push(msg.row);
    } else if (msg.stats) {
      out.stats = msg.stats;
      out.complete = true;
    }
  };

  let pending: SqlCell[][] = [];
  const flush = () => {
    if (pending.length) {
      handlers.onRows?.(pending);
      pending = [];
    }
  };

  for (;;) {
    const { done, value } = await reader.read();
    if (done) break;
    buffer += decoder.decode(value, { stream: true });
    let nl: number;
    while ((nl = buffer.indexOf('\n')) !== -1) {
      consume(buffer.slice(0, nl));
      buffer = buffer.slice(nl + 1);
    }
    flush();
  }
  // A body that ended without a trailing newline still has one message in it.
  consume(buffer);
  flush();

  return out;
}
