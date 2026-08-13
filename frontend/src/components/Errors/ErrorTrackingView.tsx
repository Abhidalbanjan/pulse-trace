"use client";

import React, { useState, useEffect, useCallback } from 'react';
import { useRouter } from 'next/navigation';
import { fetchWithAuth } from '@/lib/api';
import { BarChart, Bar, XAxis, Tooltip, ResponsiveContainer } from 'recharts';
import { useTheme } from '@/context/ThemeContext';

interface TimelineBucket { time_bucket: string; count: number }

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
  status: 'open' | 'resolved' | 'muted' | 'snoozed';
  resolved_by?: string;
  resolved_at?: string;
  assignee?: string;
  snoozed_until?: string;
}

type StatusFilter = 'all' | 'open' | 'resolved' | 'muted' | 'snoozed';

const STATUS_FILTERS: Array<{ value: StatusFilter; label: string }> = [
  { value: 'open', label: 'Open' },
  { value: 'snoozed', label: 'Snoozed' },
  { value: 'resolved', label: 'Resolved' },
  { value: 'muted', label: 'Muted' },
  { value: 'all', label: 'All' },
];

// Snooze presets → hours. A snooze auto-expires server-side back to 'open'.
const SNOOZE_OPTIONS: Array<{ label: string; hours: number }> = [
  { label: '1 hour', hours: 1 },
  { label: '1 day', hours: 24 },
  { label: '1 week', hours: 24 * 7 },
];

export function ErrorTrackingView() {
  const { tokens: t } = useTheme();
  const router = useRouter();
  const [groups, setGroups] = useState<ErrorGroup[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [statusFilter, setStatusFilter] = useState<StatusFilter>('open');
  const [actioning, setActioning] = useState<string | null>(null);

  // Inline assignee editing: which group's assignee input is open, and its draft.
  const [assigningFp, setAssigningFp] = useState<string | null>(null);
  const [assigneeDraft, setAssigneeDraft] = useState('');

  // Occurrence timeline: which group is expanded, and its fetched buckets.
  const [expanded, setExpanded] = useState<string | null>(null);
  const [timeline, setTimeline] = useState<TimelineBucket[]>([]);
  const [timelineLoading, setTimelineLoading] = useState(false);

  const toggleTimeline = (g: ErrorGroup) => {
    if (expanded === g.fingerprint) { setExpanded(null); return; }
    setExpanded(g.fingerprint);
    setTimeline([]);
    setTimelineLoading(true);
    const params = new URLSearchParams({ service: g.service, operation: g.operation, message: g.message, interval: '7d' });
    fetchWithAuth(`/api/v1/errors/groups/${g.fingerprint}/timeline?${params.toString()}`)
      .then(res => res.json())
      .then(data => setTimeline((data?.data || []).map((b: { time_bucket: string; count: string | number }) => ({ time_bucket: b.time_bucket, count: Number(b.count) }))))
      .catch(() => setTimeline([]))
      .finally(() => setTimelineLoading(false));
  };

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

  // patchGroup drives the unified triage endpoint (assignee / snooze). Resolve,
  // mute and reopen keep their dedicated POST actions above for backward compat.
  const patchGroup = async (group: ErrorGroup, body: Record<string, unknown>) => {
    setActioning(group.fingerprint);
    try {
      await fetchWithAuth(`/api/v1/errors/groups/${group.fingerprint}`, {
        method: 'PATCH',
        body: JSON.stringify({ service: group.service, operation: group.operation, message: group.message, ...body }),
      });
      await fetchGroups();
    } catch (err) {
      console.error('Failed to update error group:', err);
    } finally {
      setActioning(null);
    }
  };

  const snooze = (group: ErrorGroup, hours: number) =>
    patchGroup(group, { status: 'snoozed', snoozed_until: new Date(Date.now() + hours * 3600_000).toISOString() });

  const saveAssignee = async (group: ErrorGroup) => {
    const next = assigneeDraft.trim();
    setAssigningFp(null);
    if (next === (group.assignee ?? '')) return; // no change
    await patchGroup(group, { assignee: next });
  };

  const filtered = groups.filter(g => statusFilter === 'all' || g.status === statusFilter);

  const statusColor = (status: string) => {
    if (status === 'open') return t.red;
    if (status === 'resolved') return t.green;
    if (status === 'snoozed') return t.amber;
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
                <th style={{ padding: '16px', fontWeight: 600, color: t.text2, fontSize: '12.5px' }}>Assignee</th>
                <th style={{ padding: '16px', fontWeight: 600, color: t.text2, fontSize: '12.5px' }}>Last Seen</th>
                <th style={{ padding: '16px', fontWeight: 600, color: t.text2, fontSize: '12.5px' }}>Actions</th>
              </tr>
            </thead>
            <tbody>
              {filtered.map(g => (
                <React.Fragment key={g.fingerprint}>
                <tr style={{ borderBottom: expanded === g.fingerprint ? 'none' : '1px solid ' + t.panelBorder }}>
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
                  <td style={{ padding: '16px', fontSize: '13px' }}>
                    {assigningFp === g.fingerprint ? (
                      <input
                        autoFocus
                        value={assigneeDraft}
                        placeholder="who owns this?"
                        onChange={(e) => setAssigneeDraft(e.target.value)}
                        onBlur={() => saveAssignee(g)}
                        onKeyDown={(e) => { if (e.key === 'Enter') saveAssignee(g); if (e.key === 'Escape') setAssigningFp(null); }}
                        style={{ width: '130px', padding: '5px 8px', fontSize: '12.5px', background: t.dark ? 'rgba(255,255,255,0.06)' : '#fff', border: '1px solid ' + t.accent, borderRadius: '7px', color: t.text1 }}
                        aria-label="Assignee"
                      />
                    ) : (
                      <button
                        onClick={() => { setAssigningFp(g.fingerprint); setAssigneeDraft(g.assignee ?? ''); }}
                        style={{ ...ghostButtonStyle, borderStyle: g.assignee ? 'solid' : 'dashed', color: g.assignee ? t.text1 : t.text2 }}
                        title="Assign an owner"
                      >
                        {g.assignee || '+ Assign'}
                      </button>
                    )}
                  </td>
                  <td style={{ padding: '16px', color: t.text2, fontSize: '13px' }}>
                    {new Date(g.last_seen).toLocaleString()}
                    {g.status === 'snoozed' && g.snoozed_until && (
                      <div style={{ color: t.amber, fontSize: '11.5px', marginTop: '3px' }}>💤 until {new Date(g.snoozed_until).toLocaleString()}</div>
                    )}
                  </td>
                  <td style={{ padding: '16px' }}>
                    <div style={{ display: 'flex', gap: '7px', flexWrap: 'wrap' }}>
                      <button
                        onClick={() => toggleTimeline(g)}
                        style={{ ...ghostButtonStyle, color: expanded === g.fingerprint ? t.accent : t.text2, borderColor: expanded === g.fingerprint ? t.accent : t.panelBorder }}
                      >
                        Timeline
                      </button>
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
                      {g.status !== 'snoozed' && (
                        <select
                          disabled={actioning === g.fingerprint}
                          value=""
                          onChange={(e) => { const h = Number(e.target.value); if (h) snooze(g, h); }}
                          style={{ ...ghostButtonStyle, appearance: 'none', paddingRight: '11px' }}
                          aria-label="Snooze this error group"
                          title="Hide temporarily; auto-returns when the timer expires"
                        >
                          <option value="">Snooze…</option>
                          {SNOOZE_OPTIONS.map(o => <option key={o.hours} value={o.hours}>{o.label}</option>)}
                        </select>
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
                {expanded === g.fingerprint && (
                  <tr style={{ borderBottom: '1px solid ' + t.panelBorder }}>
                    <td colSpan={7} style={{ padding: '0 16px 20px' }}>
                      <div style={{ fontSize: '11.5px', fontWeight: 700, letterSpacing: '0.04em', color: t.text2, margin: '4px 0 10px' }}>OCCURRENCES · LAST 7 DAYS</div>
                      {timelineLoading ? (
                        <div style={{ color: t.text2, fontSize: '13px', padding: '20px 0' }}>Loading timeline…</div>
                      ) : timeline.length === 0 ? (
                        <div style={{ color: t.text2, fontSize: '13px', padding: '20px 0' }}>No occurrences in the last 7 days.</div>
                      ) : (
                        <div style={{ height: '140px' }}>
                          <ResponsiveContainer width="100%" height="100%">
                            <BarChart data={timeline}>
                              <XAxis dataKey="time_bucket" tick={{ fontSize: 10, fill: t.text2 }} minTickGap={30} tickFormatter={(v) => new Date(String(v).replace(' ', 'T') + 'Z').toLocaleDateString([], { month: 'short', day: 'numeric' })} />
                              <Tooltip
                                contentStyle={{ background: t.panelBg, border: '1px solid ' + t.panelBorder, borderRadius: '8px', fontSize: '12px' }}
                                labelFormatter={(v) => new Date(String(v).replace(' ', 'T') + 'Z').toLocaleString()}
                                formatter={(value) => [`${value} occurrences`, '']}
                              />
                              <Bar dataKey="count" fill={t.red} radius={[3, 3, 0, 0]} />
                            </BarChart>
                          </ResponsiveContainer>
                        </div>
                      )}
                    </td>
                  </tr>
                )}
                </React.Fragment>
              ))}
            </tbody>
          </table>
        )}
      </div>
    </div>
  );
}
