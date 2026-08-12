"use client";

import React, { useState, useEffect, useCallback } from 'react';
import { useRouter } from 'next/navigation';
import { fetchWithAuth } from '@/lib/api';
import { LineChart, Line, XAxis, YAxis, CartesianGrid, Tooltip, Legend, ResponsiveContainer } from 'recharts';
import { useTheme } from '@/context/ThemeContext';

interface RUMMetric { MetricName?: string; Type?: string; p75_value?: number; avg_value?: number; count?: number | string; }
interface RUMError { timestamp?: string | number; path?: string; error_msg?: string; user_agent?: string; trace_id?: string; }
interface RUMTrendRow { time_bucket?: string; metric?: string; p75?: number; }
interface RUMSession { session_id?: string; entry_path?: string; page_views?: number | string; errors?: number | string; duration_seconds?: number | string; last_seen?: string; browser?: string; os?: string; device?: string; }
interface Breakdown { name: string; count: number }
interface RUMDevices { browsers: Breakdown[]; os: Breakdown[]; devices: Breakdown[] }
// A Core Web Vitals breakdown row (RUM · E4): p75 of one metric for one
// page/device group, rated good/needs-improvement/poor server-side.
interface WVRow { group_value?: string; metric?: string; p75?: number | string; samples?: number | string; rating?: string }

const RANGE_OPTIONS = [
  { value: '24h', label: 'Last 24 Hours' },
  { value: '7d', label: 'Last 7 Days' },
];
const TREND_COLORS = ['#6366f1', '#22c55e', '#f59e0b', '#ef4444', '#06b6d4', '#a855f7'];

export function RUMView() {
  const router = useRouter();
  const { tokens: t } = useTheme();
  const [metrics, setMetrics] = useState<RUMMetric[]>([]);
  const [errors, setErrors] = useState<RUMError[]>([]);
  const [trends, setTrends] = useState<RUMTrendRow[]>([]);
  const [sessions, setSessions] = useState<RUMSession[]>([]);
  const [devices, setDevices] = useState<RUMDevices>({ browsers: [], os: [], devices: [] });
  const [range, setRange] = useState('24h');
  const [loading, setLoading] = useState(true);
  const [wvDimension, setWvDimension] = useState<'page' | 'device'>('page');
  const [webVitals, setWebVitals] = useState<WVRow[]>([]);

  // Core Web Vitals breakdown by the selected dimension + window (E4).
  useEffect(() => {
    let cancelled = false;
    fetchWithAuth(`/api/v1/rum/web-vitals?dimension=${wvDimension}&interval=${range}`)
      .then((res) => (res.ok ? res.json() : null))
      .then((j) => { if (!cancelled) setWebVitals(j?.data ?? []); })
      .catch(() => { if (!cancelled) setWebVitals([]); });
    return () => { cancelled = true; };
  }, [range, wvDimension]);

  useEffect(() => {
    // Fetch Web Vitals Analytics
    fetchWithAuth('/api/v1/rum/analytics')
      .then(res => res.json())
      .then(data => {
        if (data && data.data) {
          setMetrics(data.data);
        }
      })
      .catch(console.error);

    // Fetch Recent Errors
    fetchWithAuth('/api/v1/rum/errors')
      .then(res => res.json())
      .then(data => {
        if (data && data.data) {
          setErrors(data.data);
        }
      })
      .catch(console.error)
      .finally(() => setLoading(false));

    // Sessions are window-independent (last 24h of visits).
    fetchWithAuth('/api/v1/rum/sessions')
      .then(res => res.json())
      .then(data => setSessions(data?.data || []))
      .catch(console.error);
  }, []);

  // Trends and the device breakdown follow the selected time window.
  const fetchWindowed = useCallback(() => {
    fetchWithAuth(`/api/v1/rum/trends?interval=${range}`)
      .then(res => res.json())
      .then(data => setTrends(data?.data || []))
      .catch(console.error);
    fetchWithAuth(`/api/v1/rum/devices?interval=${range}`)
      .then(res => res.json())
      .then(data => setDevices({ browsers: data?.browsers || [], os: data?.os || [], devices: data?.devices || [] }))
      .catch(console.error);
  }, [range]);

  useEffect(() => { fetchWindowed(); }, [fetchWindowed]);

  // Reshape flat trend rows ({time_bucket, metric, p75}) into one row per bucket
  // with a column per metric, which is what recharts' per-<Line> dataKey wants.
  const trendData = React.useMemo(() => {
    const byTime = new Map<string, Record<string, string | number>>();
    for (const r of trends) {
      const key = r.time_bucket || '';
      if (!byTime.has(key)) byTime.set(key, { time_bucket: key });
      if (r.metric) byTime.get(key)![r.metric] = Number(r.p75 ?? 0);
    }
    return Array.from(byTime.values()).sort((a, b) => String(a.time_bucket).localeCompare(String(b.time_bucket)));
  }, [trends]);
  const trendMetrics = React.useMemo(() => Array.from(new Set(trends.map(r => r.metric).filter(Boolean))) as string[], [trends]);

  // Pivot flat CWV rows into one row per page/device group with a cell per metric.
  const wvPivot = React.useMemo(() => {
    const metricSet = new Set<string>();
    const byGroup = new Map<string, Record<string, { p75: number; rating: string }>>();
    for (const r of webVitals) {
      const g = r.group_value || '(none)';
      const m = r.metric || '';
      if (!m) continue;
      metricSet.add(m);
      if (!byGroup.has(g)) byGroup.set(g, {});
      byGroup.get(g)![m] = { p75: Number(r.p75 ?? 0), rating: r.rating || 'unknown' };
    }
    return {
      metrics: Array.from(metricSet).sort(),
      rows: Array.from(byGroup.entries()).map(([group, cells]) => ({ group, cells })),
    };
  }, [webVitals]);

  const ratingColor = (rating: string) => (rating === 'good' ? t.green : rating === 'poor' ? t.red : rating === 'needs-improvement' ? t.amber : t.text2);
  const fmtVital = (metric: string, p75: number) => (metric.toUpperCase() === 'CLS' ? p75.toFixed(3) : `${Math.round(p75)}ms`);

  // Core Web Vitals are rated at the 75th percentile (Google's methodology), so
  // read p75_value — the number the good/needs-improvement/poor thresholds below
  // are actually defined against. Falls back to avg_value for older API responses.
  const getMetricP75 = (name: string) => {
    const m = metrics.find(x => x.MetricName === name);
    if (!m) return 0;
    const v = m.p75_value ?? m.avg_value;
    return name === 'CLS' ? Number(v ?? 0) : Math.round(v ?? 0);
  };

  const getMetricCount = (type: string) => {
    return metrics.filter(x => x.Type === type).reduce((acc, curr) => acc + parseInt(String(curr.count ?? 0), 10), 0);
  };

  const lcp = getMetricP75('LCP');
  const cls = getMetricP75('CLS');
  const pageViews = getMetricCount('page_view');

  // Core Web Vitals thresholds (evaluated at p75): LCP good < 2.5s / poor > 4s;
  // CLS good < 0.1 / poor > 0.25.
  const lcpColor = lcp === 0 ? t.text2 : lcp < 2500 ? t.green : lcp < 4000 ? t.amber : t.red;
  const clsColor = cls === 0 ? t.text2 : cls < 0.1 ? t.green : cls < 0.25 ? t.amber : t.red;

  return (
    <div style={{ display: 'flex', flexDirection: 'column', height: '100%', overflow: 'auto' }}>

      {/* Header */}
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: '20px' }}>
        <div>
          <h2 style={{ fontSize: '26px', fontWeight: 700, margin: '0 0 8px' }}>Real User Monitoring</h2>
          <p style={{ color: t.text2, fontSize: '14.5px' }}>End-to-end visibility into user journeys, web vitals, and frontend errors.</p>
        </div>
        <div style={{ display: 'flex', gap: '12px' }}>
          <select
            value={range}
            onChange={(e) => setRange(e.target.value)}
            aria-label="Time range"
            style={{
              background: t.panelBg,
              border: '1px solid ' + t.panelBorder,
              color: t.text1,
              padding: '9px 14px',
              borderRadius: '10px',
              fontSize: '13px',
            }}
          >
            {RANGE_OPTIONS.map(o => <option key={o.value} value={o.value}>{o.label}</option>)}
          </select>
        </div>
      </div>

      {loading ? (
        <div style={{ padding: '48px', textAlign: 'center', color: t.text2 }}>Loading Real User Monitoring data...</div>
      ) : (
        <div style={{ display: 'flex', flexDirection: 'column', gap: '18px' }}>

          <div style={{ display: 'flex', gap: '18px', marginBottom: '18px', flexWrap: 'wrap' }}>

            {/* Web Vitals Overview */}
            <div
              style={{
                flex: '1 1 300px',
                padding: '24px',
                borderRadius: '20px',
                background: t.panelBg,
                border: '1px solid ' + t.panelBorder,
                backdropFilter: 'blur(30px) saturate(180%)',
                boxShadow: t.shadow,
              }}
            >
              <h3 style={{ fontSize: '16px', fontWeight: 700, margin: '0 0 20px' }}>Core Web Vitals</h3>

              <div>
                <div style={{ fontSize: '13px', color: t.text2, marginBottom: '6px' }}>Largest Contentful Paint (LCP) · p75</div>
                <div style={{ fontSize: '30px', fontWeight: 700, color: lcpColor }}>
                  {lcp === 0 ? '--' : `${(lcp / 1000).toFixed(2)}s`}
                </div>
                <div style={{ fontSize: '12px', color: t.text2 }}>Target: &lt; 2.5s</div>
              </div>

              <div style={{ marginTop: '20px', paddingTop: '20px', borderTop: '1px solid ' + t.panelBorder }}>
                <div style={{ fontSize: '13px', color: t.text2, marginBottom: '6px' }}>Cumulative Layout Shift (CLS) · p75</div>
                <div style={{ fontSize: '30px', fontWeight: 700, color: clsColor }}>
                  {cls === 0 ? '--' : cls.toFixed(3)}
                </div>
                <div style={{ fontSize: '12px', color: t.text2 }}>Target: &lt; 0.1</div>
              </div>
            </div>

            {/* Session Funnel */}
            <div
              style={{
                flex: '1 1 300px',
                padding: '24px',
                borderRadius: '20px',
                background: t.panelBg,
                border: '1px solid ' + t.panelBorder,
                backdropFilter: 'blur(30px) saturate(180%)',
                boxShadow: t.shadow,
              }}
            >
              <h3 style={{ fontSize: '16px', fontWeight: 700, margin: '0 0 20px' }}>Session Summary</h3>

              <div style={{ display: 'flex', justifyContent: 'space-between', paddingBottom: '16px', borderBottom: '1px solid ' + t.panelBorder, marginBottom: '16px' }}>
                <span style={{ color: t.text2, fontSize: '13.5px' }}>Total Page Views</span>
                <span style={{ fontWeight: 700, fontSize: '20px' }}>{pageViews}</span>
              </div>
              <div style={{ display: 'flex', justifyContent: 'space-between' }}>
                <span style={{ color: t.text2, fontSize: '13.5px' }}>Sessions with Errors</span>
                <span style={{ fontWeight: 700, fontSize: '20px', color: errors.length > 0 ? t.red : t.green }}>
                  {errors.length > 0 ? errors.length : 0}
                </span>
              </div>
            </div>
          </div>

          {/* Web Vitals Trend — time series, not a single point-in-time card */}
          <div style={{ padding: '22px 24px', borderRadius: '20px', background: t.panelBg, border: '1px solid ' + t.panelBorder, backdropFilter: 'blur(30px) saturate(180%)', boxShadow: t.shadow }}>
            <h3 style={{ fontSize: '16px', fontWeight: 700, margin: '0 0 16px' }}>Web Vitals Trend (p75)</h3>
            {trendData.length === 0 ? (
              <div style={{ padding: '32px', textAlign: 'center', color: t.text2, fontSize: '13px' }}>No web-vitals datapoints in this window yet.</div>
            ) : (
              <div style={{ height: '260px' }}>
                <ResponsiveContainer width="100%" height="100%">
                  <LineChart data={trendData} margin={{ top: 5, right: 20, left: 0, bottom: 0 }}>
                    <CartesianGrid strokeDasharray="3 3" stroke={t.panelBorder} />
                    <XAxis dataKey="time_bucket" tick={{ fontSize: 11, fill: t.text2 }} minTickGap={40} />
                    <YAxis tick={{ fontSize: 11, fill: t.text2 }} width={54} />
                    <Tooltip contentStyle={{ background: t.panelBg, border: '1px solid ' + t.panelBorder, borderRadius: '8px', fontSize: '12px' }} />
                    <Legend wrapperStyle={{ fontSize: '12px' }} />
                    {trendMetrics.map((m, i) => (
                      <Line key={m} type="monotone" dataKey={m} name={m} stroke={TREND_COLORS[i % TREND_COLORS.length]} strokeWidth={2} dot={false} />
                    ))}
                  </LineChart>
                </ResponsiveContainer>
              </div>
            )}
          </div>

          {/* Core Web Vitals breakdown by page / device (RUM · E4) */}
          <div style={{ padding: '22px 24px', borderRadius: '20px', background: t.panelBg, border: '1px solid ' + t.panelBorder, backdropFilter: 'blur(30px) saturate(180%)', boxShadow: t.shadow }}>
            <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', gap: '12px', flexWrap: 'wrap', marginBottom: '16px' }}>
              <h3 style={{ fontSize: '16px', fontWeight: 700, margin: 0 }}>Web Vitals by {wvDimension === 'page' ? 'Page' : 'Device'} (p75)</h3>
              <div style={{ display: 'flex', gap: '4px', background: t.dark ? 'rgba(0,0,0,0.25)' : 'rgba(0,0,0,0.05)', borderRadius: '8px', padding: '4px' }}>
                {(['page', 'device'] as const).map((d) => (
                  <button key={d} onClick={() => setWvDimension(d)} style={{ padding: '6px 14px', borderRadius: '6px', border: 'none', fontSize: '12.5px', fontWeight: 600, cursor: 'pointer', background: wvDimension === d ? (t.dark ? 'rgba(255,255,255,0.12)' : '#fff') : 'transparent', color: wvDimension === d ? t.text1 : t.text2, textTransform: 'capitalize' }}>{d}</button>
                ))}
              </div>
            </div>
            {wvPivot.rows.length === 0 ? (
              <div style={{ padding: '28px', textAlign: 'center', color: t.text2, fontSize: '13px' }}>No web-vitals datapoints for this breakdown yet.</div>
            ) : (
              <div style={{ overflowX: 'auto' }}>
                <table style={{ width: '100%', borderCollapse: 'collapse', textAlign: 'left' }}>
                  <thead>
                    <tr style={{ borderBottom: '1px solid ' + t.panelBorder }}>
                      <th style={{ padding: '10px 12px', fontSize: '12px', fontWeight: 600, color: t.text2, textTransform: 'capitalize' }}>{wvDimension}</th>
                      {wvPivot.metrics.map((m) => (
                        <th key={m} style={{ padding: '10px 12px', fontSize: '12px', fontWeight: 600, color: t.text2 }}>{m}</th>
                      ))}
                    </tr>
                  </thead>
                  <tbody>
                    {wvPivot.rows.map((row) => (
                      <tr key={row.group} style={{ borderBottom: '1px solid ' + t.panelBorder }}>
                        <td style={{ padding: '11px 12px', fontSize: '13px', color: t.text1, fontFamily: 'monospace', maxWidth: '280px', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }} title={row.group}>{row.group}</td>
                        {wvPivot.metrics.map((m) => {
                          const cell = row.cells[m];
                          return (
                            <td key={m} style={{ padding: '11px 12px', fontSize: '13px' }}>
                              {cell ? (
                                <span style={{ display: 'inline-flex', alignItems: 'center', gap: '6px' }}>
                                  <span style={{ width: '8px', height: '8px', borderRadius: '50%', background: ratingColor(cell.rating), flexShrink: 0 }} />
                                  <span style={{ color: t.text1 }}>{fmtVital(m, cell.p75)}</span>
                                </span>
                              ) : <span style={{ color: t.text2 }}>—</span>}
                            </td>
                          );
                        })}
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            )}
          </div>

          {/* Device / browser / OS breakdown */}
          <div style={{ display: 'flex', gap: '18px', flexWrap: 'wrap' }}>
            {([['Devices', devices.devices], ['Browsers', devices.browsers], ['Operating Systems', devices.os]] as const).map(([title, rows]) => {
              const total = rows.reduce((a, b) => a + b.count, 0);
              return (
                <div key={title} style={{ flex: '1 1 240px', padding: '20px 22px', borderRadius: '20px', background: t.panelBg, border: '1px solid ' + t.panelBorder, backdropFilter: 'blur(30px) saturate(180%)', boxShadow: t.shadow }}>
                  <h3 style={{ fontSize: '15px', fontWeight: 700, margin: '0 0 14px' }}>{title}</h3>
                  {rows.length === 0 ? (
                    <div style={{ color: t.text2, fontSize: '13px' }}>No data yet.</div>
                  ) : rows.map(row => {
                    const pct = total > 0 ? Math.round((row.count / total) * 100) : 0;
                    return (
                      <div key={row.name} style={{ marginBottom: '10px' }}>
                        <div style={{ display: 'flex', justifyContent: 'space-between', fontSize: '12.5px', marginBottom: '4px' }}>
                          <span style={{ color: t.text1 }}>{row.name}</span>
                          <span style={{ color: t.text2 }}>{row.count} · {pct}%</span>
                        </div>
                        <div style={{ height: '6px', borderRadius: '100px', background: t.dark ? 'rgba(255,255,255,0.08)' : 'rgba(0,0,0,0.06)' }}>
                          <div style={{ width: `${pct}%`, height: '100%', borderRadius: '100px', background: `linear-gradient(90deg, ${t.accent}, ${t.accent2})` }} />
                        </div>
                      </div>
                    );
                  })}
                </div>
              );
            })}
          </div>

          {/* User sessions — the session story, one row per real visit */}
          <div style={{ borderRadius: '20px', overflow: 'hidden', background: t.panelBg, border: '1px solid ' + t.panelBorder, backdropFilter: 'blur(30px) saturate(180%)', boxShadow: t.shadow }}>
            <div style={{ padding: '22px 24px 6px' }}>
              <h3 style={{ fontSize: '16px', fontWeight: 700, margin: 0 }}>User Sessions</h3>
            </div>
            <div style={{ overflowX: 'auto' }}>
              {sessions.length === 0 ? (
                <div style={{ padding: '40px', textAlign: 'center', color: t.text2, fontSize: '13px' }}>No sessions recorded in the last 24h.</div>
              ) : (
                <table style={{ width: '100%', borderCollapse: 'collapse', textAlign: 'left' }}>
                  <thead>
                    <tr>
                      {['Entry Path', 'Device', 'Page Views', 'Errors', 'Duration', 'Last Seen'].map(h => (
                        <th key={h} style={{ padding: '14px 24px', fontWeight: 500, color: t.text2, fontSize: '13px', whiteSpace: 'nowrap' }}>{h}</th>
                      ))}
                    </tr>
                  </thead>
                  <tbody>
                    {sessions.map((s, i) => (
                      <tr key={s.session_id || i} style={{ borderTop: '1px solid ' + t.panelBorder }}>
                        <td style={{ padding: '13px 24px', fontSize: '13px', fontFamily: 'monospace' }}>{s.entry_path || '—'}</td>
                        <td style={{ padding: '13px 24px', fontSize: '12.5px', color: t.text2 }}>{[s.browser, s.os, s.device].filter(Boolean).join(' · ') || '—'}</td>
                        <td style={{ padding: '13px 24px', fontSize: '13px' }}>{Number(s.page_views ?? 0)}</td>
                        <td style={{ padding: '13px 24px', fontSize: '13px', color: Number(s.errors ?? 0) > 0 ? t.red : t.text1, fontWeight: Number(s.errors ?? 0) > 0 ? 600 : 400 }}>{Number(s.errors ?? 0)}</td>
                        <td style={{ padding: '13px 24px', fontSize: '13px', color: t.text2 }}>{Number(s.duration_seconds ?? 0)}s</td>
                        <td style={{ padding: '13px 24px', fontSize: '13px', color: t.text2, whiteSpace: 'nowrap' }}>{s.last_seen ? new Date(String(s.last_seen).replace(' ', 'T') + 'Z').toLocaleString() : '—'}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              )}
            </div>
          </div>

          {/* Recent Frontend Errors */}
          <div
            style={{
              borderRadius: '20px',
              overflow: 'hidden',
              background: t.panelBg,
              border: '1px solid ' + t.panelBorder,
              backdropFilter: 'blur(30px) saturate(180%)',
              boxShadow: t.shadow,
              display: 'flex',
              flexDirection: 'column',
            }}
          >
            <div style={{ padding: '22px 24px 6px' }}>
              <h3 style={{ fontSize: '16px', fontWeight: 700, margin: 0 }}>Recent JavaScript Errors</h3>
            </div>

            <div style={{ overflowX: 'auto' }}>
              {errors.length === 0 ? (
                <div style={{ padding: '48px', textAlign: 'center', color: t.text2 }}>No frontend Javascript errors detected.</div>
              ) : (
                <table style={{ width: '100%', borderCollapse: 'collapse', textAlign: 'left' }}>
                  <thead>
                    <tr>
                      <th style={{ padding: '16px 24px', fontWeight: 500, color: t.text2, fontSize: '13px' }}>Timestamp</th>
                      <th style={{ padding: '16px', fontWeight: 500, color: t.text2, fontSize: '13px' }}>Path</th>
                      <th style={{ padding: '16px', fontWeight: 500, color: t.text2, fontSize: '13px' }}>Error Message</th>
                      <th style={{ padding: '16px', fontWeight: 500, color: t.text2, fontSize: '13px' }}>User Agent</th>
                      <th style={{ padding: '16px 24px', fontWeight: 500, color: t.text2, fontSize: '13px' }}>Backend Trace</th>
                    </tr>
                  </thead>
                  <tbody>
                    {errors.map((err, i) => (
                      <tr key={i} style={{ borderTop: '1px solid ' + t.panelBorder }}>
                        <td style={{ padding: '16px 24px', fontSize: '13px', color: t.text2, whiteSpace: 'nowrap' }}>
                          {new Date(err.timestamp ?? 0).toLocaleString()}
                        </td>
                        <td style={{ padding: '16px', fontSize: '13px', fontFamily: 'monospace' }}>
                          {err.path}
                        </td>
                        <td style={{ padding: '16px', color: t.red, fontWeight: 500, fontSize: '13px' }}>
                          {err.error_msg}
                        </td>
                        <td style={{ padding: '16px', fontSize: '12px', color: t.text2 }}>
                          {(err.user_agent ?? '').length > 40 ? (err.user_agent ?? '').substring(0, 40) + '...' : err.user_agent}
                        </td>
                        <td style={{ padding: '16px 24px' }}>
                          {err.trace_id ? (
                            <button
                              onClick={() => router.push(`/traces?trace=${err.trace_id}`)}
                              style={{
                                fontSize: '12px',
                                padding: '6px 12px',
                                background: 'transparent',
                                border: '1px solid ' + t.panelBorder,
                                borderRadius: '8px',
                                color: t.text1,
                                cursor: 'pointer',
                              }}
                            >
                              View Trace
                            </button>
                          ) : (
                            <span style={{ fontSize: '12px', color: t.text2 }}>—</span>
                          )}
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              )}
            </div>
          </div>

        </div>
      )}

    </div>
  );
}
