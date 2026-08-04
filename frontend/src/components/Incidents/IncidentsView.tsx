"use client";

import React, { useState, useEffect } from 'react';
import { fetchWithAuth } from '@/lib/api';
import { useTheme } from '@/context/ThemeContext';

interface CausalChainLink { from: string; to: string; evidence: string; }
interface UIIncident { id: string; title: string; status: string; severity: string; started_at?: string; services_affected: string[]; root_cause: string; causal_chain: CausalChainLink[]; }
interface RawCausalLink { from_service: string; to_service: string; confidence?: number; }
interface RawIncident { id: string; services?: string[]; status?: string; severity?: string; started_at?: string; causal?: { chain?: RawCausalLink[] }; }

export function IncidentsView() {
  const { tokens: t } = useTheme();
  const [incidents, setIncidents] = useState<UIIncident[]>([]);
  const [selectedIncident, setSelectedIncident] = useState<UIIncident | null>(null);
  const [loading, setLoading] = useState(false);

  useEffect(() => {
    // eslint-disable-next-line react-hooks/set-state-in-effect -- intentional one-shot fetch/hydration on mount; effect is the right place to sync from the API/localStorage
    setLoading(true);
    // Fetch live incidents from correlation-service via Next.js proxy and API Gateway
    fetchWithAuth('/api/v1/incidents')
      .then(res => res.json())
      .then(data => {
        // The API wraps results as { success, data: [...] } (see gateway-service's
        // models.OK helper) - it never returns a bare array, so the old
        // `Array.isArray(data)` check here always failed and silently discarded
        // every incident.
        const rawIncidents = Array.isArray(data) ? data : Array.isArray(data?.data) ? data.data : null;
        if (rawIncidents) {
          // Map backend model to UI fields if necessary, or just use it directly
          const mappedIncidents = rawIncidents.map((inc: RawIncident) => ({
            id: inc.id,
            // The API's incident model names this field `services` (see
            // correlation-service/internal/models), not `service_names` - every
            // incident rendered as "Incident in Unknown" with no affected-service
            // chips because this always read undefined.
            title: `Incident in ${inc.services?.[0] || 'Unknown'}`,
            status: inc.status || 'OPEN',
            severity: inc.severity || 'WARNING',
            started_at: inc.started_at,
            services_affected: inc.services || [],
            root_cause: inc.causal?.chain ? "Causal AI has identified a potential root cause path." : "Awaiting AI analysis.",
            causal_chain: (inc.causal?.chain || []).map((link: RawCausalLink) => ({
               from: link.from_service,
               to: link.to_service,
               evidence: `Confidence: ${link.confidence || 1.0}`
            }))
          }));
          setIncidents(mappedIncidents);
          if (mappedIncidents.length > 0) {
            setSelectedIncident(mappedIncidents[0]);
          }
        }
        setLoading(false);
      })
      .catch(err => {
        console.error("Failed to fetch incidents:", err);
        setLoading(false);
      });
  }, []);

  const getSeverityColor = (severity: string) => {
    switch(severity) {
      case 'CRITICAL': return t.red;
      case 'ERROR': return t.amber;
      case 'WARNING': return t.amber;
      default: return t.green;
    }
  };

  const panelStyle: React.CSSProperties = {
    background: t.panelBg,
    border: `1px solid ${t.panelBorder}`,
    borderTop: `1px solid ${t.panelTop}`,
    backdropFilter: 'blur(34px) saturate(180%)',
    WebkitBackdropFilter: 'blur(34px) saturate(180%)',
    borderRadius: '24px',
    boxShadow: t.shadow,
  };

  const criticalCount = incidents.filter(inc => inc.severity === 'CRITICAL').length;

  return (
    <div style={{ display: 'flex', gap: '20px', height: 'calc(100vh - 124px)', minWidth: 0 }}>

      {/* Sidebar - Incident List */}
      <div style={{ ...panelStyle, width: 'clamp(260px, 28vw, 380px)', flexShrink: 0, display: 'flex', flexDirection: 'column', overflow: 'hidden' }}>
        <div style={{ padding: '22px 22px', borderBottom: `1px solid ${t.panelBorder}`, display: 'flex', justifyContent: 'space-between', alignItems: 'center' }}>
          <h3 style={{ fontSize: '17px', fontWeight: 700, margin: 0, color: t.text1 }}>Active Incidents</h3>
          <span style={{ background: t.red, color: '#fff', padding: '5px 13px', borderRadius: '100px', fontSize: '11.5px', fontWeight: 700 }}>
            {criticalCount} CRITICAL
          </span>
        </div>

        <div style={{ flex: 1, overflowY: 'auto' }}>
          {loading ? (
            <div style={{ padding: '24px', textAlign: 'center', color: t.text2 }}>Loading...</div>
          ) : (
            incidents.map(inc => {
              const isSelected = selectedIncident?.id === inc.id;
              const severityColor = getSeverityColor(inc.severity);
              return (
                <div
                  key={inc.id}
                  onClick={() => setSelectedIncident(inc)}
                  style={{
                    padding: '18px 22px',
                    borderBottom: `1px solid ${t.panelBorder}`,
                    cursor: 'pointer',
                    background: isSelected ? (t.dark ? 'rgba(255,255,255,0.06)' : 'rgba(255,255,255,0.5)') : 'transparent',
                    borderLeft: isSelected ? `3px solid ${severityColor}` : '3px solid transparent',
                    transition: '0.2s'
                  }}
                >
                  <div style={{ display: 'flex', justifyContent: 'space-between', marginBottom: '8px' }}>
                    <span style={{ fontSize: '11px', fontWeight: 700, color: severityColor, letterSpacing: '0.03em' }}>
                      {inc.severity}
                    </span>
                    <span style={{ fontSize: '12px', color: t.text2 }}>
                      {new Date(inc.started_at ?? 0).toLocaleTimeString([], {hour: '2-digit', minute:'2-digit'})}
                    </span>
                  </div>
                  <h4 style={{ fontSize: '15px', fontWeight: 600, margin: '0 0 10px', lineHeight: 1.4, color: t.text1 }}>{inc.title}</h4>
                  <div style={{ display: 'flex', gap: '8px', flexWrap: 'wrap' }}>
                    {inc.services_affected.slice(0, 2).map((svc: string) => (
                      <span key={svc} style={{ fontSize: '11px', background: t.dark ? 'rgba(255,255,255,0.06)' : 'rgba(0,0,0,0.05)', padding: '4px 9px', borderRadius: '6px', color: t.text2 }}>
                        {svc}
                      </span>
                    ))}
                    {inc.services_affected.length > 2 && (
                      <span style={{ fontSize: '11px', color: t.text2 }}>+{inc.services_affected.length - 2} more</span>
                    )}
                  </div>
                </div>
              );
            })
          )}
        </div>
      </div>

      {/* Main View - Incident Details & Timeline */}
      <div style={{ ...panelStyle, flex: 1, minWidth: 0, overflowY: 'auto', overflowX: 'hidden', padding: 'clamp(20px, 3vw, 36px)' }}>
        {selectedIncident ? (
          <div>
            <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'flex-start', marginBottom: '24px' }}>
              <div>
                <div style={{ display: 'flex', alignItems: 'center', gap: '12px', marginBottom: '12px' }}>
                  <span style={{ background: getSeverityColor(selectedIncident.severity), color: '#fff', padding: '5px 14px', borderRadius: '100px', fontSize: '12px', fontWeight: 700 }}>
                    {selectedIncident.severity}
                  </span>
                  <span style={{ color: t.text2, fontSize: '13.5px' }}>ID: {selectedIncident.id}</span>
                  <span style={{ color: t.text2, fontSize: '13.5px' }}>
                    Started: {new Date(selectedIncident.started_at ?? 0).toLocaleString()}
                  </span>
                </div>
                <h2 style={{ fontSize: '26px', fontWeight: 700, margin: 0, color: t.text1 }}>{selectedIncident.title}</h2>
              </div>

              <div style={{ display: 'flex', gap: '12px' }}>
                <button style={{ padding: '10px 18px', borderRadius: '10px', border: `1px solid ${t.panelBorder}`, background: 'transparent', color: t.text1, fontWeight: 600, fontSize: '13.5px', cursor: 'pointer' }}>
                  Acknowledge
                </button>
                <button style={{ padding: '10px 18px', borderRadius: '10px', border: 'none', background: `linear-gradient(135deg, ${t.accent}, ${t.accent2})`, color: '#fff', fontWeight: 600, fontSize: '13.5px', cursor: 'pointer' }}>
                  Resolve
                </button>
              </div>
            </div>

            <div style={{ display: 'grid', gridTemplateColumns: 'minmax(0,1.6fr) minmax(0,1fr)', gap: 'clamp(16px,3vw,40px)' }}>

              {/* Left Column: Root Cause & AI Analysis */}
              <div style={{ minWidth: 0 }}>
                <h3 style={{ fontSize: '17px', fontWeight: 700, margin: '0 0 16px', color: t.text1 }}>&#10022; AI Root Cause Analysis</h3>
                <div style={{
                  background: t.accentSoft,
                  border: `1px solid ${t.accent}22`,
                  padding: '20px',
                  borderRadius: '16px',
                  lineHeight: 1.6,
                  fontSize: '14px',
                  color: t.text1,
                  marginBottom: '28px'
                }}>
                  {selectedIncident.root_cause}
                </div>

                <h3 style={{ fontSize: '17px', fontWeight: 700, margin: '0 0 16px', color: t.text1 }}>Causal Chain</h3>
                {selectedIncident.causal_chain.length > 0 ? (
                  <div style={{ display: 'flex', flexDirection: 'column', gap: '16px' }}>
                    {selectedIncident.causal_chain.map((link: CausalChainLink, i: number) => (
                      <div key={i} style={{
                        background: t.dark ? 'rgba(255,255,255,0.04)' : 'rgba(0,0,0,0.03)',
                        padding: '16px',
                        borderRadius: '12px',
                        borderLeft: `2px solid ${t.accent}`
                      }}>
                        <div style={{ display: 'flex', gap: '10px', alignItems: 'center', marginBottom: '8px', fontSize: '14px', fontWeight: 700, color: t.text1 }}>
                          <span>{link.from}</span>
                          <span style={{ color: t.text2, fontWeight: 400 }}>&#8594;</span>
                          <span>{link.to}</span>
                        </div>
                        <p style={{ color: t.text2, fontSize: '12.5px', margin: 0, fontFamily: 'monospace' }}>
                          Evidence: {link.evidence}
                        </p>
                      </div>
                    ))}
                  </div>
                ) : (
                  <p style={{ color: t.text2 }}>No causal chain data available.</p>
                )}
              </div>

              {/* Right Column: Affected Services & Integrations */}
              <div style={{ minWidth: 0 }}>
                 <h3 style={{ fontSize: '17px', fontWeight: 700, margin: '0 0 16px', color: t.text1 }}>Affected Services</h3>
                 <div style={{ display: 'flex', flexWrap: 'wrap', gap: '8px', marginBottom: '28px' }}>
                    {selectedIncident.services_affected.map((svc: string) => (
                      <span key={svc} style={{
                        background: t.dark ? 'rgba(255,255,255,0.06)' : 'rgba(255,255,255,0.6)',
                        border: `1px solid ${t.panelBorder}`,
                        padding: '6px 13px',
                        borderRadius: '100px',
                        fontSize: '13px',
                        color: t.text1
                      }}>
                        {svc}
                      </span>
                    ))}
                 </div>

                 <h3 style={{ fontSize: '17px', fontWeight: 700, margin: '0 0 16px', color: t.text1 }}>Suggested Runbooks</h3>
                 <div style={{ display: 'flex', flexDirection: 'column', gap: '12px' }}>
                    <button style={{ width: '100%', textAlign: 'left', padding: '13px 16px', borderRadius: '12px', border: `1px solid ${t.panelBorder}`, background: 'transparent', color: t.text1, fontSize: '13.5px', display: 'flex', justifyContent: 'space-between', alignItems: 'center', cursor: 'pointer' }}>
                      <span>Restart Postgres Pool</span>
                      <span className="material-symbols-outlined" style={{ fontSize: '18px', color: t.text2 }}>play_arrow</span>
                    </button>
                    <button style={{ width: '100%', textAlign: 'left', padding: '13px 16px', borderRadius: '12px', border: `1px solid ${t.panelBorder}`, background: 'transparent', color: t.text1, fontSize: '13.5px', display: 'flex', justifyContent: 'space-between', alignItems: 'center', cursor: 'pointer' }}>
                      <span>Block IP in WAF</span>
                      <span className="material-symbols-outlined" style={{ fontSize: '18px', color: t.text2 }}>play_arrow</span>
                    </button>
                 </div>
              </div>
            </div>

          </div>
        ) : (
          <div style={{ height: '100%', display: 'flex', alignItems: 'center', justifyContent: 'center', color: t.text2 }}>
            Select an incident from the list to view details.
          </div>
        )}
      </div>

    </div>
  );
}
