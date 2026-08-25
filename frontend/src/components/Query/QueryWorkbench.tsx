"use client";

// The SQL workbench (P3.6).
//
// The engine, the endpoint and the push-down all shipped before this screen
// existed, which meant the capability that closes dimension D3 was reachable
// only with curl. The plan's standing rule is that a slice is done when it is
// usable from the UI; this is that.
//
// Two things it deliberately does not do. There is no **Visualize** button:
// dashboards (P6) do not exist, and a control that opens nothing is the kind of
// stub the parity gate is meant to catch. And it does not pretty-print or
// rewrite the user's SQL — the statement sent is the statement typed, because a
// tool that silently edits a query makes every rejection harder to reason about.

import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { useSearchParams } from 'next/navigation';
import { useTheme } from '@/context/ThemeContext';
import { api } from '@/lib/api/client';
import { runSql, SqlRefusal, type SqlCell, type SqlStats } from '@/lib/api/sqlStream';
import { suggestionsFor, tokenAt, type SchemaRelation, type Suggestion } from './schema';

const STARTER = `SELECT level, count(*) AS n
FROM logs
GROUP BY level
ORDER BY n DESC`;

/**
 * What each refusal category means, in the user's terms.
 *
 * The server sends a reason code and a precise message. The message says what
 * went wrong; these say why the rule exists, which is the part that stops a
 * refusal reading as a malfunction. Anything unlisted falls back to the
 * server's message alone rather than a guess.
 */
const REASON_HELP: Record<string, string> = {
  syntax: 'The statement could not be parsed.',
  statement_too_large: 'The statement is longer than the engine accepts.',
  multiple_statements: 'Only one statement per request. Stacked statements are never run.',
  not_a_select: 'This endpoint is read-only — only SELECT is accepted.',
  unknown_relation: 'That name is not in the catalog. The sidebar lists everything you can query.',
  qualified_name: 'Physical schema and table names cannot be addressed; use the catalog name alone.',
  denied_function: 'That function can reach outside the query engine, so it is not available.',
  into_outfile: 'Writing query output to a file is not available.',
  locking_read: 'Locking reads are not available on this endpoint.',
  too_many_joins: 'The statement joins more relations than the engine will plan.',
  subquery_too_deep: 'The subqueries nest deeper than the engine will plan.',
  too_many_set_branches: 'Too many UNION/INTERSECT branches.',
  cte_shadows_relation: 'A CTE is using the name of a catalog relation. Rename the CTE.',
};

interface Refusal {
  message: string;
  reason: string;
  status: number;
}

export function QueryWorkbench() {
  const { tokens: t } = useTheme();
  const searchParams = useSearchParams();

  // A shared query arrives in the URL. Read once as the initial value rather
  // than synchronised in an effect: binding to the param would fight the user's
  // typing, and setting state from an effect costs a cascading render.
  const [sql, setSql] = useState(() => searchParams.get('sql') ?? STARTER);
  const [relations, setRelations] = useState<SchemaRelation[]>([]);
  const [schemaError, setSchemaError] = useState<string | null>(null);

  const [columns, setColumns] = useState<string[]>([]);
  const [rows, setRows] = useState<SqlCell[][]>([]);
  const [stats, setStats] = useState<SqlStats | null>(null);
  const [complete, setComplete] = useState(true);
  const [running, setRunning] = useState(false);
  const [refusal, setRefusal] = useState<Refusal | null>(null);
  const [ranOnce, setRanOnce] = useState(false);

  const [caret, setCaret] = useState(0);
  const [suggestOpen, setSuggestOpen] = useState(false);
  const [active, setActive] = useState(0);

  const editorRef = useRef<HTMLTextAreaElement | null>(null);
  const abortRef = useRef<AbortController | null>(null);

  useEffect(() => {
    let live = true;
    api
      .get<{ relations: SchemaRelation[] }>('/api/v1/query/schema')
      .then((d) => live && setRelations(d?.relations ?? []))
      .catch((e: Error) => live && setSchemaError(e.message));
    return () => {
      live = false;
    };
  }, []);

  const token = useMemo(() => tokenAt(sql, caret), [sql, caret]);
  const suggestions = useMemo(
    () => (suggestOpen ? suggestionsFor(relations, token.word) : []),
    [suggestOpen, relations, token.word],
  );

  const insert = useCallback(
    (s: Suggestion) => {
      const before = sql.slice(0, token.start);
      const after = sql.slice(caret);
      const next = before + s.value + after;
      setSql(next);
      setSuggestOpen(false);
      setActive(0);
      // Land the caret inside the backquotes for an attribute, where the key
      // goes; at the end of the inserted text for everything else.
      const offset = s.kind === 'attribute' ? s.value.length - 1 : s.value.length;
      const pos = token.start + offset;
      requestAnimationFrame(() => {
        const el = editorRef.current;
        if (!el) return;
        el.focus();
        el.setSelectionRange(pos, pos);
        setCaret(pos);
      });
    },
    [sql, token.start, caret],
  );

  const execute = useCallback(async () => {
    const statement = sql.trim();
    if (!statement || running) return;

    abortRef.current?.abort();
    const ctrl = new AbortController();
    abortRef.current = ctrl;

    setRunning(true);
    setRanOnce(true);
    setRefusal(null);
    setColumns([]);
    setRows([]);
    setStats(null);
    setComplete(true);
    setSuggestOpen(false);

    try {
      const out = await runSql(
        statement,
        {
          onColumns: setColumns,
          onRows: (batch) => setRows((prev) => prev.concat(batch)),
        },
        ctrl.signal,
      );
      setStats(out.stats);
      setComplete(out.complete);
    } catch (e) {
      if (ctrl.signal.aborted) {
        // A cancel is the user's decision, not an error. Whatever rows arrived
        // stay on screen and are marked partial by `complete`.
        setComplete(false);
      } else if (e instanceof SqlRefusal) {
        setRefusal({ message: e.message, reason: e.reason, status: e.status });
      } else {
        setRefusal({ message: (e as Error).message, reason: '', status: 0 });
      }
    } finally {
      setRunning(false);
    }
  }, [sql, running]);

  const onKeyDown = (e: React.KeyboardEvent<HTMLTextAreaElement>) => {
    if ((e.metaKey || e.ctrlKey) && e.key === 'Enter') {
      e.preventDefault();
      void execute();
      return;
    }
    if ((e.metaKey || e.ctrlKey) && e.key === ' ') {
      e.preventDefault();
      setSuggestOpen(true);
      return;
    }
    if (!suggestOpen || suggestions.length === 0) return;

    if (e.key === 'ArrowDown') {
      e.preventDefault();
      setActive((i) => (i + 1) % suggestions.length);
    } else if (e.key === 'ArrowUp') {
      e.preventDefault();
      setActive((i) => (i - 1 + suggestions.length) % suggestions.length);
    } else if (e.key === 'Tab' || (e.key === 'Enter' && suggestOpen)) {
      e.preventDefault();
      insert(suggestions[active]);
    } else if (e.key === 'Escape') {
      e.preventDefault();
      setSuggestOpen(false);
    }
  };

  const share = async () => {
    const url = `${window.location.origin}/query?sql=${encodeURIComponent(sql)}`;
    try {
      await navigator.clipboard.writeText(url);
    } catch {
      // Clipboard can be denied; putting it in the address bar still shares it.
    }
    window.history.replaceState(null, '', `/query?sql=${encodeURIComponent(sql)}`);
  };

  // ── styles ────────────────────────────────────────────────────────────────
  const panel: React.CSSProperties = {
    background: t.panelBg,
    border: `1px solid ${t.panelBorder}`,
    borderRadius: 14,
    backdropFilter: 'blur(34px) saturate(180%)',
    WebkitBackdropFilter: 'blur(34px) saturate(180%)',
  };
  const mono = 'ui-monospace, SFMono-Regular, Menlo, Consolas, monospace';
  const btn = (primary = false): React.CSSProperties => ({
    padding: '7px 14px',
    borderRadius: 9,
    fontSize: 13,
    fontWeight: 600,
    cursor: 'pointer',
    border: `1px solid ${primary ? 'transparent' : t.panelBorder}`,
    background: primary ? t.accent : t.pillBg,
    color: primary ? '#fff' : t.text1,
  });

  return (
    <div style={{ display: 'flex', gap: 14, height: '100%', minHeight: 0, padding: 16 }}>
      {/* Schema */}
      <aside
        aria-label="Query schema"
        data-testid="schema-sidebar"
        style={{ ...panel, width: 232, flexShrink: 0, overflowY: 'auto', padding: 14 }}
      >
        <h2 style={{ fontSize: 12, fontWeight: 700, letterSpacing: 0.4, color: t.text2, margin: '0 0 10px' }}>
          SCHEMA
        </h2>
        {schemaError ? (
          <p style={{ fontSize: 12.5, color: t.red, margin: 0 }}>
            Could not load the catalog: {schemaError}
          </p>
        ) : relations.length === 0 ? (
          <p style={{ fontSize: 12.5, color: t.text3, margin: 0 }}>Loading…</p>
        ) : (
          relations.map((rel) => (
            <div key={rel.name} style={{ marginBottom: 14 }}>
              <button
                onClick={() => setSql((s) => `${s}${s.endsWith(' ') || !s ? '' : ' '}${rel.name}`)}
                title={`Insert ${rel.name}`}
                style={{
                  ...btn(), width: '100%', textAlign: 'left', fontFamily: mono,
                  fontSize: 12.5, padding: '5px 8px', marginBottom: 4,
                }}
              >
                {rel.name}
                <span style={{ color: t.text3, fontWeight: 400 }}> · {rel.store}</span>
              </button>
              <ul style={{ listStyle: 'none', margin: 0, padding: '0 0 0 8px' }}>
                {rel.columns.map((c) => (
                  <li key={c} style={{ fontFamily: mono, fontSize: 11.5, color: t.text2, padding: '1px 0' }}>
                    {c}
                  </li>
                ))}
                {rel.attr_prefix && (
                  <li style={{ fontFamily: mono, fontSize: 11.5, color: t.text3, padding: '1px 0' }}>
                    `{rel.attr_prefix}.&lt;key&gt;`
                  </li>
                )}
              </ul>
            </div>
          ))
        )}
      </aside>

      {/* Editor + results */}
      <div style={{ flex: 1, minWidth: 0, display: 'flex', flexDirection: 'column', gap: 12 }}>
        <section style={{ ...panel, padding: 14 }}>
          <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', marginBottom: 8 }}>
            <h1 style={{ fontSize: 15, fontWeight: 700, color: t.text1, margin: 0 }}>Query</h1>
            <div style={{ display: 'flex', gap: 8 }}>
              <button onClick={share} style={btn()} aria-label="Copy a shareable link to this query">
                Share
              </button>
              {running ? (
                <button onClick={() => abortRef.current?.abort()} style={btn()}>
                  Cancel
                </button>
              ) : (
                <button onClick={() => void execute()} style={btn(true)} data-testid="run-query">
                  Run ⌘↵
                </button>
              )}
            </div>
          </div>

          <label htmlFor="sql-editor" style={{ position: 'absolute', width: 1, height: 1, overflow: 'hidden', clip: 'rect(0 0 0 0)' }}>
            SQL statement
          </label>
          <textarea
            id="sql-editor"
            ref={editorRef}
            data-testid="sql-editor"
            value={sql}
            spellCheck={false}
            role="combobox"
            aria-expanded={suggestOpen}
            aria-controls="sql-suggestions"
            aria-autocomplete="list"
            onChange={(e) => {
              const next = e.target.value;
              const pos = e.target.selectionStart;
              setSql(next);
              setCaret(pos);
              // Only offer suggestions while a name is actually being typed.
              // Opening on every keystroke means the list is showing during
              // whitespace and punctuation too, where it has nothing to narrow
              // on and covers the editor with the same generic twelve entries.
              setSuggestOpen(tokenAt(next, pos).word.length > 0);
              setActive(0);
            }}
            onKeyUp={(e) => setCaret((e.target as HTMLTextAreaElement).selectionStart)}
            onClick={(e) => setCaret((e.target as HTMLTextAreaElement).selectionStart)}
            onKeyDown={onKeyDown}
            onBlur={() => setSuggestOpen(false)}
            style={{
              width: '100%', minHeight: 132, resize: 'vertical', fontFamily: mono,
              fontSize: 13, lineHeight: 1.55, padding: 10, borderRadius: 10,
              border: `1px solid ${t.panelBorder}`, background: t.pillBg,
              color: t.text1, outline: 'none',
            }}
          />

          {suggestOpen && suggestions.length > 0 && (
            <ul
              id="sql-suggestions"
              role="listbox"
              aria-label="Schema suggestions"
              style={{
                ...panel, listStyle: 'none', margin: '6px 0 0', padding: 4,
                maxHeight: 168, overflowY: 'auto',
              }}
            >
              {suggestions.map((s, i) => (
                <li
                  key={s.kind + s.value}
                  role="option"
                  aria-selected={i === active}
                  // onMouseDown, not onClick: blur fires first and would close
                  // the list before a click could land.
                  onMouseDown={(e) => {
                    e.preventDefault();
                    insert(s);
                  }}
                  style={{
                    display: 'flex', justifyContent: 'space-between', gap: 12,
                    padding: '4px 8px', borderRadius: 7, cursor: 'pointer',
                    fontFamily: mono, fontSize: 12.5,
                    background: i === active ? t.accentSoft : 'transparent',
                    color: t.text1,
                  }}
                >
                  <span>{s.value}</span>
                  <span style={{ color: t.text3, fontSize: 11.5 }}>{s.detail}</span>
                </li>
              ))}
            </ul>
          )}
          <p style={{ fontSize: 11.5, color: t.text3, margin: '8px 0 0' }}>
            ⌘↵ to run · ⌃Space for suggestions · attributes are backquoted, e.g.{' '}
            <code style={{ fontFamily: mono }}>`metadata.customer_id`</code>
          </p>
        </section>

        <section style={{ ...panel, flex: 1, minHeight: 0, display: 'flex', flexDirection: 'column', padding: 14 }}>
          <ResultArea
            running={running}
            ranOnce={ranOnce}
            refusal={refusal}
            columns={columns}
            rows={rows}
            stats={stats}
            complete={complete}
          />
        </section>
      </div>
    </div>
  );
}

function ResultArea(props: {
  running: boolean;
  ranOnce: boolean;
  refusal: Refusal | null;
  columns: string[];
  rows: SqlCell[][];
  stats: SqlStats | null;
  complete: boolean;
}) {
  const { running, ranOnce, refusal, columns, rows, stats, complete } = props;
  const { tokens: t } = useTheme();
  const mono = 'ui-monospace, SFMono-Regular, Menlo, Consolas, monospace';

  if (refusal) return <RefusalNotice refusal={refusal} />;

  if (!ranOnce && !running) {
    return (
      <p style={{ fontSize: 13, color: t.text2, margin: 0 }} role="status">
        Write a statement and run it. Results appear here.
      </p>
    );
  }

  if (running && rows.length === 0) {
    return (
      <p style={{ fontSize: 13, color: t.text2, margin: 0 }} role="status" aria-live="polite">
        Running…
      </p>
    );
  }

  if (!running && columns.length === 0) {
    return (
      <p style={{ fontSize: 13, color: t.text2, margin: 0 }} role="status">
        No result.
      </p>
    );
  }

  return (
    <>
      <div style={{ flex: 1, minHeight: 0, overflow: 'auto' }}>
        <table style={{ borderCollapse: 'collapse', width: '100%', fontFamily: mono, fontSize: 12.5 }}>
          <thead>
            <tr>
              {columns.map((c) => (
                <th
                  key={c}
                  style={{
                    position: 'sticky', top: 0, textAlign: 'left', padding: '6px 10px',
                    background: t.pillBg, color: t.text2, fontWeight: 600,
                    borderBottom: `1px solid ${t.panelBorder}`, whiteSpace: 'nowrap',
                  }}
                >
                  {c}
                </th>
              ))}
            </tr>
          </thead>
          <tbody data-testid="result-rows">
            {rows.map((row, i) => (
              <tr key={i}>
                {row.map((cell, j) => (
                  <td
                    key={j}
                    style={{
                      padding: '5px 10px', color: t.text1,
                      borderBottom: `1px solid ${t.panelBorder}`, whiteSpace: 'nowrap',
                    }}
                  >
                    {cell === null ? <span style={{ color: t.text3 }}>NULL</span> : String(cell)}
                  </td>
                ))}
              </tr>
            ))}
          </tbody>
        </table>
      </div>

      <footer
        style={{ marginTop: 10, fontSize: 12, color: t.text2, display: 'flex', gap: 16, flexWrap: 'wrap' }}
        role="status"
        aria-live="polite"
      >
        <span data-testid="result-summary">
          {rows.length.toLocaleString()} row{rows.length === 1 ? '' : 's'}
        </span>
        {stats && (
          <>
            <span>{stats.duration_ms} ms</span>
            {/* Zero scanned rows is not a missing number — it is the push-down
                working, and it is the most interesting thing on this line. */}
            <span title="Rows fetched from a store into the query engine">
              {stats.rows_scanned === 0
                ? 'answered in the store · 0 rows moved'
                : `${stats.rows_scanned.toLocaleString()} rows scanned`}
            </span>
          </>
        )}
        {!complete && !running && (
          <span style={{ color: t.amber, fontWeight: 600 }} data-testid="partial-warning">
            Partial — the stream ended before the server finished. These rows are a prefix of the answer, not all of it.
          </span>
        )}
      </footer>
    </>
  );
}

function RefusalNotice({ refusal }: { refusal: Refusal }) {
  const { tokens: t } = useTheme();
  const mono = 'ui-monospace, SFMono-Regular, Menlo, Consolas, monospace';

  const budget = refusal.reason.startsWith('budget:');
  const title = budget
    ? 'Too much data for one query'
    : refusal.status === 400
      ? 'The engine refused this statement'
      : 'The query failed';

  const help = budget
    ? `This query is legitimate but larger than the per-query limit (${refusal.reason.slice('budget:'.length)}). Narrow the time range, add a filter, or aggregate — an aggregate over the same rows is usually answered in the store without moving them.`
    : REASON_HELP[refusal.reason] ??
      (refusal.status >= 500
        ? 'Something went wrong inside the query engine. The detail is in the server log rather than here, because an internal error can describe schema that should not leak into a response.'
        : '');

  return (
    <div
      role="alert"
      data-testid="refusal"
      style={{
        border: `1px solid ${budget ? t.amber : t.red}`,
        background: budget ? 'transparent' : t.redSoft,
        borderRadius: 12, padding: 14,
      }}
    >
      <h2 style={{ margin: '0 0 6px', fontSize: 13.5, fontWeight: 700, color: budget ? t.amber : t.red }}>
        {title}
      </h2>
      {help && <p style={{ margin: '0 0 8px', fontSize: 13, color: t.text1 }}>{help}</p>}
      <pre
        data-testid="refusal-message"
        style={{
          margin: 0, fontFamily: mono, fontSize: 12, color: t.text2,
          whiteSpace: 'pre-wrap', wordBreak: 'break-word',
        }}
      >
        {refusal.message}
      </pre>
      {refusal.reason && (
        <p style={{ margin: '8px 0 0', fontSize: 11.5, color: t.text3, fontFamily: mono }}>
          reason: {refusal.reason}
        </p>
      )}
    </div>
  );
}
