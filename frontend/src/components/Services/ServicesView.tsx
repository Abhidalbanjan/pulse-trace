"use client";

import React, { useState, useEffect, useCallback } from 'react';
import { useRouter } from 'next/navigation';
import { fetchWithAuth } from '@/lib/api';
import { useTheme } from '@/context/ThemeContext';

interface ServiceRow {
  service: string;
  requests: number;
  errors: number;
  p50_ms: number;
  p90_ms: number;
  p99_ms: number;
  health_score?: number;
  health_band?: string;
}

function errorRate(row: ServiceRow): number {
  return row.requests > 0 ? (row.errors / row.requests) * 100 : 0;
}

export function ServicesView() {
  const router = useRouter();
  const { tokens: t } = useTheme();
  const [services, setServices] = useState<ServiceRow[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  // Sort by health (Services · E2): null = server default (by request volume),
  // else surface worst/best services first.
  const [sortHealth, setSortHealth] = useState<'asc' | 'desc' | null>(null);

  const healthColor = useCallback((row: ServiceRow): string => {
    const rate = errorRate(row);
    if (rate > 5) return t.red;
    if (rate > 0) return t.amber;
    return t.green;
  }, [t]);

  const bandColor = useCallback((band?: string): string => {
    if (band === 'healthy') return t.green;
    if (band === 'degraded') return t.amber;
    return t.red; // unhealthy | critical
  }, [t]);

  const fetchServices = useCallback(() => {
    fetchWithAuth('/api/v1/services')
      .then(async res => {
        if (!res.ok) throw new Error(await res.text());
        return res.json();
      })
      .then(json => {
        setServices(json.data || []);
        setError(null);
      })
      .catch(err => setError(err.message || 'Failed to load services'))
      .finally(() => setLoading(false));
  }, []);

  useEffect(() => {
    fetchServices();
    const interval = setInterval(fetchServices, 15000);
    return () => clearInterval(interval);
  }, [fetchServices]);

  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: '16px', height: '100%' }}>
      <div>
        <h2 style={{ fontSize: '26px', fontWeight: 700, margin: '0 0 8px', color: t.text1 }}>Services</h2>
        <p style={{ color: t.text2, fontSize: '14.5px' }}>Request rate, error rate, and latency for every service reporting traces (last 15 minutes).</p>
      </div>

      <div style={{
        flex: 1,
        overflow: 'auto',
        borderRadius: '20px',
        background: t.panelBg,
        border: '1px solid ' + t.panelBorder,
        backdropFilter: 'blur(30px) saturate(180%)',
        WebkitBackdropFilter: 'blur(30px) saturate(180%)',
        boxShadow: t.shadow,
      }}>
        {loading ? (
          <div style={{ padding: '48px', textAlign: 'center', color: t.text2 }}>Loading services...</div>
        ) : error ? (
          <div style={{ padding: '48px', textAlign: 'center', color: t.red }}>{error}</div>
        ) : services.length === 0 ? (
          <div style={{ padding: '48px', textAlign: 'center', color: t.text2 }}>No services reporting traces yet.</div>
        ) : (
          <table style={{ width: '100%', borderCollapse: 'collapse', textAlign: 'left' }}>
            <thead>
              <tr style={{ borderBottom: '1px solid ' + t.panelBorder, background: t.dark ? 'rgba(0,0,0,0.15)' : 'rgba(0,0,0,0.03)' }}>
                <th style={{ padding: '16px', fontWeight: 600, color: t.text2, fontSize: '12.5px' }}>Service</th>
                <th
                  onClick={() => setSortHealth(s => (s === 'asc' ? 'desc' : s === 'desc' ? null : 'asc'))}
                  style={{ padding: '16px', fontWeight: 600, color: sortHealth ? t.text1 : t.text2, fontSize: '12.5px', cursor: 'pointer', userSelect: 'none' }}
                  title="Sort by health score"
                >
                  Health {sortHealth === 'asc' ? '▲' : sortHealth === 'desc' ? '▼' : ''}
                </th>
                <th style={{ padding: '16px', fontWeight: 600, color: t.text2, fontSize: '12.5px' }}>Requests</th>
                <th style={{ padding: '16px', fontWeight: 600, color: t.text2, fontSize: '12.5px' }}>Error Rate</th>
                <th style={{ padding: '16px', fontWeight: 600, color: t.text2, fontSize: '12.5px' }}>p50</th>
                <th style={{ padding: '16px', fontWeight: 600, color: t.text2, fontSize: '12.5px' }}>p90</th>
                <th style={{ padding: '16px', fontWeight: 600, color: t.text2, fontSize: '12.5px' }}>p99</th>
              </tr>
            </thead>
            <tbody>
              {(sortHealth
                ? [...services].sort((a, b) => {
                    const av = a.health_score ?? 101, bv = b.health_score ?? 101;
                    return sortHealth === 'asc' ? av - bv : bv - av;
                  })
                : services
              ).map(row => (
                <tr key={row.service}
                    onClick={() => router.push(`/services/${encodeURIComponent(row.service)}`)}
                    style={{ borderBottom: '1px solid ' + t.panelBorder, cursor: 'pointer' }}
                    onMouseEnter={(e) => (e.currentTarget.style.background = t.dark ? 'rgba(255,255,255,0.04)' : 'rgba(0,0,0,0.02)')}
                    onMouseLeave={(e) => (e.currentTarget.style.background = 'transparent')}>
                  <td style={{ padding: '15px 16px', fontSize: '13.5px', fontWeight: 500, color: t.text1 }}>
                    <span style={{ display: 'inline-block', width: 8, height: 8, borderRadius: '50%', background: healthColor(row), marginRight: 10 }} />
                    {row.service}
                  </td>
                  <td style={{ padding: '15px 16px' }}>
                    {typeof row.health_score === 'number' ? (
                      <span style={{ display: 'inline-flex', alignItems: 'center', gap: '7px' }}>
                        <span style={{ fontSize: '13.5px', fontWeight: 700, color: bandColor(row.health_band) }}>{row.health_score}</span>
                        <span style={{ fontSize: '11px', padding: '2px 8px', borderRadius: '100px', border: '1px solid ' + bandColor(row.health_band), color: bandColor(row.health_band), textTransform: 'capitalize' }}>{row.health_band}</span>
                      </span>
                    ) : <span style={{ color: t.text2 }}>—</span>}
                  </td>
                  <td style={{ padding: '15px 16px', fontSize: '13.5px', color: t.text1 }}>{row.requests.toLocaleString()}</td>
                  <td style={{ padding: '15px 16px', fontSize: '13.5px', color: healthColor(row), fontWeight: 600 }}>{errorRate(row).toFixed(2)}%</td>
                  <td style={{ padding: '15px 16px', fontSize: '13.5px', color: t.text1 }}>{Math.round(row.p50_ms)}ms</td>
                  <td style={{ padding: '15px 16px', fontSize: '13.5px', color: t.text1 }}>{Math.round(row.p90_ms)}ms</td>
                  <td style={{ padding: '15px 16px', fontSize: '13.5px', color: t.text1 }}>{Math.round(row.p99_ms)}ms</td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>
    </div>
  );
}
