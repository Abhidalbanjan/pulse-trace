"use client";

import React, { useState, useEffect, useCallback } from 'react';
import { useRouter } from 'next/navigation';
import { fetchWithAuth } from '@/lib/api';
import { useTheme } from '@/context/ThemeContext';

interface ErrorGroup {
  fingerprint: string;
  service: string;
  operation: string;
  message: string; // normalized template (dynamic values like IDs/UUIDs/numbers stripped) - the grouping key
  sample_message: string; // most recent raw message - what actually happened
  occurrences: number;
  first_seen: string;
  last_seen: string;
  sample_trace_id: string;
  status: 'open' | 'resolved' | 'muted';
  resolved_by?: string;
  resolved_at?: string;
}

const STATUS_FILTERS: Array<{ value: 'open' | 'resolved' | 'muted' | 'all'; label: string }> = [
  { value: 'open', label: 'Open' },
  { value: 'resolved', label: 'Resolved' },
  { value: 'muted', label: 'Muted' },
  { value: 'all', label: 'All' },
];

export function ErrorTrackingView() {
  const { tokens: t } = useTheme();
  const router = useRouter();
  const [groups, setGroups] = useState<ErrorGroup[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [statusFilter, setStatusFilter] = useState<'all' | 'open' | 'resolved' | 'muted'>('open');
  const [actioning, setActioning] = useState<string | null>(null);

  const fetchGroups = useCallback(() => {
    fetchWithAuth('/api/v1/errors/groups')
      .then(async res => {
        if (!res.ok) throw new Error(await res.text());
        return res.json();
      })
      .then(json => {
        setGroups(json.data || []);
        setError(null);
      })
      .catch(err => setError(err.message || 'Failed to load error groups'))
      .finally(() => setLoading(false));
  }, []);

  useEffect(() => {
    fetchGroups();
    const interval = setInterval(fetchGroups, 20000);
    return () => clearInterval(interval);
  }, [fetchGroups]);

  const runAction = async (group: ErrorGroup, action: 'resolve' | 'mute' | 'reopen') => {
    setActioning(group.fingerprint);
    try {
      await fetchWithAuth(`/api/v1/errors/groups/${group.fingerprint}/${action}`, {
        method: 'POST',
        body: JSON.stringify({ service: group.service, operation: group.operation, message: group.message }),
      });
      await fetchGroups();
    } catch (err) {
      console.error(`Failed to ${action} error group:`, err);
    } finally {
      setActioning(null);
    }
  };

  const filtered = groups.filter(g => statusFilter === 'all' || g.status === statusFilter);

  const statusColor = (status: string) => {
    if (status === 'open') return t.red;
    if (status === 'resolved') return t.green;
    return t.text2;
  };

  const ghostButtonStyle: React.CSSProperties = {
    fontSize: '12px',
    padding: '6px 11px',
    background: 'transparent',
    border: '1px solid ' + t.panelBorder,
    borderRadius: '8px',
    color: t.text2,
    cursor: 'pointer',
  };

  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: '16px', height: '100%' }}>
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: '20px' }}>
        <div>
          <h2 style={{ fontSize: '26px', fontWeight: 700, margin: '0 0 8px' }}>Error Tracking</h2>
          <p style={{ color: t.text2, fontSize: '14.5px' }}>Errors grouped by service, operation, and message across the last 7 days.</p>
        </div>
        <div style={{ display: 'flex', gap: '8px' }}>
          {STATUS_FILTERS.map(f => {
            const active = statusFilter === f.value;
            return (
              <button
                key={f.value}
                onClick={() => setStatusFilter(f.value)}
                style={{
                  padding: '9px 16px',
                  borderRadius: '10px',
                  border: '1px solid ' + t.panelBorder,
                  background: active ? t.accent : 'transparent',
                  color: active ? '#fff' : t.text2,
                  fontSize: '13px',
                  fontWeight: 600,
                  cursor: 'pointer',
                }}
              >
                {f.label}
              </button>
            );
          })}
        </div>
      </div>

      <div
        style={{
          flex: 1,
          overflow: 'auto',
          borderRadius: '20px',
          background: t.panelBg,
          border: '1px solid ' + t.panelBorder,
          backdropFilter: 'blur(30px) saturate(180%)',
          boxShadow: t.shadow,
        }}
      >
        {loading ? (
          <div style={{ padding: '48px', textAlign: 'center', color: t.text2 }}>Loading error groups...</div>
        ) : error ? (
          <div style={{ padding: '48px', textAlign: 'center', color: t.red }}>{error}</div>
        ) : filtered.length === 0 ? (
          <div style={{ padding: '48px', textAlign: 'center', color: t.green }}>No {statusFilter !== 'all' ? statusFilter : ''} errors found.</div>
        ) : (
          <table style={{ width: '100%', borderCollapse: 'collapse', textAlign: 'left' }}>
            <thead>
              <tr style={{ borderBottom: '1px solid ' + t.panelBorder, background: t.dark ? 'rgba(0,0,0,0.15)' : 'rgba(0,0,0,0.03)' }}>
                <th style={{ padding: '16px', fontWeight: 600, color: t.text2, fontSize: '12.5px' }}>Status</th>
                <th style={{ padding: '16px', fontWeight: 600, color: t.text2, fontSize: '12.5px' }}>Service / Operation</th>
                <th style={{ padding: '16px', fontWeight: 600, color: t.text2, fontSize: '12.5px' }}>Message</th>
                <th style={{ padding: '16px', fontWeight: 600, color: t.text2, fontSize: '12.5px' }}>Occurrences</th>
                <th style={{ padding: '16px', fontWeight: 600, color: t.text2, fontSize: '12.5px' }}>Last Seen</th>
                <th style={{ padding: '16px', fontWeight: 600, color: t.text2, fontSize: '12.5px' }}>Actions</th>
              </tr>
            </thead>
            <tbody>
              {filtered.map(g => (
                <tr key={g.fingerprint} style={{ borderBottom: '1px solid ' + t.panelBorder }}>
                  <td style={{ padding: '16px' }}>
                    <span
                      style={{
                        color: statusColor(g.status),
                        fontSize: '11.5px',
                        padding: '4px 10px',
                        background: t.dark ? 'rgba(255,255,255,0.06)' : 'rgba(0,0,0,0.05)',
                        borderRadius: '100px',
                        textTransform: 'capitalize',
                      }}
                    >
                      {g.status}
                    </span>
                  </td>
                  <td style={{ padding: '16px' }}>
                    <div style={{ fontWeight: 700, color: t.accent }}>{g.service}</div>
                    <div style={{ fontSize: '12px', color: t.text2, fontFamily: 'monospace' }}>{g.operation}</div>
                  </td>
                  <td style={{ padding: '16px', maxWidth: '320px', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap', fontSize: '13px' }} title={`Latest: ${g.sample_message}\nGrouped by template: ${g.message}`}>
                    {g.sample_message || g.message || <span style={{ color: t.text2 }}>(no message)</span>}
                  </td>
                  <td style={{ padding: '16px', fontWeight: 700 }}>{g.occurrences.toLocaleString()}</td>
                  <td style={{ padding: '16px', color: t.text2, fontSize: '13px' }}>{new Date(g.last_seen).toLocaleString()}</td>
                  <td style={{ padding: '16px' }}>
                    <div style={{ display: 'flex', gap: '7px', flexWrap: 'wrap' }}>
                      {g.sample_trace_id && (
                        <button
                          onClick={() => router.push(`/traces?trace=${g.sample_trace_id}`)}
                          style={ghostButtonStyle}
                        >
                          View Trace
                        </button>
                      )}
                      {g.status !== 'resolved' && (
                        <button
                          disabled={actioning === g.fingerprint}
                          onClick={() => runAction(g, 'resolve')}
                          style={{
                            fontSize: '12px',
                            padding: '6px 11px',
                            background: t.dark ? 'rgba(52,199,126,0.14)' : 'rgba(37,169,107,0.1)',
                            border: '1px solid ' + t.green,
                            borderRadius: '8px',
                            color: t.green,
                            cursor: 'pointer',
                          }}
                        >
                          Resolve
                        </button>
                      )}
                      {g.status !== 'muted' && (
                        <button
                          disabled={actioning === g.fingerprint}
                          onClick={() => runAction(g, 'mute')}
                          style={ghostButtonStyle}
                        >
                          Mute
                        </button>
                      )}
                      {g.status !== 'open' && (
                        <button
                          disabled={actioning === g.fingerprint}
                          onClick={() => runAction(g, 'reopen')}
                          style={ghostButtonStyle}
                        >
                          Reopen
                        </button>
                      )}
                    </div>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>
    </div>
  );
}
