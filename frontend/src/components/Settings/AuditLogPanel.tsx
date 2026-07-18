"use client";

import React, { useState, useEffect, useCallback } from 'react';
import { fetchWithAuth } from '@/lib/api';
import { useTheme } from '@/context/ThemeContext';

interface AuditEntry {
  id: number;
  actor: string;
  action: 'create' | 'update' | 'delete';
  target_type: string;
  target_id: string;
  before_state?: any;
  after_state?: any;
  created_at: string;
}

export function AuditLogPanel() {
  const { tokens: t } = useTheme();
  const [entries, setEntries] = useState<AuditEntry[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [expanded, setExpanded] = useState<number | null>(null);

  const fetchLog = useCallback(() => {
    fetchWithAuth('/api/v1/admin/audit-log')
      .then(async res => {
        if (!res.ok) throw new Error(await res.text());
        return res.json();
      })
      .then(json => {
        setEntries(json.data || []);
        setError(null);
      })
      .catch(err => setError(err.message || 'Failed to load audit log'))
      .finally(() => setLoading(false));
  }, []);

  useEffect(() => { fetchLog(); }, [fetchLog]);

  const actionColor = (action: string) => action === 'create' ? t.green : action === 'delete' ? t.red : t.amber;

  return (
    <div>
      <div style={{ marginBottom: '28px' }}>
        <h3 style={{ fontSize: '19px', fontWeight: 700, margin: '0 0 8px', color: t.text1 }}>Audit Log</h3>
        <p style={{ color: t.text2, fontSize: '13.5px', maxWidth: '520px', lineHeight: 1.6 }}>Every role, policy, and user change — who made it, when, and what changed.</p>
      </div>

      {error && (
        <div style={{ padding: '16px', background: t.redSoft, color: t.red, borderRadius: '8px', marginBottom: '24px' }}>{error}</div>
      )}

      <table style={{ width: '100%', borderCollapse: 'collapse' }}>
        <thead>
          <tr style={{ borderBottom: '1px solid ' + t.panelBorder, textAlign: 'left' }}>
            <th style={{ padding: '10px 8px', fontWeight: 600, color: t.text2, fontSize: '12px' }}>When</th>
            <th style={{ padding: '10px 8px', fontWeight: 600, color: t.text2, fontSize: '12px' }}>Actor</th>
            <th style={{ padding: '10px 8px', fontWeight: 600, color: t.text2, fontSize: '12px' }}>Action</th>
            <th style={{ padding: '10px 8px', fontWeight: 600, color: t.text2, fontSize: '12px' }}>Target</th>
            <th style={{ padding: '10px 8px', fontWeight: 600, color: t.text2, fontSize: '12px' }}></th>
          </tr>
        </thead>
        <tbody>
          {loading ? (
            <tr><td colSpan={5} style={{ padding: '24px', textAlign: 'center', color: t.text2 }}>Loading audit log...</td></tr>
          ) : entries.length === 0 ? (
            <tr><td colSpan={5} style={{ padding: '24px', textAlign: 'center', color: t.text2 }}>No changes recorded yet.</td></tr>
          ) : entries.map(e => (
            <React.Fragment key={e.id}>
              <tr
                onClick={() => setExpanded(expanded === e.id ? null : e.id)}
                style={{ borderBottom: '1px solid ' + t.panelBorder, cursor: 'pointer' }}
              >
                <td style={{ padding: '14px 8px', fontSize: '13px', color: t.text2, whiteSpace: 'nowrap' }}>{new Date(e.created_at).toLocaleString()}</td>
                <td style={{ padding: '14px 8px', fontSize: '13.5px', fontWeight: 500, color: t.text1 }}>{e.actor}</td>
                <td style={{ padding: '14px 8px' }}>
                  <span style={{ color: actionColor(e.action), fontSize: '11.5px', textTransform: 'uppercase', fontWeight: 700 }}>{e.action}</span>
                </td>
                <td style={{ padding: '14px 8px', fontSize: '12px', fontFamily: 'monospace', color: t.text1 }}>{e.target_type}:{e.target_id}</td>
                <td style={{ padding: '14px 8px', fontSize: '12px', color: t.accent }}>{expanded === e.id ? 'Hide' : 'Details'}</td>
              </tr>
              {expanded === e.id && (
                <tr style={{ borderBottom: '1px solid ' + t.panelBorder }}>
                  <td colSpan={5} style={{ padding: '16px', background: t.dark ? 'rgba(0,0,0,0.2)' : 'rgba(0,0,0,0.03)' }}>
                    <div style={{ display: 'flex', gap: '24px' }}>
                      <div style={{ flex: 1 }}>
                        <div style={{ fontSize: '11px', color: t.text2, marginBottom: '6px', textTransform: 'uppercase' }}>Before</div>
                        <pre style={{ fontSize: '12px', whiteSpace: 'pre-wrap', wordBreak: 'break-all', margin: 0, color: t.text1 }}>{e.before_state ? JSON.stringify(e.before_state, null, 2) : '—'}</pre>
                      </div>
                      <div style={{ flex: 1 }}>
                        <div style={{ fontSize: '11px', color: t.text2, marginBottom: '6px', textTransform: 'uppercase' }}>After</div>
                        <pre style={{ fontSize: '12px', whiteSpace: 'pre-wrap', wordBreak: 'break-all', margin: 0, color: t.text1 }}>{e.after_state ? JSON.stringify(e.after_state, null, 2) : '—'}</pre>
                      </div>
                    </div>
                  </td>
                </tr>
              )}
            </React.Fragment>
          ))}
        </tbody>
      </table>
    </div>
  );
}
