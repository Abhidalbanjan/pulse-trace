"use client";

// SLOs / error-budget / burn-rate screen (ROAD_TO_100 · F2).
//
// The correlation backend has a real SLO engine (definitions, per-window SLI
// computation, error budget, burn-rate breach alerts) but no view. This screen
// closes that parity gap: define SLOs, see budget-remaining gauges + burn rate +
// SLI trend per service, and read the recent budget-alert feed — all on the F0.4
// typed platform.

import React, { useState } from 'react';
import { useTheme } from '@/context/ThemeContext';
import { api, ApiError } from '@/lib/api/client';
import type { SLOBudgetAlert, SLODashboardItem, SLIType } from '@/lib/api/types';
import { useApiResource } from '@/lib/hooks/useApiResource';
import { StateBoundary, ConfirmDialog, useToast } from '@/components/ui';

function errMsg(err: unknown, fallback: string): string {
  return err instanceof ApiError || err instanceof Error ? err.message : fallback;
}

const SLI_TYPES: SLIType[] = ['availability', 'latency'];
const WINDOW_OPTIONS = [7, 14, 30, 90];

// Sparkline over the SLI trend — pure SVG, no dependency.
function Sparkline({ points, color }: { points: number[]; color: string }) {
  if (points.length < 2) return <div style={{ height: '32px' }} />;
  const min = Math.min(...points);
  const max = Math.max(...points);
  const span = max - min || 1;
  const w = 120;
  const h = 32;
  const d = points
    .map((p, i) => {
      const x = (i / (points.length - 1)) * w;
      const y = h - ((p - min) / span) * h;
      return `${i === 0 ? 'M' : 'L'}${x.toFixed(1)},${y.toFixed(1)}`;
    })
    .join(' ');
  return (
    <svg width={w} height={h} viewBox={`0 0 ${w} ${h}`} aria-hidden="true" style={{ display: 'block' }}>
      <path d={d} fill="none" stroke={color} strokeWidth={1.5} />
    </svg>
  );
}

export function SLOView() {
  const { tokens: t } = useTheme();
  const toast = useToast();

  const dashboard = useApiResource<SLODashboardItem[]>(
    () => api.getData<SLODashboardItem[]>('/api/v1/slo/dashboard').then((d) => d ?? []),
    { pollMs: 30000 },
  );
  const items = dashboard.data ?? [];

  const budgetAlerts = useApiResource<SLOBudgetAlert[]>(
    () => api.getData<SLOBudgetAlert[]>('/api/v1/slo/budget-alerts').then((d) => d ?? []),
    { pollMs: 30000 },
  );

  const [showForm, setShowForm] = useState(false);
  const [service, setService] = useState('');
  const [target, setTarget] = useState('99.9');
  const [sliType, setSliType] = useState<SLIType>('availability');
  const [windowDays, setWindowDays] = useState(30);
  const [creating, setCreating] = useState(false);

  const [pendingDelete, setPendingDelete] = useState<SLODashboardItem | null>(null);
  const [deleting, setDeleting] = useState(false);

  const refreshAll = async () => { await Promise.all([dashboard.refetch(), budgetAlerts.refetch()]); };

  const createSLO = async (e: React.FormEvent) => {
    e.preventDefault();
    const targetNum = parseFloat(target);
    if (!service.trim() || creating) return;
    if (!Number.isFinite(targetNum) || targetNum <= 0 || targetNum > 100) {
      toast.error('Target must be a percentage between 0 and 100');
      return;
    }
    setCreating(true);
    try {
      await api.post('/api/v1/slo/definitions', {
        service_name: service.trim(),
        slo_target: targetNum,
        sli_type: sliType,
        window_days: windowDays,
      });
      setService('');
      setTarget('99.9');
      setShowForm(false);
      toast.success(`SLO for "${service.trim()}" created`);
      await refreshAll();
    } catch (err) {
      toast.error(`Error creating SLO: ${errMsg(err, 'request failed')}`);
    } finally {
      setCreating(false);
    }
  };

  const confirmDelete = async () => {
    if (!pendingDelete) return;
    setDeleting(true);
    try {
      await api.del(`/api/v1/slo/definitions/${encodeURIComponent(pendingDelete.definition.id)}`);
      toast.success(`SLO for "${pendingDelete.definition.service_name}" deleted`);
      setPendingDelete(null);
      await refreshAll();
    } catch (err) {
      toast.error(`Error deleting SLO: ${errMsg(err, 'request failed')}`);
    } finally {
      setDeleting(false);
    }
  };

  const statusColor = (s: string) => (s === 'critical' ? t.red : s === 'warning' ? t.amber : t.green);
  const sevColor = (s: string) => (s === 'critical' ? t.red : s === 'warning' ? t.amber : t.text2);

  const primaryBtnStyle: React.CSSProperties = {
    padding: '10px 18px', borderRadius: '10px', border: 'none',
    background: `linear-gradient(135deg, ${t.accent}, ${t.accent2})`, color: '#fff',
    fontWeight: 600, fontSize: '13px', cursor: 'pointer', flexShrink: 0,
  };
  const inputStyle: React.CSSProperties = {
    padding: '10px 12px', background: t.dark ? 'rgba(255,255,255,0.05)' : 'rgba(255,255,255,0.7)',
    border: '1px solid ' + t.panelBorder, borderRadius: '8px', color: t.text1,
  };

  return (
    <div style={{ maxWidth: '1100px', margin: '0 auto', width: '100%' }}>
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'flex-start', marginBottom: '24px', gap: '16px', flexWrap: 'wrap' }}>
        <div>
          <h2 style={{ fontSize: '24px', fontWeight: 700, margin: '0 0 8px', color: t.text1 }}>Service Level Objectives</h2>
          <p style={{ color: t.text2, fontSize: '13.5px', maxWidth: '560px', lineHeight: 1.6 }}>
            Track error budgets and burn rate per service. Budgets are computed over each SLO&apos;s
            window from real SLI measurements; a fast burn raises a budget alert.
          </p>
        </div>
        <button onClick={() => setShowForm((v) => !v)} style={primaryBtnStyle}>{showForm ? 'Cancel' : '+ New SLO'}</button>
      </div>

      {showForm && (
        <form onSubmit={createSLO} style={{ background: t.panelBg, border: '1px solid ' + t.panelBorder, borderRadius: '16px', padding: '20px', marginBottom: '24px', display: 'flex', gap: '12px', flexWrap: 'wrap', alignItems: 'flex-end' }}>
          <label style={{ display: 'flex', flexDirection: 'column', gap: '6px', flex: 1, minWidth: '180px' }}>
            <span style={{ fontSize: '12px', color: t.text2 }}>Service</span>
            <input required placeholder="e.g. payment-service" value={service} onChange={(e) => setService(e.target.value)} style={inputStyle} />
          </label>
          <label style={{ display: 'flex', flexDirection: 'column', gap: '6px' }}>
            <span style={{ fontSize: '12px', color: t.text2 }}>Target %</span>
            <input required type="number" step="0.01" min="0" max="100" value={target} onChange={(e) => setTarget(e.target.value)} style={{ ...inputStyle, width: '110px' }} />
          </label>
          <label style={{ display: 'flex', flexDirection: 'column', gap: '6px' }}>
            <span style={{ fontSize: '12px', color: t.text2 }}>SLI type</span>
            <select value={sliType} onChange={(e) => setSliType(e.target.value)} style={inputStyle} aria-label="SLI type">
              {SLI_TYPES.map((s) => <option key={s} value={s}>{s}</option>)}
            </select>
          </label>
          <label style={{ display: 'flex', flexDirection: 'column', gap: '6px' }}>
            <span style={{ fontSize: '12px', color: t.text2 }}>Window</span>
            <select value={windowDays} onChange={(e) => setWindowDays(parseInt(e.target.value, 10))} style={inputStyle} aria-label="Window (days)">
              {WINDOW_OPTIONS.map((d) => <option key={d} value={d}>{d} days</option>)}
            </select>
          </label>
          <button type="submit" disabled={!service.trim() || creating} style={{ ...primaryBtnStyle, opacity: !service.trim() || creating ? 0.5 : 1, cursor: !service.trim() || creating ? 'not-allowed' : 'pointer' }}>
            {creating ? 'Creating…' : 'Create SLO'}
          </button>
        </form>
      )}

      <StateBoundary
        loading={dashboard.loading}
        error={dashboard.error}
        empty={items.length === 0}
        onRetry={dashboard.refetch}
        loadingLabel="Loading SLOs…"
        emptyLabel="No SLOs defined yet. Create one to start tracking error budgets."
      >
        <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fill, minmax(320px, 1fr))', gap: '16px', marginBottom: '32px' }}>
          {items.map((it) => {
            const c = statusColor(it.status);
            const remaining = Math.max(0, Math.min(100, it.budget_remaining_pct));
            return (
              <div key={it.definition.id} style={{ background: t.panelBg, border: '1px solid ' + t.panelBorder, borderRadius: '16px', padding: '18px' }}>
                <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'flex-start', marginBottom: '12px', gap: '8px' }}>
                  <div>
                    <div style={{ fontWeight: 700, fontSize: '15px', color: t.text1 }}>{it.definition.service_name}</div>
                    <div style={{ fontSize: '12px', color: t.text2 }}>
                      {it.definition.slo_target}% {it.definition.sli_type} · {it.definition.window_days}d
                    </div>
                  </div>
                  <span style={{ background: c + '22', color: c, padding: '3px 10px', borderRadius: '100px', fontSize: '11px', fontWeight: 700 }}>{it.status}</span>
                </div>

                <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'flex-end', marginBottom: '6px' }}>
                  <span style={{ fontSize: '12px', color: t.text2 }}>Error budget remaining</span>
                  <span style={{ fontSize: '13px', fontWeight: 700, color: c }}>{remaining.toFixed(1)}%</span>
                </div>
                <div style={{ height: '8px', background: t.dark ? 'rgba(255,255,255,0.08)' : 'rgba(0,0,0,0.06)', borderRadius: '100px', overflow: 'hidden', marginBottom: '14px' }}>
                  <div style={{ width: `${remaining}%`, height: '100%', background: c, transition: 'width 0.3s' }} />
                </div>

                <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center' }}>
                  <div style={{ display: 'flex', flexDirection: 'column', gap: '3px' }}>
                    <span style={{ fontSize: '12px', color: t.text2 }}>Current SLI <strong style={{ color: t.text1 }}>{it.current_sli.toFixed(2)}%</strong></span>
                    <span style={{ fontSize: '12px', color: t.text2 }}>Burn rate <strong style={{ color: it.burn_rate > 1 ? t.red : t.text1 }}>{it.burn_rate.toFixed(2)}×</strong></span>
                  </div>
                  <Sparkline points={it.trend.map((p) => p.sli_value)} color={c} />
                </div>

                <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginTop: '12px', paddingTop: '12px', borderTop: '1px solid ' + t.panelBorder }}>
                  <span style={{ fontSize: '11.5px', color: t.text2 }}>
                    {Math.round(it.budget_used_min)} / {Math.round(it.budget_total_min)} min used
                  </span>
                  <button onClick={() => setPendingDelete(it)} style={{ padding: '5px 11px', fontSize: '12px', borderRadius: '8px', border: '1px solid ' + t.red, background: 'transparent', color: t.red, cursor: 'pointer' }}>Delete</button>
                </div>
              </div>
            );
          })}
        </div>
      </StateBoundary>

      <h3 style={{ fontSize: '17px', fontWeight: 700, margin: '0 0 14px', color: t.text1 }}>Budget Alerts</h3>
      <StateBoundary
        loading={budgetAlerts.loading}
        error={budgetAlerts.error}
        empty={(budgetAlerts.data ?? []).length === 0}
        onRetry={budgetAlerts.refetch}
        loadingLabel="Loading budget alerts…"
        emptyLabel="No burn-rate breaches recorded."
      >
        <div style={{ display: 'flex', flexDirection: 'column', gap: '10px' }}>
          {(budgetAlerts.data ?? []).map((a) => {
            const c = sevColor(a.severity);
            return (
              <div key={a.id} style={{ background: t.panelBg, border: '1px solid ' + t.panelBorder, borderLeft: `3px solid ${c}`, borderRadius: '12px', padding: '14px 16px', display: 'flex', justifyContent: 'space-between', gap: '12px', flexWrap: 'wrap' }}>
                <div>
                  <div style={{ fontSize: '13.5px', color: t.text1, fontWeight: 600 }}>{a.service_name} <span style={{ color: c, fontWeight: 700, textTransform: 'uppercase', fontSize: '11px' }}>· {a.severity}</span></div>
                  <div style={{ fontSize: '12.5px', color: t.text2, marginTop: '3px' }}>{a.message}</div>
                </div>
                <div style={{ textAlign: 'right', whiteSpace: 'nowrap' }}>
                  <div style={{ fontSize: '12.5px', color: t.text1 }}>{a.burn_rate.toFixed(2)}× burn · {a.budget_remaining_pct.toFixed(0)}% left</div>
                  <div style={{ fontSize: '11.5px', color: t.text2 }}>{new Date(a.triggered_at).toLocaleString()}</div>
                </div>
              </div>
            );
          })}
        </div>
      </StateBoundary>

      <ConfirmDialog
        open={pendingDelete !== null}
        danger
        busy={deleting}
        title={`Delete SLO for "${pendingDelete?.definition.service_name ?? ''}"?`}
        body="This removes the objective and stops tracking its error budget. Recorded snapshots are retained."
        confirmLabel="Delete SLO"
        onConfirm={confirmDelete}
        onCancel={() => setPendingDelete(null)}
      />
    </div>
  );
}
