"use client";

import React, { useState, useEffect, useCallback } from 'react';
import { fetchWithAuth } from '@/lib/api';
import { LineChart, Line, XAxis, YAxis, CartesianGrid, Tooltip, Legend, ResponsiveContainer, AreaChart, Area } from 'recharts';
import { useTheme } from '@/context/ThemeContext';

const INTERVAL_OPTIONS: { value: string; label: string }[] = [
  { value: '1h', label: 'Last 1 Hour' },
  { value: '24h', label: 'Last 24 Hours' },
  { value: '7d', label: 'Last 7 Days' },
];

export function TraceAnalyticsView() {
  const { tokens: t } = useTheme();
  const [data, setData] = useState<any[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  const [interval, setInterval_] = useState('1h');
  const [serviceFacets, setServiceFacets] = useState<string[]>([]);
  const [routeFacets, setRouteFacets] = useState<string[]>([]);
  const [selectedServices, setSelectedServices] = useState<Set<string>>(new Set());
  const [selectedRoutes, setSelectedRoutes] = useState<Set<string>>(new Set());

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

    fetchWithAuth(`/api/v1/analytics/traces?${params.toString()}`)
      .then(async res => {
        if (!res.ok) throw new Error(await res.text());
        return res.json();
      })
      .then(jsonData => {
        // ClickHouse JSON format returns rows in .data
        if (jsonData && jsonData.data) {
          const formatted = jsonData.data.map((row: any) => ({
            time: new Date(row.time_bucket).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' }),
            p50: Math.round(row.p50_ms),
            p90: Math.round(row.p90_ms),
            p99: Math.round(row.p99_ms),
            count: parseInt(row.total_traces, 10)
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
    fetchAnalytics();
  }, [fetchAnalytics]);

  const toggleFacet = (set: Set<string>, setSet: (s: Set<string>) => void, value: string) => {
    const next = new Set(set);
    if (next.has(value)) next.delete(value);
    else next.add(value);
    setSet(next);
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
        <div style={{ display: 'flex', gap: '12px' }}>
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
