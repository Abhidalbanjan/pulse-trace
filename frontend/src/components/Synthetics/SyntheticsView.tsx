"use client";

import React, { useState, useEffect } from 'react';
import { fetchWithAuth } from '@/lib/api';

export function SyntheticsView() {
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

  const totalTests = results.length;
  const failingTests = results.filter(r => r.uptime_percent < 100).length;
  const globalUptime = totalTests > 0 
    ? (results.reduce((acc, r) => acc + parseFloat(r.uptime_percent), 0) / totalTests).toFixed(2)
    : 0;

  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: '24px', height: '100%', overflow: 'auto' }}>
      
      {/* Header */}
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center' }}>
        <div>
          <h2 style={{ fontSize: '24px', fontWeight: 600, marginBottom: '8px' }}>Synthetic Monitoring</h2>
          <p style={{ color: 'var(--text-secondary)' }}>Proactively monitor API uptime and latency from global locations.</p>
        </div>
        <div style={{ display: 'flex', gap: '12px' }}>
          <button onClick={() => setShowModal(true)} className="btn-primary" style={{ padding: '10px 20px' }}>Create Test</button>
        </div>
      </div>

      {showModal && (
        <div style={{ position: 'fixed', top: 0, left: 0, right: 0, bottom: 0, background: 'rgba(0,0,0,0.8)', zIndex: 1000, display: 'flex', alignItems: 'center', justifyContent: 'center' }}>
          <div style={{ background: '#1e1e1e', padding: '32px', borderRadius: '16px', width: '400px', border: '1px solid var(--border-color)' }}>
            <h3 style={{ fontSize: '20px', fontWeight: 600, marginBottom: '24px' }}>Create Synthetic Test</h3>
            <form onSubmit={handleCreate} style={{ display: 'flex', flexDirection: 'column', gap: '16px' }}>
              <div>
                <label style={{ display: 'block', fontSize: '13px', color: 'var(--text-secondary)', marginBottom: '8px' }}>Endpoint URL</label>
                <input type="url" placeholder="http://gateway:8080/health" required value={newUrl} onChange={e => setNewUrl(e.target.value)} style={{ width: '100%', padding: '12px', background: 'rgba(255,255,255,0.05)', border: '1px solid var(--border-color)', borderRadius: '8px', color: '#fff' }} />
              </div>
              <div style={{ display: 'flex', gap: '12px', marginTop: '16px' }}>
                <button type="button" onClick={() => setShowModal(false)} className="btn-secondary" style={{ flex: 1 }}>Cancel</button>
                <button type="submit" className="btn-primary" style={{ flex: 1 }}>Start Monitoring</button>
              </div>
            </form>
          </div>
        </div>
      )}

      {loading ? (
        <div style={{ padding: '48px', textAlign: 'center', color: 'var(--text-secondary)' }}>Loading synthetics data...</div>
      ) : (
        <div style={{ display: 'flex', gap: '24px', flexWrap: 'wrap' }}>
          
          {/* Overview Cards */}
          <div style={{ display: 'flex', gap: '16px', width: '100%' }}>
            <div className="glass-panel" style={{ flex: 1, padding: '24px' }}>
              <div style={{ fontSize: '13px', color: 'var(--text-secondary)', marginBottom: '4px' }}>Global Uptime</div>
              <div style={{ fontSize: '32px', fontWeight: 700, color: Number(globalUptime) >= 99.9 ? 'var(--status-green)' : 'var(--status-yellow)' }}>
                {globalUptime}%
              </div>
            </div>
            <div className="glass-panel" style={{ flex: 1, padding: '24px' }}>
              <div style={{ fontSize: '13px', color: 'var(--text-secondary)', marginBottom: '4px' }}>Active Tests</div>
              <div style={{ fontSize: '32px', fontWeight: 700, color: 'white' }}>
                {totalTests}
              </div>
            </div>
            <div className="glass-panel" style={{ flex: 1, padding: '24px' }}>
              <div style={{ fontSize: '13px', color: 'var(--text-secondary)', marginBottom: '4px' }}>Failing Tests</div>
              <div style={{ fontSize: '32px', fontWeight: 700, color: failingTests > 0 ? 'var(--status-red)' : 'var(--status-green)' }}>
                {failingTests}
              </div>
            </div>
          </div>

          {/* Table */}
          <div className="glass-panel" style={{ flex: '1 1 100%', padding: '0', overflow: 'hidden' }}>
            <table style={{ width: '100%', borderCollapse: 'collapse', textAlign: 'left' }}>
              <thead>
                <tr style={{ background: 'rgba(0,0,0,0.2)' }}>
                  <th style={{ padding: '16px', fontWeight: 500, color: 'var(--text-secondary)', fontSize: '13px' }}>Status</th>
                  <th style={{ padding: '16px', fontWeight: 500, color: 'var(--text-secondary)', fontSize: '13px' }}>Endpoint URL</th>
                  <th style={{ padding: '16px', fontWeight: 500, color: 'var(--text-secondary)', fontSize: '13px' }}>Avg Latency</th>
                  <th style={{ padding: '16px', fontWeight: 500, color: 'var(--text-secondary)', fontSize: '13px' }}>Uptime (1h)</th>
                </tr>
              </thead>
              <tbody>
                {results.length === 0 ? (
                  <tr>
                    <td colSpan={4} style={{ padding: '48px', textAlign: 'center', color: 'var(--text-secondary)' }}>No synthetic tests have run yet.</td>
                  </tr>
                ) : (
                  results.map((r, i) => (
                    <tr key={i} style={{ borderTop: '1px solid var(--border-color)' }}>
                      <td style={{ padding: '16px' }}>
                        <div style={{ 
                          width: '10px', height: '10px', borderRadius: '50%', 
                          background: parseFloat(r.uptime_percent) === 100 ? 'var(--status-green)' : 'var(--status-red)' 
                        }} />
                      </td>
                      <td style={{ padding: '16px', fontSize: '14px', fontFamily: 'monospace' }}>
                        {r.URL}
                      </td>
                      <td style={{ padding: '16px', fontWeight: 500, fontSize: '14px' }}>
                        {Math.round(r.avg_latency_ms)} ms
                      </td>
                      <td style={{ padding: '16px', fontSize: '14px', color: parseFloat(r.uptime_percent) >= 99.9 ? 'var(--status-green)' : 'var(--status-red)' }}>
                        {parseFloat(r.uptime_percent).toFixed(2)}%
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
