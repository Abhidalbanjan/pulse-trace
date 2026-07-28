"use client";

import React, { useState, useEffect } from 'react';
import { useRouter } from 'next/navigation';
import { fetchWithAuth } from '@/lib/api';
import { useTheme } from '@/context/ThemeContext';

export function RUMView() {
  const router = useRouter();
  const { tokens: t } = useTheme();
  const [metrics, setMetrics] = useState<any[]>([]);
  const [errors, setErrors] = useState<any[]>([]);
  const [loading, setLoading] = useState(true);

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
  }, []);

  // Core Web Vitals are rated at the 75th percentile (Google's methodology), so
  // read p75_value — the number the good/needs-improvement/poor thresholds below
  // are actually defined against. Falls back to avg_value for older API responses.
  const getMetricP75 = (name: string) => {
    const m = metrics.find(x => x.MetricName === name);
    if (!m) return 0;
    const v = m.p75_value ?? m.avg_value;
    return name === 'CLS' ? Number(v) : Math.round(v);
  };

  const getMetricCount = (type: string) => {
    return metrics.filter(x => x.Type === type).reduce((acc, curr) => acc + parseInt(curr.count, 10), 0);
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
            style={{
              background: t.panelBg,
              border: '1px solid ' + t.panelBorder,
              color: t.text1,
              padding: '9px 14px',
              borderRadius: '10px',
              fontSize: '13px',
            }}
          >
            <option>Last 24 Hours</option>
            <option>Last 7 Days</option>
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
              <h3 style={{ fontSize: '16px', fontWeight: 700, margin: '0 0 20px' }}>User Sessions</h3>

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
                          {new Date(err.timestamp).toLocaleString()}
                        </td>
                        <td style={{ padding: '16px', fontSize: '13px', fontFamily: 'monospace' }}>
                          {err.path}
                        </td>
                        <td style={{ padding: '16px', color: t.red, fontWeight: 500, fontSize: '13px' }}>
                          {err.error_msg}
                        </td>
                        <td style={{ padding: '16px', fontSize: '12px', color: t.text2 }}>
                          {err.user_agent?.length > 40 ? err.user_agent.substring(0, 40) + '...' : err.user_agent}
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
