"use client";

import React, { useState } from 'react';

export function DeploymentsView() {
  const [gates] = useState<any[]>([]);

  return (
    <div style={{ padding: '40px', maxWidth: '1200px', margin: '0 auto', width: '100%', height: 'calc(100vh - 120px)' }}>
      
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'flex-start', marginBottom: '32px' }}>
        <div>
          <h2 style={{ fontSize: '28px', fontWeight: 600, marginBottom: '8px' }}>Shift-Left Deployment Gates</h2>
          <p style={{ color: 'var(--text-secondary)' }}>Pull Requests automatically analyzed and blocked by PulseTrace Causal AI.</p>
        </div>
        <button className="btn-secondary" style={{ padding: '8px 16px' }}>Configure Webhooks</button>
      </div>

      <div className="glass-panel" style={{ padding: 0, overflow: 'hidden' }}>
        <table style={{ width: '100%', borderCollapse: 'collapse', textAlign: 'left' }}>
          <thead>
            <tr style={{ background: 'rgba(0,0,0,0.2)', color: 'var(--text-secondary)', fontSize: '13px' }}>
              <th style={{ padding: '16px 24px', fontWeight: 500 }}>Pull Request</th>
              <th style={{ padding: '16px 24px', fontWeight: 500 }}>Repository</th>
              <th style={{ padding: '16px 24px', fontWeight: 500 }}>AI Decision</th>
              <th style={{ padding: '16px 24px', fontWeight: 500 }}>Timestamp</th>
              <th style={{ padding: '16px 24px', fontWeight: 500 }}>Actions</th>
            </tr>
          </thead>
          <tbody>
            {gates.map(gate => (
              <tr key={gate.id} style={{ borderBottom: '1px solid var(--border-color)', background: gate.status === 'BLOCKED' ? 'rgba(239, 68, 68, 0.05)' : 'transparent' }}>
                <td style={{ padding: '20px 24px' }}>
                  <div style={{ fontWeight: 500, marginBottom: '4px' }}>{gate.title}</div>
                  <div style={{ fontSize: '12px', color: 'var(--text-secondary)' }}>#{gate.id.replace('pr-', '')} by {gate.author}</div>
                </td>
                <td style={{ padding: '20px 24px', fontSize: '14px', color: 'var(--accent-blue)' }}>{gate.repo}</td>
                <td style={{ padding: '20px 24px' }}>
                  <div style={{ display: 'flex', alignItems: 'center', gap: '8px', marginBottom: '4px' }}>
                    <span style={{ 
                      background: gate.status === 'BLOCKED' ? 'var(--status-red)' : 'var(--status-green)', 
                      color: 'white', 
                      padding: '4px 12px', 
                      borderRadius: '128px', 
                      fontSize: '11px', 
                      fontWeight: 600 
                    }}>
                      {gate.status}
                    </span>
                  </div>
                  <div style={{ fontSize: '12px', color: 'var(--text-secondary)', maxWidth: '250px' }}>{gate.reason}</div>
                </td>
                <td style={{ padding: '20px 24px', fontSize: '13px', color: 'var(--text-secondary)' }}>
                  {new Date(gate.timestamp).toLocaleString()}
                </td>
                <td style={{ padding: '20px 24px' }}>
                  {gate.status === 'BLOCKED' ? (
                     <button className="btn-secondary" style={{ padding: '6px 12px', fontSize: '12px', borderColor: 'var(--status-red)', color: 'var(--status-red)' }}>Override Gate</button>
                  ) : (
                     <button className="btn-secondary" style={{ padding: '6px 12px', fontSize: '12px' }}>View Trace</button>
                  )}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>

    </div>
  );
}
