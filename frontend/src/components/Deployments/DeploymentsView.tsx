"use client";

// Shift-left deploy gates (ROAD_TO_100 · F5).
//
// Wired to the real feed: the GitHub webhook runs each PR through PulseTrace's
// SLO-risk evaluator and records the verdict; this screen renders that recorded
// history (GET /api/v1/deployments/gates) on the F0.4 typed platform. Read-only
// by design — the gate decision is returned to GitHub as a commit status, so
// there's no in-app override to fake.

import React, { useState } from 'react';
import { useTheme } from '@/context/ThemeContext';
import { api } from '@/lib/api/client';
import type { DeployGate } from '@/lib/api/types';
import { useApiResource } from '@/lib/hooks/useApiResource';
import { StateBoundary } from '@/components/ui';

interface DORAMetrics {
  window_days: number;
  total_deploys: number;
  deploy_frequency_per_day: number; deploy_frequency_rating: string;
  change_failure_rate_pct: number; change_failure_rating: string;
  mttr_minutes: number; mttr_rating: string;
  resolved_incidents: number;
}

export function DeploymentsView() {
  const { tokens: t } = useTheme();
  const [showSetup, setShowSetup] = useState(false);

  const gates = useApiResource<DeployGate[]>(
    () => api.getData<DeployGate[]>('/api/v1/deployments/gates').then((d) => d ?? []),
    { pollMs: 20000 },
  );
  const list = gates.data ?? [];

  // DORA scorecard (Deploy Gates · E2).
  const dora = useApiResource<DORAMetrics | null>(
    () => api.get<DORAMetrics>('/api/v1/deployments/dora').then((d) => d ?? null),
    { pollMs: 60000 },
  );

  const ratingColor = (r: string) => (r === 'elite' ? t.green : r === 'high' ? t.accent : r === 'medium' ? t.amber : r === 'n/a' ? t.text2 : t.red);
  const fmtMTTR = (min: number) => (min <= 0 ? '—' : min < 60 ? `${Math.round(min)}m` : min < 24 * 60 ? `${(min / 60).toFixed(1)}h` : `${(min / 1440).toFixed(1)}d`);
  const fmtFreq = (perDay: number) => (perDay >= 1 ? `${perDay.toFixed(1)}/day` : perDay > 0 ? `${(perDay * 7).toFixed(1)}/wk` : '—');

  const webhookUrl = typeof window !== 'undefined' ? `${window.location.origin}/api/v1/webhooks/github` : '/api/v1/webhooks/github';

  return (
    <div style={{ padding: '40px', maxWidth: '1200px', margin: '0 auto', width: '100%', height: 'calc(100vh - 120px)', overflowY: 'auto' }}>
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'flex-start', marginBottom: '20px', gap: '16px', flexWrap: 'wrap' }}>
        <div>
          <h2 style={{ fontSize: '26px', fontWeight: 700, margin: '0 0 8px' }}>Shift-Left Deployment Gates</h2>
          <p style={{ color: t.text2, fontSize: '14.5px' }}>Pull requests automatically analyzed for SLO-violation risk by PulseTrace before they merge.</p>
        </div>
        <button
          onClick={() => setShowSetup((v) => !v)}
          style={{ padding: '10px 18px', borderRadius: '10px', border: '1px solid ' + t.panelBorder, background: 'transparent', color: t.text1, fontWeight: 600, fontSize: '13.5px', cursor: 'pointer' }}
        >
          {showSetup ? 'Hide setup' : 'Configure webhook'}
        </button>
      </div>

      {/* DORA scorecard (Deploy Gates · E2) */}
      {dora.data && (
        <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(180px, 1fr))', gap: '12px', marginBottom: '24px' }}>
          {[
            { label: 'Deploy frequency', value: fmtFreq(dora.data.deploy_frequency_per_day), rating: dora.data.deploy_frequency_rating, sub: `${dora.data.total_deploys} deploys · ${Math.round(dora.data.window_days)}d` },
            { label: 'Change-failure rate', value: `${dora.data.change_failure_rate_pct.toFixed(0)}%`, rating: dora.data.change_failure_rating, sub: 'deploys followed by an incident' },
            { label: 'Time to restore (MTTR)', value: fmtMTTR(dora.data.mttr_minutes), rating: dora.data.mttr_rating, sub: `${dora.data.resolved_incidents} resolved incidents` },
            { label: 'Lead time', value: 'n/a', rating: 'n/a', sub: 'needs commit timestamps' },
          ].map((tile) => (
            <div key={tile.label} style={{ background: t.panelBg, border: '1px solid ' + t.panelBorder, borderRadius: '14px', padding: '16px 18px' }}>
              <div style={{ fontSize: '12px', color: t.text2, marginBottom: '8px' }}>{tile.label}</div>
              <div style={{ display: 'flex', alignItems: 'baseline', gap: '10px' }}>
                <span style={{ fontSize: '24px', fontWeight: 700, color: t.text1 }}>{tile.value}</span>
                <span style={{ fontSize: '11px', fontWeight: 700, textTransform: 'uppercase', color: ratingColor(tile.rating) }}>{tile.rating}</span>
              </div>
              <div style={{ fontSize: '11.5px', color: t.text2, marginTop: '6px' }}>{tile.sub}</div>
            </div>
          ))}
        </div>
      )}

      {showSetup && (
        <div style={{ background: t.panelBg, border: '1px solid ' + t.panelBorder, borderRadius: '14px', padding: '18px', marginBottom: '20px' }}>
          <div style={{ fontWeight: 700, color: t.text1, marginBottom: '8px' }}>GitHub webhook setup</div>
          <p style={{ color: t.text2, fontSize: '13px', lineHeight: 1.6, margin: '0 0 10px', maxWidth: '640px' }}>
            In your repo → Settings → Webhooks → Add webhook, set the payload URL below, content type
            <code style={{ margin: '0 4px' }}>application/json</code>, the <strong style={{ color: t.text1 }}>Pull requests</strong> event, and
            a secret matching <code>GITHUB_WEBHOOK_SECRET</code> on the gateway (HMAC-verified when set).
          </p>
          <code style={{ display: 'block', fontFamily: 'monospace', fontSize: '13px', color: t.accent, background: t.dark ? 'rgba(0,0,0,0.3)' : 'rgba(0,0,0,0.05)', padding: '10px 12px', borderRadius: '8px', border: '1px solid ' + t.panelBorder, wordBreak: 'break-all' }}>
            {webhookUrl}
          </code>
        </div>
      )}

      <div style={{ borderRadius: '20px', overflow: 'hidden', background: t.panelBg, border: '1px solid ' + t.panelBorder, boxShadow: t.shadow }}>
        <StateBoundary
          loading={gates.loading}
          error={gates.error}
          empty={list.length === 0}
          onRetry={gates.refetch}
          loadingLabel="Loading deploy gates…"
          emptyLabel="No deployment gates yet. Pull-request activity analyzed by PulseTrace will appear here once the webhook is configured."
        >
          <table style={{ width: '100%', borderCollapse: 'collapse', textAlign: 'left' }}>
            <thead>
              <tr style={{ borderBottom: '1px solid ' + t.panelBorder, background: t.dark ? 'rgba(0,0,0,0.15)' : 'rgba(0,0,0,0.03)' }}>
                {['Pull Request', 'Repository', 'AI Decision', 'When'].map((h) => (
                  <th key={h} style={{ padding: '16px', fontWeight: 600, color: t.text2, fontSize: '12.5px' }}>{h}</th>
                ))}
              </tr>
            </thead>
            <tbody>
              {list.map((g) => {
                const blocked = g.decision === 'BLOCK';
                return (
                  <tr key={g.id} style={{ borderBottom: '1px solid ' + t.panelBorder, background: blocked ? (t.dark ? 'rgba(241,107,99,0.06)' : 'rgba(224,82,75,0.04)') : 'transparent' }}>
                    <td style={{ padding: '18px 16px' }}>
                      <div style={{ fontWeight: 700, marginBottom: '4px', color: t.text1 }}>
                        {g.pr_url ? <a href={g.pr_url} target="_blank" rel="noopener noreferrer" style={{ color: t.text1, textDecoration: 'none' }}>{g.title}</a> : g.title}
                      </div>
                      <div style={{ fontSize: '12.5px', color: t.text2 }}>#{g.pr_number}{g.author ? ` by ${g.author}` : ''}</div>
                    </td>
                    <td style={{ padding: '18px 16px', fontSize: '13.5px', color: t.accent }}>{g.repo || '—'}</td>
                    <td style={{ padding: '18px 16px' }}>
                      <span style={{ background: blocked ? t.red : t.green, color: '#fff', padding: '4px 12px', borderRadius: '100px', fontSize: '11px', fontWeight: 700 }}>{g.decision}</span>
                      <div style={{ fontSize: '12px', color: t.text2, marginTop: '6px', maxWidth: '320px' }}>{g.reason}</div>
                    </td>
                    <td style={{ padding: '18px 16px', fontSize: '12.5px', color: t.text2 }}>{new Date(g.created_at).toLocaleString()}</td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </StateBoundary>
      </div>
    </div>
  );
}
