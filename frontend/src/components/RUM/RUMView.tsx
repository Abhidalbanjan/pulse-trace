"use client";

import React, { useState, useEffect } from 'react';
import { fetchWithAuth } from '@/lib/api';

export function RUMView() {
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

  const getMetricAvg = (name: string) => {
    const m = metrics.find(x => x.MetricName === name);
    return m ? Math.round(m.avg_value) : 0;
  };

  const getMetricCount = (type: string) => {
    return metrics.filter(x => x.Type === type).reduce((acc, curr) => acc + parseInt(curr.count, 10), 0);
  };

  const lcp = getMetricAvg('LCP');
  const cls = getMetricAvg('CLS');
  const pageViews = getMetricCount('page_view');
  
  // Format based on Datadog thresholds
  const lcpColor = lcp === 0 ? 'var(--text-secondary)' : lcp < 2500 ? 'var(--status-green)' : lcp < 4000 ? 'var(--status-yellow)' : 'var(--status-red)';
  const clsColor = cls === 0 ? 'var(--text-secondary)' : cls < 0.1 ? 'var(--status-green)' : cls < 0.25 ? 'var(--status-yellow)' : 'var(--status-red)';

  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: '24px', height: '100%', overflow: 'auto' }}>
      
      {/* Header */}
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center' }}>
        <div>
          <h2 style={{ fontSize: '24px', fontWeight: 600, marginBottom: '8px' }}>Real User Monitoring</h2>
          <p style={{ color: 'var(--text-secondary)' }}>End-to-end visibility into user journeys, web vitals, and frontend errors.</p>
        </div>
        <div style={{ display: 'flex', gap: '12px' }}>
          <select className="input-field" style={{ padding: '8px 12px' }}>
            <option>Last 24 Hours</option>
            <option>Last 7 Days</option>
          </select>
        </div>
      </div>

      {loading ? (
        <div style={{ padding: '48px', textAlign: 'center', color: 'var(--text-secondary)' }}>Loading Real User Monitoring data...</div>
      ) : (
        <div style={{ display: 'flex', gap: '24px', flexWrap: 'wrap' }}>
          
          {/* Web Vitals Overview */}
          <div className="glass-panel" style={{ flex: '1 1 300px', padding: '24px' }}>
            <h3 style={{ fontSize: '16px', fontWeight: 600, marginBottom: '24px' }}>Core Web Vitals</h3>
            
            <div style={{ display: 'flex', flexDirection: 'column', gap: '24px' }}>
              <div>
                <div style={{ fontSize: '13px', color: 'var(--text-secondary)', marginBottom: '4px' }}>Largest Contentful Paint (LCP)</div>
                <div style={{ fontSize: '32px', fontWeight: 700, color: lcpColor }}>
                  {lcp === 0 ? '--' : `${(lcp / 1000).toFixed(2)}s`}
                </div>
                <div style={{ fontSize: '12px', color: 'var(--text-secondary)', marginTop: '4px' }}>Target: &lt; 2.5s</div>
              </div>

              <div>
                <div style={{ fontSize: '13px', color: 'var(--text-secondary)', marginBottom: '4px' }}>Cumulative Layout Shift (CLS)</div>
                <div style={{ fontSize: '32px', fontWeight: 700, color: clsColor }}>
                  {cls === 0 ? '--' : cls.toFixed(3)}
                </div>
                <div style={{ fontSize: '12px', color: 'var(--text-secondary)', marginTop: '4px' }}>Target: &lt; 0.1</div>
              </div>
            </div>
          </div>

          {/* Session Funnel */}
          <div className="glass-panel" style={{ flex: '1 1 300px', padding: '24px' }}>
            <h3 style={{ fontSize: '16px', fontWeight: 600, marginBottom: '24px' }}>User Sessions</h3>
            
            <div style={{ display: 'flex', flexDirection: 'column', gap: '16px' }}>
              <div style={{ display: 'flex', justifyContent: 'space-between', paddingBottom: '16px', borderBottom: '1px solid var(--border-color)' }}>
                <span style={{ color: 'var(--text-secondary)' }}>Total Page Views</span>
                <span style={{ fontWeight: 600, fontSize: '20px' }}>{pageViews}</span>
              </div>
              <div style={{ display: 'flex', justifyContent: 'space-between', paddingBottom: '16px', borderBottom: '1px solid var(--border-color)' }}>
                <span style={{ color: 'var(--text-secondary)' }}>Sessions with Errors</span>
                <span style={{ fontWeight: 600, fontSize: '20px', color: errors.length > 0 ? 'var(--status-red)' : 'var(--status-green)' }}>
                  {errors.length > 0 ? errors.length : 0}
                </span>
              </div>
            </div>
          </div>

          {/* Recent Frontend Errors */}
          <div className="glass-panel" style={{ flex: '1 1 100%', padding: '0', display: 'flex', flexDirection: 'column' }}>
            <div style={{ padding: '24px', borderBottom: '1px solid var(--border-color)' }}>
              <h3 style={{ fontSize: '16px', fontWeight: 600 }}>Recent Javascript Errors</h3>
            </div>
            
            <div style={{ overflowX: 'auto' }}>
              {errors.length === 0 ? (
                <div style={{ padding: '48px', textAlign: 'center', color: 'var(--text-secondary)' }}>No frontend Javascript errors detected.</div>
              ) : (
                <table style={{ width: '100%', borderCollapse: 'collapse', textAlign: 'left' }}>
                  <thead>
                    <tr style={{ background: 'rgba(0,0,0,0.2)' }}>
                      <th style={{ padding: '16px', fontWeight: 500, color: 'var(--text-secondary)', fontSize: '13px' }}>Timestamp</th>
                      <th style={{ padding: '16px', fontWeight: 500, color: 'var(--text-secondary)', fontSize: '13px' }}>Path</th>
                      <th style={{ padding: '16px', fontWeight: 500, color: 'var(--text-secondary)', fontSize: '13px' }}>Error Message</th>
                      <th style={{ padding: '16px', fontWeight: 500, color: 'var(--text-secondary)', fontSize: '13px' }}>User Agent</th>
                    </tr>
                  </thead>
                  <tbody>
                    {errors.map((err, i) => (
                      <tr key={i} style={{ borderTop: '1px solid var(--border-color)' }}>
                        <td style={{ padding: '16px', fontSize: '13px', color: 'var(--text-secondary)', whiteSpace: 'nowrap' }}>
                          {new Date(err.timestamp).toLocaleString()}
                        </td>
                        <td style={{ padding: '16px', fontSize: '13px', fontFamily: 'monospace' }}>
                          {err.path}
                        </td>
                        <td style={{ padding: '16px', color: 'var(--status-red)', fontWeight: 500, fontSize: '13px' }}>
                          {err.error_msg}
                        </td>
                        <td style={{ padding: '16px', fontSize: '12px', color: 'var(--text-secondary)' }}>
                          {err.user_agent?.length > 40 ? err.user_agent.substring(0, 40) + '...' : err.user_agent}
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
