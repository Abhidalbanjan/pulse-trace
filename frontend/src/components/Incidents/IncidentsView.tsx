"use client";

import React, { useState, useEffect } from 'react';
import { fetchWithAuth } from '@/lib/api';

export function IncidentsView() {
  const [incidents, setIncidents] = useState<any[]>([]);
  const [selectedIncident, setSelectedIncident] = useState<any | null>(null);
  const [loading, setLoading] = useState(false);

  useEffect(() => {
    setLoading(true);
    // Fetch live incidents from correlation-service via Next.js proxy and API Gateway
    fetchWithAuth('/api/v1/incidents')
      .then(res => res.json())
      .then(data => {
        if (data && Array.isArray(data)) {
          // Map backend model to UI fields if necessary, or just use it directly
          const mappedIncidents = data.map(inc => ({
            id: inc.id,
            title: `Incident in ${inc.service_names?.[0] || 'Unknown'}`,
            status: inc.status || 'OPEN',
            severity: inc.severity || 'WARNING',
            started_at: inc.started_at,
            services_affected: inc.service_names || [],
            root_cause: inc.causal?.chain ? "Causal AI has identified a potential root cause path." : "Awaiting AI analysis.",
            causal_chain: (inc.causal?.chain || []).map((link: any) => ({
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
      case 'CRITICAL': return 'var(--status-red)';
      case 'ERROR': return 'var(--status-orange)';
      case 'WARNING': return 'var(--status-orange)'; // Or yellow
      default: return 'var(--status-green)';
    }
  };

  return (
    <div style={{ display: 'flex', gap: '24px', height: 'calc(100vh - 120px)' }}>
      
      {/* Sidebar - Incident List */}
      <div className="glass-panel" style={{ width: '380px', display: 'flex', flexDirection: 'column', overflow: 'hidden' }}>
        <div style={{ padding: '24px', borderBottom: '1px solid var(--border-color)', display: 'flex', justifyContent: 'space-between', alignItems: 'center' }}>
          <h3 style={{ fontSize: '18px', fontWeight: 600 }}>Active Incidents</h3>
          <span style={{ background: 'var(--status-red)', color: 'white', padding: '4px 12px', borderRadius: '128px', fontSize: '12px', fontWeight: 600 }}>
             1 CRITICAL
          </span>
        </div>
        
        <div style={{ flex: 1, overflowY: 'auto' }}>
          {loading ? (
             <div style={{ padding: '24px', textAlign: 'center', color: 'var(--text-secondary)' }}>Loading...</div>
          ) : (
            incidents.map(inc => (
              <div 
                key={inc.id}
                onClick={() => setSelectedIncident(inc)}
                style={{ 
                  padding: '20px 24px', 
                  borderBottom: '1px solid var(--border-color)',
                  cursor: 'pointer',
                  background: selectedIncident?.id === inc.id ? 'rgba(255,255,255,0.05)' : 'transparent',
                  borderLeft: selectedIncident?.id === inc.id ? `3px solid ${getSeverityColor(inc.severity)}` : '3px solid transparent',
                  transition: '0.2s'
                }}
              >
                <div style={{ display: 'flex', justifyContent: 'space-between', marginBottom: '8px' }}>
                  <span style={{ fontSize: '12px', fontWeight: 600, color: getSeverityColor(inc.severity) }}>
                    {inc.severity}
                  </span>
                  <span style={{ fontSize: '12px', color: 'var(--text-secondary)' }}>
                    {new Date(inc.started_at).toLocaleTimeString([], {hour: '2-digit', minute:'2-digit'})}
                  </span>
                </div>
                <h4 style={{ fontSize: '16px', fontWeight: 500, marginBottom: '8px', lineHeight: '1.4' }}>{inc.title}</h4>
                <div style={{ display: 'flex', gap: '8px', flexWrap: 'wrap' }}>
                  {inc.services_affected.slice(0, 2).map((svc: string) => (
                    <span key={svc} style={{ fontSize: '11px', background: 'rgba(0,0,0,0.2)', padding: '4px 8px', borderRadius: '4px', color: 'var(--text-secondary)' }}>
                      {svc}
                    </span>
                  ))}
                  {inc.services_affected.length > 2 && (
                    <span style={{ fontSize: '11px', color: 'var(--text-secondary)' }}>+{inc.services_affected.length - 2} more</span>
                  )}
                </div>
              </div>
            ))
          )}
        </div>
      </div>

      {/* Main View - Incident Details & Timeline */}
      <div className="glass-panel" style={{ flex: 1, display: 'flex', flexDirection: 'column', overflowY: 'auto', padding: '40px' }}>
        {selectedIncident ? (
          <div>
            <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'flex-start', marginBottom: '24px' }}>
              <div>
                <div style={{ display: 'flex', alignItems: 'center', gap: '12px', marginBottom: '12px' }}>
                  <span style={{ background: getSeverityColor(selectedIncident.severity), color: '#fff', padding: '4px 12px', borderRadius: '12px', fontSize: '12px', fontWeight: 600 }}>
                    {selectedIncident.severity}
                  </span>
                  <span style={{ color: 'var(--text-secondary)', fontSize: '14px' }}>ID: {selectedIncident.id}</span>
                  <span style={{ color: 'var(--text-secondary)', fontSize: '14px' }}>
                    Started: {new Date(selectedIncident.started_at).toLocaleString()}
                  </span>
                </div>
                <h2 style={{ fontSize: '28px', fontWeight: 600 }}>{selectedIncident.title}</h2>
              </div>
              
              <div style={{ display: 'flex', gap: '12px' }}>
                <button className="btn-secondary" style={{ padding: '8px 16px' }}>Ack</button>
                <button className="btn-primary" style={{ padding: '8px 16px' }}>Resolve</button>
              </div>
            </div>

            <div style={{ display: 'grid', gridTemplateColumns: '2fr 1fr', gap: '32px' }}>
              
              {/* Left Column: Root Cause & AI Analysis */}
              <div>
                <h3 style={{ fontSize: '18px', fontWeight: 600, marginBottom: '16px' }}>✨ AI Root Cause Analysis</h3>
                <div style={{ 
                  background: 'rgba(100, 100, 255, 0.1)', 
                  border: '1px solid rgba(100, 100, 255, 0.2)',
                  padding: '24px',
                  borderRadius: '12px',
                  lineHeight: '1.6',
                  color: 'var(--text-primary)',
                  marginBottom: '32px'
                }}>
                  {selectedIncident.root_cause}
                </div>

                <h3 style={{ fontSize: '18px', fontWeight: 600, marginBottom: '16px' }}>Causal Chain</h3>
                {selectedIncident.causal_chain.length > 0 ? (
                  <div style={{ display: 'flex', flexDirection: 'column', gap: '16px' }}>
                    {selectedIncident.causal_chain.map((link: any, i: number) => (
                      <div key={i} style={{ 
                        background: 'rgba(0,0,0,0.2)', 
                        padding: '16px', 
                        borderRadius: '8px',
                        borderLeft: '2px solid var(--accent-purple)'
                      }}>
                        <div style={{ display: 'flex', gap: '12px', alignItems: 'center', marginBottom: '8px', fontSize: '14px', fontWeight: 500 }}>
                          <span>{link.from}</span>
                          <span style={{ color: 'var(--text-secondary)' }}>→</span>
                          <span>{link.to}</span>
                        </div>
                        <p style={{ color: 'var(--text-secondary)', fontSize: '13px', margin: 0, fontFamily: 'monospace' }}>
                          Evidence: {link.evidence}
                        </p>
                      </div>
                    ))}
                  </div>
                ) : (
                  <p style={{ color: 'var(--text-secondary)' }}>No causal chain data available.</p>
                )}
              </div>

              {/* Right Column: Affected Services & Integrations */}
              <div>
                 <h3 style={{ fontSize: '18px', fontWeight: 600, marginBottom: '16px' }}>Affected Services</h3>
                 <div style={{ display: 'flex', flexWrap: 'wrap', gap: '8px', marginBottom: '32px' }}>
                    {selectedIncident.services_affected.map((svc: string) => (
                      <span key={svc} style={{ 
                        background: 'rgba(255, 255, 255, 0.05)', 
                        border: '1px solid var(--border-color)',
                        padding: '6px 12px', 
                        borderRadius: '128px', 
                        fontSize: '13px' 
                      }}>
                        {svc}
                      </span>
                    ))}
                 </div>

                 <h3 style={{ fontSize: '18px', fontWeight: 600, marginBottom: '16px' }}>Runbooks</h3>
                 <div style={{ display: 'flex', flexDirection: 'column', gap: '12px' }}>
                    <button className="btn-secondary" style={{ width: '100%', textAlign: 'left', padding: '12px', display: 'flex', justifyContent: 'space-between' }}>
                      <span>Restart Postgres Pool</span>
                      <span>▶</span>
                    </button>
                    <button className="btn-secondary" style={{ width: '100%', textAlign: 'left', padding: '12px', display: 'flex', justifyContent: 'space-between' }}>
                      <span>Block IP in WAF</span>
                      <span>▶</span>
                    </button>
                 </div>
              </div>
            </div>

          </div>
        ) : (
          <div style={{ height: '100%', display: 'flex', alignItems: 'center', justifyContent: 'center', color: 'var(--text-secondary)' }}>
            Select an incident from the list to view details.
          </div>
        )}
      </div>

    </div>
  );
}
