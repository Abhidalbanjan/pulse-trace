"use client";

import React, { useState, useCallback } from 'react';
import { fetchWithAuth } from '@/lib/api';
import { useTheme } from '@/context/ThemeContext';

// First-class trace search over ClickHouse otel_traces (Traces · E1). Filters by
// service / operation / duration / status / span tag → per-trace summaries; a
// row opens a native span waterfall from /api/v1/traces/{id}.

interface TraceSummary {
  trace_id: string;
  root_service: string;
  root_operation: string;
  start_time: string;
  duration_ms: number | string;
  span_count: number | string;
  error_count: number | string;
  status: string;
}

interface Span {
  trace_id: string;
  span_id: string;
  parent_span_id: string;
  service: string;
  operation: string;
  start_time: string;
  duration_ms: number | string;
  status_code: string;
  attributes: Record<string, string>;
}

const INTERVALS = ['1h', '24h', '7d'];
const num = (v: number | string | undefined) => (typeof v === 'number' ? v : parseFloat(String(v ?? '0')) || 0);

// parseCH turns a ClickHouse DateTime[64] string into epoch-ms, tolerating the
// space separator and fractional seconds, and treating the value as UTC.
const parseCH = (ts: string): number => {
  if (!ts) return 0;
  let s = ts.trim().replace(' ', 'T').replace(/(\.\d{3})\d+/, '$1');
  if (!/[zZ]|[+-]\d{2}:?\d{2}$/.test(s)) s += 'Z';
  const p = Date.parse(s);
  return Number.isNaN(p) ? 0 : p;
};

const fmtMs = (ms: number) => (ms >= 1000 ? `${(ms / 1000).toFixed(2)}s` : `${ms.toFixed(ms < 10 ? 2 : 0)}ms`);

// serviceColor deterministically maps a service name to a stable hue.
const serviceColor = (svc: string): string => {
  let h = 0;
  for (let i = 0; i < svc.length; i++) h = (h * 31 + svc.charCodeAt(i)) % 360;
  return `hsl(${h}, 62%, 55%)`;
};

export function TraceSearchView() {
  const { tokens: t } = useTheme();
  const [service, setService] = useState('');
  const [operation, setOperation] = useState('');
  const [status, setStatus] = useState<'any' | 'error' | 'ok'>('any');
  const [minMs, setMinMs] = useState('');
  const [maxMs, setMaxMs] = useState('');
  const [tags, setTags] = useState('');
  const [interval, setInterval] = useState('1h');

  const [results, setResults] = useState<TraceSummary[]>([]);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [searched, setSearched] = useState(false);

  const [openTrace, setOpenTrace] = useState<string | null>(null);
  const [spans, setSpans] = useState<Span[]>([]);
  const [spansLoading, setSpansLoading] = useState(false);
  const [selectedSpan, setSelectedSpan] = useState<Span | null>(null);

  const search = useCallback(() => {
    setLoading(true);
    setError(null);
    setSearched(true);
    const p = new URLSearchParams({ interval });
    if (service.trim()) p.set('service', service.trim());
    if (operation.trim()) p.set('operation', operation.trim());
    if (status !== 'any') p.set('status', status);
    if (minMs.trim()) p.set('minDurationMs', minMs.trim());
    if (maxMs.trim()) p.set('maxDurationMs', maxMs.trim());
    // tags: comma-separated `key:value`, sent as repeated `tag` params.
    tags.split(',').map((s) => s.trim()).filter(Boolean).forEach((tag) => p.append('tag', tag));

    fetchWithAuth(`/api/v1/traces?${p.toString()}`)
      .then(async (res) => {
        if (!res.ok) throw new Error(await res.text());
        return res.json();
      })
      .then((json) => setResults(json.data ?? []))
      .catch(() => setError('Failed to search traces.'))
      .finally(() => setLoading(false));
  }, [service, operation, status, minMs, maxMs, tags, interval]);

  const loadTrace = useCallback((id: string) => {
    setOpenTrace(id);
    setSelectedSpan(null);
    setSpansLoading(true);
    fetchWithAuth(`/api/v1/traces/${encodeURIComponent(id)}`)
      .then(async (res) => {
        if (!res.ok) throw new Error(await res.text());
        return res.json();
      })
      .then((json) => setSpans(json.data ?? []))
      .catch(() => setSpans([]))
      .finally(() => setSpansLoading(false));
  }, []);

  const input: React.CSSProperties = {
    background: t.dark ? 'rgba(255,255,255,0.06)' : 'rgba(255,255,255,0.7)',
    border: '1px solid ' + t.panelBorder, color: t.text1, padding: '9px 12px', borderRadius: '10px', outline: 'none', fontSize: '13px',
  };
  const panel: React.CSSProperties = { background: t.panelBg, border: '1px solid ' + t.panelBorder, borderRadius: '18px' };

  // ── Waterfall for one opened trace ─────────────────────────────────────────
  if (openTrace) {
    const start = spans.length ? Math.min(...spans.map((s) => parseCH(s.start_time))) : 0;
    const total = spans.length ? Math.max(...spans.map((s) => parseCH(s.start_time) - start + num(s.duration_ms))) : 1;
    return (
      <div style={{ display: 'flex', flexDirection: 'column', height: '100%', minHeight: 0, gap: '14px' }}>
        <button onClick={() => setOpenTrace(null)} style={{ ...input, alignSelf: 'flex-start', cursor: 'pointer' }}>← Back to results</button>
        <div style={{ display: 'flex', gap: '14px', minHeight: 0, flex: 1 }}>
          <div style={{ ...panel, flex: 1, overflow: 'auto', padding: '14px' }}>
            <div style={{ fontSize: '12px', color: t.text2, fontFamily: 'monospace', marginBottom: '12px' }}>trace {openTrace}</div>
            {spansLoading ? (
              <div style={{ padding: '32px', textAlign: 'center', color: t.text2 }}>Loading spans…</div>
            ) : spans.length === 0 ? (
              <div style={{ padding: '32px', textAlign: 'center', color: t.text2 }}>No spans found for this trace.</div>
            ) : (
              <div style={{ display: 'flex', flexDirection: 'column', gap: '4px' }}>
                {spans.map((s) => {
                  const offset = ((parseCH(s.start_time) - start) / total) * 100;
                  const width = Math.max(0.5, (num(s.duration_ms) / total) * 100);
                  const isErr = s.status_code === 'STATUS_CODE_ERROR';
                  return (
                    <div key={s.span_id} onClick={() => setSelectedSpan(s)} style={{ cursor: 'pointer', padding: '3px 6px', borderRadius: '6px', background: selectedSpan?.span_id === s.span_id ? (t.dark ? 'rgba(255,255,255,0.06)' : 'rgba(0,0,0,0.04)') : 'transparent' }}>
                      <div style={{ display: 'flex', justifyContent: 'space-between', fontSize: '11.5px', marginBottom: '2px' }}>
                        <span style={{ color: t.text1, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap', maxWidth: '70%' }}>
                          <span style={{ color: serviceColor(s.service), fontWeight: 600 }}>{s.service}</span> · {s.operation}
                        </span>
                        <span style={{ color: t.text2 }}>{fmtMs(num(s.duration_ms))}</span>
                      </div>
                      <div style={{ height: '10px', background: t.dark ? 'rgba(255,255,255,0.05)' : 'rgba(0,0,0,0.04)', borderRadius: '3px', position: 'relative' }}>
                        <div style={{ position: 'absolute', left: `${offset}%`, width: `${width}%`, top: 0, bottom: 0, background: serviceColor(s.service), borderRadius: '3px', border: isErr ? `2px solid ${t.red}` : 'none' }} />
                      </div>
                    </div>
                  );
                })}
              </div>
            )}
          </div>
          {selectedSpan && (
            <div style={{ ...panel, width: '340px', flexShrink: 0, overflow: 'auto', padding: '16px' }}>
              <div style={{ fontWeight: 700, color: t.text1, fontSize: '14px', marginBottom: '4px' }}>{selectedSpan.operation}</div>
              <div style={{ fontSize: '12px', color: serviceColor(selectedSpan.service), marginBottom: '12px' }}>{selectedSpan.service} · {fmtMs(num(selectedSpan.duration_ms))}{selectedSpan.status_code === 'STATUS_CODE_ERROR' && <span style={{ color: t.red, marginLeft: 8 }}>ERROR</span>}</div>
              <div style={{ fontSize: '11px', color: t.text2, textTransform: 'uppercase', marginBottom: '8px' }}>Attributes</div>
              <div style={{ display: 'flex', flexDirection: 'column', gap: '5px' }}>
                {Object.entries(selectedSpan.attributes || {}).map(([k, v]) => (
                  <div key={k} style={{ display: 'flex', gap: '8px', fontSize: '12px', fontFamily: 'monospace' }}>
                    <span style={{ color: t.text2, flexShrink: 0 }}>{k}</span>
                    <span style={{ color: t.text1, wordBreak: 'break-all' }}>{String(v)}</span>
                  </div>
                ))}
                {Object.keys(selectedSpan.attributes || {}).length === 0 && <span style={{ color: t.text2, fontSize: '12px' }}>No attributes</span>}
              </div>
            </div>
          )}
        </div>
      </div>
    );
  }

  // ── Search + results ───────────────────────────────────────────────────────
  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: '14px', height: '100%', minHeight: 0 }}>
      <div style={{ ...panel, padding: '14px', display: 'flex', gap: '10px', flexWrap: 'wrap', alignItems: 'center' }}>
        <input value={service} onChange={(e) => setService(e.target.value)} placeholder="Service" aria-label="Service" style={{ ...input, width: '150px' }} />
        <input value={operation} onChange={(e) => setOperation(e.target.value)} placeholder="Operation" aria-label="Operation" style={{ ...input, width: '170px' }} />
        <select value={status} onChange={(e) => setStatus(e.target.value as 'any' | 'error' | 'ok')} aria-label="Status" style={input}>
          <option value="any">Any status</option>
          <option value="error">Errors only</option>
          <option value="ok">OK only</option>
        </select>
        <input value={minMs} onChange={(e) => setMinMs(e.target.value)} placeholder="min ms" aria-label="Min duration ms" style={{ ...input, width: '80px' }} inputMode="numeric" />
        <input value={maxMs} onChange={(e) => setMaxMs(e.target.value)} placeholder="max ms" aria-label="Max duration ms" style={{ ...input, width: '80px' }} inputMode="numeric" />
        <input value={tags} onChange={(e) => setTags(e.target.value)} placeholder="tags e.g. http.method:POST" aria-label="Tags" onKeyDown={(e) => e.key === 'Enter' && search()} style={{ ...input, flex: 1, minWidth: '160px' }} />
        <select value={interval} onChange={(e) => setInterval(e.target.value)} aria-label="Interval" style={input}>
          {INTERVALS.map((i) => <option key={i} value={i}>Last {i}</option>)}
        </select>
        <button onClick={search} style={{ padding: '9px 20px', borderRadius: '10px', border: 'none', background: `linear-gradient(135deg, ${t.accent}, ${t.accent2})`, color: '#fff', fontWeight: 600, cursor: 'pointer', fontSize: '13px' }}>Search</button>
      </div>

      <div style={{ ...panel, flex: 1, overflow: 'auto', boxShadow: t.shadow }}>
        {loading ? (
          <div style={{ padding: '48px', textAlign: 'center', color: t.text2 }}>Searching otel_traces…</div>
        ) : error ? (
          <div style={{ padding: '48px', textAlign: 'center', color: t.red }}>{error}</div>
        ) : results.length === 0 ? (
          <div style={{ padding: '48px', textAlign: 'center', color: t.text2 }}>{searched ? 'No traces match these filters.' : 'Set filters and search.'}</div>
        ) : (
          <table style={{ width: '100%', borderCollapse: 'collapse', textAlign: 'left' }}>
            <thead>
              <tr style={{ borderBottom: '1px solid ' + t.panelBorder, background: t.dark ? 'rgba(0,0,0,0.15)' : 'rgba(0,0,0,0.03)' }}>
                {['Status', 'Trace', 'Root', 'Spans', 'Errors', 'Duration', 'Started'].map((h) => (
                  <th key={h} style={{ padding: '13px 16px', fontWeight: 600, color: t.text2, fontSize: '12px' }}>{h}</th>
                ))}
              </tr>
            </thead>
            <tbody>
              {results.map((r) => {
                const isErr = r.status === 'error';
                return (
                  <tr key={r.trace_id} onClick={() => loadTrace(r.trace_id)} style={{ borderBottom: '1px solid ' + t.panelBorder, cursor: 'pointer' }}>
                    <td style={{ padding: '13px 16px' }}>
                      <span style={{ color: isErr ? t.red : t.green, fontSize: '11px', fontWeight: 700, padding: '3px 9px', borderRadius: '100px', background: (isErr ? t.red : t.green) + '18' }}>{isErr ? 'Error' : 'OK'}</span>
                    </td>
                    <td style={{ padding: '13px 16px', fontSize: '12.5px', fontFamily: 'monospace', color: t.text1 }}>{r.trace_id.substring(0, 14)}…</td>
                    <td style={{ padding: '13px 16px', fontSize: '13px', color: t.text1 }}><span style={{ color: t.accent, marginRight: 8 }}>{r.root_service}</span>{r.root_operation}</td>
                    <td style={{ padding: '13px 16px', fontSize: '13px', color: t.text2 }}>{num(r.span_count)}</td>
                    <td style={{ padding: '13px 16px', fontSize: '13px', color: num(r.error_count) > 0 ? t.red : t.text2 }}>{num(r.error_count)}</td>
                    <td style={{ padding: '13px 16px', fontSize: '13px', fontWeight: 600, color: t.text1 }}>{fmtMs(num(r.duration_ms))}</td>
                    <td style={{ padding: '13px 16px', fontSize: '12.5px', color: t.text2 }}>{r.start_time ? new Date(parseCH(r.start_time)).toLocaleString() : '—'}</td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        )}
      </div>
    </div>
  );
}
