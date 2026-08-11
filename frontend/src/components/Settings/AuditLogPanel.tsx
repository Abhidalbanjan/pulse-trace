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
  before_state?: unknown;
  after_state?: unknown;
  created_at: string;
  prev_hash?: string;
  entry_hash?: string;
}

interface Verification {
  valid: boolean;
  count: number;
  first_broken_id?: number;
  message: string;
}

export function AuditLogPanel() {
  const { tokens: t } = useTheme();
  const [entries, setEntries] = useState<AuditEntry[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [expanded, setExpanded] = useState<number | null>(null);
  const [verifying, setVerifying] = useState(false);
  const [verification, setVerification] = useState<Verification | null>(null);
  const [exporting, setExporting] = useState(false);

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

  // Verify integrity: replay the hash chain server-side and surface whether the
  // trail is intact or where it was tampered.
  const verify = useCallback(() => {
    setVerifying(true);
    setVerification(null);
    fetchWithAuth('/api/v1/admin/audit-log/verify')
      .then(async res => {
        if (!res.ok) throw new Error(await res.text());
        return res.json();
      })
      .then(json => setVerification(json.data as Verification))
      .catch(err => setVerification({ valid: false, count: 0, message: err.message || 'Verification failed' }))
      .finally(() => setVerifying(false));
  }, []);

  // Export the full chain as an NDJSON attachment. The endpoint is auth-gated,
  // so we fetch the body with credentials and trigger a client-side download
  // rather than navigating a bare link (which wouldn't carry the token).
  const exportLog = useCallback(async () => {
    setExporting(true);
    try {
      const res = await fetchWithAuth('/api/v1/admin/audit-log/export');
      if (!res.ok) throw new Error(await res.text());
      const blob = await res.blob();
      const url = URL.createObjectURL(blob);
      const a = document.createElement('a');
      a.href = url;
      a.download = `audit-log-${new Date().toISOString().replace(/[:.]/g, '-')}.ndjson`;
      document.body.appendChild(a);
      a.click();
      a.remove();
      URL.revokeObjectURL(url);
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Export failed');
    } finally {
      setExporting(false);
    }
  }, []);

  const actionColor = (action: string) => action === 'create' ? t.green : action === 'delete' ? t.red : t.amber;

  const btnStyle: React.CSSProperties = {
    padding: '8px 14px', borderRadius: '8px', border: `1px solid ${t.panelBorder}`,
    background: t.dark ? 'rgba(255,255,255,0.06)' : 'rgba(0,0,0,0.03)', color: t.text1,
    fontSize: '13px', fontWeight: 600, cursor: 'pointer',
  };
  const shortHash = (h?: string) => (h && h.length >= 12 ? `${h.slice(0, 8)}…${h.slice(-4)}` : h || '—');

  return (
    <div>
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'flex-start', gap: '16px', flexWrap: 'wrap', marginBottom: '28px' }}>
        <div>
          <h3 style={{ fontSize: '19px', fontWeight: 700, margin: '0 0 8px', color: t.text1 }}>Audit Log</h3>
          <p style={{ color: t.text2, fontSize: '13.5px', maxWidth: '520px', lineHeight: 1.6 }}>Every role, policy, and user change — who made it, when, and what changed. Each entry is hash-chained to the one before it, so the trail is tamper-evident and can be exported for compliance.</p>
        </div>
        <div style={{ display: 'flex', gap: '10px', flexShrink: 0 }}>
          <button onClick={verify} disabled={verifying} style={{ ...btnStyle, opacity: verifying ? 0.6 : 1 }}>
            {verifying ? 'Verifying…' : 'Verify integrity'}
          </button>
          <button onClick={exportLog} disabled={exporting} style={{ ...btnStyle, opacity: exporting ? 0.6 : 1 }}>
            {exporting ? 'Exporting…' : 'Export'}
          </button>
        </div>
      </div>

      {verification && (
        <div
          role="status"
          style={{
            padding: '14px 16px', borderRadius: '10px', marginBottom: '24px', fontSize: '13.5px', fontWeight: 600,
            display: 'flex', alignItems: 'center', gap: '10px',
            background: verification.valid ? t.green + '18' : t.redSoft,
            color: verification.valid ? t.green : t.red,
            border: `1px solid ${verification.valid ? t.green + '55' : t.red + '55'}`,
          }}
        >
          <span style={{ fontSize: '16px' }}>{verification.valid ? '✓' : '⚠'}</span>
          <span>
            {verification.valid
              ? `Tamper-evident — ${verification.count} ${verification.count === 1 ? 'entry' : 'entries'} verified, the trail is intact.`
              : `Integrity check failed${verification.first_broken_id ? ` at entry #${verification.first_broken_id}` : ''}: ${verification.message}`}
          </span>
        </div>
      )}

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
                    {e.entry_hash && (
                      <div style={{ marginTop: '14px', paddingTop: '12px', borderTop: `1px solid ${t.panelBorder}`, fontSize: '11px', color: t.text2, fontFamily: 'monospace', display: 'flex', gap: '20px', flexWrap: 'wrap' }}>
                        <span title={e.prev_hash}>prev: {shortHash(e.prev_hash)}</span>
                        <span title={e.entry_hash} style={{ color: t.text1 }}>hash: {shortHash(e.entry_hash)}</span>
                      </div>
                    )}
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
