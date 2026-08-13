"use client";

// Alerts screen (ROAD_TO_100 · Wave 2 parity).
//
// The raw alert stream from alert-service — the per-signal layer beneath
// Incidents (which cluster alerts). Lists alerts with service/level filters and
// opens a detail panel that fetches the canonical record by id, closing the
// GET /api/v1/alerts and GET /api/v1/alerts/{id} parity orphans.

import React, { useState, useEffect } from 'react';
import { useTheme } from '@/context/ThemeContext';
import { api } from '@/lib/api/client';
import { fetchWithAuth } from '@/lib/api';
import type { Alert, AlertGroup, AlertSilence } from '@/lib/api/types';
import { useApiResource } from '@/lib/hooks/useApiResource';
import { StateBoundary } from '@/components/ui';

const LEVELS = ['', 'CRITICAL', 'ERROR', 'WARNING', 'INFO'];

export function AlertsView() {
  const { tokens: t } = useTheme();

  const [service, setService] = useState('');
  const [level, setLevel] = useState('');
  const [grouped, setGrouped] = useState(false);
  const [selectedId, setSelectedId] = useState<string | null>(null);
  const [expanded, setExpanded] = useState<Set<string>>(new Set());

  // Silences / maintenance windows (Alerts · E2).
  const [showSilences, setShowSilences] = useState(false);
  const silences = useApiResource<AlertSilence[]>(
    () => api.list<AlertSilence>('/api/v1/alerts/silences').then((r) => r.items),
    { key: 'silences', pollMs: 30000 },
  );
  const silenceList = silences.data ?? [];

  const refetchAll = () => { alerts.refetch(); groups.refetch(); silences.refetch(); };

  // Create a silence, then refresh alerts so matches immediately read as silenced.
  const createSilence = async (matcher: { service?: string; level?: string; message_contains?: string }, hours: number) => {
    const now = new Date();
    const ends = new Date(now.getTime() + hours * 3600_000);
    await fetchWithAuth('/api/v1/alerts/silences', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ matcher, starts_at: now.toISOString(), ends_at: ends.toISOString() }),
    });
    refetchAll();
  };

  const deleteSilence = async (id: string) => {
    await fetchWithAuth(`/api/v1/alerts/silences/${encodeURIComponent(id)}`, { method: 'DELETE' });
    refetchAll();
  };

  const silenceAlert = (a: Alert) => createSilence({ service: a.service, level: a.level }, 1);

  const query = `service=${encodeURIComponent(service)}&level=${encodeURIComponent(level)}`;
  const alerts = useApiResource<Alert[]>(
    () => api.list<Alert>(`/api/v1/alerts?${query}`).then((r) => r.items),
    { key: query, pollMs: 15000, enabled: !grouped },
  );
  const list = alerts.data ?? [];

  const groups = useApiResource<AlertGroup[]>(
    () => api.list<AlertGroup>(`/api/v1/alerts?${query}&group=true`).then((r) => r.items),
    { key: query, pollMs: 15000, enabled: grouped },
  );
  const groupList = groups.data ?? [];

  const toggleExpanded = (key: string) =>
    setExpanded((prev) => {
      const next = new Set(prev);
      if (next.has(key)) next.delete(key);
      else next.add(key);
      return next;
    });

  const detail = useApiResource<Alert | null>(
    () => {
      const id = encodeURIComponent(selectedId ?? '');
      return api.getData<Alert>(`/api/v1/alerts/${id}`).then((d) => d ?? null);
    },
    { key: selectedId ?? '', enabled: !!selectedId },
  );

  const levelColor = (l: string) =>
    l === 'CRITICAL' ? t.red : l === 'ERROR' ? t.red : l === 'WARNING' ? t.amber : t.text2;

  const inputStyle: React.CSSProperties = {
    padding: '9px 12px', background: t.dark ? 'rgba(255,255,255,0.05)' : 'rgba(255,255,255,0.7)',
    border: '1px solid ' + t.panelBorder, borderRadius: '8px', color: t.text1,
  };

  return (
    <div style={{ maxWidth: '1100px', margin: '0 auto', width: '100%' }}>
      <div style={{ marginBottom: '20px' }}>
        <h2 style={{ fontSize: '24px', fontWeight: 700, margin: '0 0 8px', color: t.text1 }}>Alerts</h2>
        <p style={{ color: t.text2, fontSize: '13.5px', maxWidth: '560px', lineHeight: 1.6 }}>
          The raw alert stream. Related alerts are clustered into Incidents; here you see every
          individual signal as it fired.
        </p>
      </div>

      <div style={{ display: 'flex', gap: '10px', marginBottom: '16px', flexWrap: 'wrap' }}>
        <input placeholder="Filter by service…" value={service} onChange={(e) => setService(e.target.value)} style={{ ...inputStyle, minWidth: '200px' }} aria-label="Filter by service" />
        <select value={level} onChange={(e) => setLevel(e.target.value)} style={inputStyle} aria-label="Filter by level">
          {LEVELS.map((l) => <option key={l || 'all'} value={l}>{l || 'All levels'}</option>)}
        </select>
        <button
          onClick={() => { setGrouped((g) => !g); setSelectedId(null); }}
          aria-pressed={grouped}
          style={{
            ...inputStyle, cursor: 'pointer', fontWeight: 600, fontSize: '13px',
            background: grouped ? t.accent : inputStyle.background,
            color: grouped ? '#fff' : t.text1, borderColor: grouped ? t.accent : t.panelBorder,
          }}
          title="Collapse near-identical alerts into deduplicated groups"
        >
          {grouped ? '⊟ Grouped' : '⊞ Group similar'}
        </button>
        <button
          onClick={() => setShowSilences((v) => !v)}
          aria-pressed={showSilences}
          style={{
            ...inputStyle, cursor: 'pointer', fontWeight: 600, fontSize: '13px',
            background: showSilences ? t.accent : inputStyle.background,
            color: showSilences ? '#fff' : t.text1, borderColor: showSilences ? t.accent : t.panelBorder,
          }}
          title="Manage alert silences / maintenance windows"
        >
          🔇 Silences{silenceList.length > 0 ? ` (${silenceList.length})` : ''}
        </button>
      </div>

      {showSilences && (
        <SilencesPanel
          silences={silenceList}
          onCreate={createSilence}
          onDelete={deleteSilence}
          defaultService={service}
          defaultLevel={level}
          t={t}
        />
      )}

      <div style={{ display: 'flex', gap: '20px', alignItems: 'flex-start', flexWrap: 'wrap' }}>
        <div style={{ flex: 1, minWidth: '320px' }}>
          {grouped ? (
            <StateBoundary
              loading={groups.loading}
              error={groups.error}
              empty={groupList.length === 0}
              onRetry={groups.refetch}
              loadingLabel="Grouping alerts…"
              emptyLabel="No alerts match these filters."
            >
              <div style={{ display: 'flex', flexDirection: 'column', gap: '8px' }}>
                {groupList.map((g) => {
                  const c = levelColor(g.level);
                  const isOpen = expanded.has(g.key);
                  return (
                    <div key={g.key} style={{ background: t.panelBg, border: '1px solid ' + t.panelBorder, borderLeft: `3px solid ${c}`, borderRadius: '12px', overflow: 'hidden' }}>
                      <div onClick={() => toggleExpanded(g.key)} style={{ padding: '12px 14px', cursor: 'pointer' }}>
                        <div style={{ display: 'flex', justifyContent: 'space-between', gap: '10px', alignItems: 'center' }}>
                          <span style={{ fontWeight: 600, color: t.text1, fontSize: '13.5px' }}>
                            <span style={{ color: t.text2, marginRight: '6px', fontSize: '11px' }}>{isOpen ? '▾' : '▸'}</span>
                            {g.service}
                          </span>
                          <span style={{ display: 'flex', gap: '8px', alignItems: 'center' }}>
                            <span style={{ background: c, color: '#fff', fontWeight: 700, fontSize: '11px', borderRadius: '999px', padding: '1px 8px' }}>×{g.count}</span>
                            <span style={{ color: c, fontWeight: 700, fontSize: '11px' }}>{g.level}</span>
                          </span>
                        </div>
                        <div style={{ color: t.text2, fontSize: '12.5px', marginTop: '4px', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{g.sample}</div>
                        <div style={{ color: t.text2, fontSize: '11.5px', marginTop: '4px' }}>
                          {g.count === 1
                            ? new Date(g.last_seen).toLocaleString()
                            : `${g.count} alerts · ${new Date(g.first_seen).toLocaleString()} → ${new Date(g.last_seen).toLocaleString()}`}
                        </div>
                      </div>
                      {isOpen && (
                        <div style={{ borderTop: '1px solid ' + t.panelBorder, padding: '6px 8px 8px', display: 'flex', flexDirection: 'column', gap: '4px' }}>
                          {(g.instances ?? []).map((a) => (
                            <div
                              key={a.id}
                              onClick={() => setSelectedId(a.id)}
                              style={{ padding: '7px 10px', borderRadius: '8px', cursor: 'pointer', background: selectedId === a.id ? (t.dark ? 'rgba(255,255,255,0.06)' : 'rgba(0,0,0,0.04)') : 'transparent', display: 'flex', justifyContent: 'space-between', gap: '10px' }}
                            >
                              <span style={{ color: t.text2, fontSize: '12px', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{a.message}</span>
                              <span style={{ color: t.text2, fontSize: '11px', flexShrink: 0 }}>{new Date(a.triggered_at).toLocaleTimeString()}</span>
                            </div>
                          ))}
                        </div>
                      )}
                    </div>
                  );
                })}
              </div>
            </StateBoundary>
          ) : (
            <StateBoundary
              loading={alerts.loading}
              error={alerts.error}
              empty={list.length === 0}
              onRetry={alerts.refetch}
              loadingLabel="Loading alerts…"
              emptyLabel="No alerts match these filters."
            >
              <div style={{ display: 'flex', flexDirection: 'column', gap: '8px' }}>
                {list.map((a) => {
                  const c = levelColor(a.level);
                  const isSel = selectedId === a.id;
                  return (
                    <div
                      key={a.id}
                      onClick={() => setSelectedId(a.id)}
                      style={{ background: t.panelBg, border: '1px solid ' + (isSel ? t.accent : t.panelBorder), borderLeft: `3px solid ${c}`, borderRadius: '12px', padding: '12px 14px', cursor: 'pointer', opacity: a.silenced ? 0.5 : 1 }}
                    >
                      <div style={{ display: 'flex', justifyContent: 'space-between', gap: '10px', alignItems: 'center' }}>
                        <span style={{ fontWeight: 600, color: t.text1, fontSize: '13.5px' }}>
                          {a.service}
                          {a.silenced && <span style={{ marginLeft: '8px', fontSize: '10px', fontWeight: 700, color: t.text2, border: '1px solid ' + t.panelBorder, borderRadius: '100px', padding: '1px 7px' }}>🔇 silenced</span>}
                        </span>
                        <span style={{ display: 'inline-flex', gap: '8px', alignItems: 'center' }}>
                          {!a.silenced && (
                            <button
                              onClick={(e) => { e.stopPropagation(); silenceAlert(a); }}
                              title={`Silence ${a.service}/${a.level} for 1 hour`}
                              style={{ background: 'none', border: '1px solid ' + t.panelBorder, borderRadius: '7px', color: t.text2, cursor: 'pointer', fontSize: '10.5px', padding: '2px 7px' }}
                            >
                              🔇 Silence
                            </button>
                          )}
                          <span style={{ color: c, fontWeight: 700, fontSize: '11px' }}>{a.level}</span>
                        </span>
                      </div>
                      <div style={{ color: t.text2, fontSize: '12.5px', marginTop: '4px', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{a.message}</div>
                      <div style={{ color: t.text2, fontSize: '11.5px', marginTop: '4px' }}>{new Date(a.triggered_at).toLocaleString()}</div>
                    </div>
                  );
                })}
              </div>
            </StateBoundary>
          )}
        </div>

        {selectedId && (
          <div style={{ width: '360px', flexShrink: 0, background: t.panelBg, border: '1px solid ' + t.panelBorder, borderRadius: '14px', padding: '18px', position: 'sticky', top: '20px' }}>
            <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: '12px' }}>
              <h3 style={{ fontSize: '15px', fontWeight: 700, margin: 0, color: t.text1 }}>Alert detail</h3>
              <button onClick={() => setSelectedId(null)} aria-label="Close detail" style={{ background: 'none', border: 'none', color: t.text2, cursor: 'pointer', fontSize: '18px', lineHeight: 1 }}>×</button>
            </div>
            <StateBoundary loading={detail.loading} error={detail.error} onRetry={detail.refetch} loadingLabel="Loading…">
              {detail.data && (
                <div style={{ display: 'flex', flexDirection: 'column', gap: '10px', fontSize: '13px' }}>
                  <Field label="Service" value={detail.data.service} t={t} />
                  <Field label="Level" value={detail.data.level} t={t} color={levelColor(detail.data.level)} />
                  <div>
                    <div style={{ color: t.text2, fontSize: '11.5px', marginBottom: '3px' }}>Message</div>
                    <div style={{ color: t.text1, lineHeight: 1.5 }}>{detail.data.message}</div>
                  </div>
                  {detail.data.trace_id && <Field label="Trace" value={detail.data.trace_id} mono t={t} />}
                  <Field label="Triggered" value={new Date(detail.data.triggered_at).toLocaleString()} t={t} />
                  <Field label="Alert ID" value={detail.data.id} mono t={t} />
                </div>
              )}
            </StateBoundary>
          </div>
        )}
      </div>
    </div>
  );
}

function Field({ label, value, mono, color, t }: { label: string; value: string; mono?: boolean; color?: string; t: ReturnType<typeof useTheme>['tokens'] }) {
  return (
    <div>
      <div style={{ color: t.text2, fontSize: '11.5px', marginBottom: '3px' }}>{label}</div>
      <div style={{ color: color ?? t.text1, fontFamily: mono ? 'monospace' : 'inherit', fontSize: mono ? '12px' : '13px', wordBreak: 'break-all' }}>{value}</div>
    </div>
  );
}

// SilencesPanel manages alert silences (Alerts · E2): a create form (matcher +
// duration) and the list of existing silences with delete.
function SilencesPanel({
  silences, onCreate, onDelete, defaultService, defaultLevel, t,
}: {
  silences: AlertSilence[];
  onCreate: (matcher: { service?: string; level?: string; message_contains?: string }, hours: number) => Promise<void>;
  onDelete: (id: string) => Promise<void>;
  defaultService: string;
  defaultLevel: string;
  t: ReturnType<typeof useTheme>['tokens'];
}) {
  const [svc, setSvc] = useState(defaultService);
  const [lvl, setLvl] = useState(defaultLevel);
  const [msg, setMsg] = useState('');
  const [hours, setHours] = useState(1);
  const [busy, setBusy] = useState(false);
  // A ticking clock so the ACTIVE/expired badge stays accurate without reading
  // the clock during render (react-hooks/purity).
  const [nowMs, setNowMs] = useState<number>(() => Date.now());
  useEffect(() => {
    const id = setInterval(() => setNowMs(Date.now()), 30000);
    return () => clearInterval(id);
  }, []);

  const field: React.CSSProperties = { padding: '7px 10px', fontSize: '12.5px', background: t.dark ? 'rgba(255,255,255,0.05)' : '#fff', border: '1px solid ' + t.panelBorder, borderRadius: '8px', color: t.text1 };

  const submit = async () => {
    setBusy(true);
    try {
      await onCreate(
        { service: svc.trim() || undefined, level: lvl || undefined, message_contains: msg.trim() || undefined },
        hours,
      );
      setMsg('');
    } finally { setBusy(false); }
  };

  const fmtWindow = (s: AlertSilence) => `${new Date(s.starts_at).toLocaleString()} → ${new Date(s.ends_at).toLocaleString()}`;
  const isActive = (s: AlertSilence) => nowMs >= Date.parse(s.starts_at) && nowMs < Date.parse(s.ends_at);
  const describe = (m: AlertSilence['matcher']) => {
    const parts = [m.service && `service=${m.service}`, m.level && `level=${m.level}`, m.message_contains && `msg~"${m.message_contains}"`].filter(Boolean);
    return parts.length ? parts.join(' · ') : 'all alerts (blanket window)';
  };

  return (
    <div style={{ background: t.panelBg, border: '1px solid ' + t.panelBorder, borderRadius: '14px', padding: '16px', marginBottom: '16px' }}>
      <div style={{ fontSize: '13px', fontWeight: 700, color: t.text1, marginBottom: '10px' }}>Alert silences · maintenance windows</div>
      <div style={{ display: 'flex', gap: '8px', flexWrap: 'wrap', alignItems: 'center', marginBottom: '14px' }}>
        <input value={svc} onChange={(e) => setSvc(e.target.value)} placeholder="service (any)" aria-label="Silence service" style={{ ...field, width: '150px' }} />
        <select value={lvl} onChange={(e) => setLvl(e.target.value)} aria-label="Silence level" style={field}>
          {LEVELS.map((l) => <option key={l || 'any'} value={l}>{l || 'any level'}</option>)}
        </select>
        <input value={msg} onChange={(e) => setMsg(e.target.value)} placeholder="message contains… (any)" aria-label="Silence message contains" style={{ ...field, minWidth: '180px', flex: 1 }} />
        <select value={hours} onChange={(e) => setHours(Number(e.target.value))} aria-label="Silence duration" style={field}>
          <option value={1}>1 hour</option>
          <option value={4}>4 hours</option>
          <option value={24}>1 day</option>
          <option value={168}>1 week</option>
        </select>
        <button onClick={submit} disabled={busy} style={{ padding: '7px 16px', borderRadius: '8px', border: 'none', background: t.accent, color: '#fff', fontWeight: 600, fontSize: '12.5px', cursor: busy ? 'not-allowed' : 'pointer', opacity: busy ? 0.6 : 1 }}>Add silence</button>
      </div>

      {silences.length === 0 ? (
        <div style={{ color: t.text2, fontSize: '12.5px' }}>No silences configured.</div>
      ) : (
        <div style={{ display: 'flex', flexDirection: 'column', gap: '6px' }}>
          {silences.map((s) => (
            <div key={s.id} style={{ display: 'flex', alignItems: 'center', gap: '10px', padding: '8px 10px', borderRadius: '8px', background: t.dark ? 'rgba(255,255,255,0.03)' : 'rgba(0,0,0,0.02)' }}>
              <span style={{ fontSize: '10px', fontWeight: 700, color: isActive(s) ? t.green : t.text2, border: '1px solid ' + (isActive(s) ? t.green : t.panelBorder), borderRadius: '100px', padding: '1px 8px' }}>{isActive(s) ? 'ACTIVE' : 'scheduled/expired'}</span>
              <span style={{ fontSize: '12.5px', color: t.text1, fontWeight: 600 }}>{describe(s.matcher)}</span>
              <span style={{ fontSize: '11px', color: t.text2, flex: 1, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{fmtWindow(s)}</span>
              <button onClick={() => onDelete(s.id)} title="Delete silence" style={{ background: 'none', border: 'none', color: t.text2, cursor: 'pointer', fontSize: '15px', lineHeight: 1 }}>×</button>
            </div>
          ))}
        </div>
      )}
    </div>
  );
}
