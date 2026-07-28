"use client";

import React, { useState, useEffect } from 'react';
import { fetchWithAuth } from '@/lib/api';
import { useTheme } from '@/context/ThemeContext';

export function SyntheticsView() {
  const { tokens: t } = useTheme();
  const [results, setResults] = useState<any[]>([]);
  const [loading, setLoading] = useState(true);
  const [showModal, setShowModal] = useState(false);
  const [newUrl, setNewUrl] = useState('');

  const fetchResults = () => {
    fetchWithAuth('/api/v1/synthetics/results')
      .then(res => res.json())
      .then(data => {
        if (data && data.data) {
          setResults(data.data);
        }
      })
      .catch(console.error)
      .finally(() => setLoading(false));
  };

  useEffect(() => {
    fetchResults();
  }, []);

  const handleCreate = async (e: React.FormEvent) => {
    e.preventDefault();
    try {
      const res = await fetchWithAuth('/api/v1/synthetics/tests', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ url: newUrl })
      });
      if (!res.ok) throw new Error(await res.text());
      setShowModal(false);
      setNewUrl('');
      // Optimistically wait a moment then refresh
      setTimeout(fetchResults, 2000);
    } catch (err: any) {
      alert(`Failed to create test: ${err.message}`);
    }
  };

  const handleDelete = async (url: string) => {
    if (!confirm(`Stop monitoring ${url}?`)) return;
    try {
      const res = await fetchWithAuth(`/api/v1/synthetics/tests?url=${encodeURIComponent(url)}`, {
        method: 'DELETE',
      });
      if (!res.ok) throw new Error(await res.text());
      setResults((prev) => prev.filter((r) => r.URL !== url));
    } catch (err: any) {
      alert(`Failed to delete test: ${err.message}`);
    }
  };

  const totalTests = results.length;
  const failingTests = results.filter(r => r.uptime_percent < 100).length;
  const globalUptime = totalTests > 0
    ? (results.reduce((acc, r) => acc + parseFloat(r.uptime_percent), 0) / totalTests).toFixed(2)
    : 0;

  const primaryButtonStyle: React.CSSProperties = {
    padding: '11px 22px',
    borderRadius: '11px',
    border: 'none',
    background: `linear-gradient(135deg, ${t.accent}, ${t.accent2})`,
    color: '#fff',
    fontWeight: 600,
    fontSize: '13.5px',
    cursor: 'pointer',
  };

  const secondaryButtonStyle: React.CSSProperties = {
    padding: '11px 22px',
    borderRadius: '11px',
    border: '1px solid ' + t.panelBorder,
    background: 'transparent',
    color: t.text2,
    fontWeight: 600,
    fontSize: '13.5px',
    cursor: 'pointer',
  };

  const kpiTileStyle: React.CSSProperties = {
    flex: 1,
    padding: '22px',
    borderRadius: '18px',
    background: t.panelBg,
    border: '1px solid ' + t.panelBorder,
    backdropFilter: 'blur(30px) saturate(180%)',
    boxShadow: t.shadow,
  };

  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: '18px', height: '100%', overflow: 'auto' }}>

      {/* Header */}
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: '20px' }}>
        <div>
          <h2 style={{ fontSize: '26px', fontWeight: 700, margin: '0 0 8px' }}>Synthetic Monitoring</h2>
          <p style={{ color: t.text2, fontSize: '14.5px' }}>Proactively monitor API uptime and latency from global locations.</p>
        </div>
        <button onClick={() => setShowModal(true)} style={primaryButtonStyle}>Create Test</button>
      </div>

      {showModal && (
        <div
          style={{
            position: 'fixed',
            top: 0,
            left: 0,
            right: 0,
            bottom: 0,
            background: 'rgba(0,0,0,0.5)',
            backdropFilter: 'blur(6px)',
            zIndex: 1000,
            display: 'flex',
            alignItems: 'center',
            justifyContent: 'center',
          }}
        >
          <div
            style={{
              background: t.panelBg,
              border: '1px solid ' + t.panelBorder,
              backdropFilter: 'blur(30px) saturate(180%)',
              boxShadow: t.shadow,
              padding: '32px',
              borderRadius: '20px',
              width: '400px',
              color: t.text1,
            }}
          >
            <h3 style={{ fontSize: '20px', fontWeight: 700, margin: '0 0 24px' }}>Create Synthetic Test</h3>
            <form onSubmit={handleCreate} style={{ display: 'flex', flexDirection: 'column', gap: '16px' }}>
              <div>
                <label style={{ display: 'block', fontSize: '13px', color: t.text2, marginBottom: '8px' }}>Endpoint URL</label>
                <input
                  type="url"
                  placeholder="http://gateway:8080/health"
                  required
                  value={newUrl}
                  onChange={e => setNewUrl(e.target.value)}
                  style={{
                    width: '100%',
                    padding: '12px',
                    background: t.dark ? 'rgba(255,255,255,0.05)' : 'rgba(0,0,0,0.03)',
                    border: '1px solid ' + t.panelBorder,
                    borderRadius: '10px',
                    color: t.text1,
                    fontSize: '13.5px',
                    boxSizing: 'border-box',
                  }}
                />
              </div>
              <div style={{ display: 'flex', gap: '12px', marginTop: '16px' }}>
                <button type="button" onClick={() => setShowModal(false)} style={{ ...secondaryButtonStyle, flex: 1 }}>Cancel</button>
                <button type="submit" style={{ ...primaryButtonStyle, flex: 1 }}>Start Monitoring</button>
              </div>
            </form>
          </div>
        </div>
      )}

      {loading ? (
        <div style={{ padding: '48px', textAlign: 'center', color: t.text2 }}>Loading synthetics data...</div>
      ) : (
        <div style={{ display: 'flex', flexDirection: 'column', gap: '18px' }}>

          {/* KPI Row */}
          <div style={{ display: 'flex', gap: '16px', marginBottom: '18px' }}>
            <div style={kpiTileStyle}>
              <div style={{ fontSize: '13px', color: t.text2, marginBottom: '8px' }}>Global Uptime</div>
              <div style={{ fontSize: '30px', fontWeight: 700, color: t.green }}>
                {globalUptime}%
              </div>
            </div>
            <div style={kpiTileStyle}>
              <div style={{ fontSize: '13px', color: t.text2, marginBottom: '8px' }}>Active Tests</div>
              <div style={{ fontSize: '30px', fontWeight: 700, color: t.text1 }}>
                {totalTests}
              </div>
            </div>
            <div style={kpiTileStyle}>
              <div style={{ fontSize: '13px', color: t.text2, marginBottom: '8px' }}>Failing Tests</div>
              <div style={{ fontSize: '30px', fontWeight: 700, color: t.red }}>
                {failingTests}
              </div>
            </div>
          </div>

          {/* Table */}
          <div
            style={{
              borderRadius: '20px',
              background: t.panelBg,
              border: '1px solid ' + t.panelBorder,
              backdropFilter: 'blur(30px) saturate(180%)',
              boxShadow: t.shadow,
              overflow: 'hidden',
            }}
          >
            <table style={{ width: '100%', borderCollapse: 'collapse', textAlign: 'left' }}>
              <thead>
                <tr style={{ borderBottom: '1px solid ' + t.panelBorder, background: t.dark ? 'rgba(0,0,0,0.15)' : 'rgba(0,0,0,0.03)' }}>
                  <th style={{ padding: '16px', fontWeight: 600, color: t.text2, fontSize: '12.5px' }}>Status</th>
                  <th style={{ padding: '16px', fontWeight: 600, color: t.text2, fontSize: '12.5px' }}>Endpoint URL</th>
                  <th style={{ padding: '16px', fontWeight: 600, color: t.text2, fontSize: '12.5px' }}>Avg Latency</th>
                  <th style={{ padding: '16px', fontWeight: 600, color: t.text2, fontSize: '12.5px' }}>Uptime (1h)</th>
                  <th style={{ padding: '16px', fontWeight: 600, color: t.text2, fontSize: '12.5px', width: '60px' }}></th>
                </tr>
              </thead>
              <tbody>
                {results.length === 0 ? (
                  <tr>
                    <td colSpan={5} style={{ padding: '48px', textAlign: 'center', color: t.text2 }}>No synthetic tests have run yet.</td>
                  </tr>
                ) : (
                  results.map((r, i) => (
                    <tr key={i} style={{ borderBottom: '1px solid ' + t.panelBorder }}>
                      <td style={{ padding: '16px' }}>
                        <div
                          style={{
                            width: '10px',
                            height: '10px',
                            borderRadius: '50%',
                            background: parseFloat(r.uptime_percent) === 100 ? t.green : t.red,
                          }}
                        />
                      </td>
                      <td style={{ padding: '16px', fontSize: '13.5px', fontFamily: 'monospace', color: t.text1 }}>
                        {r.URL}
                      </td>
                      <td style={{ padding: '16px', fontWeight: 600, fontSize: '13.5px', color: t.text1 }}>
                        {Math.round(r.avg_latency_ms)} ms
                      </td>
                      <td style={{ padding: '16px', fontSize: '13.5px', color: parseFloat(r.uptime_percent) >= 99.9 ? t.green : t.red }}>
                        {parseFloat(r.uptime_percent).toFixed(2)}%
                      </td>
                      <td style={{ padding: '16px', textAlign: 'right' }}>
                        <button
                          onClick={() => handleDelete(r.URL)}
                          title="Stop monitoring this endpoint"
                          style={{ background: 'transparent', border: 'none', color: t.text2, cursor: 'pointer', fontSize: '16px', lineHeight: 1, padding: '2px 6px' }}
                        >
                          ✕
                        </button>
                      </td>
                    </tr>
                  ))
                )}
              </tbody>
            </table>
          </div>

        </div>
      )}

    </div>
  );
}
