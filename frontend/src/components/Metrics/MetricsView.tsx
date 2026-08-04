"use client";

import React, { useState, useEffect, useCallback } from 'react';
import { fetchWithAuth } from '@/lib/api';
import { LineChart, Line, XAxis, YAxis, CartesianGrid, Tooltip, Legend, ResponsiveContainer } from 'recharts';
import { useTheme } from '@/context/ThemeContext';

const INTERVAL_OPTIONS = [
  { value: '1h', label: 'Last 1 Hour' },
  { value: '24h', label: 'Last 24 Hours' },
  { value: '7d', label: 'Last 7 Days' },
];

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

export function MetricsView() {
  const { tokens: t } = useTheme();
  const [names, setNames] = useState<MetricName[]>([]);
  const [namesLoading, setNamesLoading] = useState(true);
  const [namesError, setNamesError] = useState<string | null>(null);
  const [selected, setSelected] = useState<MetricName | null>(null);
  const [interval, setInterval_] = useState('1h');
  const [series, setSeries] = useState<Record<string, MetricRow[]>>({});
  const [chartLoading, setChartLoading] = useState(false);
  const [chartError, setChartError] = useState<string | null>(null);
  const [search, setSearch] = useState('');

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
    const params = new URLSearchParams({ metric: selected.name, type: selected.type, interval });
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
  }, [selected, interval]);

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
              <div style={{ display: 'flex', gap: '8px' }}>
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
                    <YAxis tick={{ fontSize: 11, fill: t.text2 }} />
                    <Tooltip contentStyle={{ background: t.panelBg, border: '1px solid ' + t.panelBorder, borderRadius: '8px', fontSize: '12px' }} />
                    <Legend wrapperStyle={{ fontSize: '12px' }} />
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
