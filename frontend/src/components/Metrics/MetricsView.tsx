"use client";

import React, { useState, useEffect, useCallback } from 'react';
import { fetchWithAuth } from '@/lib/api';
import { LineChart, Line, XAxis, YAxis, CartesianGrid, Tooltip, Legend, ResponsiveContainer, ReferenceLine } from 'recharts';
import { snapDeploymentsToBuckets, toEpochMs } from '@/lib/deployMarkers';
import { useTheme } from '@/context/ThemeContext';

const INTERVAL_OPTIONS = [
  { value: '1h', label: 'Last 1 Hour' },
  { value: '24h', label: 'Last 24 Hours' },
  { value: '7d', label: 'Last 7 Days' },
];

// Per-bucket aggregation functions, mirroring the gateway's metricAggExpr
// allowlist. rate() is a per-second counter increase; p50–p99 are the
// distribution of datapoint values within each bucket.
const FN_OPTIONS = [
  { value: 'avg', label: 'avg' },
  { value: 'rate', label: 'rate' },
  { value: 'max', label: 'max' },
  { value: 'min', label: 'min' },
  { value: 'sum', label: 'sum' },
  { value: 'p50', label: 'p50' },
  { value: 'p90', label: 'p90' },
  { value: 'p95', label: 'p95' },
  { value: 'p99', label: 'p99' },
];

// formatMetricValue renders a metric value compactly (k/M/G) so a Y-axis tick or
// tooltip stays legible at any magnitude.
function formatMetricValue(v: number): string {
  if (!Number.isFinite(v)) return String(v);
  const abs = Math.abs(v);
  if (abs >= 1e9) return (v / 1e9).toFixed(1) + 'G';
  if (abs >= 1e6) return (v / 1e6).toFixed(1) + 'M';
  if (abs >= 1e3) return (v / 1e3).toFixed(1) + 'k';
  return Number.isInteger(v) ? String(v) : v.toFixed(2);
}

const SERIES_COLORS = ['#6366f1', '#22c55e', '#f59e0b', '#ef4444', '#06b6d4', '#a855f7', '#ec4899', '#84cc16'];

interface MetricName {
  name: string;
  description: string;
  unit: string;
  service: string;
  type: 'gauge' | 'sum';
}

// This is PulseTrace's native metrics pillar: raw OTLP gauge/sum datapoints,
// stored directly in ClickHouse by the collector (see
// otel-collector/otel-collector-config.yaml's clickhouse/metrics exporter)
// and queried here via gateway-service's MetricsHandler — not a redirect to
// a bundled Grafana. Distinct from the Services RED view, which derives
// rate/error/duration from trace spans; this view is for arbitrary
// instrumented counters and gauges (queue depth, cache hit rate, collector
// throughput, etc).
interface MetricRow { time_bucket: string; value: number; }
// A label dimension of a metric (Metrics · E1 explorer): key → value + count.
interface MetricLabel { label_key: string; label_value: string; n: number | string; }

export function MetricsView() {
  const { tokens: t } = useTheme();
  const [names, setNames] = useState<MetricName[]>([]);
  const [namesLoading, setNamesLoading] = useState(true);
  const [namesError, setNamesError] = useState<string | null>(null);
  const [selected, setSelected] = useState<MetricName | null>(null);
  const [interval, setInterval_] = useState('1h');
  const [fn, setFn] = useState('avg');
  const [series, setSeries] = useState<Record<string, MetricRow[]>>({});
  const [chartLoading, setChartLoading] = useState(false);
  const [chartError, setChartError] = useState<string | null>(null);
  const [search, setSearch] = useState('');
  const [labels, setLabels] = useState<MetricLabel[]>([]);
  const [deployments, setDeployments] = useState<{ deployed_at: string; version: string }[]>([]);

  // Deploy markers (Deploy Gates · E1): fetch the selected service's deploys
  // inside the chart window so the "what changed" lines can overlay the metric.
  useEffect(() => {
    let cancelled = false;
    const load = async () => {
      const svc = selected?.service;
      if (!svc) { if (!cancelled) setDeployments([]); return; }
      const spanMs = interval === '7d' ? 7 * 86400e3 : interval === '24h' ? 86400e3 : 3600e3;
      const to = new Date();
      const from = new Date(to.getTime() - spanMs);
      const p = new URLSearchParams({ service: svc, from: from.toISOString(), to: to.toISOString() });
      try {
        const res = await fetchWithAuth(`/api/v1/deployments?${p.toString()}`);
        const j = res.ok ? await res.json() : null;
        if (!cancelled) setDeployments(j?.data ?? []);
      } catch {
        if (!cancelled) setDeployments([]);
      }
    };
    load();
    return () => { cancelled = true; };
  }, [selected, interval]);

  // Metric explorer (E1): when a metric is selected, discover its label
  // dimensions so the user can see what the series can be sliced by.
  useEffect(() => {
    if (!selected) return;
    let cancelled = false;
    const p = new URLSearchParams({ metric: selected.name, type: selected.type });
    fetchWithAuth(`/api/v1/metrics/catalog?${p.toString()}`)
      .then((res) => (res.ok ? res.json() : null))
      .then((j) => { if (!cancelled) setLabels(j?.data ?? []); })
      .catch(() => { if (!cancelled) setLabels([]); });
    return () => { cancelled = true; };
  }, [selected]);

  useEffect(() => {
    fetchWithAuth('/api/v1/metrics')
      .then(async res => {
        if (!res.ok) throw new Error(await res.text());
        return res.json();
      })
      .then(json => {
        const rows: MetricName[] = json.data || [];
        setNames(rows);
        setNamesError(null);
        if (rows.length > 0 && !selected) setSelected(rows[0]);
      })
      .catch(err => setNamesError(err.message || 'Failed to load metric catalog'))
      .finally(() => setNamesLoading(false));
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  const fetchSeries = useCallback(() => {
    if (!selected) return;
    setChartLoading(true);
    const params = new URLSearchParams({ metric: selected.name, type: selected.type, interval, fn });
    fetchWithAuth(`/api/v1/metrics/query?${params.toString()}`)
      .then(async res => {
        if (!res.ok) throw new Error(await res.text());
        return res.json();
      })
      .then(json => {
        setSeries(json.series || {});
        setChartError(null);
      })
      .catch(err => setChartError(err.message || 'Failed to load metric data'))
      .finally(() => setChartLoading(false));
  }, [selected, interval, fn]);

  // eslint-disable-next-line react-hooks/set-state-in-effect -- intentional one-shot fetch/hydration on mount; effect is the right place to sync from the API/localStorage
  useEffect(() => { fetchSeries(); }, [fetchSeries]);

  const primaryBtnStyle: React.CSSProperties = {
    padding: '8px 14px', borderRadius: '9px', border: '1px solid ' + t.panelBorder,
    background: t.dark ? 'rgba(255,255,255,0.05)' : 'rgba(255,255,255,0.7)', color: t.text1,
    fontSize: '12.5px', cursor: 'pointer',
  };

  // Reshape { serviceName: [{time_bucket, value}, ...] } into one array keyed
  // by time_bucket with one column per service, which is what recharts'
  // <Line dataKey="serviceName" /> per series expects from a single dataset.
  const chartData = React.useMemo(() => {
    const byTime = new Map<string, Record<string, string | number>>();
    Object.entries(series).forEach(([svc, rows]) => {
      rows.forEach(row => {
        const key = row.time_bucket;
        if (!byTime.has(key)) byTime.set(key, { time_bucket: key });
        byTime.get(key)![svc || '(unknown)'] = row.value;
      });
    });
    return Array.from(byTime.values()).sort((a, b) => String(a.time_bucket).localeCompare(String(b.time_bucket)));
  }, [series]);

  const serviceKeys = Object.keys(series);
  const filteredNames = names.filter(n => n.name.toLowerCase().includes(search.toLowerCase()));

  // Snap the fetched deployments onto the chart's own bucket labels (E1).
  const deployMarkers = React.useMemo(() => {
    const buckets = chartData.map((r) => ({ label: String(r.time_bucket), ms: toEpochMs(String(r.time_bucket)) }));
    return snapDeploymentsToBuckets(deployments, buckets);
  }, [chartData, deployments]);

  // Unit shown on the Y-axis / tooltip. OTLP uses "1" for dimensionless; rate
  // turns any unit into a per-second rate.
  const unitLabel = (() => {
    const u = selected?.unit && selected.unit !== '1' ? selected.unit : '';
    if (fn === 'rate') return u ? `${u}/s` : 'per second';
    return u;
  })();

  return (
    <div style={{ display: 'flex', height: '100%', gap: '20px' }}>
      {/* Metric catalog sidebar */}
      <div style={{ width: '280px', flexShrink: 0, display: 'flex', flexDirection: 'column', borderRight: '1px solid ' + t.panelBorder, paddingRight: '16px' }}>
        <h3 style={{ fontSize: '17px', fontWeight: 700, margin: '0 0 4px', color: t.text1 }}>Metrics</h3>
        <p style={{ color: t.text2, fontSize: '12px', marginBottom: '14px', lineHeight: 1.5 }}>
          Native OTLP gauges &amp; counters, queried directly from ClickHouse.
        </p>
        <input
          placeholder="Filter metrics..."
          value={search}
          onChange={e => setSearch(e.target.value)}
          style={{
            padding: '8px 10px', marginBottom: '12px', fontSize: '12.5px',
            background: t.dark ? 'rgba(255,255,255,0.05)' : 'rgba(255,255,255,0.7)',
            border: '1px solid ' + t.panelBorder, borderRadius: '8px', color: t.text1,
          }}
        />
        <div style={{ flex: 1, overflowY: 'auto' }}>
          {namesLoading ? (
            <div style={{ color: t.text2, fontSize: '13px', padding: '12px 4px' }}>Loading metric catalog...</div>
          ) : namesError ? (
            <div style={{ color: t.red, fontSize: '12.5px', padding: '12px 4px' }}>{namesError}</div>
          ) : filteredNames.length === 0 ? (
            <div style={{ color: t.text2, fontSize: '13px', padding: '12px 4px', lineHeight: 1.6 }}>
              No metrics ingested in the last 24h yet. Point an OTLP metrics exporter or Prometheus-scraped service at the collector to populate this view.
            </div>
          ) : (
            filteredNames.map((n, i) => {
              const isActive = selected?.name === n.name && selected?.type === n.type && selected?.service === n.service;
              return (
                <div
                  key={`${n.name}-${n.type}-${n.service}-${i}`}
                  onClick={() => setSelected(n)}
                  style={{
                    padding: '9px 10px', borderRadius: '8px', cursor: 'pointer', marginBottom: '2px',
                    background: isActive ? t.accentSoft : 'transparent',
                    color: isActive ? t.text1 : t.text2,
                  }}
                >
                  <div style={{ fontSize: '12.5px', fontWeight: isActive ? 600 : 500, fontFamily: 'monospace' }}>{n.name}</div>
                  <div style={{ fontSize: '11px', color: t.text2, marginTop: '2px' }}>{n.service} · {n.type}{n.unit ? ` · ${n.unit}` : ''}</div>
                </div>
              );
            })
          )}
        </div>
      </div>

      {/* Chart */}
      <div style={{ flex: 1, display: 'flex', flexDirection: 'column', minWidth: 0 }}>
        {!selected ? (
          <div style={{ color: t.text2, fontSize: '14px', margin: 'auto' }}>Select a metric to view its time series.</div>
        ) : (
          <>
            <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'flex-start', marginBottom: '16px', gap: '16px', flexWrap: 'wrap' }}>
              <div>
                <h3 style={{ fontSize: '18px', fontWeight: 700, margin: '0 0 4px', color: t.text1, fontFamily: 'monospace' }}>{selected.name}</h3>
                {selected.description && <p style={{ color: t.text2, fontSize: '13px' }}>{selected.description}</p>}
              </div>
              <div style={{ display: 'flex', gap: '8px', alignItems: 'center' }}>
                <select
                  value={fn}
                  onChange={e => setFn(e.target.value)}
                  aria-label="Aggregation function"
                  title="Aggregation function applied per time bucket"
                  style={{ ...primaryBtnStyle, paddingRight: '8px' }}
                >
                  {FN_OPTIONS.map(opt => (
                    <option key={opt.value} value={opt.value}>{opt.label}</option>
                  ))}
                </select>
                {INTERVAL_OPTIONS.map(opt => (
                  <button
                    key={opt.value}
                    onClick={() => setInterval_(opt.value)}
                    style={{ ...primaryBtnStyle, background: interval === opt.value ? t.accentSoft : primaryBtnStyle.background, fontWeight: interval === opt.value ? 600 : 400 }}
                  >
                    {opt.label}
                  </button>
                ))}
              </div>
            </div>

            {/* Label dimensions (Metrics · E1 explorer) */}
            {labels.length > 0 && (() => {
              const grouped = labels.reduce<Record<string, string[]>>((acc, l) => {
                (acc[l.label_key] ||= []).push(l.label_value);
                return acc;
              }, {});
              return (
                <div style={{ display: 'flex', flexWrap: 'wrap', gap: '16px', marginBottom: '16px', padding: '12px 14px', borderRadius: '12px', background: t.dark ? 'rgba(255,255,255,0.03)' : 'rgba(0,0,0,0.02)', border: '1px solid ' + t.panelBorder }}>
                  <span style={{ fontSize: '11px', color: t.text2, textTransform: 'uppercase', fontWeight: 700, alignSelf: 'center' }}>Dimensions</span>
                  {Object.entries(grouped).map(([key, values]) => (
                    <div key={key} style={{ display: 'flex', alignItems: 'center', gap: '6px', flexWrap: 'wrap' }}>
                      <span style={{ fontSize: '12px', fontWeight: 600, color: t.text1, fontFamily: 'monospace' }}>{key}:</span>
                      {values.slice(0, 8).map((v) => (
                        <span key={v} title={`${key} = ${v}`} style={{ fontSize: '11.5px', color: t.text2, padding: '2px 8px', borderRadius: '100px', background: t.dark ? 'rgba(255,255,255,0.06)' : 'rgba(0,0,0,0.05)' }}>{v}</span>
                      ))}
                      {values.length > 8 && <span style={{ fontSize: '11px', color: t.text2 }}>+{values.length - 8}</span>}
                    </div>
                  ))}
                </div>
              );
            })()}

            {chartError && (
              <div style={{ padding: '16px', background: t.redSoft, color: t.red, borderRadius: '8px', marginBottom: '16px' }}>{chartError}</div>
            )}

            <div style={{ flex: 1, minHeight: '320px', background: t.dark ? 'rgba(0,0,0,0.15)' : 'rgba(0,0,0,0.02)', borderRadius: '12px', border: '1px solid ' + t.panelBorder, padding: '16px' }}>
              {chartLoading ? (
                <div style={{ color: t.text2, fontSize: '13px', display: 'flex', height: '100%', alignItems: 'center', justifyContent: 'center' }}>Loading...</div>
              ) : chartData.length === 0 ? (
                <div style={{ color: t.text2, fontSize: '13px', display: 'flex', height: '100%', alignItems: 'center', justifyContent: 'center' }}>No data points in this window.</div>
              ) : (
                <ResponsiveContainer width="100%" height="100%">
                  <LineChart data={chartData} margin={{ top: 5, right: 20, left: 0, bottom: 0 }}>
                    <CartesianGrid strokeDasharray="3 3" stroke={t.panelBorder} />
                    <XAxis dataKey="time_bucket" tick={{ fontSize: 11, fill: t.text2 }} minTickGap={40} />
                    <YAxis
                      tick={{ fontSize: 11, fill: t.text2 }}
                      width={54}
                      tickFormatter={formatMetricValue}
                      label={unitLabel ? { value: unitLabel, angle: -90, position: 'insideLeft', style: { fill: t.text2, fontSize: 11, textAnchor: 'middle' } } : undefined}
                    />
                    <Tooltip
                      contentStyle={{ background: t.panelBg, border: '1px solid ' + t.panelBorder, borderRadius: '8px', fontSize: '12px' }}
                      formatter={(value, name) => [`${formatMetricValue(Number(value))}${unitLabel ? ' ' + unitLabel : ''}`, name]}
                    />
                    <Legend wrapperStyle={{ fontSize: '12px' }} />
                    {deployMarkers.map((m, i) => (
                      <ReferenceLine
                        key={`deploy-${i}-${m.label}`}
                        x={m.label}
                        stroke={t.accent}
                        strokeDasharray="4 3"
                        strokeOpacity={0.7}
                        label={{ value: `⧗ ${m.version}`, position: 'top', fill: t.accent, fontSize: 10 }}
                      />
                    ))}
                    {serviceKeys.map((svc, i) => (
                      <Line key={svc} type="monotone" dataKey={svc} name={svc} stroke={SERIES_COLORS[i % SERIES_COLORS.length]} strokeWidth={2} dot={false} />
                    ))}
                  </LineChart>
                </ResponsiveContainer>
              )}
            </div>
          </>
        )}
      </div>
    </div>
  );
}
