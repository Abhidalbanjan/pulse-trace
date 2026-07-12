"use client";

import React, { useState, useEffect } from 'react';
import { fetchWithAuth } from '@/lib/api';
import { LineChart, Line, XAxis, YAxis, CartesianGrid, Tooltip, Legend, ResponsiveContainer, AreaChart, Area } from 'recharts';

export function TraceAnalyticsView() {
  const [data, setData] = useState<any[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    fetchWithAuth('/api/v1/analytics/traces')
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
        }
        setLoading(false);
      })
      .catch(err => {
        setError(err.message || err.toString());
        setLoading(false);
      });
  }, []);

  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: '24px', height: '100%', overflow: 'auto' }}>
      
      {/* Header */}
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center' }}>
        <div>
          <h2 style={{ fontSize: '24px', fontWeight: 600, marginBottom: '8px' }}>Trace Analytics</h2>
          <p style={{ color: 'var(--text-secondary)' }}>Analyze application performance over time using high-cardinality slicing.</p>
        </div>
        <div style={{ display: 'flex', gap: '12px' }}>
          <select className="input-field" style={{ padding: '8px 12px' }}>
            <option>Last 1 Hour</option>
            <option>Last 24 Hours</option>
            <option>Last 7 Days</option>
          </select>
          <button className="btn-primary">Query Analytics</button>
        </div>
      </div>

      {error && (
        <div style={{ padding: '16px', background: 'rgba(255, 60, 60, 0.1)', color: 'var(--status-red)', borderRadius: '8px', border: '1px solid rgba(255, 60, 60, 0.3)' }}>
          <strong>Analytics Error:</strong> {error}
        </div>
      )}

      {/* Main Charts Area */}
      <div style={{ display: 'flex', gap: '24px', flex: 1, minHeight: '400px' }}>
        
        {/* Facet Sidebar (Simulated) */}
        <div className="glass-panel" style={{ width: '280px', padding: '24px', display: 'flex', flexDirection: 'column', gap: '24px' }}>
          <h3 style={{ fontSize: '14px', textTransform: 'uppercase', color: 'var(--text-secondary)', letterSpacing: '0.05em', fontWeight: 700 }}>Group By (Facets)</h3>
          
          <div>
            <div style={{ fontSize: '13px', fontWeight: 600, marginBottom: '12px' }}>Service Name</div>
            {['frontend', 'checkout-service', 'inventory-service'].map(svc => (
              <label key={svc} style={{ display: 'flex', alignItems: 'center', gap: '8px', marginBottom: '8px', fontSize: '13px', cursor: 'pointer' }}>
                <input type="checkbox" /> {svc}
              </label>
            ))}
          </div>

          <div>
            <div style={{ fontSize: '13px', fontWeight: 600, marginBottom: '12px' }}>HTTP Route</div>
            {['/checkout', '/cart', '/api/products'].map(route => (
              <label key={route} style={{ display: 'flex', alignItems: 'center', gap: '8px', marginBottom: '8px', fontSize: '13px', cursor: 'pointer' }}>
                <input type="checkbox" /> {route}
              </label>
            ))}
          </div>
        </div>

        {/* Charts */}
        <div style={{ flex: 1, display: 'flex', flexDirection: 'column', gap: '24px' }}>
          
          {/* Latency Percentiles */}
          <div className="glass-panel" style={{ flex: 1, padding: '24px', display: 'flex', flexDirection: 'column' }}>
            <h3 style={{ fontSize: '16px', fontWeight: 600, marginBottom: '16px' }}>Latency Percentiles (p50, p90, p99)</h3>
            <div style={{ flex: 1, minHeight: 0 }}>
              {loading ? (
                <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'center', height: '100%', color: 'var(--text-secondary)' }}>Loading analytical data from ClickHouse...</div>
              ) : (
                <ResponsiveContainer width="100%" height="100%">
                  <LineChart data={data} margin={{ top: 10, right: 30, left: 0, bottom: 0 }}>
                    <CartesianGrid strokeDasharray="3 3" stroke="rgba(255,255,255,0.1)" />
                    <XAxis dataKey="time" stroke="var(--text-secondary)" tick={{ fill: 'var(--text-secondary)', fontSize: 12 }} />
                    <YAxis stroke="var(--text-secondary)" tick={{ fill: 'var(--text-secondary)', fontSize: 12 }} unit="ms" />
                    <Tooltip 
                      contentStyle={{ backgroundColor: 'rgba(0,0,0,0.9)', border: '1px solid var(--border-color)', borderRadius: '8px' }}
                      itemStyle={{ fontSize: '13px' }}
                    />
                    <Legend />
                    <Line type="monotone" dataKey="p99" name="p99 Latency" stroke="#ff4d4f" strokeWidth={2} dot={false} />
                    <Line type="monotone" dataKey="p90" name="p90 Latency" stroke="#faad14" strokeWidth={2} dot={false} />
                    <Line type="monotone" dataKey="p50" name="p50 Latency" stroke="#52c41a" strokeWidth={2} dot={false} />
                  </LineChart>
                </ResponsiveContainer>
              )}
            </div>
          </div>

          {/* Trace Volume */}
          <div className="glass-panel" style={{ flex: 1, padding: '24px', display: 'flex', flexDirection: 'column' }}>
            <h3 style={{ fontSize: '16px', fontWeight: 600, marginBottom: '16px' }}>Total Trace Volume</h3>
            <div style={{ flex: 1, minHeight: 0 }}>
              {loading ? (
                <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'center', height: '100%', color: 'var(--text-secondary)' }}>Loading volume data...</div>
              ) : (
                <ResponsiveContainer width="100%" height="100%">
                  <AreaChart data={data} margin={{ top: 10, right: 30, left: 0, bottom: 0 }}>
                    <CartesianGrid strokeDasharray="3 3" stroke="rgba(255,255,255,0.1)" />
                    <XAxis dataKey="time" stroke="var(--text-secondary)" tick={{ fill: 'var(--text-secondary)', fontSize: 12 }} />
                    <YAxis stroke="var(--text-secondary)" tick={{ fill: 'var(--text-secondary)', fontSize: 12 }} />
                    <Tooltip 
                      contentStyle={{ backgroundColor: 'rgba(0,0,0,0.9)', border: '1px solid var(--border-color)', borderRadius: '8px' }}
                      itemStyle={{ fontSize: '13px' }}
                    />
                    <Area type="step" dataKey="count" name="Trace Count" stroke="var(--accent-blue)" fill="var(--accent-blue)" fillOpacity={0.3} />
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
