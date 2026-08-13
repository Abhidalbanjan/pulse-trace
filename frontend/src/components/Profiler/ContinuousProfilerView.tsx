"use client";

import React, { useState, useEffect, useCallback } from 'react';
import { useSearchParams } from 'next/navigation';
import { fetchWithAuth } from '@/lib/api';
import { PROFILED_SERVICES } from '@/lib/profiledServices';
import { useTheme } from '@/context/ThemeContext';
import { FlameGraph, type FlameFrame } from './FlameGraph';

const TIME_RANGE_SECONDS: Record<string, number> = {
  'Last 15 minutes': 15 * 60,
  'Last 1 hour': 60 * 60,
  'Last 24 hours': 24 * 60 * 60,
};

interface FuncStat { name: string; self: number; pct: number }
interface FuncDiff {
  name: string;
  baseline_pct: number;
  comparison_pct: number;
  delta_pct: number;
  regression: boolean;
}

export function ContinuousProfilerView() {
  const { tokens: t } = useTheme();
  const searchParams = useSearchParams();
  const spanId = searchParams.get('spanId');
  const requestedService = searchParams.get('service');

  const [profileType, setProfileType] = useState('process_cpu');
  const [service, setService] = useState(
    requestedService && PROFILED_SERVICES.includes(requestedService) ? requestedService : PROFILED_SERVICES[0]
  );
  const [timeRange, setTimeRange] = useState('Last 1 hour');
  const [compareMode, setCompareMode] = useState(false);

  const [functions, setFunctions] = useState<FuncStat[]>([]);
  const [flame, setFlame] = useState<FlameFrame[]>([]);
  const [total, setTotal] = useState(0);
  const [flatView, setFlatView] = useState<'flame' | 'list'>('flame');
  const [diffs, setDiffs] = useState<FuncDiff[]>([]);
  const [regressionCount, setRegressionCount] = useState(0);
  // Diff flame graph (Profiler · E2).
  const [diffFlame, setDiffFlame] = useState<FlameFrame[]>([]);
  const [deltaByName, setDeltaByName] = useState<Record<string, number>>({});
  const [compTotal, setCompTotal] = useState(0);
  const [compareView, setCompareView] = useState<'flame' | 'table'>('flame');
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  const load = useCallback(() => {
    setLoading(true);
    setError(null);
    const rangeSeconds = TIME_RANGE_SECONDS[timeRange] || 3600;
    const params = new URLSearchParams({ service, profile_type: profileType, range_seconds: String(rangeSeconds) });
    if (spanId) params.set('span_id', spanId);
    const path = compareMode ? 'diff' : 'functions';
    fetchWithAuth(`/api/v1/profiler/${path}?${params.toString()}`)
      .then(async res => { if (!res.ok) throw new Error(await res.text()); return res.json(); })
      .then(data => {
        if (compareMode) {
          setDiffs(data.functions || []);
          setRegressionCount(data.regression_count || 0);
          setDiffFlame(data.flame || []);
          setDeltaByName(data.delta_by_name || {});
          setCompTotal(data.comparison_total || 0);
        } else {
          setFunctions(data.functions || []);
          setFlame(data.flame || []);
          setTotal(data.total || 0);
        }
      })
      .catch(err => setError(err.message || 'Failed to load profile'))
      .finally(() => setLoading(false));
  }, [service, profileType, timeRange, compareMode, spanId]);

  // eslint-disable-next-line react-hooks/set-state-in-effect -- load reacts to the toolbar selectors and sets state from the API response; the leading setLoading(true) is the intended pending state
  useEffect(() => { load(); }, [load]);

  const selectStyle: React.CSSProperties = {
    background: t.dark ? 'rgba(255,255,255,0.06)' : 'rgba(255,255,255,0.7)',
    border: '1px solid ' + t.panelBorder, color: t.text1, padding: '9px 13px', borderRadius: '10px', fontSize: '13px', minWidth: '180px',
  };

  const panelStyle: React.CSSProperties = {
    flex: 1, borderRadius: '20px', overflow: 'hidden', background: t.panelBg,
    border: '1px solid ' + t.panelBorder, backdropFilter: 'blur(30px) saturate(180%)', boxShadow: t.shadow, minHeight: '420px', display: 'flex', flexDirection: 'column',
  };

  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: '24px', height: '100%' }}>

      {/* Header */}
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center' }}>
        <div>
          <h2 style={{ fontSize: '26px', fontWeight: 700, margin: '0 0 8px' }}>Continuous Profiler</h2>
          <p style={{ color: t.text2, fontSize: '14.5px' }}>
            {spanId ? (
              <>Filtered to span <code style={{ background: t.dark ? 'rgba(255,255,255,0.1)' : 'rgba(0,0,0,0.06)', padding: '2px 6px', borderRadius: '4px' }}>{spanId}</code> — {' '}
                <a href="/profiler" style={{ color: t.accent }}>clear filter</a>
              </>
            ) : (
              'The hottest code paths in production, and how they regress release-over-release.'
            )}
          </p>
        </div>
      </div>

      {/* Toolbar */}
      <div style={{ display: 'flex', gap: '12px', padding: '16px', borderRadius: '18px', background: t.panelBg, border: '1px solid ' + t.panelBorder, backdropFilter: 'blur(30px)', flexWrap: 'wrap', alignItems: 'center' }}>
        <select value={service} onChange={(e) => setService(e.target.value)} style={selectStyle}>
          {PROFILED_SERVICES.map(s => <option key={s} value={s}>{s}</option>)}
        </select>
        <select value={profileType} onChange={(e) => setProfileType(e.target.value)} style={selectStyle}>
          <option value="process_cpu">CPU (process_cpu)</option>
          <option value="memory_alloc_objects">Memory Allocations</option>
          <option value="memory_inuse_space">Memory In-Use</option>
        </select>
        <select value={timeRange} onChange={(e) => setTimeRange(e.target.value)} style={selectStyle}>
          <option>Last 15 minutes</option>
          <option>Last 1 hour</option>
          <option>Last 24 hours</option>
        </select>
        <div style={{ flex: 1 }} />
        <button
          onClick={() => setCompareMode(prev => !prev)}
          style={{ padding: '10px 22px', borderRadius: '10px', border: 'none', background: compareMode ? t.accent : `linear-gradient(135deg, ${t.accent}, ${t.accent2})`, color: '#fff', fontWeight: 600, fontSize: '13px', cursor: 'pointer' }}
        >
          {compareMode ? 'Back to Flat Profile' : 'Detect Regressions'}
        </button>
      </div>

      {/* Regression callout (compare mode) */}
      {compareMode && !loading && !error && (
        <div style={{
          padding: '14px 20px', borderRadius: '14px',
          background: regressionCount > 0 ? (t.dark ? 'rgba(241,107,99,0.12)' : 'rgba(224,82,75,0.08)') : (t.dark ? 'rgba(52,199,126,0.12)' : 'rgba(37,169,107,0.08)'),
          border: '1px solid ' + (regressionCount > 0 ? t.red : t.green), color: regressionCount > 0 ? t.red : t.green, fontSize: '13.5px', fontWeight: 600,
        }}>
          {regressionCount > 0
            ? `▲ ${regressionCount} function${regressionCount > 1 ? 's' : ''} regressed vs. the preceding ${timeRange.toLowerCase().replace('last ', '')} — their share of ${profileType} grew.`
            : `✓ No profile regressions vs. the preceding period.`}
        </div>
      )}

      {/* Data panel */}
      <div style={panelStyle}>
        <div style={{ padding: '16px 24px', borderBottom: '1px solid ' + t.panelBorder, fontSize: '14px', fontWeight: 700, display: 'flex', justifyContent: 'space-between', alignItems: 'center', gap: '12px' }}>
          <span>
            {compareMode
              ? `Regression diff — ${service} (${profileType}): ${timeRange} vs. preceding period`
              : flatView === 'flame'
                ? `Flame graph — ${service} (${profileType})`
                : `Top functions by self time — ${service} (${profileType})`}
          </span>
          {!compareMode ? (
            <div style={{ display: 'flex', gap: '2px', background: t.dark ? 'rgba(255,255,255,0.06)' : 'rgba(0,0,0,0.05)', borderRadius: '9px', padding: '2px' }}>
              {(['flame', 'list'] as const).map(v => (
                <button
                  key={v}
                  onClick={() => setFlatView(v)}
                  style={{ padding: '6px 14px', borderRadius: '7px', border: 'none', cursor: 'pointer', fontSize: '12px', fontWeight: 600, textTransform: 'capitalize', background: flatView === v ? t.accent : 'transparent', color: flatView === v ? '#fff' : t.text2 }}
                >
                  {v === 'flame' ? '🔥 Flame' : '☰ List'}
                </button>
              ))}
            </div>
          ) : (
            <div style={{ display: 'flex', gap: '2px', background: t.dark ? 'rgba(255,255,255,0.06)' : 'rgba(0,0,0,0.05)', borderRadius: '9px', padding: '2px' }}>
              {(['flame', 'table'] as const).map(v => (
                <button
                  key={v}
                  onClick={() => setCompareView(v)}
                  style={{ padding: '6px 14px', borderRadius: '7px', border: 'none', cursor: 'pointer', fontSize: '12px', fontWeight: 600, textTransform: 'capitalize', background: compareView === v ? t.accent : 'transparent', color: compareView === v ? '#fff' : t.text2 }}
                >
                  {v === 'flame' ? '🔥 Diff flame' : '☰ Table'}
                </button>
              ))}
            </div>
          )}
        </div>

        <div style={{ flex: 1, overflow: 'auto' }}>
          {loading ? (
            <div style={{ padding: '48px', textAlign: 'center', color: t.text2 }}>Loading profile…</div>
          ) : error ? (
            <div style={{ padding: '48px', textAlign: 'center', color: t.red }}>{error}</div>
          ) : compareMode && compareView === 'flame' ? (
            diffFlame.length === 0 ? (
              <div style={{ padding: '48px', textAlign: 'center', color: t.text2 }}>No profile samples in either window yet.</div>
            ) : (
              <div style={{ padding: '16px 24px' }}>
                <div style={{ display: 'flex', gap: '16px', marginBottom: '12px', fontSize: '12px', color: t.text2, alignItems: 'center' }}>
                  <span><span style={{ display: 'inline-block', width: 10, height: 10, borderRadius: 2, background: 'rgba(224,82,75,0.7)', marginRight: 5 }} />grew vs baseline</span>
                  <span><span style={{ display: 'inline-block', width: 10, height: 10, borderRadius: 2, background: 'rgba(37,169,107,0.7)', marginRight: 5 }} />shrank</span>
                  <span><span style={{ display: 'inline-block', width: 10, height: 10, borderRadius: 2, background: t.dark ? 'rgba(255,255,255,0.14)' : 'rgba(0,0,0,0.14)', marginRight: 5 }} />unchanged</span>
                </div>
                <FlameGraph frames={diffFlame} rootTotal={compTotal} t={t} deltaByName={deltaByName} />
              </div>
            )
          ) : compareMode ? (
            diffs.length === 0 ? (
              <div style={{ padding: '48px', textAlign: 'center', color: t.text2 }}>No profile samples in either window yet.</div>
            ) : (
              <table style={{ width: '100%', borderCollapse: 'collapse', textAlign: 'left' }}>
                <thead>
                  <tr style={{ borderBottom: '1px solid ' + t.panelBorder, background: t.dark ? 'rgba(0,0,0,0.15)' : 'rgba(0,0,0,0.03)' }}>
                    <th style={{ padding: '13px 24px', fontWeight: 600, color: t.text2, fontSize: '12.5px' }}>Function</th>
                    <th style={{ padding: '13px 16px', fontWeight: 600, color: t.text2, fontSize: '12.5px' }}>Baseline</th>
                    <th style={{ padding: '13px 16px', fontWeight: 600, color: t.text2, fontSize: '12.5px' }}>Current</th>
                    <th style={{ padding: '13px 16px', fontWeight: 600, color: t.text2, fontSize: '12.5px' }}>Δ (share)</th>
                  </tr>
                </thead>
                <tbody>
                  {diffs.map((d, i) => {
                    const up = d.delta_pct > 0;
                    const color = d.regression ? t.red : d.delta_pct < 0 ? t.green : t.text2;
                    return (
                      <tr key={i} style={{ borderBottom: '1px solid ' + t.panelBorder }}>
                        <td style={{ padding: '12px 24px', fontFamily: 'monospace', fontSize: '12.5px', color: t.text1, maxWidth: '460px', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }} title={d.name}>{d.name}</td>
                        <td style={{ padding: '12px 16px', fontSize: '13px', color: t.text2 }}>{d.baseline_pct.toFixed(1)}%</td>
                        <td style={{ padding: '12px 16px', fontSize: '13px', color: t.text1 }}>{d.comparison_pct.toFixed(1)}%</td>
                        <td style={{ padding: '12px 16px', fontSize: '13px', fontWeight: 700, color }}>
                          {up ? '▲' : d.delta_pct < 0 ? '▼' : ''} {d.delta_pct > 0 ? '+' : ''}{d.delta_pct.toFixed(1)} pp
                        </td>
                      </tr>
                    );
                  })}
                </tbody>
              </table>
            )
          ) : functions.length === 0 ? (
            <div style={{ padding: '48px', textAlign: 'center', color: t.text2 }}>No profile samples in this window yet. Profiling data appears once the selected service reports to Pyroscope.</div>
          ) : flatView === 'flame' ? (
            <div style={{ padding: '16px 24px' }}>
              <FlameGraph frames={flame} rootTotal={total} t={t} />
            </div>
          ) : (
            <div style={{ padding: '12px 24px' }}>
              {functions.map((f, i) => (
                <div key={i} style={{ padding: '9px 0', borderBottom: '1px solid ' + t.panelBorder }}>
                  <div style={{ display: 'flex', justifyContent: 'space-between', gap: '16px', marginBottom: '5px' }}>
                    <span style={{ fontFamily: 'monospace', fontSize: '12.5px', color: t.text1, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }} title={f.name}>{f.name}</span>
                    <span style={{ fontSize: '12.5px', fontWeight: 700, color: t.accent, flexShrink: 0 }}>{f.pct.toFixed(1)}%</span>
                  </div>
                  <div style={{ height: '6px', borderRadius: '100px', background: t.dark ? 'rgba(255,255,255,0.08)' : 'rgba(0,0,0,0.06)' }}>
                    <div style={{ width: `${Math.min(f.pct, 100)}%`, height: '100%', borderRadius: '100px', background: `linear-gradient(90deg, ${t.accent}, ${t.accent2})` }} />
                  </div>
                </div>
              ))}
            </div>
          )}
        </div>
      </div>

    </div>
  );
}
