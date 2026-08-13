"use client";

// Usage & quota dashboard (Settings · E1). Shows each metered signal's
// month-to-date consumption against the plan quota, its daily trend, and a
// run-rate projection so a tenant sees a breach coming rather than hitting a wall.

import React, { useState, useEffect } from 'react';
import { fetchWithAuth } from '@/lib/api';
import { useTheme } from '@/context/ThemeContext';
import { LineChart, Line, ResponsiveContainer, Tooltip, XAxis } from 'recharts';

interface DayPoint { day: string; count: number }
interface Projection { projected: number; limit: number; used_pct: number; project_pct: number; will_exceed: boolean }
interface SignalReport { signal: string; total: number; limit: number; series: DayPoint[]; projection: Projection }
interface UsageSeries {
  plan: string;
  period: { from: string; to: string; days: number; elapsed_days: number };
  signals: SignalReport[];
}

function humanize(n: number): string {
  if (n >= 1_000_000_000) return (n / 1_000_000_000).toFixed(2) + 'B';
  if (n >= 1_000_000) return (n / 1_000_000).toFixed(2) + 'M';
  if (n >= 1_000) return (n / 1_000).toFixed(1) + 'K';
  return String(n);
}

export function UsagePanel() {
  const { tokens: t } = useTheme();
  const [data, setData] = useState<UsageSeries | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    fetchWithAuth('/api/v1/usage/series')
      .then(async res => { if (!res.ok) throw new Error(await res.text()); return res.json(); })
      .then((d: UsageSeries) => { setData(d); setError(null); })
      .catch(err => setError(err.message || 'Failed to load usage'))
      .finally(() => setLoading(false));
  }, []);

  const barColor = (p: Projection) => {
    if (p.limit <= 0) return t.accent;
    if (p.used_pct >= 100) return t.red;
    if (p.will_exceed) return t.amber;
    return t.green;
  };

  return (
    <div>
      <div style={{ marginBottom: '24px' }}>
        <h3 style={{ fontSize: '19px', fontWeight: 700, margin: '0 0 8px', color: t.text1 }}>Usage & Quota</h3>
        <p style={{ color: t.text2, fontSize: '13.5px', maxWidth: '560px', lineHeight: 1.6 }}>
          Month-to-date ingestion against your plan quota, with a run-rate projection to end of period.
          {data && <> Plan: <strong style={{ color: t.text1, textTransform: 'capitalize' }}>{data.plan}</strong> · day {data.period.elapsed_days} of {data.period.days}.</>}
        </p>
      </div>

      {loading ? (
        <div style={{ color: t.text2, fontSize: '13.5px', padding: '20px 0' }}>Loading usage…</div>
      ) : error ? (
        <div style={{ padding: '16px', background: t.redSoft, color: t.red, borderRadius: '8px' }}>{error}</div>
      ) : !data || data.signals.length === 0 ? (
        <div style={{ color: t.text2, fontSize: '13.5px' }}>No usage recorded this period yet.</div>
      ) : (
        <div style={{ display: 'flex', flexDirection: 'column', gap: '16px' }}>
          {data.signals.map(s => {
            const p = s.projection;
            const unlimited = s.limit <= 0;
            const usedPct = Math.max(0, Math.min(100, p.used_pct));
            return (
              <div key={s.signal} style={{ border: '1px solid ' + t.panelBorder, borderRadius: '14px', padding: '16px 18px', background: t.dark ? 'rgba(255,255,255,0.02)' : 'rgba(0,0,0,0.01)' }}>
                <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'baseline', gap: '12px', marginBottom: '10px' }}>
                  <span style={{ fontWeight: 700, color: t.text1, fontSize: '14px', textTransform: 'capitalize' }}>{s.signal}</span>
                  <span style={{ fontSize: '12.5px', color: t.text2 }}>
                    {humanize(s.total)}{unlimited ? '' : <> / {humanize(s.limit)} <span style={{ color: barColor(p), fontWeight: 700 }}>({usedPct.toFixed(0)}%)</span></>}
                  </span>
                </div>

                {/* Quota bar */}
                {!unlimited && (
                  <div style={{ height: '8px', borderRadius: '100px', background: t.dark ? 'rgba(255,255,255,0.08)' : 'rgba(0,0,0,0.06)', overflow: 'hidden', marginBottom: '10px' }}>
                    <div style={{ width: `${usedPct}%`, height: '100%', background: barColor(p), transition: 'width 0.3s' }} />
                  </div>
                )}

                <div style={{ display: 'flex', gap: '16px', alignItems: 'center', flexWrap: 'wrap' }}>
                  {/* Daily trend */}
                  <div style={{ flex: 1, minWidth: '180px', height: '52px' }}>
                    {s.series.length > 1 ? (
                      <ResponsiveContainer width="100%" height="100%">
                        <LineChart data={s.series}>
                          <XAxis dataKey="day" hide />
                          <Tooltip
                            contentStyle={{ background: t.panelBg, border: '1px solid ' + t.panelBorder, borderRadius: '8px', fontSize: '12px' }}
                            labelFormatter={(v) => new Date(String(v)).toLocaleDateString()}
                            formatter={(v) => [humanize(Number(v)), s.signal]}
                          />
                          <Line type="monotone" dataKey="count" stroke={t.accent} strokeWidth={1.6} dot={false} />
                        </LineChart>
                      </ResponsiveContainer>
                    ) : (
                      <span style={{ color: t.text2, fontSize: '12px' }}>Not enough days for a trend yet.</span>
                    )}
                  </div>

                  {/* Projection callout */}
                  {!unlimited && (
                    <div style={{ fontSize: '12px', color: p.will_exceed ? t.red : t.text2, fontWeight: p.will_exceed ? 700 : 400, minWidth: '190px', textAlign: 'right' }}>
                      {p.will_exceed
                        ? `⚠ Projected ${humanize(p.projected)} (${p.project_pct.toFixed(0)}%) — over quota by period end`
                        : `Projected ${humanize(p.projected)} (${p.project_pct.toFixed(0)}%) by period end`}
                    </div>
                  )}
                </div>
              </div>
            );
          })}
        </div>
      )}
    </div>
  );
}
