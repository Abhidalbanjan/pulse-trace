"use client";

import React, { useState } from 'react';
import { useTheme } from '@/context/ThemeContext';

export function DeploymentsView() {
  const [gates] = useState<any[]>([]);
  const { tokens: t } = useTheme();

  return (
    <div style={{ padding: '40px', maxWidth: '1200px', margin: '0 auto', width: '100%', height: 'calc(100vh - 120px)' }}>

      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'flex-start', marginBottom: '20px' }}>
        <div>
          <h2 style={{ fontSize: '26px', fontWeight: 700, margin: '0 0 8px' }}>Shift-Left Deployment Gates</h2>
          <p style={{ color: t.text2, fontSize: '14.5px' }}>Pull requests automatically analyzed and blocked by PulseTrace Causal AI.</p>
        </div>
        <button
          style={{
            padding: '10px 18px',
            borderRadius: '10px',
            border: '1px solid ' + t.panelBorder,
            background: 'transparent',
            color: t.text1,
            fontWeight: 600,
            fontSize: '13.5px',
            cursor: 'pointer',
          }}
        >
          Configure Webhooks
        </button>
      </div>

      <div
        style={{
          borderRadius: '20px',
          overflow: 'hidden',
          background: t.panelBg,
          border: '1px solid ' + t.panelBorder,
          backdropFilter: 'blur(30px) saturate(180%)',
          boxShadow: t.shadow,
        }}
      >
        <table style={{ width: '100%', borderCollapse: 'collapse', textAlign: 'left' }}>
          <thead>
            <tr
              style={{
                borderBottom: '1px solid ' + t.panelBorder,
                background: t.dark ? 'rgba(0,0,0,0.15)' : 'rgba(0,0,0,0.03)',
              }}
            >
              <th style={{ padding: '16px', fontWeight: 600, color: t.text2, fontSize: '12.5px' }}>Pull Request</th>
              <th style={{ padding: '16px', fontWeight: 600, color: t.text2, fontSize: '12.5px' }}>Repository</th>
              <th style={{ padding: '16px', fontWeight: 600, color: t.text2, fontSize: '12.5px' }}>AI Decision</th>
              <th style={{ padding: '16px', fontWeight: 600, color: t.text2, fontSize: '12.5px' }}>Timestamp</th>
              <th style={{ padding: '16px', fontWeight: 600, color: t.text2, fontSize: '12.5px' }}>Actions</th>
            </tr>
          </thead>
          <tbody>
            {gates.map(gate => (
              <tr
                key={gate.id}
                style={{
                  borderBottom: '1px solid ' + t.panelBorder,
                  background: gate.status === 'BLOCKED' ? (t.dark ? 'rgba(241,107,99,0.06)' : 'rgba(224,82,75,0.04)') : 'transparent',
                }}
              >
                <td style={{ padding: '20px 16px' }}>
                  <div style={{ fontWeight: 700, marginBottom: '4px' }}>{gate.title}</div>
                  <div style={{ fontSize: '12.5px', color: t.text2 }}>#{gate.id.replace('pr-', '')} by {gate.author}</div>
                </td>
                <td style={{ padding: '20px 16px', fontSize: '13.5px', color: t.accent }}>{gate.repo}</td>
                <td style={{ padding: '20px 16px' }}>
                  <div style={{ display: 'flex', alignItems: 'center', gap: '8px', marginBottom: '4px' }}>
                    <span
                      style={{
                        background: gate.status === 'BLOCKED' ? t.red : t.green,
                        color: '#fff',
                        padding: '4px 12px',
                        borderRadius: '100px',
                        fontSize: '11px',
                        fontWeight: 700,
                      }}
                    >
                      {gate.status}
                    </span>
                  </div>
                  <div style={{ fontSize: '12px', color: t.text2, marginTop: '6px', maxWidth: '280px' }}>{gate.reason}</div>
                </td>
                <td style={{ padding: '20px 16px', fontSize: '12.5px', color: t.text2 }}>
                  {new Date(gate.timestamp).toLocaleString()}
                </td>
                <td style={{ padding: '20px 16px' }}>
                  <button
                    style={{
                      padding: '7px 14px',
                      fontSize: '12.5px',
                      borderRadius: '9px',
                      border: '1px solid ' + (gate.status === 'BLOCKED' ? t.red : t.panelBorder),
                      background: 'transparent',
                      color: gate.status === 'BLOCKED' ? t.red : t.text1,
                      cursor: 'pointer',
                    }}
                  >
                    {gate.status === 'BLOCKED' ? 'Override Gate' : 'View Trace'}
                  </button>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
        {gates.length === 0 && (
          <div style={{ padding: '60px 24px', textAlign: 'center', color: t.text2, fontSize: '14px' }}>
            No deployment gates yet. Pull request activity analyzed by PulseTrace Causal AI will appear here.
          </div>
        )}
      </div>

    </div>
  );
}
