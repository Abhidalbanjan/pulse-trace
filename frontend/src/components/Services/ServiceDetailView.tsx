"use client";

import React, { useState, useEffect, useCallback } from 'react';
import { useRouter } from 'next/navigation';
import { fetchWithAuth } from '@/lib/api';
import { LineChart, Line, AreaChart, Area, XAxis, YAxis, CartesianGrid, Tooltip, Legend, ResponsiveContainer, ReferenceLine } from 'recharts';
import { DeploymentsPanel } from './DeploymentsPanel';
import { PROFILED_SERVICES } from '@/lib/profiledServices';
import { useTheme } from '@/context/ThemeContext';

const INTERVAL_OPTIONS = [
  { value: '1h', label: 'Last 1 Hour' },
  { value: '24h', label: 'Last 24 Hours' },
  { value: '7d', label: 'Last 7 Days' },
];

interface ResourceRow {
  operation: string;
  requests: number;
  errors: number;
  p50_ms: number;
  p90_ms: number;
  p99_ms: number;
  total_ms: number;
}

interface VersionRow {
  version: string;
  requests: number;
  errors: number;
  p50_ms: number;
  p90_ms: number;
  p99_ms: number;
  first_seen: string;
  last_seen: string;
  is_regression?: boolean;
  previous_version?: string;
  error_rate_delta_pct?: number;
  p99_delta_pct?: number;
}

interface Deployment {
  version: string;
  deployed_at: string;
}

interface ServiceDetail {
  service: string;
  summary: { requests: number; errors: number; p50_ms: number; p90_ms: number; p99_ms: number };
  timeseries: Array<{ time_bucket: string; requests: number; errors: number; p50_ms: number; p90_ms: number; p99_ms: number }>;
  resources: ResourceRow[];
  versions: VersionRow[];
}

export function ServiceDetailView({ serviceName }: { serviceName: string }) {
  const router = useRouter();
  const { tokens: t } = useTheme();
  const [interval, setInterval_] = useState('1h');
  const [detail, setDetail] = useState<ServiceDetail | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [deployments, setDeployments] = useState<Deployment[]>([]);

  const fetchDetail = useCallback(() => {
    fetchWithAuth(`/api/v1/services/${encodeURIComponent(serviceName)}?interval=${interval}`)
      .then(async res => {
        if (!res.ok) throw new Error(await res.text());
        return res.json();
      })
      .then(json => {
        setDetail(json);
        setError(null);
      })
      .catch(err => setError(err.message || 'Failed to load service detail'))
      .finally(() => setLoading(false));
  }, [serviceName, interval]);

  useEffect(() => {
    setLoading(true);
    fetchDetail();
    const t = window.setInterval(fetchDetail, 15000);
    return () => window.clearInterval(t);
  }, [fetchDetail]);

  useEffect(() => {
    fetchWithAuth(`/api/v1/deployments?service=${encodeURIComponent(serviceName)}`)
      .then(res => (res.ok ? res.json() : { data: [] }))
      .then(json => setDeployments(json.data || []))
      .catch(() => setDeployments([]));
  }, [serviceName]);

  const chartData = (detail?.timeseries || []).map(row => ({
    time: new Date(row.time_bucket).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' }),
    bucketMs: new Date(row.time_bucket).getTime(),
    requests: row.requests,
    errorRate: row.requests > 0 ? (row.errors / row.requests) * 100 : 0,
    p50: Math.round(row.p50_ms),
    p90: Math.round(row.p90_ms),
    p99: Math.round(row.p99_ms),
  }));

  // Deployment markers overlaid on the RED charts: each deployment is snapped to
  // the nearest time bucket already on the x-axis (a categorical string axis, so
  // the marker's x must match one of the chart's own labels exactly).
  const deploymentMarkers = deployments
    .map(d => {
      if (chartData.length === 0) return null;
      const deployedAtMs = new Date(d.deployed_at).getTime();
      let closest = chartData[0];
      let closestDelta = Math.abs(chartData[0].bucketMs - deployedAtMs);
      for (const row of chartData) {
        const delta = Math.abs(row.bucketMs - deployedAtMs);
        if (delta < closestDelta) {
          closest = row;
          closestDelta = delta;
        }
      }
      return { version: d.version, chartLabel: closest.time };
    })
    .filter((m): m is { version: string; chartLabel: string } => m !== null);

  const summary = detail?.summary;
  const summaryErrorRate = summary && summary.requests > 0 ? (summary.errors / summary.requests) * 100 : 0;

  const cardStyle: React.CSSProperties = {
    borderRadius: '20px',
    background: t.panelBg,
    border: '1px solid ' + t.panelBorder,
    backdropFilter: 'blur(30px) saturate(180%)',
    WebkitBackdropFilter: 'blur(30px) saturate(180%)',
    boxShadow: t.shadow,
  };

  const gridStroke = t.dark ? 'rgba(255,255,255,0.1)' : 'rgba(0,0,0,0.08)';
  const tooltipStyle: React.CSSProperties = {
    backgroundColor: t.dark ? 'rgba(20,20,26,0.92)' : 'rgba(255,255,255,0.95)',
    border: '1px solid ' + t.panelBorder,
    borderRadius: '10px',
    color: t.text1,
  };

  const thStyle: React.CSSProperties = { padding: '12px 20px', fontWeight: 600, color: t.text2, fontSize: '12px' };
  const tdStyle: React.CSSProperties = { padding: '12px 20px', fontSize: '13px', color: t.text1 };
  const theadRowStyle: React.CSSProperties = { borderBottom: '1px solid ' + t.panelBorder, background: t.dark ? 'rgba(0,0,0,0.15)' : 'rgba(0,0,0,0.03)' };
  const selectStyle: React.CSSProperties = {
    padding: '8px 12px',
    borderRadius: '10px',
    background: t.dark ? 'rgba(255,255,255,0.05)' : 'rgba(0,0,0,0.03)',
    border: '1px solid ' + t.panelBorder,
    color: t.text1,
    fontSize: '13px',
    outline: 'none',
  };

  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: '20px', height: '100%', overflow: 'auto' }}>
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center' }}>
        <div>
          <button
            onClick={() => router.push('/services')}
            style={{ background: 'none', border: 'none', color: t.accent, cursor: 'pointer', padding: 0, marginBottom: '8px', fontSize: '13px', fontWeight: 600 }}
          >
            ← Back to Services
          </button>
          <h2 style={{ fontSize: '26px', fontWeight: 700, margin: 0, color: t.text1 }}>{serviceName}</h2>
        </div>
        <select style={selectStyle} value={interval} onChange={(e) => setInterval_(e.target.value)}>
          {INTERVAL_OPTIONS.map(opt => <option key={opt.value} value={opt.value}>{opt.label}</option>)}
        </select>
      </div>

      {error && (
        <div style={{ padding: '16px', background: t.redSoft, color: t.red, borderRadius: '14px', border: '1px solid ' + t.red }}>
          {error}
        </div>
      )}

      {detail?.versions.some(v => v.is_regression) && (
        <div style={{ padding: '16px', background: t.redSoft, color: t.red, borderRadius: '14px', border: '1px solid ' + t.red }}>
          <span className="material-symbols-outlined" style={{ fontSize: '16px', verticalAlign: 'middle', marginRight: '6px' }}>warning</span>
          Regression detected: {detail.versions.filter(v => v.is_regression).map(v =>
            `${v.version} vs ${v.previous_version} (error rate ${v.error_rate_delta_pct! >= 0 ? '+' : ''}${v.error_rate_delta_pct!.toFixed(1)}pp, p99 ${v.p99_delta_pct! >= 0 ? '+' : ''}${v.p99_delta_pct!.toFixed(0)}%)`
          ).join('; ')}
        </div>
      )}

      {/* Summary tiles */}
      <div style={{ display: 'grid', gridTemplateColumns: 'repeat(4, 1fr)', gap: '16px' }}>
        {[
          { label: 'Requests', value: summary ? summary.requests.toLocaleString() : '—' },
          { label: 'Error Rate', value: summary ? `${summaryErrorRate.toFixed(2)}%` : '—', color: summaryErrorRate > 5 ? t.red : summaryErrorRate > 0 ? t.amber : t.green },
          { label: 'p90 Latency', value: summary ? `${Math.round(summary.p90_ms)}ms` : '—' },
          { label: 'p99 Latency', value: summary ? `${Math.round(summary.p99_ms)}ms` : '—' },
        ].map(tile => (
          <div key={tile.label} style={{ ...cardStyle, padding: '20px' }}>
            <div style={{ fontSize: '12px', color: t.text2, textTransform: 'uppercase', letterSpacing: '0.05em', marginBottom: '8px' }}>{tile.label}</div>
            <div style={{ fontSize: '28px', fontWeight: 700, color: tile.color || t.text1 }}>{tile.value}</div>
          </div>
        ))}
      </div>

      {loading ? (
        <div style={{ padding: '48px', textAlign: 'center', color: t.text2 }}>Loading service metrics...</div>
      ) : (
        <>
          {/* Charts */}
          <div style={{ display: 'flex', gap: '20px' }}>
            <div style={{ ...cardStyle, flex: 1, padding: '20px', height: '280px', display: 'flex', flexDirection: 'column' }}>
              <h3 style={{ fontSize: '14px', fontWeight: 600, marginBottom: '12px', color: t.text1 }}>Latency Percentiles</h3>
              <div style={{ flex: 1, minHeight: 0 }}>
                <ResponsiveContainer width="100%" height="100%">
                  <LineChart data={chartData} margin={{ top: 5, right: 20, left: 0, bottom: 0 }}>
                    <CartesianGrid strokeDasharray="3 3" stroke={gridStroke} />
                    <XAxis dataKey="time" stroke={t.text2} tick={{ fontSize: 11, fill: t.text2 }} />
                    <YAxis stroke={t.text2} tick={{ fontSize: 11, fill: t.text2 }} unit="ms" />
                    <Tooltip contentStyle={tooltipStyle} />
                    <Legend wrapperStyle={{ color: t.text2, fontSize: '12px' }} />
                    <Line type="monotone" dataKey="p99" stroke={t.red} strokeWidth={2} dot={false} />
                    <Line type="monotone" dataKey="p90" stroke={t.amber} strokeWidth={2} dot={false} />
                    <Line type="monotone" dataKey="p50" stroke={t.green} strokeWidth={2} dot={false} />
                    {deploymentMarkers.map((m, i) => (
                      <ReferenceLine key={i} x={m.chartLabel} stroke={t.gold} strokeDasharray="3 3"
                        label={{ value: m.version, position: 'top', fill: t.gold, fontSize: 10 }} />
                    ))}
                  </LineChart>
                </ResponsiveContainer>
              </div>
            </div>

            <div style={{ ...cardStyle, flex: 1, padding: '20px', height: '280px', display: 'flex', flexDirection: 'column' }}>
              <h3 style={{ fontSize: '14px', fontWeight: 600, marginBottom: '12px', color: t.text1 }}>Request Rate & Error Rate</h3>
              <div style={{ flex: 1, minHeight: 0 }}>
                <ResponsiveContainer width="100%" height="100%">
                  <AreaChart data={chartData} margin={{ top: 5, right: 20, left: 0, bottom: 0 }}>
                    <CartesianGrid strokeDasharray="3 3" stroke={gridStroke} />
                    <XAxis dataKey="time" stroke={t.text2} tick={{ fontSize: 11, fill: t.text2 }} />
                    <YAxis yAxisId="left" stroke={t.text2} tick={{ fontSize: 11, fill: t.text2 }} />
                    <YAxis yAxisId="right" orientation="right" stroke={t.red} tick={{ fontSize: 11, fill: t.text2 }} unit="%" />
                    <Tooltip contentStyle={tooltipStyle} />
                    <Legend wrapperStyle={{ color: t.text2, fontSize: '12px' }} />
                    <Area yAxisId="left" type="monotone" dataKey="requests" name="Requests" stroke={t.accent} fill={t.accent} fillOpacity={0.25} />
                    <Line yAxisId="right" type="monotone" dataKey="errorRate" name="Error Rate %" stroke={t.red} strokeWidth={2} dot={false} />
                    {deploymentMarkers.map((m, i) => (
                      <ReferenceLine key={i} yAxisId="left" x={m.chartLabel} stroke={t.gold} strokeDasharray="3 3"
                        label={{ value: m.version, position: 'top', fill: t.gold, fontSize: 10 }} />
                    ))}
                  </AreaChart>
                </ResponsiveContainer>
              </div>
            </div>
          </div>

          {/* Resources table */}
          <div style={{ ...cardStyle, padding: 0, overflow: 'auto' }}>
            <div style={{ padding: '16px 20px', borderBottom: '1px solid ' + t.panelBorder }}>
              <h3 style={{ fontSize: '14px', fontWeight: 600, color: t.text1, margin: 0 }}>Resources</h3>
            </div>
            {!detail || detail.resources.length === 0 ? (
              <div style={{ padding: '32px', textAlign: 'center', color: t.text2 }}>No resources recorded for this window.</div>
            ) : (
              <table style={{ width: '100%', borderCollapse: 'collapse', textAlign: 'left' }}>
                <thead>
                  <tr style={theadRowStyle}>
                    <th style={thStyle}>Resource</th>
                    <th style={thStyle}>Requests</th>
                    <th style={thStyle}>Error Rate</th>
                    <th style={thStyle}>p50</th>
                    <th style={thStyle}>p90</th>
                    <th style={thStyle}>p99</th>
                    <th style={thStyle}>Total Time</th>
                  </tr>
                </thead>
                <tbody>
                  {detail.resources.map(r => {
                    const rate = r.requests > 0 ? (r.errors / r.requests) * 100 : 0;
                    return (
                      <tr key={r.operation} style={{ borderBottom: '1px solid ' + t.panelBorder }}>
                        <td style={{ ...tdStyle, fontFamily: 'monospace' }}>{r.operation}</td>
                        <td style={tdStyle}>{r.requests.toLocaleString()}</td>
                        <td style={{ ...tdStyle, color: rate > 5 ? t.red : rate > 0 ? t.amber : t.green, fontWeight: 600 }}>{rate.toFixed(2)}%</td>
                        <td style={tdStyle}>{Math.round(r.p50_ms)}ms</td>
                        <td style={tdStyle}>{Math.round(r.p90_ms)}ms</td>
                        <td style={tdStyle}>{Math.round(r.p99_ms)}ms</td>
                        <td style={tdStyle}>{(r.total_ms / 1000).toFixed(1)}s</td>
                      </tr>
                    );
                  })}
                </tbody>
              </table>
            )}
          </div>

          {/* Version comparison - Deployment Tracking */}
          <div style={{ ...cardStyle, padding: 0, overflow: 'auto' }}>
            <div style={{ padding: '16px 20px', borderBottom: '1px solid ' + t.panelBorder }}>
              <h3 style={{ fontSize: '14px', fontWeight: 600, color: t.text1, margin: 0 }}>Versions</h3>
            </div>
            {!detail || detail.versions.length === 0 ? (
              <div style={{ padding: '32px', textAlign: 'center', color: t.text2 }}>No version data recorded for this window.</div>
            ) : (
              <table style={{ width: '100%', borderCollapse: 'collapse', textAlign: 'left' }}>
                <thead>
                  <tr style={theadRowStyle}>
                    <th style={thStyle}>Version</th>
                    <th style={thStyle}>Requests</th>
                    <th style={thStyle}>Error Rate</th>
                    <th style={thStyle}>p50</th>
                    <th style={thStyle}>p90</th>
                    <th style={thStyle}>p99</th>
                    <th style={thStyle}>Last Seen</th>
                    <th style={thStyle}>vs. Previous</th>
                  </tr>
                </thead>
                <tbody>
                  {detail.versions.map(v => {
                    const rate = v.requests > 0 ? (v.errors / v.requests) * 100 : 0;
                    return (
                      <tr key={v.version || '(unknown)'} style={{ borderBottom: '1px solid ' + t.panelBorder, background: v.is_regression ? t.redSoft : undefined }}>
                        <td style={{ padding: '12px 20px', fontFamily: 'monospace', fontSize: '13px', color: t.accent }}>
                          {v.is_regression && <span className="material-symbols-outlined" title="Regression vs previous version" style={{ fontSize: '13px', verticalAlign: 'middle', marginRight: '6px', color: t.red }}>warning</span>}
                          {v.version || '(unknown)'}
                        </td>
                        <td style={tdStyle}>{v.requests.toLocaleString()}</td>
                        <td style={{ ...tdStyle, color: rate > 5 ? t.red : rate > 0 ? t.amber : t.green, fontWeight: 600 }}>{rate.toFixed(2)}%</td>
                        <td style={tdStyle}>{Math.round(v.p50_ms)}ms</td>
                        <td style={tdStyle}>{Math.round(v.p90_ms)}ms</td>
                        <td style={tdStyle}>{Math.round(v.p99_ms)}ms</td>
                        <td style={{ padding: '12px 20px', fontSize: '13px', color: t.text2 }}>{new Date(v.last_seen).toLocaleString()}</td>
                        <td style={{ padding: '12px 20px', fontSize: '12px', color: v.is_regression ? t.red : t.text2 }}>
                          {v.previous_version ? (
                            <>Δerr {v.error_rate_delta_pct! >= 0 ? '+' : ''}{v.error_rate_delta_pct!.toFixed(1)}pp, Δp99 {v.p99_delta_pct! >= 0 ? '+' : ''}{v.p99_delta_pct!.toFixed(0)}%</>
                          ) : '—'}
                        </td>
                      </tr>
                    );
                  })}
                </tbody>
              </table>
            )}
          </div>

          {/* Code Hotspots: automatically surfaced here instead of requiring a
              manual trip to the separate Profiler tab - what CPU/memory is this
              service actually spending its time on right now. */}
          <div style={{ ...cardStyle, padding: 0, overflow: 'hidden', display: 'flex', flexDirection: 'column' }}>
            <div style={{ padding: '16px 20px', borderBottom: '1px solid ' + t.panelBorder, display: 'flex', justifyContent: 'space-between', alignItems: 'center' }}>
              <h3 style={{ fontSize: '14px', fontWeight: 600, color: t.text1, margin: 0 }}>Code Hotspots</h3>
              {PROFILED_SERVICES.includes(serviceName) && (
                <button
                  style={{ padding: '6px 12px', fontSize: '12px', fontWeight: 600, borderRadius: '8px', background: 'transparent', border: '1px solid ' + t.panelBorder, color: t.text1, cursor: 'pointer' }}
                  onClick={() => router.push(`/profiler?service=${encodeURIComponent(serviceName)}`)}
                >
                  Open full Profiler →
                </button>
              )}
            </div>
            {PROFILED_SERVICES.includes(serviceName) ? (
              <div style={{ height: '360px', background: '#ffffff' }}>
                <iframe
                  src={`/api/v1/profiler/?query=${encodeURIComponent(`${serviceName}.process_cpu{}`)}`}
                  style={{ width: '100%', height: '100%', border: 'none' }}
                  title={`${serviceName} CPU flame graph`}
                  sandbox="allow-same-origin allow-scripts allow-popups allow-forms"
                />
              </div>
            ) : (
              <div style={{ padding: '32px', textAlign: 'center', color: t.text2 }}>
                No continuous profiler wired into this service.
              </div>
            )}
          </div>

          <DeploymentsPanel serviceName={serviceName} />
        </>
      )}
    </div>
  );
}
