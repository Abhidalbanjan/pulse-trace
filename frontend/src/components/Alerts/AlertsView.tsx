"use client";

// Alerts screen (ROAD_TO_100 · Wave 2 parity).
//
// The raw alert stream from alert-service — the per-signal layer beneath
// Incidents (which cluster alerts). Lists alerts with service/level filters and
// opens a detail panel that fetches the canonical record by id, closing the
// GET /api/v1/alerts and GET /api/v1/alerts/{id} parity orphans.

import React, { useState } from 'react';
import { useTheme } from '@/context/ThemeContext';
import { api } from '@/lib/api/client';
import type { Alert } from '@/lib/api/types';
import { useApiResource } from '@/lib/hooks/useApiResource';
import { StateBoundary } from '@/components/ui';

const LEVELS = ['', 'CRITICAL', 'ERROR', 'WARNING', 'INFO'];

export function AlertsView() {
  const { tokens: t } = useTheme();

  const [service, setService] = useState('');
  const [level, setLevel] = useState('');
  const [selectedId, setSelectedId] = useState<string | null>(null);

  const query = `service=${encodeURIComponent(service)}&level=${encodeURIComponent(level)}`;
  const alerts = useApiResource<Alert[]>(
    () => api.list<Alert>(`/api/v1/alerts?${query}`).then((r) => r.items),
    { key: query, pollMs: 15000 },
  );
  const list = alerts.data ?? [];

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
      </div>

      <div style={{ display: 'flex', gap: '20px', alignItems: 'flex-start', flexWrap: 'wrap' }}>
        <div style={{ flex: 1, minWidth: '320px' }}>
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
                    style={{ background: t.panelBg, border: '1px solid ' + (isSel ? t.accent : t.panelBorder), borderLeft: `3px solid ${c}`, borderRadius: '12px', padding: '12px 14px', cursor: 'pointer' }}
                  >
                    <div style={{ display: 'flex', justifyContent: 'space-between', gap: '10px' }}>
                      <span style={{ fontWeight: 600, color: t.text1, fontSize: '13.5px' }}>{a.service}</span>
                      <span style={{ color: c, fontWeight: 700, fontSize: '11px' }}>{a.level}</span>
                    </div>
                    <div style={{ color: t.text2, fontSize: '12.5px', marginTop: '4px', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{a.message}</div>
                    <div style={{ color: t.text2, fontSize: '11.5px', marginTop: '4px' }}>{new Date(a.triggered_at).toLocaleString()}</div>
                  </div>
                );
              })}
            </div>
          </StateBoundary>
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
