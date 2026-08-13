"use client";

import React, { useState, useEffect } from 'react';
import { errMessage } from '@/lib/errMessage';
import { fetchWithAuth } from '@/lib/api';
import { useTheme } from '@/context/ThemeContext';

interface SyntheticResult {
  URL: string;
  check_name?: string;
  uptime_percent: number;
  avg_latency_ms: number;
  latency_history?: number[];
  last_failure?: string;
}

interface UptimeBucket { start: string; total: number; success: number; uptime_pct: number; status: 'up' | 'degraded' | 'down' | 'no-data'; }
interface UptimeSummary { target: string; uptime_pct: number; total: number; success: number; buckets: UptimeBucket[]; }

const METHODS = ['GET', 'HEAD', 'POST'];

// AvailabilityStrip renders the 24h red/green SLA timeline (Synthetics · E2).
function AvailabilityStrip({ summary, t }: { summary: UptimeSummary; t: ReturnType<typeof useTheme>['tokens'] }) {
  const color = (s: UptimeBucket['status']) =>
    s === 'up' ? t.green : s === 'degraded' ? t.amber : s === 'down' ? t.red : (t.dark ? 'rgba(255,255,255,0.12)' : 'rgba(0,0,0,0.1)');
  if (summary.buckets.length === 0) {
    return <div style={{ color: t.text2, fontSize: '12.5px', padding: '8px 0' }}>No probe history in this window.</div>;
  }
  return (
    <div>
      <div style={{ display: 'flex', gap: '2px', height: '28px', borderRadius: '5px', overflow: 'hidden' }}>
        {summary.buckets.map((b, i) => (
          <div
            key={i}
            style={{ flex: 1, background: color(b.status), minWidth: '2px' }}
            title={`${new Date(b.start.replace(' ', 'T') + 'Z').toLocaleString()} · ${b.status}${b.total ? ` · ${b.success}/${b.total} ok` : ''}`}
          />
        ))}
      </div>
      <div style={{ display: 'flex', justifyContent: 'space-between', marginTop: '6px', fontSize: '11.5px', color: t.text2 }}>
        <span>{summary.buckets.length} buckets · last 24h</span>
        <span style={{ color: summary.uptime_pct >= 99.9 ? t.green : summary.uptime_pct >= 99 ? t.amber : t.red, fontWeight: 700 }}>
          {summary.total > 0 ? `${summary.uptime_pct.toFixed(3)}% uptime` : 'no data'}
        </span>
      </div>
    </div>
  );
}

// A step in the builder form. Assertion fields are strings (raw input) and are
// only sent when non-empty, so an unset field means "no assertion", not zero.
interface StepForm {
  method: string;
  url: string;
  status: string;
  maxLatency: string;
  bodyContains: string;
}

const emptyStep = (): StepForm => ({ method: 'GET', url: '', status: '', maxLatency: '', bodyContains: '' });

// Sparkline renders a compact latency trend from the recent history array.
function Sparkline({ data, color }: { data: number[]; color: string }) {
  if (!data || data.length < 2) return <span style={{ opacity: 0.4, fontSize: '11px' }}>—</span>;
  const w = 90, h = 24;
  const max = Math.max(...data), min = Math.min(...data);
  const span = max - min || 1;
  const pts = data.map((v, i) => {
    const x = (i / (data.length - 1)) * w;
    const y = h - ((v - min) / span) * (h - 3) - 1.5;
    return `${x.toFixed(1)},${y.toFixed(1)}`;
  }).join(' ');
  return (
    <svg width={w} height={h} style={{ display: 'block' }}>
      <polyline points={pts} fill="none" stroke={color} strokeWidth={1.5} strokeLinejoin="round" strokeLinecap="round" />
    </svg>
  );
}

export function SyntheticsView() {
  const { tokens: t } = useTheme();
  const [results, setResults] = useState<SyntheticResult[]>([]);
  const [loading, setLoading] = useState(true);
  const [showModal, setShowModal] = useState(false);
  const [name, setName] = useState('');
  const [steps, setSteps] = useState<StepForm[]>([emptyStep()]);
  const [submitting, setSubmitting] = useState(false);
  const [formError, setFormError] = useState<string | null>(null);

  // Uptime timeline (Synthetics · E2): which check's SLA strip is expanded.
  const [uptimeTarget, setUptimeTarget] = useState<string | null>(null);
  const [uptime, setUptime] = useState<UptimeSummary | null>(null);
  const [uptimeLoading, setUptimeLoading] = useState(false);

  const toggleUptime = (r: SyntheticResult) => {
    const key = r.check_name || r.URL;
    if (uptimeTarget === key) { setUptimeTarget(null); return; }
    setUptimeTarget(key);
    setUptime(null);
    setUptimeLoading(true);
    fetchWithAuth(`/api/v1/synthetics/uptime?target=${encodeURIComponent(key)}`)
      .then(res => (res.ok ? res.json() : null))
      .then((data: UptimeSummary | null) => setUptime(data))
      .catch(() => setUptime(null))
      .finally(() => setUptimeLoading(false));
  };

  const fetchResults = () => {
    fetchWithAuth('/api/v1/synthetics/results')
      .then(res => res.json())
      .then(data => { if (data && data.data) setResults(data.data); })
      .catch(console.error)
      .finally(() => setLoading(false));
  };

  useEffect(() => {
    fetchResults();
    // Load configured checks so a just-created (not-yet-probed) check still shows.
    fetchWithAuth('/api/v1/synthetics/tests')
      .then(res => res.json())
      .then(data => {
        const checks = data?.data || [];
        setResults(prev => {
          const seen = new Set(prev.map(r => r.URL));
          const pending = checks
            .filter((c: { url: string }) => !seen.has(c.url))
            .map((c: { url: string; name?: string }) => ({ URL: c.url, check_name: c.name, uptime_percent: 100, avg_latency_ms: 0 }));
          return [...prev, ...pending];
        });
      })
      .catch(console.error);
  }, []);

  const updateStep = (i: number, patch: Partial<StepForm>) =>
    setSteps(prev => prev.map((s, idx) => (idx === i ? { ...s, ...patch } : s)));

  const resetForm = () => { setName(''); setSteps([emptyStep()]); setFormError(null); };

  const handleCreate = async (e: React.FormEvent) => {
    e.preventDefault();
    setFormError(null);
    const payloadSteps = steps
      .filter(s => s.url.trim())
      .map(s => {
        const assert: Record<string, unknown> = {};
        if (s.status.trim()) assert.status = Number(s.status);
        if (s.maxLatency.trim()) assert.max_latency_ms = Number(s.maxLatency);
        if (s.bodyContains.trim()) assert.body_contains = s.bodyContains;
        return { method: s.method, url: s.url.trim(), assert };
      });
    if (payloadSteps.length === 0) { setFormError('Add at least one step with a URL.'); return; }

    setSubmitting(true);
    try {
      const res = await fetchWithAuth('/api/v1/synthetics/tests', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ name: name.trim(), steps: payloadSteps }),
      });
      if (!res.ok) throw new Error(await res.text());
      setShowModal(false);
      resetForm();
      // Reflect the new check immediately; probe results fill in on the next poll.
      setResults(prev => (prev.some(r => r.URL === payloadSteps[0].url)
        ? prev
        : [...prev, { URL: payloadSteps[0].url, check_name: name.trim(), uptime_percent: 100, avg_latency_ms: 0 }]));
      setTimeout(fetchResults, 2500);
    } catch (err) {
      setFormError(errMessage(err));
    } finally {
      setSubmitting(false);
    }
  };

  const handleDelete = async (url: string) => {
    if (!confirm(`Stop monitoring ${url}?`)) return;
    try {
      const res = await fetchWithAuth(`/api/v1/synthetics/tests?url=${encodeURIComponent(url)}`, { method: 'DELETE' });
      if (!res.ok) throw new Error(await res.text());
      setResults(prev => prev.filter(r => r.URL !== url));
    } catch (err) {
      alert(`Failed to delete test: ${errMessage(err)}`);
    }
  };

  const totalTests = results.length;
  const failingTests = results.filter(r => r.uptime_percent < 100).length;
  const globalUptime = totalTests > 0
    ? (results.reduce((acc, r) => acc + r.uptime_percent, 0) / totalTests).toFixed(2)
    : 0;

  const primaryButtonStyle: React.CSSProperties = {
    padding: '11px 22px', borderRadius: '11px', border: 'none',
    background: `linear-gradient(135deg, ${t.accent}, ${t.accent2})`, color: '#fff', fontWeight: 600, fontSize: '13.5px', cursor: 'pointer',
  };
  const secondaryButtonStyle: React.CSSProperties = {
    padding: '11px 22px', borderRadius: '11px', border: '1px solid ' + t.panelBorder, background: 'transparent', color: t.text2, fontWeight: 600, fontSize: '13.5px', cursor: 'pointer',
  };
  const kpiTileStyle: React.CSSProperties = {
    flex: 1, padding: '22px', borderRadius: '18px', background: t.panelBg, border: '1px solid ' + t.panelBorder, backdropFilter: 'blur(30px) saturate(180%)', boxShadow: t.shadow,
  };
  const fieldStyle: React.CSSProperties = {
    padding: '9px 11px', background: t.dark ? 'rgba(255,255,255,0.05)' : 'rgba(0,0,0,0.03)', border: '1px solid ' + t.panelBorder, borderRadius: '9px', color: t.text1, fontSize: '13px', boxSizing: 'border-box',
  };
  const labelStyle: React.CSSProperties = { display: 'block', fontSize: '11.5px', color: t.text2, marginBottom: '5px' };

  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: '18px', height: '100%', overflow: 'auto' }}>

      {/* Header */}
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: '20px' }}>
        <div>
          <h2 style={{ fontSize: '26px', fontWeight: 700, margin: '0 0 8px' }}>Synthetic Monitoring</h2>
          <p style={{ color: t.text2, fontSize: '14.5px' }}>Multi-step checks with assertions that page on-call when they fail.</p>
        </div>
        <button onClick={() => { resetForm(); setShowModal(true); }} style={primaryButtonStyle}>Create Check</button>
      </div>

      {showModal && (
        <div style={{ position: 'fixed', inset: 0, background: 'rgba(0,0,0,0.5)', backdropFilter: 'blur(6px)', zIndex: 1000, display: 'flex', alignItems: 'center', justifyContent: 'center', padding: '24px' }}>
          <div style={{ background: t.panelBg, border: '1px solid ' + t.panelBorder, backdropFilter: 'blur(30px) saturate(180%)', boxShadow: t.shadow, padding: '28px', borderRadius: '20px', width: 'min(640px, 94vw)', maxHeight: '88vh', overflowY: 'auto', color: t.text1 }}>
            <h3 style={{ fontSize: '20px', fontWeight: 700, margin: '0 0 20px' }}>Create Synthetic Check</h3>
            <form onSubmit={handleCreate} style={{ display: 'flex', flexDirection: 'column', gap: '18px' }}>
              <div>
                <label style={labelStyle}>Check name</label>
                <input value={name} onChange={e => setName(e.target.value)} placeholder="e.g. Checkout flow" style={{ ...fieldStyle, width: '100%' }} />
              </div>

              {steps.map((s, i) => (
                <div key={i} style={{ border: '1px solid ' + t.panelBorder, borderRadius: '12px', padding: '16px', position: 'relative' }}>
                  <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: '12px' }}>
                    <span style={{ fontSize: '12px', fontWeight: 700, color: t.text2, letterSpacing: '0.04em' }}>STEP {i + 1}</span>
                    {steps.length > 1 && (
                      <button type="button" onClick={() => setSteps(prev => prev.filter((_, idx) => idx !== i))} style={{ background: 'transparent', border: 'none', color: t.text2, cursor: 'pointer', fontSize: '15px' }}>✕</button>
                    )}
                  </div>
                  <div style={{ display: 'flex', gap: '10px', marginBottom: '12px' }}>
                    <div style={{ width: '110px' }}>
                      <label style={labelStyle}>Method</label>
                      <select value={s.method} onChange={e => updateStep(i, { method: e.target.value })} style={{ ...fieldStyle, width: '100%' }}>
                        {METHODS.map(m => <option key={m} value={m}>{m}</option>)}
                      </select>
                    </div>
                    <div style={{ flex: 1 }}>
                      <label style={labelStyle}>URL</label>
                      <input type="url" required value={s.url} onChange={e => updateStep(i, { url: e.target.value })} placeholder="https://api.acme.io/health" style={{ ...fieldStyle, width: '100%' }} />
                    </div>
                  </div>
                  <div style={{ display: 'flex', gap: '10px' }}>
                    <div style={{ flex: 1 }}>
                      <label style={labelStyle}>Expect status</label>
                      <input value={s.status} onChange={e => updateStep(i, { status: e.target.value.replace(/[^0-9]/g, '') })} placeholder="2xx" style={{ ...fieldStyle, width: '100%' }} />
                    </div>
                    <div style={{ flex: 1 }}>
                      <label style={labelStyle}>Max latency (ms)</label>
                      <input value={s.maxLatency} onChange={e => updateStep(i, { maxLatency: e.target.value.replace(/[^0-9]/g, '') })} placeholder="none" style={{ ...fieldStyle, width: '100%' }} />
                    </div>
                    <div style={{ flex: 2 }}>
                      <label style={labelStyle}>Body contains</label>
                      <input value={s.bodyContains} onChange={e => updateStep(i, { bodyContains: e.target.value })} placeholder="optional substring" style={{ ...fieldStyle, width: '100%' }} />
                    </div>
                  </div>
                </div>
              ))}

              <button type="button" onClick={() => setSteps(prev => [...prev, emptyStep()])} style={{ ...secondaryButtonStyle, alignSelf: 'flex-start', padding: '8px 14px' }}>
                + Add step
              </button>

              {formError && <div style={{ fontSize: '12.5px', color: t.red }}>{formError}</div>}

              <div style={{ display: 'flex', gap: '12px', marginTop: '4px' }}>
                <button type="button" onClick={() => setShowModal(false)} style={{ ...secondaryButtonStyle, flex: 1 }}>Cancel</button>
                <button type="submit" disabled={submitting} style={{ ...primaryButtonStyle, flex: 1, opacity: submitting ? 0.7 : 1 }}>{submitting ? 'Saving…' : 'Start monitoring'}</button>
              </div>
            </form>
          </div>
        </div>
      )}

      {loading ? (
        <div style={{ padding: '48px', textAlign: 'center', color: t.text2 }}>Loading synthetics data...</div>
      ) : (
        <div style={{ display: 'flex', flexDirection: 'column', gap: '18px' }}>

          {/* KPI Row */}
          <div style={{ display: 'flex', gap: '16px', marginBottom: '18px' }}>
            <div style={kpiTileStyle}>
              <div style={{ fontSize: '13px', color: t.text2, marginBottom: '8px' }}>Global Uptime</div>
              <div style={{ fontSize: '30px', fontWeight: 700, color: t.green }}>{globalUptime}%</div>
            </div>
            <div style={kpiTileStyle}>
              <div style={{ fontSize: '13px', color: t.text2, marginBottom: '8px' }}>Active Checks</div>
              <div style={{ fontSize: '30px', fontWeight: 700, color: t.text1 }}>{totalTests}</div>
            </div>
            <div style={kpiTileStyle}>
              <div style={{ fontSize: '13px', color: t.text2, marginBottom: '8px' }}>Failing Checks</div>
              <div style={{ fontSize: '30px', fontWeight: 700, color: t.red }}>{failingTests}</div>
            </div>
          </div>

          {/* Table */}
          <div style={{ borderRadius: '20px', background: t.panelBg, border: '1px solid ' + t.panelBorder, backdropFilter: 'blur(30px) saturate(180%)', boxShadow: t.shadow, overflow: 'hidden' }}>
            <table style={{ width: '100%', borderCollapse: 'collapse', textAlign: 'left' }}>
              <thead>
                <tr style={{ borderBottom: '1px solid ' + t.panelBorder, background: t.dark ? 'rgba(0,0,0,0.15)' : 'rgba(0,0,0,0.03)' }}>
                  <th style={{ padding: '16px', fontWeight: 600, color: t.text2, fontSize: '12.5px' }}>Status</th>
                  <th style={{ padding: '16px', fontWeight: 600, color: t.text2, fontSize: '12.5px' }}>Endpoint</th>
                  <th style={{ padding: '16px', fontWeight: 600, color: t.text2, fontSize: '12.5px' }}>Latency Trend</th>
                  <th style={{ padding: '16px', fontWeight: 600, color: t.text2, fontSize: '12.5px' }}>Avg</th>
                  <th style={{ padding: '16px', fontWeight: 600, color: t.text2, fontSize: '12.5px' }}>Uptime (1h)</th>
                  <th style={{ padding: '16px', fontWeight: 600, color: t.text2, fontSize: '12.5px' }}>Last Failure</th>
                  <th style={{ padding: '16px', fontWeight: 600, color: t.text2, fontSize: '12.5px', width: '48px' }}></th>
                </tr>
              </thead>
              <tbody>
                {results.length === 0 ? (
                  <tr><td colSpan={7} style={{ padding: '48px', textAlign: 'center', color: t.text2 }}>No synthetic checks yet. Create one to start monitoring.</td></tr>
                ) : (
                  results.map((r, i) => {
                    const healthy = r.uptime_percent === 100;
                    const key = r.check_name || r.URL;
                    const isOpen = uptimeTarget === key;
                    return (
                      <React.Fragment key={i}>
                      <tr style={{ borderBottom: isOpen ? 'none' : '1px solid ' + t.panelBorder }}>
                        <td style={{ padding: '16px' }}>
                          <div style={{ width: '10px', height: '10px', borderRadius: '50%', background: healthy ? t.green : t.red }} />
                        </td>
                        <td style={{ padding: '16px', fontSize: '13px', color: t.text1 }}>
                          {r.check_name && <div style={{ fontWeight: 600, marginBottom: '2px' }}>{r.check_name}</div>}
                          <div style={{ fontFamily: 'monospace', color: t.text2, fontSize: '12px', maxWidth: '320px', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{r.URL}</div>
                        </td>
                        <td style={{ padding: '16px' }}><Sparkline data={r.latency_history || []} color={healthy ? t.accent : t.red} /></td>
                        <td style={{ padding: '16px', fontWeight: 600, fontSize: '13.5px', color: t.text1 }}>{Math.round(r.avg_latency_ms)} ms</td>
                        <td style={{ padding: '16px', fontSize: '13.5px' }}>
                          <button
                            onClick={() => toggleUptime(r)}
                            title="Show the 24h availability timeline"
                            style={{ background: 'transparent', border: 'none', cursor: 'pointer', padding: 0, display: 'inline-flex', alignItems: 'center', gap: '6px', color: r.uptime_percent >= 99.9 ? t.green : t.red, fontSize: '13.5px', fontWeight: 600 }}
                          >
                            <span style={{ color: t.text2, fontSize: '10px' }}>{isOpen ? '▾' : '▸'}</span>
                            {r.uptime_percent.toFixed(2)}%
                          </button>
                        </td>
                        <td style={{ padding: '16px', fontSize: '12px', color: r.last_failure ? t.red : t.text2, maxWidth: '220px' }}>{r.last_failure || '—'}</td>
                        <td style={{ padding: '16px', textAlign: 'right' }}>
                          <button onClick={() => handleDelete(r.URL)} title="Stop monitoring this endpoint" style={{ background: 'transparent', border: 'none', color: t.text2, cursor: 'pointer', fontSize: '16px', lineHeight: 1, padding: '2px 6px' }}>✕</button>
                        </td>
                      </tr>
                      {isOpen && (
                        <tr style={{ borderBottom: '1px solid ' + t.panelBorder }}>
                          <td colSpan={7} style={{ padding: '4px 16px 20px' }}>
                            <div style={{ fontSize: '11.5px', fontWeight: 700, letterSpacing: '0.04em', color: t.text2, margin: '4px 0 10px' }}>AVAILABILITY · LAST 24 HOURS</div>
                            {uptimeLoading ? (
                              <div style={{ color: t.text2, fontSize: '13px' }}>Loading timeline…</div>
                            ) : uptime ? (
                              <AvailabilityStrip summary={uptime} t={t} />
                            ) : (
                              <div style={{ color: t.text2, fontSize: '13px' }}>No uptime data available.</div>
                            )}
                          </td>
                        </tr>
                      )}
                      </React.Fragment>
                    );
                  })
                )}
              </tbody>
            </table>
          </div>

        </div>
      )}

    </div>
  );
}
