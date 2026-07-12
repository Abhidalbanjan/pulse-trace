"use client";

import React, { useState, useEffect } from 'react';
import { fetchWithAuth } from '@/lib/api';

interface TraceWaterfallProps {
  traceId: string;
}

export function TraceWaterfall({ traceId }: TraceWaterfallProps) {
  const [traceData, setTraceData] = useState<any>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [selectedSpan, setSelectedSpan] = useState<any>(null);
  const [correlatedLogs, setCorrelatedLogs] = useState<any[]>([]);

  useEffect(() => {
    setLoading(true);
    fetchWithAuth(`/api/traces/${traceId}`)
      .then(async res => {
        if (!res.ok) throw new Error(await res.text());
        return res.json();
      })
      .then(data => {
        if (data && data.data && data.data.length > 0) {
          const trace = data.data[0];
          // Sort spans by start time
          trace.spans.sort((a: any, b: any) => a.startTime - b.startTime);
          setTraceData(trace);
        } else {
          setError("Trace not found");
        }
      })
      .catch(err => setError(err.message))
      .finally(() => setLoading(false));
  }, [traceId]);

  const handleSpanClick = async (span: any) => {
    setSelectedSpan(span);
    // Trace-to-Log Correlation: Search Quickwit for this trace_id
    try {
      const res = await fetchWithAuth('/api/v1/search/pulsetrace-logs/search', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ query: `trace_id:"${traceId}"`, max_hits: 100 })
      });
      if (!res.ok) throw new Error(await res.text());
      const logData = await res.json();
      setCorrelatedLogs(logData.hits || []);
    } catch (e) {
      console.error("Log correlation failed", e);
    }
  };

  const formatDuration = (micros: number) => {
    if (micros > 1000000) return `${(micros / 1000000).toFixed(2)}s`;
    if (micros > 1000) return `${(micros / 1000).toFixed(2)}ms`;
    return `${micros}µs`;
  };

  if (loading) return <div className="glass-panel" style={{ padding: '48px', textAlign: 'center' }}>Loading Trace Waterfall...</div>;
  if (error || !traceData) return <div className="glass-panel" style={{ padding: '48px', textAlign: 'center', color: 'var(--status-red)' }}>{error || 'Failed to load trace'}</div>;

  const minStartTime = traceData.spans[0]?.startTime || 0;
  const maxEndTime = Math.max(...traceData.spans.map((s: any) => s.startTime + s.duration));
  const totalDuration = maxEndTime - minStartTime;

  return (
    <div style={{ display: 'flex', gap: '24px', height: '100%', overflow: 'hidden' }}>
      
      {/* Flame Graph View */}
      <div className="glass-panel" style={{ flex: 2, overflow: 'auto', padding: '24px' }}>
        <h3 style={{ fontSize: '20px', fontWeight: 600, marginBottom: '24px' }}>Trace Timeline: {traceId.substring(0, 8)}</h3>
        
        <div style={{ position: 'relative', width: '100%' }}>
          {traceData.spans.map((span: any, index: number) => {
            const serviceName = traceData.processes[span.processID]?.serviceName || 'Unknown';
            const offsetPercentage = ((span.startTime - minStartTime) / totalDuration) * 100;
            const widthPercentage = Math.max((span.duration / totalDuration) * 100, 0.5); // Min width 0.5% for visibility
            const hasError = span.tags?.some((t: any) => t.key === 'error' && t.value === true);
            const isSelected = selectedSpan?.spanID === span.spanID;

            return (
              <div 
                key={span.spanID} 
                onClick={() => handleSpanClick(span)}
                style={{ 
                  marginBottom: '4px', 
                  position: 'relative',
                  cursor: 'pointer',
                  opacity: isSelected ? 1 : 0.85
                }}
              >
                {/* Background Track */}
                <div style={{ 
                  width: '100%', height: '28px', 
                  background: isSelected ? 'rgba(255,255,255,0.08)' : 'transparent',
                  borderRadius: '4px', position: 'absolute', top: 0, left: 0 
                }} />
                
                {/* Span Bar */}
                <div style={{
                  position: 'relative',
                  left: `${offsetPercentage}%`,
                  width: `${widthPercentage}%`,
                  height: '28px',
                  background: hasError ? 'var(--status-red)' : 'var(--accent-blue)',
                  borderRadius: '4px',
                  display: 'flex',
                  alignItems: 'center',
                  padding: '0 8px',
                  boxShadow: '0 2px 8px rgba(0,0,0,0.2)',
                  transition: 'all 0.2s',
                  border: isSelected ? '1px solid white' : '1px solid transparent',
                  overflow: 'hidden'
                }}>
                  <span style={{ color: 'white', fontSize: '11px', fontWeight: 600, whiteSpace: 'nowrap', textOverflow: 'ellipsis' }}>
                    {serviceName} : {span.operationName}
                  </span>
                </div>
              </div>
            );
          })}
        </div>
      </div>

      {/* Details & Correlated Logs View */}
      <div className="glass-panel" style={{ flex: 1, display: 'flex', flexDirection: 'column', overflow: 'hidden' }}>
        {selectedSpan ? (
          <>
            <div style={{ padding: '24px', borderBottom: '1px solid var(--border-color)' }}>
              <h4 style={{ fontSize: '18px', fontWeight: 600, marginBottom: '8px' }}>Span Details</h4>
              <p style={{ fontSize: '14px', color: 'var(--text-secondary)', marginBottom: '4px' }}>
                <strong style={{ color: 'white' }}>Service:</strong> {traceData.processes[selectedSpan.processID]?.serviceName}
              </p>
              <p style={{ fontSize: '14px', color: 'var(--text-secondary)', marginBottom: '4px' }}>
                <strong style={{ color: 'white' }}>Operation:</strong> {selectedSpan.operationName}
              </p>
              <p style={{ fontSize: '14px', color: 'var(--text-secondary)' }}>
                <strong style={{ color: 'white' }}>Duration:</strong> {formatDuration(selectedSpan.duration)}
              </p>
              
              <div style={{ marginTop: '16px' }}>
                <h5 style={{ fontSize: '12px', textTransform: 'uppercase', color: 'var(--text-secondary)', marginBottom: '8px' }}>Tags</h5>
                <div style={{ display: 'flex', flexWrap: 'wrap', gap: '8px' }}>
                  {selectedSpan.tags?.map((t: any, i: number) => (
                    <span key={i} style={{ fontSize: '11px', background: 'rgba(255,255,255,0.1)', padding: '4px 8px', borderRadius: '4px' }}>
                      <span style={{ color: 'var(--accent-blue)' }}>{t.key}:</span> {t.value?.toString()}
                    </span>
                  ))}
                </div>
              </div>
            </div>

            <div style={{ padding: '24px', flex: 1, overflow: 'auto' }}>
              <h4 style={{ fontSize: '18px', fontWeight: 600, marginBottom: '16px', display: 'flex', alignItems: 'center', gap: '8px' }}>
                <span>▤</span> Correlated Logs
              </h4>
              {correlatedLogs.length === 0 ? (
                <p style={{ fontSize: '13px', color: 'var(--text-secondary)' }}>No logs found for this exact trace ID in Quickwit.</p>
              ) : (
                <div style={{ display: 'flex', flexDirection: 'column', gap: '12px' }}>
                  {correlatedLogs.map((log: any, i: number) => (
                    <div key={i} style={{ 
                      background: 'rgba(0,0,0,0.3)', 
                      border: '1px solid var(--border-color)',
                      padding: '12px',
                      borderRadius: '8px',
                      fontSize: '12px',
                      fontFamily: 'monospace'
                    }}>
                      <span style={{ color: 'var(--status-green)', marginRight: '8px' }}>[{log.level || 'INFO'}]</span>
                      <span style={{ color: 'var(--text-secondary)', marginRight: '8px' }}>{new Date(log.timestamp).toLocaleTimeString()}</span>
                      <span style={{ color: 'white' }}>{log.message}</span>
                    </div>
                  ))}
                </div>
              )}
            </div>
          </>
        ) : (
          <div style={{ padding: '48px', textAlign: 'center', color: 'var(--text-secondary)' }}>
            Select a span in the flame graph to view details and correlated logs.
          </div>
        )}
      </div>

    </div>
  );
}
