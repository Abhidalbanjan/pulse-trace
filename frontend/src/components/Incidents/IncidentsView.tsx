"use client";

// Incidents screen (ROAD_TO_100 · F1).
//
// Rewritten onto the F0.4 typed platform: the list, per-incident detail, and
// timeline all come from the real API (no lossy `any` mapping, no hardcoded
// root-cause string, no fake "Suggested Runbooks" buttons). The detail pane now
// shows the true causal analysis and hosts the self-healing RemediationPanel,
// closing the F1 parity gap.

import React, { useState } from 'react';
import { useTheme } from '@/context/ThemeContext';
import { api } from '@/lib/api/client';
import type { Incident, IncidentTimelineEvent, CausalProviders } from '@/lib/api/types';
import { useApiResource } from '@/lib/hooks/useApiResource';
import { StateBoundary } from '@/components/ui';
import { RemediationPanel } from './RemediationPanel';
import { PostmortemPanel } from './PostmortemPanel';

export function IncidentsView() {
  const { tokens: t } = useTheme();

  // Live incident list (polled so new incidents surface without a manual reload).
  const incidents = useApiResource<Incident[]>(
    () => api.getData<Incident[]>('/api/v1/incidents').then((d) => d ?? []),
    { pollMs: 15000 },
  );
  const list = incidents.data ?? [];

  // Selection: sticky once the user clicks; defaults to the first incident so the
  // detail pane is never empty when there's data (derived, so no effect needed).
  const [selectedId, setSelectedId] = useState<string | null>(null);
  // Detail-pane tab: the live causal analysis vs. the AI-drafted postmortem (E1).
  const [detailTab, setDetailTab] = useState<'analysis' | 'postmortem'>('analysis');
  const effectiveId = selectedId ?? list[0]?.id ?? null;

  // Per-incident detail carries the full causal analysis + playbook; refetched
  // after a remediation action via RemediationPanel's onChanged.
  const detail = useApiResource<Incident | null>(
    () => {
      const id = encodeURIComponent(effectiveId ?? '');
      return api.getData<Incident>(`/api/v1/incidents/${id}`).then((d) => d ?? null);
    },
    { key: effectiveId ?? '', enabled: !!effectiveId },
  );
  const inc = detail.data;

  const timeline = useApiResource<IncidentTimelineEvent[]>(
    () => {
      const id = encodeURIComponent(effectiveId ?? '');
      return api.getData<IncidentTimelineEvent[]>(`/api/v1/incidents/${id}/timeline`).then((d) => d ?? []);
    },
    { key: effectiveId ?? '', enabled: !!effectiveId },
  );

  // Causal-AI provider chain health — deployment-wide, so fetched once and
  // polled slowly. Backs the analyzer-health badge; a failure here must never
  // break the incident view, so errors just leave the badge absent.
  const providers = useApiResource<CausalProviders | null>(
    () => api.getData<CausalProviders>('/api/v1/causal/providers').then((d) => d ?? null),
    { pollMs: 60000 },
  );

  const getSeverityColor = (severity: string) => {
    switch (severity) {
      case 'CRITICAL': return t.red;
      case 'ERROR': return t.amber;
      case 'WARNING': return t.amber;
      default: return t.green;
    }
  };

  const titleOf = (i: Incident) => i.title || `Incident in ${i.services?.[0] || 'Unknown'}`;
  const primaryService = (i?: Incident | null) => i?.causal?.chain?.[0]?.from_service || i?.services?.[0] || '';

  const panelStyle: React.CSSProperties = {
    background: t.panelBg,
    border: `1px solid ${t.panelBorder}`,
    borderTop: `1px solid ${t.panelTop}`,
    backdropFilter: 'blur(34px) saturate(180%)',
    WebkitBackdropFilter: 'blur(34px) saturate(180%)',
    borderRadius: '24px',
    boxShadow: t.shadow,
  };

  const criticalCount = list.filter((i) => i.severity === 'CRITICAL').length;

  // Provider-health badge: a small pill telling the operator whether the
  // flagship causal AI is running on a live LLM, degraded to a backup, or on
  // the deterministic rule-based analyzer — without opening a config file.
  const providerBadge = () => {
    const ph = providers.data;
    if (!ph) return null;
    if (!ph.llm_enabled) {
      return badgePill('Rule-based analyzer', t.text2, t.dark ? 'rgba(255,255,255,0.06)' : 'rgba(0,0,0,0.05)',
        'No LLM provider configured — causal analysis uses the deterministic rule-based engine.');
    }
    const plist = ph.providers ?? [];
    const healthy = plist.filter((p) => p.healthy);
    const allHealthy = plist.length > 0 && healthy.length === plist.length;
    const anyHealthy = healthy.length > 0;
    const color = allHealthy ? t.green : anyHealthy ? t.amber : t.red;
    const label = !anyHealthy
      ? 'Causal AI: all providers down'
      : allHealthy
        ? `Causal AI: ${healthy[0].name}`
        : `Causal AI: ${healthy[0].name} (backup)`;
    const title = plist
      .map((p) => `${p.name}: ${p.healthy ? 'healthy' : `cooling down${p.cooldown_remaining ? ` ${p.cooldown_remaining}` : ''} (${p.failures} failures)`}`)
      .join('\n');
    return (
      <span title={title} style={{ display: 'inline-flex', alignItems: 'center', gap: '6px', fontSize: '11px', fontWeight: 600, color: t.text2, background: t.dark ? 'rgba(255,255,255,0.06)' : 'rgba(0,0,0,0.05)', padding: '3px 10px', borderRadius: '100px' }}>
        <span style={{ width: '7px', height: '7px', borderRadius: '50%', background: color }} />
        {label}
      </span>
    );
  };

  // Grounding badge: whether the narrative shown survived the hallucination
  // guardrail intact ("Grounded") or had fabricated causal links removed
  // ("Adjusted"). This is what lets an on-call engineer trust the story.
  const groundingBadge = (inc: Incident) => {
    const g = inc.causal?.grounding;
    if (!g) return null;
    if (g.grounded) {
      return badgePill('✓ Grounded', t.green, `${t.green}18`,
        'Every causal link was verified against services in this incident’s evidence.');
    }
    const dropped = g.dropped_links ?? 0;
    const unknown = (g.unknown_services ?? []).join(', ');
    return badgePill('⚠ Adjusted', t.amber, `${t.amber}20`,
      `Guardrail removed ${dropped} unverifiable causal link(s)${unknown ? ` referencing: ${unknown}` : ''}. Confidence was capped.`);
  };

  function badgePill(label: string, fg: string, bg: string, title: string) {
    return (
      <span title={title} style={{ fontSize: '11px', fontWeight: 700, color: fg, background: bg, padding: '3px 10px', borderRadius: '100px', whiteSpace: 'nowrap' }}>
        {label}
      </span>
    );
  }

  return (
    <div style={{ display: 'flex', gap: '20px', height: 'calc(100vh - 124px)', minWidth: 0 }}>
      {/* Sidebar — incident list */}
      <div style={{ ...panelStyle, width: 'clamp(260px, 28vw, 380px)', flexShrink: 0, display: 'flex', flexDirection: 'column', overflow: 'hidden' }}>
        <div style={{ padding: '22px 22px', borderBottom: `1px solid ${t.panelBorder}`, display: 'flex', justifyContent: 'space-between', alignItems: 'center' }}>
          <h3 style={{ fontSize: '17px', fontWeight: 700, margin: 0, color: t.text1 }}>Active Incidents</h3>
          <span style={{ background: t.red, color: '#fff', padding: '5px 13px', borderRadius: '100px', fontSize: '11.5px', fontWeight: 700 }}>
            {criticalCount} CRITICAL
          </span>
        </div>

        <div style={{ flex: 1, overflowY: 'auto' }}>
          <StateBoundary
            loading={incidents.loading}
            error={incidents.error}
            empty={list.length === 0}
            onRetry={incidents.refetch}
            loadingLabel="Loading incidents…"
            emptyLabel="No active incidents."
          >
            {list.map((i) => {
              const isSelected = effectiveId === i.id;
              const severityColor = getSeverityColor(i.severity);
              return (
                <div
                  key={i.id}
                  onClick={() => setSelectedId(i.id)}
                  style={{
                    padding: '18px 22px',
                    borderBottom: `1px solid ${t.panelBorder}`,
                    cursor: 'pointer',
                    background: isSelected ? (t.dark ? 'rgba(255,255,255,0.06)' : 'rgba(255,255,255,0.5)') : 'transparent',
                    borderLeft: isSelected ? `3px solid ${severityColor}` : '3px solid transparent',
                    transition: '0.2s',
                  }}
                >
                  <div style={{ display: 'flex', justifyContent: 'space-between', marginBottom: '8px' }}>
                    <span style={{ fontSize: '11px', fontWeight: 700, color: severityColor, letterSpacing: '0.03em' }}>{i.severity}</span>
                    <span style={{ fontSize: '12px', color: t.text2 }}>
                      {new Date(i.started_at ?? 0).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })}
                    </span>
                  </div>
                  <h4 style={{ fontSize: '15px', fontWeight: 600, margin: '0 0 10px', lineHeight: 1.4, color: t.text1 }}>{titleOf(i)}</h4>
                  <div style={{ display: 'flex', gap: '8px', flexWrap: 'wrap' }}>
                    {(i.services ?? []).slice(0, 2).map((svc) => (
                      <span key={svc} style={{ fontSize: '11px', background: t.dark ? 'rgba(255,255,255,0.06)' : 'rgba(0,0,0,0.05)', padding: '4px 9px', borderRadius: '6px', color: t.text2 }}>{svc}</span>
                    ))}
                    {(i.services?.length ?? 0) > 2 && <span style={{ fontSize: '11px', color: t.text2 }}>+{(i.services?.length ?? 0) - 2} more</span>}
                  </div>
                </div>
              );
            })}
          </StateBoundary>
        </div>
      </div>

      {/* Main — detail, causal analysis, remediation, timeline */}
      <div style={{ ...panelStyle, flex: 1, minWidth: 0, overflowY: 'auto', overflowX: 'hidden', padding: 'clamp(20px, 3vw, 36px)' }}>
        {!effectiveId ? (
          <div style={{ height: '100%', display: 'flex', alignItems: 'center', justifyContent: 'center', color: t.text2 }}>
            Select an incident from the list to view details.
          </div>
        ) : (
          <StateBoundary loading={detail.loading} error={detail.error} onRetry={detail.refetch} loadingLabel="Loading incident…">
            {inc && (
              <div>
                <div style={{ marginBottom: '24px' }}>
                  <div style={{ display: 'flex', alignItems: 'center', gap: '12px', marginBottom: '12px', flexWrap: 'wrap' }}>
                    <span style={{ background: getSeverityColor(inc.severity), color: '#fff', padding: '5px 14px', borderRadius: '100px', fontSize: '12px', fontWeight: 700 }}>{inc.severity}</span>
                    <span style={{ color: t.text2, fontSize: '13.5px' }}>ID: {inc.id}</span>
                    <span style={{ color: t.text2, fontSize: '13.5px' }}>Status: {inc.status}</span>
                    {inc.started_at && <span style={{ color: t.text2, fontSize: '13.5px' }}>Started: {new Date(inc.started_at).toLocaleString()}</span>}
                  </div>
                  <h2 style={{ fontSize: '26px', fontWeight: 700, margin: 0, color: t.text1 }}>{titleOf(inc)}</h2>
                </div>

                {/* Detail tabs: live analysis vs. AI-drafted postmortem (E1) */}
                <div style={{ display: 'flex', gap: '8px', marginBottom: '20px' }}>
                  {([['analysis', 'Analysis'], ['postmortem', 'Postmortem']] as const).map(([tab, label]) => (
                    <button
                      key={tab}
                      onClick={() => setDetailTab(tab)}
                      style={{
                        padding: '8px 16px', borderRadius: '10px', border: `1px solid ${t.panelBorder}`,
                        background: detailTab === tab ? t.accentSoft : 'transparent',
                        color: detailTab === tab ? t.accent : t.text2, fontWeight: 600, fontSize: '13px', cursor: 'pointer',
                      }}
                    >
                      {label}
                    </button>
                  ))}
                </div>

                {detailTab === 'postmortem' ? (
                  <PostmortemPanel incidentId={inc.id} />
                ) : (
                <div style={{ display: 'grid', gridTemplateColumns: 'minmax(0,1.6fr) minmax(0,1fr)', gap: 'clamp(16px,3vw,40px)' }}>
                  {/* Left: causal analysis */}
                  <div style={{ minWidth: 0 }}>
                    <div style={{ display: 'flex', alignItems: 'center', gap: '10px', marginBottom: '16px', flexWrap: 'wrap' }}>
                      <h3 style={{ fontSize: '17px', fontWeight: 700, margin: 0, color: t.text1 }}>&#10022; AI Root Cause Analysis</h3>
                      {inc.causal && groundingBadge(inc)}
                      {providerBadge()}
                      {inc.causal && (
                        <span style={{ fontSize: '11px', color: t.text2 }}>
                          {inc.causal.model ? `${inc.causal.model} · ` : ''}
                          {typeof inc.causal.confidence === 'number' ? `${Math.round(inc.causal.confidence * 100)}% confidence` : ''}
                        </span>
                      )}
                    </div>
                    <div style={{ background: t.accentSoft, border: `1px solid ${t.accent}22`, padding: '20px', borderRadius: '16px', lineHeight: 1.6, fontSize: '14px', color: t.text1, marginBottom: '28px' }}>
                      {inc.causal?.narrative || inc.causal?.root_cause || inc.root_cause || 'Awaiting AI analysis.'}
                    </div>

                    <h3 style={{ fontSize: '17px', fontWeight: 700, margin: '0 0 16px', color: t.text1 }}>Causal Chain</h3>
                    {inc.causal?.chain && inc.causal.chain.length > 0 ? (
                      <div style={{ display: 'flex', flexDirection: 'column', gap: '16px' }}>
                        {inc.causal.chain.map((link, i) => (
                          <div key={i} style={{ background: t.dark ? 'rgba(255,255,255,0.04)' : 'rgba(0,0,0,0.03)', padding: '16px', borderRadius: '12px', borderLeft: `2px solid ${t.accent}` }}>
                            <div style={{ display: 'flex', gap: '10px', alignItems: 'center', marginBottom: '8px', fontSize: '14px', fontWeight: 700, color: t.text1 }}>
                              <span>{link.from_service}</span>
                              <span style={{ color: t.text2, fontWeight: 400 }}>&#8594;</span>
                              <span>{link.to_service}</span>
                            </div>
                            {link.evidence && <p style={{ color: t.text2, fontSize: '12.5px', margin: 0, fontFamily: 'monospace' }}>Evidence: {link.evidence}</p>}
                          </div>
                        ))}
                      </div>
                    ) : (
                      <p style={{ color: t.text2 }}>No causal chain data available.</p>
                    )}
                  </div>

                  {/* Right: services, remediation, timeline */}
                  <div style={{ minWidth: 0 }}>
                    <h3 style={{ fontSize: '17px', fontWeight: 700, margin: '0 0 16px', color: t.text1 }}>Affected Services</h3>
                    <div style={{ display: 'flex', flexWrap: 'wrap', gap: '8px', marginBottom: '28px' }}>
                      {(inc.services ?? []).map((svc) => (
                        <span key={svc} style={{ background: t.dark ? 'rgba(255,255,255,0.06)' : 'rgba(255,255,255,0.6)', border: `1px solid ${t.panelBorder}`, padding: '6px 13px', borderRadius: '100px', fontSize: '13px', color: t.text1 }}>{svc}</span>
                      ))}
                    </div>

                    <div style={{ marginBottom: '28px' }}>
                      <RemediationPanel
                        key={inc.id}
                        incidentId={inc.id}
                        playbook={inc.causal?.playbook ?? null}
                        primaryService={primaryService(inc)}
                        onChanged={detail.refetch}
                      />
                    </div>

                    <h3 style={{ fontSize: '17px', fontWeight: 700, margin: '0 0 16px', color: t.text1 }}>Timeline</h3>
                    <StateBoundary
                      loading={timeline.loading}
                      error={timeline.error}
                      empty={(timeline.data ?? []).length === 0}
                      onRetry={timeline.refetch}
                      loadingLabel="Loading timeline…"
                      emptyLabel="No timeline events."
                    >
                      <div style={{ display: 'flex', flexDirection: 'column', gap: '12px' }}>
                        {(timeline.data ?? []).map((ev, i) => (
                          <div key={i} style={{ display: 'flex', gap: '12px' }}>
                            <div style={{ width: '8px', height: '8px', borderRadius: '50%', background: t.accent, marginTop: '5px', flexShrink: 0 }} />
                            <div>
                              <div style={{ fontSize: '13px', color: t.text1 }}>{ev.description}</div>
                              <div style={{ fontSize: '11.5px', color: t.text2 }}>
                                {ev.service ? `${ev.service} · ` : ''}{new Date(ev.at).toLocaleString()}
                              </div>
                            </div>
                          </div>
                        ))}
                      </div>
                    </StateBoundary>
                  </div>
                </div>
                )}
              </div>
            )}
          </StateBoundary>
        )}
      </div>
    </div>
  );
}
