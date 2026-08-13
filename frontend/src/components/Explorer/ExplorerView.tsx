"use client";

import React, { useState, useEffect, useRef } from 'react';
import { useRouter } from 'next/navigation';
import { fetchWithAuth } from '@/lib/api';
import { BarChart, Bar, Tooltip, ResponsiveContainer, Brush } from 'recharts';
import { useTheme } from '@/context/ThemeContext';

interface LogEntry {
  id: string;
  timestamp: string;
  service_name: string;
  level: string;
  message: string;
  trace_id?: string;
  tenant_id: string;
  _raw: unknown;
}

interface QuickwitHit { id?: string; trace_id?: string; timestamp?: string; service_name?: string; level?: string; message?: string; tenant_id?: string; }

// Map a raw Quickwit hit to a LogEntry. Prefer the document's own id (what the
// GET /api/v1/logs/{id} permalink endpoint resolves by); fall back to trace_id.
function toLogEntry(hit: QuickwitHit): LogEntry {
  return {
    id: hit.id || hit.trace_id || Math.random().toString(),
    timestamp: hit.timestamp || new Date().toISOString(),
    service_name: hit.service_name || 'unknown',
    level: hit.level || 'INFO',
    message: hit.message || JSON.stringify(hit),
    trace_id: hit.trace_id,
    tenant_id: hit.tenant_id || 'default',
    _raw: hit,
  };
}

interface Bucket {
  key: string | number;
  doc_count: number;
}

type TimeRange = "15m" | "1h" | "24h" | "7d" | "all";

const TIME_RANGES: { key: TimeRange; label: string; ms: number }[] = [
  { key: "15m", label: "15m", ms: 15 * 60_000 },
  { key: "1h", label: "1h", ms: 60 * 60_000 },
  { key: "24h", label: "24h", ms: 24 * 60 * 60_000 },
  { key: "7d", label: "7d", ms: 7 * 24 * 60 * 60_000 },
  { key: "all", label: "All", ms: 0 },
];

interface SavedSearch {
  id: string;
  name: string;
  shared: boolean;
  mine: boolean;
  query_params: { query?: string; regex?: string; range?: string };
}

export function ExplorerView() {
  const { tokens: t } = useTheme();
  const router = useRouter();
  const [logs, setLogs] = useState<LogEntry[]>([]);
  const [loading, setLoading] = useState(false);
  const [query, setQuery] = useState("*");
  const [selectedLog, setSelectedLog] = useState<LogEntry | null>(null);
  const [permalinkCopied, setPermalinkCopied] = useState(false);
  const [searchLinkCopied, setSearchLinkCopied] = useState(false);

  // F6: surrounding-context view. Given an anchor log, fetch the logs
  // immediately before/after it on the same service (regardless of the current
  // query) so an operator can read an error line in situ.
  const [context, setContext] = useState<{ anchor: LogEntry; before: LogEntry[]; after: LogEntry[] } | null>(null);
  const [contextLoading, setContextLoading] = useState(false);
  const [contextError, setContextError] = useState<string | null>(null);

  const openContext = async (log: LogEntry) => {
    setContext(null);
    setContextError(null);
    setContextLoading(true);
    try {
      const res = await fetchWithAuth(`/api/v1/logs/${encodeURIComponent(log.id)}/context?before=25&after=25`);
      if (!res.ok) throw new Error(`Context unavailable (${res.status})`);
      const body = await res.json();
      const d = body?.data ?? body;
      setContext({
        anchor: d.anchor ? toLogEntry(d.anchor) : log,
        before: (d.before || []).map(toLogEntry),
        after: (d.after || []).map(toLogEntry),
      });
    } catch (err) {
      setContextError(err instanceof Error ? err.message : 'Failed to load context');
    } finally {
      setContextLoading(false);
    }
  };

  // F6: single-log permalink. Copy a shareable URL to a specific log, and on
  // load resolve ?log=<id> straight to its canonical record via GET /logs/{id}.
  const copyPermalink = async (id: string) => {
    const url = `${window.location.origin}/explorer?log=${encodeURIComponent(id)}`;
    try {
      await navigator.clipboard.writeText(url);
      setPermalinkCopied(true);
      window.setTimeout(() => setPermalinkCopied(false), 2000);
    } catch {
      // Clipboard unavailable (insecure context) — the URL is still shareable.
    }
  };

  useEffect(() => {
    const logId = new URLSearchParams(window.location.search).get('log');
    if (!logId) return;
    fetchWithAuth(`/api/v1/logs/${encodeURIComponent(logId)}`)
      .then((res) => (res.ok ? res.json() : null))
      .then((data) => {
        const hit = data?.data ?? data;
        if (hit) setSelectedLog(toLogEntry(hit));
      })
      .catch(() => {});
  }, []);

  // Query-depth controls: treat the search box as a regex on the message, and
  // scope to a relative time window. These build extra Quickwit clauses at
  // fetch time (see buildEffectiveQuery) rather than mutating the box text, so
  // the user's typed query stays intact as they toggle them.
  const [regexMode, setRegexMode] = useState(false);
  const [timeRange, setTimeRange] = useState<TimeRange>("all");

  // Saved searches (per-user, from the gateway). savedError surfaces a failed
  // save/list so the control never silently no-ops.
  const [savedSearches, setSavedSearches] = useState<SavedSearch[]>([]);
  const [savedOpen, setSavedOpen] = useState(false);
  const [saveName, setSaveName] = useState("");
  const [saveShared, setSaveShared] = useState(false);
  const [savedError, setSavedError] = useState<string | null>(null);

  // Aggregation State
  const [serviceBuckets, setServiceBuckets] = useState<Bucket[]>([]);
  const [levelBuckets, setLevelBuckets] = useState<Bucket[]>([]);
  const [volumeData, setVolumeData] = useState<{ time: string; iso: string; count: number }[]>([]);
  // Histogram brush-to-zoom (Log Explorer · E5): an explicit [start,end] window
  // selected on the volume chart, plus the pending brush selection before apply.
  const [brushRange, setBrushRange] = useState<{ start: string; end: string } | null>(null);
  const [pendingBrush, setPendingBrush] = useState<{ startIndex: number; endIndex: number } | null>(null);

  // Live Tail State
  const [isLiveTail, setIsLiveTail] = useState(false);
  const liveTailRef = useRef<NodeJS.Timeout | null>(null);

  // timeClause turns the selected relative window into a Quickwit datetime
  // range on the timestamp field ("all" means no bound).
  const timeClause = (range: TimeRange): string => {
    const r = TIME_RANGES.find((x) => x.key === range);
    if (!r || r.ms === 0) return "";
    const start = new Date(Date.now() - r.ms).toISOString();
    return `timestamp:[${start} TO *]`;
  };

  // buildEffectiveQuery combines the search box, the regex toggle, and the time
  // window into the actual query sent to Quickwit — without mutating the box so
  // toggles stay reversible.
  const buildEffectiveQuery = (): string => {
    const clauses: string[] = [];
    const raw = query.trim();
    if (regexMode) {
      // Treat the box as a regex over the message field. Escape '/' so it can't
      // terminate the literal early (mirrors the server-side log search).
      if (raw && raw !== "*") clauses.push(`message:/${raw.replace(/\//g, "\\/")}/`);
    } else if (raw && raw !== "*") {
      clauses.push(raw);
    }
    // A brush-selected window (E5) takes precedence over the relative preset.
    if (brushRange) {
      clauses.push(`timestamp:[${brushRange.start} TO ${brushRange.end}]`);
    } else {
      const tc = timeClause(timeRange);
      if (tc) clauses.push(tc);
    }
    return clauses.length ? clauses.join(" AND ") : "*";
  };

  // applyBrushZoom turns the pending brush selection into an explicit time window
  // and re-queries; picking a preset range clears it.
  const applyBrushZoom = () => {
    if (!pendingBrush || volumeData.length === 0) return;
    const lo = Math.max(0, Math.min(pendingBrush.startIndex, pendingBrush.endIndex));
    const hi = Math.min(volumeData.length - 1, Math.max(pendingBrush.startIndex, pendingBrush.endIndex));
    const start = volumeData[lo]?.iso;
    // End at the following bucket's start (the histogram interval) so the last
    // selected bucket is inclusive; fall back to the last bucket + a second.
    const endBase = volumeData[Math.min(hi + 1, volumeData.length - 1)]?.iso ?? volumeData[hi]?.iso;
    if (!start || !endBase) return;
    setBrushRange({ start, end: endBase });
    setPendingBrush(null);
  };

  // F6: shareable query URLs. Hydrate the full search state (box text, regex
  // toggle, time window) from the URL on mount so a pasted link reproduces the
  // exact search — not just a single-log permalink. `?log=` still deep-links to
  // one record (handled separately above); these params drive the query itself.
  useEffect(() => {
    const p = new URLSearchParams(window.location.search);
    const q = p.get('q');
    const regex = p.get('regex');
    const range = p.get('range');
    // eslint-disable-next-line react-hooks/set-state-in-effect -- one-shot hydration from the URL on mount; the effect is the correct client-only place to read window.location
    if (q !== null) setQuery(q);
    if (regex === 'true') setRegexMode(true);
    if (range && TIME_RANGES.some((r) => r.key === range)) setTimeRange(range as TimeRange);
  }, []);

  // copySearchLink builds a shareable URL encoding the current search state.
  const copySearchLink = async () => {
    const p = new URLSearchParams();
    if (query && query !== '*') p.set('q', query);
    if (regexMode) p.set('regex', 'true');
    if (timeRange !== 'all') p.set('range', timeRange);
    const qs = p.toString();
    const url = `${window.location.origin}/explorer${qs ? `?${qs}` : ''}`;
    try {
      await navigator.clipboard.writeText(url);
      setSearchLinkCopied(true);
      window.setTimeout(() => setSearchLinkCopied(false), 2000);
    } catch {
      // Clipboard unavailable (insecure context) — the URL is still shareable.
    }
  };

  const fetchLogs = () => {
    setLoading(true);
    // Add aggregations to the Quickwit query
    const requestBody = {
      query: buildEffectiveQuery(),
      max_hits: 100,
      sort_by_field: "-timestamp",
      aggs: {
        service_counts: {
          terms: { field: "service_name" }
        },
        level_counts: {
          terms: { field: "level" }
        },
        volume_over_time: {
          date_histogram: { field: "timestamp", fixed_interval: "10s" }
        }
      }
    };

    fetchWithAuth('/api/v1/search/pulsetrace-logs/search', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(requestBody)
    })
      .then(res => res.json())
      .then(data => {
        if (data && data.hits) {
          const formattedLogs = data.hits.map(toLogEntry);
          setLogs(formattedLogs);
        }

        if (data && data.aggregations) {
          if (data.aggregations.service_counts) setServiceBuckets(data.aggregations.service_counts.buckets || []);
          if (data.aggregations.level_counts) setLevelBuckets(data.aggregations.level_counts.buckets || []);

          if (data.aggregations.volume_over_time && data.aggregations.volume_over_time.buckets) {
            const chartData = data.aggregations.volume_over_time.buckets.map((b: { key: string | number; doc_count: number }) => ({
              time: new Date(b.key).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', second:'2-digit' }),
              iso: new Date(b.key).toISOString(),
              count: b.doc_count
            }));
            setVolumeData(chartData);
          }
        }
      })
      .catch(err => console.error("Failed to fetch from Quickwit:", err))
      .finally(() => setLoading(false));
  };

  useEffect(() => {
    // eslint-disable-next-line react-hooks/set-state-in-effect -- intentional one-shot fetch/hydration on mount; effect is the right place to sync from the API/localStorage
    fetchLogs();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [query, regexMode, timeRange, brushRange]);

  // Selecting a relative preset clears any brush zoom (the two are exclusive).
  const selectTimeRange = (range: TimeRange) => {
    setBrushRange(null);
    setPendingBrush(null);
    setTimeRange(range);
  };

  const loadSavedSearches = () => {
    fetchWithAuth('/api/v1/saved-searches?kind=logs')
      .then((res) => (res.ok ? res.json() : Promise.reject(res.status)))
      .then((data) => setSavedSearches(data?.data || []))
      .catch((err) => console.error('Failed to load saved searches:', err));
  };

  // Load this user's saved searches once on mount.
  useEffect(() => {
    loadSavedSearches();
  }, []);

  const applySavedSearch = (s: SavedSearch) => {
    const p = s.query_params || {};
    setQuery(p.query || '*');
    setRegexMode(p.regex === 'true');
    setBrushRange(null);
    setPendingBrush(null);
    setTimeRange((p.range as TimeRange) || 'all');
    setSavedOpen(false);
  };

  const saveCurrentSearch = () => {
    const name = saveName.trim();
    if (!name) return;
    setSavedError(null);
    fetchWithAuth('/api/v1/saved-searches', {
      method: 'POST',
      body: JSON.stringify({
        name,
        kind: 'logs',
        shared: saveShared,
        query_params: { query, regex: String(regexMode), range: timeRange },
      }),
    })
      .then(async (res) => {
        if (res.status === 201) {
          setSaveName('');
          setSaveShared(false);
          loadSavedSearches();
          return;
        }
        if (res.status === 409) throw new Error('A saved search with that name already exists.');
        if (res.status === 403) throw new Error('You do not have permission to save searches.');
        throw new Error(`Save failed (${res.status})`);
      })
      .catch((err) => setSavedError(err.message || 'Save failed'));
  };

  const deleteSavedSearch = (id: string) => {
    fetchWithAuth(`/api/v1/saved-searches/${id}`, { method: 'DELETE' })
      .then((res) => {
        if (res.ok) loadSavedSearches();
      })
      .catch((err) => console.error('Failed to delete saved search:', err));
  };

  useEffect(() => {
    if (isLiveTail) {
      liveTailRef.current = setInterval(() => {
        fetchLogs();
      }, 3000);
    } else if (liveTailRef.current) {
      clearInterval(liveTailRef.current);
    }
    return () => {
      if (liveTailRef.current) clearInterval(liveTailRef.current);
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [isLiveTail, query, regexMode, timeRange]);

  const toggleLiveTail = () => setIsLiveTail(!isLiveTail);

  const getLevelColor = (level: string) => {
    switch (level.toUpperCase()) {
      case 'ERROR':
      case 'FATAL': return t.red;
      case 'WARN': return t.amber;
      case 'INFO': return t.green;
      case 'DEBUG': return t.text2;
      default: return t.text1;
    }
  };

  const addFilter = (field: string, value: string) => {
    const newTerm = `${field}:"${value}"`;
    if (query === "*") {
      setQuery(newTerm);
    } else if (!query.includes(newTerm)) {
      setQuery(`${query} AND ${newTerm}`);
    }
  };

  const badgeStyle: React.CSSProperties = {
    color: t.text2,
    fontSize: '11px',
    background: t.dark ? 'rgba(255,255,255,0.08)' : 'rgba(0,0,0,0.06)',
    padding: '2px 7px',
    borderRadius: '100px',
  };

  const glassPanel: React.CSSProperties = {
    background: t.panelBg,
    border: `1px solid ${t.panelBorder}`,
    backdropFilter: 'blur(34px) saturate(180%)',
    WebkitBackdropFilter: 'blur(34px) saturate(180%)',
    boxShadow: t.shadow,
  };

  return (
    <div style={{ display: 'flex', gap: '18px', height: 'calc(100vh - 124px)', minWidth: 0 }}>

      {/* Left Facet Panel */}
      <div style={{ ...glassPanel, width: '260px', flexShrink: 0, padding: '22px', borderRadius: '22px', overflowY: 'auto' }}>
        <div style={{ marginBottom: '24px' }}>
          <div style={{ fontSize: '11px', fontWeight: 700, letterSpacing: '0.06em', color: t.text2, marginBottom: '12px' }}>
            SERVICE NAME
          </div>
          <div style={{ display: 'flex', flexDirection: 'column', gap: '2px' }}>
            {serviceBuckets.length === 0 ? (
              <div style={{ fontSize: '13px', color: t.text2 }}>No data</div>
            ) : null}
            {serviceBuckets.map(b => (
              <div
                key={b.key}
                onClick={() => addFilter("service_name", b.key.toString())}
                style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', padding: '5px 8px', borderRadius: '8px', cursor: 'pointer' }}
                onMouseEnter={(e) => e.currentTarget.style.background = t.dark ? 'rgba(255,255,255,0.06)' : 'rgba(0,0,0,0.04)'}
                onMouseLeave={(e) => e.currentTarget.style.background = 'transparent'}
              >
                <span style={{ color: t.accent, fontSize: '13px', whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis' }}>{b.key}</span>
                <span style={badgeStyle}>{b.doc_count}</span>
              </div>
            ))}
          </div>
        </div>

        <div>
          <div style={{ fontSize: '11px', fontWeight: 700, letterSpacing: '0.06em', color: t.text2, marginBottom: '12px' }}>
            LEVEL
          </div>
          <div style={{ display: 'flex', flexDirection: 'column', gap: '2px' }}>
            {levelBuckets.length === 0 ? (
              <div style={{ fontSize: '13px', color: t.text2 }}>No data</div>
            ) : null}
            {levelBuckets.map(b => (
              <div
                key={b.key}
                onClick={() => addFilter("level", b.key.toString())}
                style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', padding: '5px 8px', borderRadius: '8px', cursor: 'pointer' }}
                onMouseEnter={(e) => e.currentTarget.style.background = t.dark ? 'rgba(255,255,255,0.06)' : 'rgba(0,0,0,0.04)'}
                onMouseLeave={(e) => e.currentTarget.style.background = 'transparent'}
              >
                <span style={{ display: 'flex', alignItems: 'center', gap: '8px' }}>
                  <span style={{ width: '7px', height: '7px', borderRadius: '50%', background: getLevelColor(b.key.toString()), flexShrink: 0 }} />
                  <span style={{ color: getLevelColor(b.key.toString()), fontSize: '13px', fontWeight: 500 }}>{b.key}</span>
                </span>
                <span style={badgeStyle}>{b.doc_count}</span>
              </div>
            ))}
          </div>
        </div>
      </div>

      {/* Center Column */}
      <div style={{ flex: 1, display: 'flex', flexDirection: 'column', gap: '14px', minWidth: 0, overflow: 'hidden' }}>

        {/* Toolbar */}
        <div style={{ display: 'flex', gap: '14px' }}>
          <div style={{ flex: 1, display: 'flex', alignItems: 'center', gap: '8px', background: t.panelBg, border: `1px solid ${t.panelBorder}`, borderRadius: '14px', padding: '11px 16px', backdropFilter: 'blur(20px) saturate(180%)', WebkitBackdropFilter: 'blur(20px) saturate(180%)' }}>
            <span className="material-symbols-outlined" style={{ fontSize: '18px', color: t.text2 }}>search</span>
            <input
              type="text"
              value={query}
              onChange={(e) => setQuery(e.target.value)}
              placeholder={regexMode ? 'Regex on message... (e.g. timeout|refused)' : 'Search logs... (e.g. level:ERROR)'}
              style={{ flex: 1, background: 'transparent', border: 'none', color: t.text1, outline: 'none', fontSize: '13.5px', fontFamily: regexMode ? 'monospace' : 'inherit' }}
              onKeyDown={(e) => e.key === 'Enter' && fetchLogs()}
            />
            {/* Regex toggle — treats the box as a /pattern/ over the message. */}
            <button
              onClick={() => setRegexMode((v) => !v)}
              title="Toggle regex search on the message field"
              style={{
                fontFamily: 'monospace',
                fontSize: '13px',
                fontWeight: 700,
                padding: '2px 8px',
                borderRadius: '7px',
                cursor: 'pointer',
                border: `1px solid ${regexMode ? t.accent : t.panelBorder}`,
                background: regexMode ? t.accent : 'transparent',
                color: regexMode ? '#fff' : t.text2,
              }}
            >
              .*
            </button>
            {loading && <span style={{ fontSize: '12px', color: t.text2 }}>Searching...</span>}
          </div>

          {/* Time range selector */}
          <div style={{ display: 'flex', alignItems: 'center', gap: '2px', background: t.panelBg, border: `1px solid ${t.panelBorder}`, borderRadius: '14px', padding: '4px', backdropFilter: 'blur(20px) saturate(180%)', WebkitBackdropFilter: 'blur(20px) saturate(180%)' }}>
            {TIME_RANGES.map((r) => (
              <button
                key={r.key}
                onClick={() => selectTimeRange(r.key)}
                style={{
                  padding: '7px 11px',
                  borderRadius: '10px',
                  border: 'none',
                  cursor: 'pointer',
                  fontSize: '12.5px',
                  fontWeight: 600,
                  background: timeRange === r.key ? t.accent : 'transparent',
                  color: timeRange === r.key ? '#fff' : t.text2,
                }}
              >
                {r.label}
              </button>
            ))}
          </div>

          {/* Saved searches */}
          <div style={{ position: 'relative' }}>
            <button
              onClick={() => setSavedOpen((v) => !v)}
              title="Saved searches"
              style={{
                display: 'flex', alignItems: 'center', gap: '8px', height: '100%',
                padding: '11px 16px', borderRadius: '14px',
                border: `1px solid ${savedOpen ? t.accent : t.panelBorder}`,
                background: 'transparent', color: t.text2, fontSize: '13px', fontWeight: 600, cursor: 'pointer', whiteSpace: 'nowrap',
              }}
            >
              <span className="material-symbols-outlined" style={{ fontSize: '18px' }}>bookmark</span>
              Saved
            </button>
            {savedOpen && (
              <div style={{ ...glassPanel, position: 'absolute', top: 'calc(100% + 8px)', right: 0, width: '320px', zIndex: 20, borderRadius: '16px', padding: '14px', maxHeight: '420px', overflowY: 'auto' }}>
                {/* Save current */}
                <div style={{ fontSize: '11px', fontWeight: 700, letterSpacing: '0.05em', color: t.text2, marginBottom: '8px' }}>SAVE CURRENT SEARCH</div>
                <div style={{ display: 'flex', gap: '6px', marginBottom: '6px' }}>
                  <input
                    type="text"
                    value={saveName}
                    onChange={(e) => setSaveName(e.target.value)}
                    placeholder="Name this search"
                    style={{ flex: 1, background: t.dark ? 'rgba(0,0,0,0.25)' : 'rgba(0,0,0,0.05)', border: `1px solid ${t.panelBorder}`, borderRadius: '9px', padding: '8px 10px', color: t.text1, outline: 'none', fontSize: '13px' }}
                    onKeyDown={(e) => e.key === 'Enter' && saveCurrentSearch()}
                  />
                  <button
                    onClick={saveCurrentSearch}
                    style={{ padding: '8px 14px', borderRadius: '9px', border: 'none', background: t.accent, color: '#fff', fontSize: '13px', fontWeight: 600, cursor: 'pointer' }}
                  >
                    Save
                  </button>
                </div>
                <label style={{ display: 'flex', alignItems: 'center', gap: '7px', fontSize: '12.5px', color: t.text2, marginBottom: savedError ? '6px' : '14px', cursor: 'pointer' }}>
                  <input type="checkbox" checked={saveShared} onChange={(e) => setSaveShared(e.target.checked)} />
                  Share with my team
                </label>
                {savedError && <div style={{ fontSize: '12px', color: t.red, marginBottom: '12px' }}>{savedError}</div>}

                <div style={{ fontSize: '11px', fontWeight: 700, letterSpacing: '0.05em', color: t.text2, margin: '4px 0 8px' }}>MY SEARCHES</div>
                {savedSearches.length === 0 ? (
                  <div style={{ fontSize: '13px', color: t.text2, padding: '6px 0' }}>No saved searches yet.</div>
                ) : (
                  <div style={{ display: 'flex', flexDirection: 'column', gap: '2px' }}>
                    {savedSearches.map((s) => (
                      <div
                        key={s.id}
                        style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', padding: '7px 9px', borderRadius: '9px', cursor: 'pointer' }}
                        onMouseEnter={(e) => (e.currentTarget.style.background = t.dark ? 'rgba(255,255,255,0.06)' : 'rgba(0,0,0,0.04)')}
                        onMouseLeave={(e) => (e.currentTarget.style.background = 'transparent')}
                      >
                        <span onClick={() => applySavedSearch(s)} style={{ flex: 1, display: 'flex', alignItems: 'center', gap: '8px', color: t.text1, fontSize: '13px', overflow: 'hidden' }}>
                          <span style={{ whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis' }}>{s.name}</span>
                          {s.shared && <span style={badgeStyle}>{s.mine ? 'shared' : 'team'}</span>}
                        </span>
                        {s.mine && (
                          <button
                            onClick={() => deleteSavedSearch(s.id)}
                            title="Delete"
                            style={{ background: 'transparent', border: 'none', color: t.text2, cursor: 'pointer', fontSize: '15px', lineHeight: 1, padding: '0 2px' }}
                          >
                            ✕
                          </button>
                        )}
                      </div>
                    ))}
                  </div>
                )}
              </div>
            )}
          </div>

          <button
            onClick={copySearchLink}
            title="Copy a shareable link that reproduces this exact search"
            style={{
              display: 'flex', alignItems: 'center', gap: '8px',
              padding: '11px 16px', borderRadius: '14px',
              border: `1px solid ${searchLinkCopied ? t.green : t.panelBorder}`,
              background: 'transparent', color: searchLinkCopied ? t.green : t.text2,
              fontSize: '13px', fontWeight: 600, cursor: 'pointer', whiteSpace: 'nowrap',
            }}
          >
            <span className="material-symbols-outlined" style={{ fontSize: '18px' }}>{searchLinkCopied ? 'check' : 'link'}</span>
            {searchLinkCopied ? 'Copied' : 'Share'}
          </button>

          <button
            onClick={toggleLiveTail}
            style={{
              display: 'flex',
              alignItems: 'center',
              gap: '9px',
              padding: '11px 20px',
              borderRadius: '14px',
              border: `1px solid ${t.panelBorder}`,
              background: isLiveTail ? t.accent : 'transparent',
              color: isLiveTail ? '#fff' : t.text2,
              fontSize: '13px',
              fontWeight: 600,
              cursor: 'pointer',
              whiteSpace: 'nowrap',
            }}
          >
            <span style={{ width: '8px', height: '8px', borderRadius: '50%', background: isLiveTail ? '#fff' : t.text2 }} />
            Live Tail {isLiveTail ? 'On' : 'Off'}
          </button>
        </div>

        {/* Volume Chart with brush-to-zoom (E5) */}
        {volumeData.length > 0 && (
          <div style={{ padding: '12px 16px', borderRadius: '18px', background: t.panelBg, border: `1px solid ${t.panelBorder}`, backdropFilter: 'blur(20px) saturate(180%)', WebkitBackdropFilter: 'blur(20px) saturate(180%)' }}>
            <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: '6px', minHeight: '22px' }}>
              <span style={{ fontSize: '11px', color: t.text2 }}>
                {brushRange
                  ? `Zoomed: ${new Date(brushRange.start).toLocaleTimeString()} → ${new Date(brushRange.end).toLocaleTimeString()}`
                  : 'Drag across the histogram to zoom into a time window'}
              </span>
              <div style={{ display: 'flex', gap: '8px' }}>
                {pendingBrush && (
                  <button onClick={applyBrushZoom} style={{ fontSize: '11px', fontWeight: 600, padding: '4px 10px', borderRadius: '7px', border: 'none', background: t.accent, color: '#fff', cursor: 'pointer' }}>🔍 Zoom to selection</button>
                )}
                {brushRange && (
                  <button onClick={() => { setBrushRange(null); setPendingBrush(null); }} style={{ fontSize: '11px', fontWeight: 600, padding: '4px 10px', borderRadius: '7px', border: '1px solid ' + t.panelBorder, background: 'transparent', color: t.text2, cursor: 'pointer' }}>Clear zoom</button>
                )}
              </div>
            </div>
            <div style={{ height: '96px' }}>
              <ResponsiveContainer width="100%" height="100%">
                <BarChart data={volumeData}>
                  <defs>
                    <linearGradient id="explorerVolumeGradient" x1="0" y1="0" x2="0" y2="1">
                      <stop offset="0%" stopColor={t.accent} />
                      <stop offset="100%" stopColor={t.accent2} />
                    </linearGradient>
                  </defs>
                  <Tooltip
                    contentStyle={{ backgroundColor: t.dark ? 'rgba(20,20,26,0.9)' : 'rgba(255,255,255,0.9)', border: `1px solid ${t.panelBorder}`, borderRadius: '8px', fontSize: '12px' }}
                    itemStyle={{ color: t.accent }}
                    labelStyle={{ color: t.text2 }}
                  />
                  <Bar dataKey="count" fill="url(#explorerVolumeGradient)" radius={[4, 4, 0, 0]} />
                  <Brush
                    dataKey="time"
                    height={16}
                    travellerWidth={8}
                    stroke={t.accent}
                    fill={t.dark ? 'rgba(255,255,255,0.04)' : 'rgba(0,0,0,0.03)'}
                    onChange={(range) => {
                      const r = range as { startIndex?: number; endIndex?: number };
                      if (typeof r.startIndex === 'number' && typeof r.endIndex === 'number' && r.endIndex > r.startIndex) {
                        setPendingBrush({ startIndex: r.startIndex, endIndex: r.endIndex });
                      }
                    }}
                  />
                </BarChart>
              </ResponsiveContainer>
            </div>
          </div>
        )}

        {/* Log List */}
        <div style={{ flex: 1, overflowY: 'auto', padding: '10px', borderRadius: '18px', background: t.panelBg, border: `1px solid ${t.panelBorder}`, backdropFilter: 'blur(20px) saturate(180%)', WebkitBackdropFilter: 'blur(20px) saturate(180%)', display: 'flex', flexDirection: 'column', gap: '2px' }}>
          {logs.length === 0 && !loading ? (
            <div style={{ color: t.text2, textAlign: 'center', padding: '40px', fontSize: '13px' }}>No logs match the query.</div>
          ) : (
            logs.map((log) => {
              const isSelected = selectedLog?.id === log.id;
              return (
                <div
                  key={log.id}
                  onClick={() => setSelectedLog(log)}
                  style={{
                    display: 'flex',
                    gap: '16px',
                    padding: '9px 14px',
                    borderRadius: '8px',
                    cursor: 'pointer',
                    fontFamily: 'monospace',
                    fontSize: '12.5px',
                    borderLeft: `3px solid ${getLevelColor(log.level)}`,
                    background: isSelected ? (t.dark ? 'rgba(255,255,255,0.08)' : 'rgba(0,0,0,0.05)') : 'transparent',
                  }}
                  onMouseEnter={(e) => { if (!isSelected) e.currentTarget.style.background = t.dark ? 'rgba(255,255,255,0.05)' : 'rgba(0,0,0,0.03)'; }}
                  onMouseLeave={(e) => { e.currentTarget.style.background = isSelected ? (t.dark ? 'rgba(255,255,255,0.08)' : 'rgba(0,0,0,0.05)') : 'transparent'; }}
                >
                  <span style={{ color: t.text2, width: '80px', flexShrink: 0 }}>
                    {new Date(log.timestamp).toLocaleTimeString([], { hour12: false })}
                  </span>
                  <span style={{ color: getLevelColor(log.level), width: '48px', fontWeight: 700, flexShrink: 0 }}>
                    {log.level}
                  </span>
                  <span style={{ color: t.accent, width: '140px', flexShrink: 0, whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis' }}>
                    {log.service_name}
                  </span>
                  <span style={{ color: t.text1, flex: 1, whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis' }}>
                    {log.message}
                  </span>
                </div>
              );
            })
          )}
        </div>
      </div>

      {/* Right Drawer - Log Details */}
      {selectedLog && (
        <div style={{ ...glassPanel, width: 'clamp(260px, 26vw, 380px)', flexShrink: 0, display: 'flex', flexDirection: 'column', borderRadius: '22px', overflow: 'hidden' }}>
          <div style={{ padding: '18px 20px', borderBottom: `1px solid ${t.panelBorder}`, display: 'flex', justifyContent: 'space-between', alignItems: 'center' }}>
            <h3 style={{ fontSize: '15px', fontWeight: 600, color: t.text1, margin: 0 }}>Log Details</h3>
            <div style={{ display: 'flex', alignItems: 'center', gap: '10px' }}>
              <button
                onClick={() => copyPermalink(selectedLog.id)}
                title="Copy a shareable link to this log"
                style={{ background: 'transparent', border: `1px solid ${t.panelBorder}`, color: permalinkCopied ? t.green : t.text2, cursor: 'pointer', fontSize: '11.5px', padding: '4px 10px', borderRadius: '8px' }}
              >
                {permalinkCopied ? 'Copied ✓' : 'Permalink'}
              </button>
              <button
                onClick={() => setSelectedLog(null)}
                aria-label="Close log details"
                style={{ background: 'transparent', border: 'none', color: t.text2, cursor: 'pointer', fontSize: '18px', lineHeight: 1, padding: 0 }}
              >
                ✕
              </button>
            </div>
          </div>
          <div style={{ padding: '20px', overflowY: 'auto', flex: 1 }}>

            <div style={{ marginBottom: '20px' }}>
              <div style={{ fontSize: '11px', color: t.text2, textTransform: 'uppercase', letterSpacing: '0.04em', marginBottom: '6px' }}>Timestamp</div>
              <div style={{ fontSize: '14px', color: t.text1 }}>{new Date(selectedLog.timestamp).toLocaleString()}</div>
            </div>

            <div style={{ marginBottom: '20px' }}>
              <div style={{ fontSize: '11px', color: t.text2, textTransform: 'uppercase', letterSpacing: '0.04em', marginBottom: '6px' }}>Service</div>
              <div style={{ fontSize: '14px', color: t.accent }}>{selectedLog.service_name}</div>
            </div>

            <div style={{ marginBottom: '20px' }}>
              <div style={{ fontSize: '11px', color: t.text2, textTransform: 'uppercase', letterSpacing: '0.04em', marginBottom: '6px' }}>Message</div>
              <div style={{ fontSize: '12.5px', fontFamily: 'monospace', color: t.text1, background: t.dark ? 'rgba(0,0,0,0.25)' : 'rgba(0,0,0,0.05)', padding: '12px', borderRadius: '10px', wordBreak: 'break-word' }}>
                {selectedLog.message}
              </div>
            </div>

            <div style={{ marginBottom: '20px' }}>
              <button
                onClick={() => openContext(selectedLog)}
                title="Show the logs immediately before and after this one on the same service"
                style={{
                  width: '100%', padding: '11px', borderRadius: '10px',
                  border: `1px solid ${t.panelBorder}`, background: 'transparent',
                  color: t.text1, fontSize: '13px', fontWeight: 600,
                  display: 'flex', justifyContent: 'center', alignItems: 'center', gap: '8px', cursor: 'pointer',
                }}
              >
                <span className="material-symbols-outlined" style={{ fontSize: '16px' }}>unfold_more</span>
                View in context
              </button>
            </div>

            {selectedLog.trace_id && (
              <div style={{ marginBottom: '20px' }}>
                <div style={{ fontSize: '11px', color: t.text2, textTransform: 'uppercase', letterSpacing: '0.04em', marginBottom: '6px' }}>Distributed Trace</div>
                <button
                  onClick={() => router.push(`/traces?trace=${encodeURIComponent(selectedLog.trace_id!)}`)}
                  title="Open the distributed trace this log belongs to"
                  style={{
                    width: '100%',
                    padding: '11px',
                    borderRadius: '10px',
                    border: `1px solid ${t.accent}44`,
                    background: 'transparent',
                    color: t.accent,
                    fontSize: '13px',
                    fontWeight: 600,
                    display: 'flex',
                    justifyContent: 'center',
                    alignItems: 'center',
                    gap: '8px',
                    cursor: 'pointer',
                  }}
                >
                  <span className="material-symbols-outlined" style={{ fontSize: '16px' }}>hub</span>
                  View Trace {selectedLog.trace_id.substring(0, 8)}...
                </button>
              </div>
            )}

            <div>
              <div style={{ fontSize: '11px', color: t.text2, textTransform: 'uppercase', letterSpacing: '0.04em', marginBottom: '6px' }}>Raw JSON</div>
              <pre style={{ margin: 0, fontSize: '11px', color: t.text2, background: t.dark ? 'rgba(0,0,0,0.25)' : 'rgba(0,0,0,0.05)', padding: '12px', borderRadius: '10px', whiteSpace: 'pre-wrap', wordBreak: 'break-all' }}>
                {JSON.stringify(selectedLog._raw, null, 2)}
              </pre>
            </div>

          </div>
        </div>
      )}

      {/* Surrounding-context overlay (F6) */}
      {(context || contextLoading || contextError) && (
        <div
          onClick={() => { setContext(null); setContextError(null); }}
          style={{ position: 'fixed', inset: 0, zIndex: 50, background: 'rgba(0,0,0,0.45)', display: 'flex', alignItems: 'center', justifyContent: 'center', padding: '32px' }}
        >
          <div
            onClick={(e) => e.stopPropagation()}
            role="dialog"
            aria-label="Log context"
            style={{ ...glassPanel, width: 'min(880px, 92vw)', maxHeight: '82vh', borderRadius: '20px', display: 'flex', flexDirection: 'column', overflow: 'hidden' }}
          >
            <div style={{ padding: '16px 20px', borderBottom: `1px solid ${t.panelBorder}`, display: 'flex', justifyContent: 'space-between', alignItems: 'center' }}>
              <h3 style={{ fontSize: '15px', fontWeight: 600, color: t.text1, margin: 0 }}>
                Log context{context ? ` · ${context.anchor.service_name}` : ''}
              </h3>
              <button onClick={() => { setContext(null); setContextError(null); }} aria-label="Close context" style={{ background: 'transparent', border: 'none', color: t.text2, cursor: 'pointer', fontSize: '18px', lineHeight: 1 }}>✕</button>
            </div>
            <div style={{ overflowY: 'auto', padding: '10px', display: 'flex', flexDirection: 'column', gap: '2px' }}>
              {contextLoading && <div style={{ color: t.text2, textAlign: 'center', padding: '40px', fontSize: '13px' }}>Loading context…</div>}
              {contextError && <div style={{ color: t.red, textAlign: 'center', padding: '40px', fontSize: '13px' }}>{contextError}</div>}
              {context && [...context.before, context.anchor, ...context.after].map((log, i) => {
                const isAnchor = log.id === context.anchor.id && i === context.before.length;
                return (
                  <div
                    key={`${log.id}-${i}`}
                    style={{
                      display: 'flex', gap: '16px', padding: '8px 14px', borderRadius: '8px',
                      fontFamily: 'monospace', fontSize: '12.5px',
                      borderLeft: `3px solid ${getLevelColor(log.level)}`,
                      background: isAnchor ? (t.dark ? 'rgba(255,255,255,0.12)' : 'rgba(0,0,0,0.08)') : 'transparent',
                      outline: isAnchor ? `1px solid ${t.accent}` : 'none',
                    }}
                  >
                    <span style={{ color: t.text2, width: '90px', flexShrink: 0 }}>{new Date(log.timestamp).toLocaleTimeString([], { hour12: false })}</span>
                    <span style={{ color: getLevelColor(log.level), width: '48px', fontWeight: 700, flexShrink: 0 }}>{log.level}</span>
                    <span style={{ color: t.text1, flex: 1, whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis' }}>{log.message}</span>
                  </div>
                );
              })}
              {context && context.before.length === 0 && context.after.length === 0 && (
                <div style={{ color: t.text2, textAlign: 'center', padding: '20px', fontSize: '12.5px' }}>No surrounding logs on this service.</div>
              )}
            </div>
          </div>
        </div>
      )}

    </div>
  );
}
