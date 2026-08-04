"use client";

import React, { useState, useEffect, useCallback, useRef } from 'react';
import { fetchWithAuth } from '@/lib/api';
import { LineChart, Line, XAxis, YAxis, CartesianGrid, Tooltip, Legend, ResponsiveContainer, AreaChart, Area } from 'recharts';
import { useTheme } from '@/context/ThemeContext';

const INTERVAL_OPTIONS: { value: string; label: string }[] = [
  { value: '1h', label: 'Last 1 Hour' },
  { value: '24h', label: 'Last 24 Hours' },
  { value: '7d', label: 'Last 7 Days' },
];

interface TraceSavedSearch {
  id: string;
  name: string;
  shared: boolean;
  mine: boolean;
  query_params: {
    interval?: string;
    services?: string;
    routes?: string;
    route_regex?: string;
    operation_regex?: string;
  };
}

interface TraceChartRow { time: string; p50: number; p90: number; p99: number; count: number; }
interface RawTraceRow { time_bucket: string; p50_ms: number; p90_ms: number; p99_ms: number; total_traces: number | string; }

export function TraceAnalyticsView() {
  const { tokens: t } = useTheme();
  const [data, setData] = useState<TraceChartRow[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  const [interval, setInterval_] = useState('1h');
  const [serviceFacets, setServiceFacets] = useState<string[]>([]);
  const [routeFacets, setRouteFacets] = useState<string[]>([]);
  const [selectedServices, setSelectedServices] = useState<Set<string>>(new Set());
  const [selectedRoutes, setSelectedRoutes] = useState<Set<string>>(new Set());

  // Regex depth: server-side RE2 match() on route / operation name. Kept in refs
  // as well as state so a fetch reads the current value without the regex inputs
  // having to be reactive dependencies (which would fire a request — and a 400 on
  // a half-typed pattern — on every keystroke). Regex applies on Enter/Query only.
  const [routeRegex, setRouteRegex] = useState('');
  const [operationRegex, setOperationRegex] = useState('');
  const routeRegexRef = useRef('');
  const operationRegexRef = useRef('');

  // Saved searches (kind=traces).
  const [savedSearches, setSavedSearches] = useState<TraceSavedSearch[]>([]);
  const [savedOpen, setSavedOpen] = useState(false);
  const [saveName, setSaveName] = useState('');
  const [saveShared, setSaveShared] = useState(false);
  const [savedError, setSavedError] = useState<string | null>(null);

  useEffect(() => {
    fetchWithAuth('/api/v1/analytics/traces/facets')
      .then(async res => {
        if (!res.ok) throw new Error(await res.text());
        return res.json();
      })
      .then(json => {
        setServiceFacets(json.services || []);
        setRouteFacets(json.routes || []);
      })
      .catch(err => console.error('Failed to fetch trace facets:', err));
  }, []);

  const fetchAnalytics = useCallback(() => {
    setLoading(true);
    setError(null);
    const params = new URLSearchParams({ interval });
    selectedServices.forEach(s => params.append('service', s));
    selectedRoutes.forEach(r => params.append('route', r));
    if (routeRegexRef.current.trim()) params.set('route_regex', routeRegexRef.current.trim());
    if (operationRegexRef.current.trim()) params.set('operation_regex', operationRegexRef.current.trim());

    fetchWithAuth(`/api/v1/analytics/traces?${params.toString()}`)
      .then(async res => {
        if (!res.ok) throw new Error(await res.text());
        return res.json();
      })
      .then(jsonData => {
        // ClickHouse JSON format returns rows in .data
        if (jsonData && jsonData.data) {
          const formatted = jsonData.data.map((row: RawTraceRow) => ({
            time: new Date(row.time_bucket).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' }),
            p50: Math.round(row.p50_ms),
            p90: Math.round(row.p90_ms),
            p99: Math.round(row.p99_ms),
            count: parseInt(String(row.total_traces), 10)
          }));
          setData(formatted);
        } else {
          setData([]);
        }
        setLoading(false);
      })
      .catch(err => {
        setError(err.message || err.toString());
        setLoading(false);
      });
  }, [interval, selectedServices, selectedRoutes]);

  useEffect(() => {
    // eslint-disable-next-line react-hooks/set-state-in-effect -- intentional one-shot fetch/hydration on mount; effect is the right place to sync from the API/localStorage
    fetchAnalytics();
  }, [fetchAnalytics]);

  const toggleFacet = (set: Set<string>, setSet: (s: Set<string>) => void, value: string) => {
    const next = new Set(set);
    if (next.has(value)) next.delete(value);
    else next.add(value);
    setSet(next);
  };

  const setRouteRegexBoth = (v: string) => { setRouteRegex(v); routeRegexRef.current = v; };
  const setOperationRegexBoth = (v: string) => { setOperationRegex(v); operationRegexRef.current = v; };

  // ── Saved searches (kind=traces) ──────────────────────────────────────────
  const loadSavedSearches = () => {
    fetchWithAuth('/api/v1/saved-searches?kind=traces')
      .then((res) => (res.ok ? res.json() : Promise.reject(res.status)))
      .then((json) => setSavedSearches(json?.data || []))
      .catch((err) => console.error('Failed to load saved searches:', err));
  };

  useEffect(() => {
    loadSavedSearches();
  }, []);

  const applySavedSearch = (s: TraceSavedSearch) => {
    const p = s.query_params || {};
    setInterval_(p.interval || '1h');
    setSelectedServices(new Set(p.services ? p.services.split(',').filter(Boolean) : []));
    setSelectedRoutes(new Set(p.routes ? p.routes.split(',').filter(Boolean) : []));
    setRouteRegexBoth(p.route_regex || '');
    setOperationRegexBoth(p.operation_regex || '');
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
        kind: 'traces',
        shared: saveShared,
        query_params: {
          interval,
          services: Array.from(selectedServices).join(','),
          routes: Array.from(selectedRoutes).join(','),
          route_regex: routeRegex,
          operation_regex: operationRegex,
        },
      }),
    })
      .then((res) => {
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
      .then((res) => { if (res.ok) loadSavedSearches(); })
      .catch((err) => console.error('Failed to delete saved search:', err));
  };

  const cardStyle: React.CSSProperties = {
    borderRadius: '20px',
    background: t.panelBg,
    border: '1px solid ' + t.panelBorder,
    backdropFilter: 'blur(30px) saturate(180%)',
    boxShadow: t.shadow,
  };

  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: '24px', height: '100%', overflow: 'auto' }}>

      {/* Header */}
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center' }}>
        <div>
          <h2 style={{ fontSize: '24px', fontWeight: 600, marginBottom: '8px', color: t.text1 }}>Trace Analytics</h2>
          <p style={{ color: t.text2 }}>Analyze application performance over time using high-cardinality slicing.</p>
        </div>
        <div style={{ display: 'flex', gap: '12px', alignItems: 'center' }}>
          {/* Regex filters (RE2, server-side match()). Apply on Enter or Query. */}
          <input
            type="text"
            value={routeRegex}
            onChange={(e) => setRouteRegexBoth(e.target.value)}
            onKeyDown={(e) => e.key === 'Enter' && fetchAnalytics()}
            placeholder="route regex…"
            title="Regex match on the HTTP route (e.g. /api/v[0-9]+/.*)"
            style={{
              background: t.dark ? 'rgba(255,255,255,0.06)' : 'rgba(255,255,255,0.7)',
              border: '1px solid ' + (routeRegex ? t.accent : t.panelBorder),
              color: t.text1, padding: '10px 12px', borderRadius: '10px', outline: 'none',
              fontFamily: 'monospace', fontSize: '13px', width: '150px',
            }}
          />
          <input
            type="text"
            value={operationRegex}
            onChange={(e) => setOperationRegexBoth(e.target.value)}
            onKeyDown={(e) => e.key === 'Enter' && fetchAnalytics()}
            placeholder="operation regex…"
            title="Regex match on the span/operation name (e.g. GET .*)"
            style={{
              background: t.dark ? 'rgba(255,255,255,0.06)' : 'rgba(255,255,255,0.7)',
              border: '1px solid ' + (operationRegex ? t.accent : t.panelBorder),
              color: t.text1, padding: '10px 12px', borderRadius: '10px', outline: 'none',
              fontFamily: 'monospace', fontSize: '13px', width: '150px',
            }}
          />
          <select
            style={{
              background: t.dark ? 'rgba(255,255,255,0.06)' : 'rgba(255,255,255,0.7)',
              border: '1px solid ' + t.panelBorder,
              color: t.text1,
              padding: '10px 14px',
              borderRadius: '10px',
              outline: 'none',
            }}
            value={interval}
            onChange={(e) => setInterval_(e.target.value)}
          >
            {INTERVAL_OPTIONS.map(opt => (
              <option key={opt.value} value={opt.value}>{opt.label}</option>
            ))}
          </select>

          {/* Saved searches (kind=traces) */}
          <div style={{ position: 'relative' }}>
            <button
              onClick={() => setSavedOpen((v) => !v)}
              title="Saved searches"
              style={{
                display: 'flex', alignItems: 'center', gap: '7px',
                padding: '10px 14px', borderRadius: '10px',
                border: '1px solid ' + (savedOpen ? t.accent : t.panelBorder),
                background: 'transparent', color: t.text2, fontWeight: 600, cursor: 'pointer',
              }}
            >
              <span className="material-symbols-outlined" style={{ fontSize: '18px' }}>bookmark</span>
              Saved
            </button>
            {savedOpen && (
              <div style={{ ...cardStyle, position: 'absolute', top: 'calc(100% + 8px)', right: 0, width: '320px', zIndex: 20, padding: '14px', maxHeight: '420px', overflowY: 'auto' }}>
                <div style={{ fontSize: '11px', fontWeight: 700, letterSpacing: '0.05em', color: t.text2, marginBottom: '8px' }}>SAVE CURRENT VIEW</div>
                <div style={{ display: 'flex', gap: '6px', marginBottom: '6px' }}>
                  <input
                    type="text"
                    value={saveName}
                    onChange={(e) => setSaveName(e.target.value)}
                    placeholder="Name this view"
                    style={{ flex: 1, background: t.dark ? 'rgba(0,0,0,0.25)' : 'rgba(0,0,0,0.05)', border: `1px solid ${t.panelBorder}`, borderRadius: '9px', padding: '8px 10px', color: t.text1, outline: 'none', fontSize: '13px' }}
                    onKeyDown={(e) => e.key === 'Enter' && saveCurrentSearch()}
                  />
                  <button onClick={saveCurrentSearch} style={{ padding: '8px 14px', borderRadius: '9px', border: 'none', background: t.accent, color: '#fff', fontSize: '13px', fontWeight: 600, cursor: 'pointer' }}>Save</button>
                </div>
                <label style={{ display: 'flex', alignItems: 'center', gap: '7px', fontSize: '12.5px', color: t.text2, marginBottom: savedError ? '6px' : '14px', cursor: 'pointer' }}>
                  <input type="checkbox" checked={saveShared} onChange={(e) => setSaveShared(e.target.checked)} />
                  Share with my team
                </label>
                {savedError && <div style={{ fontSize: '12px', color: t.red, marginBottom: '12px' }}>{savedError}</div>}

                <div style={{ fontSize: '11px', fontWeight: 700, letterSpacing: '0.05em', color: t.text2, margin: '4px 0 8px' }}>MY VIEWS</div>
                {savedSearches.length === 0 ? (
                  <div style={{ fontSize: '13px', color: t.text2, padding: '6px 0' }}>No saved views yet.</div>
                ) : (
                  <div style={{ display: 'flex', flexDirection: 'column', gap: '2px' }}>
                    {savedSearches.map((s) => (
                      <div key={s.id}
                        style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', padding: '7px 9px', borderRadius: '9px' }}
                        onMouseEnter={(e) => (e.currentTarget.style.background = t.dark ? 'rgba(255,255,255,0.06)' : 'rgba(0,0,0,0.04)')}
                        onMouseLeave={(e) => (e.currentTarget.style.background = 'transparent')}
                      >
                        <span onClick={() => applySavedSearch(s)} style={{ flex: 1, display: 'flex', alignItems: 'center', gap: '8px', color: t.text1, fontSize: '13px', cursor: 'pointer', overflow: 'hidden' }}>
                          <span style={{ whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis' }}>{s.name}</span>
                          {s.shared && <span style={{ fontSize: '10px', color: t.text2, background: t.dark ? 'rgba(255,255,255,0.08)' : 'rgba(0,0,0,0.06)', padding: '2px 7px', borderRadius: '100px' }}>{s.mine ? 'shared' : 'team'}</span>}
                        </span>
                        {s.mine && (
                          <button onClick={() => deleteSavedSearch(s.id)} title="Delete" style={{ background: 'transparent', border: 'none', color: t.text2, cursor: 'pointer', fontSize: '15px', lineHeight: 1, padding: '0 2px' }}>✕</button>
                        )}
                      </div>
                    ))}
                  </div>
                )}
              </div>
            )}
          </div>

          <button
            onClick={fetchAnalytics}
            style={{
              padding: '10px 24px',
              borderRadius: '10px',
              border: 'none',
              background: `linear-gradient(135deg, ${t.accent}, ${t.accent2})`,
              color: '#fff',
              fontWeight: 600,
              cursor: 'pointer',
            }}
          >
            Query Analytics
          </button>
        </div>
      </div>

      {error && (
        <div style={{ padding: '16px', background: t.redSoft, color: t.red, borderRadius: '12px', border: '1px solid ' + t.panelBorder }}>
          <strong>Analytics Error:</strong> {error}
        </div>
      )}

      {/* Main Charts Area */}
      <div style={{ display: 'flex', gap: '18px', flex: 1, minHeight: '400px' }}>

        {/* Facet Sidebar */}
        <div style={{ ...cardStyle, width: '280px', padding: '24px', display: 'flex', flexDirection: 'column', gap: '24px' }}>
          <h3 style={{ fontSize: '14px', textTransform: 'uppercase', color: t.text2, letterSpacing: '0.05em', fontWeight: 700 }}>Group By (Facets)</h3>

          <div>
            <div style={{ fontSize: '13px', fontWeight: 600, marginBottom: '12px', color: t.text1 }}>Service Name</div>
            {serviceFacets.length === 0 ? (
              <div style={{ fontSize: '12px', color: t.text2 }}>No services indexed yet</div>
            ) : serviceFacets.map(svc => (
              <label key={svc} style={{ display: 'flex', alignItems: 'center', gap: '8px', marginBottom: '8px', fontSize: '13px', cursor: 'pointer', color: t.text1 }}>
                <input
                  type="checkbox"
                  checked={selectedServices.has(svc)}
                  onChange={() => toggleFacet(selectedServices, setSelectedServices, svc)}
                /> {svc}
              </label>
            ))}
          </div>

          <div>
            <div style={{ fontSize: '13px', fontWeight: 600, marginBottom: '12px', color: t.text1 }}>HTTP Route</div>
            {routeFacets.length === 0 ? (
              <div style={{ fontSize: '12px', color: t.text2 }}>No routes indexed yet</div>
            ) : routeFacets.map(route => (
              <label key={route} style={{ display: 'flex', alignItems: 'center', gap: '8px', marginBottom: '8px', fontSize: '13px', cursor: 'pointer', color: t.text1 }}>
                <input
                  type="checkbox"
                  checked={selectedRoutes.has(route)}
                  onChange={() => toggleFacet(selectedRoutes, setSelectedRoutes, route)}
                /> {route}
              </label>
            ))}
          </div>
        </div>

        {/* Charts */}
        <div style={{ flex: 1, display: 'flex', flexDirection: 'column', gap: '18px', minWidth: 0 }}>

          {/* Latency Percentiles */}
          <div style={{ ...cardStyle, flex: 1, padding: '24px', display: 'flex', flexDirection: 'column' }}>
            <h3 style={{ fontSize: '16px', fontWeight: 600, marginBottom: '16px', color: t.text1 }}>Latency Percentiles (p50, p90, p99)</h3>
            <div style={{ flex: 1, minHeight: 0 }}>
              {loading ? (
                <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'center', height: '100%', color: t.text2 }}>Loading analytical data from ClickHouse...</div>
              ) : (
                <ResponsiveContainer width="100%" height="100%">
                  <LineChart data={data} margin={{ top: 10, right: 30, left: 0, bottom: 0 }}>
                    <CartesianGrid strokeDasharray="3 3" stroke={t.panelBorder} />
                    <XAxis dataKey="time" stroke={t.text2} tick={{ fill: t.text2, fontSize: 12 }} />
                    <YAxis stroke={t.text2} tick={{ fill: t.text2, fontSize: 12 }} unit="ms" />
                    <Tooltip
                      contentStyle={{ backgroundColor: t.dark ? 'rgba(20,20,26,0.9)' : 'rgba(255,255,255,0.95)', border: '1px solid ' + t.panelBorder, borderRadius: '8px' }}
                      itemStyle={{ fontSize: '13px' }}
                    />
                    <Legend />
                    <Line type="monotone" dataKey="p99" name="p99 Latency" stroke={t.red} strokeWidth={2} dot={false} />
                    <Line type="monotone" dataKey="p90" name="p90 Latency" stroke={t.accent2} strokeWidth={2} dot={false} />
                    <Line type="monotone" dataKey="p50" name="p50 Latency" stroke={t.accent} strokeWidth={2} dot={false} />
                  </LineChart>
                </ResponsiveContainer>
              )}
            </div>
          </div>

          {/* Trace Volume */}
          <div style={{ ...cardStyle, flex: 1, padding: '24px', display: 'flex', flexDirection: 'column' }}>
            <h3 style={{ fontSize: '16px', fontWeight: 600, marginBottom: '16px', color: t.text1 }}>Total Trace Volume</h3>
            <div style={{ flex: 1, minHeight: 0 }}>
              {loading ? (
                <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'center', height: '100%', color: t.text2 }}>Loading volume data...</div>
              ) : (
                <ResponsiveContainer width="100%" height="100%">
                  <AreaChart data={data} margin={{ top: 10, right: 30, left: 0, bottom: 0 }}>
                    <CartesianGrid strokeDasharray="3 3" stroke={t.panelBorder} />
                    <XAxis dataKey="time" stroke={t.text2} tick={{ fill: t.text2, fontSize: 12 }} />
                    <YAxis stroke={t.text2} tick={{ fill: t.text2, fontSize: 12 }} />
                    <Tooltip
                      contentStyle={{ backgroundColor: t.dark ? 'rgba(20,20,26,0.9)' : 'rgba(255,255,255,0.95)', border: '1px solid ' + t.panelBorder, borderRadius: '8px' }}
                      itemStyle={{ fontSize: '13px' }}
                    />
                    <Area type="step" dataKey="count" name="Trace Count" stroke={t.accent} fill={t.accent} fillOpacity={0.3} />
                  </AreaChart>
                </ResponsiveContainer>
              )}
            </div>
          </div>

        </div>
      </div>
    </div>
  );
}
