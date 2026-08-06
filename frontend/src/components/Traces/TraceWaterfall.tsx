"use client";

import React, { useState, useEffect } from 'react';
import { useRouter } from 'next/navigation';
import { fetchWithAuth } from '@/lib/api';
import { useTheme } from '@/context/ThemeContext';

interface TraceWaterfallProps {
  traceId: string;
}

// Jaeger-shaped trace model (what /api/traces/:id returns).
interface SpanTag {
  key: string;
  value: unknown;
}
interface Span {
  spanID: string;
  processID: string;
  operationName: string;
  startTime: number;
  duration: number;
  tags?: SpanTag[];
}
interface Trace {
  spans: Span[];
  processes: Record<string, { serviceName: string } | undefined>;
}
interface CorrelatedLog {
  level?: string;
  timestamp?: string | number;
  message?: string;
}

export function TraceWaterfall({ traceId }: TraceWaterfallProps) {
  const { tokens: t } = useTheme();
  const router = useRouter();
  const [traceData, setTraceData] = useState<Trace | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [selectedSpan, setSelectedSpan] = useState<Span | null>(null);
  const [correlatedLogs, setCorrelatedLogs] = useState<CorrelatedLog[]>([]);

  useEffect(() => {
    // eslint-disable-next-line react-hooks/set-state-in-effect -- intentional one-shot fetch/hydration on mount; effect is the right place to sync from the API/localStorage
    setLoading(true);
    fetchWithAuth(`/api/traces/${traceId}`)
      .then(async res => {
        if (!res.ok) throw new Error(await res.text());
        return res.json();
      })
      .then(data => {
        if (data && data.data && data.data.length > 0) {
          const trace = data.data[0] as Trace;
          // Sort spans by start time
          trace.spans.sort((a: Span, b: Span) => a.startTime - b.startTime);
          setTraceData(trace);
        } else {
          setError("Trace not found");
        }
      })
      .catch(err => setError(err.message))
      .finally(() => setLoading(false));
  }, [traceId]);

  const handleSpanClick = async (span: Span) => {
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

  const cardStyle: React.CSSProperties = {
    borderRadius: '20px',
    background: t.panelBg,
    border: '1px solid ' + t.panelBorder,
    backdropFilter: 'blur(30px) saturate(180%)',
    boxShadow: t.shadow,
  };

  if (loading) return <div style={{ ...cardStyle, padding: '48px', textAlign: 'center', color: t.text2 }}>Loading Trace Waterfall...</div>;
  if (error || !traceData) return <div style={{ ...cardStyle, padding: '48px', textAlign: 'center', color: t.red }}>{error || 'Failed to load trace'}</div>;

  const minStartTime = traceData.spans[0]?.startTime || 0;
  const maxEndTime = Math.max(...traceData.spans.map((s: Span) => s.startTime + s.duration));
  const totalDuration = maxEndTime - minStartTime;

  return (
    <div style={{ display: 'flex', gap: '18px', flex: 1, minHeight: 0, minWidth: 0, height: '100%', overflow: 'hidden' }}>

      {/* Flame Graph View */}
      <div style={{ ...cardStyle, flex: 2, minWidth: 0, overflowY: 'auto', padding: '24px' }}>
        <h3 style={{ fontSize: '20px', fontWeight: 600, marginBottom: '24px', color: t.text1 }}>Trace Timeline: {traceId.substring(0, 8)}</h3>

        <div style={{ position: 'relative', width: '100%' }}>
          {traceData.spans.map((span: Span, index: number) => {
            const serviceName = traceData.processes[span.processID]?.serviceName || 'Unknown';
            const offsetPercentage = ((span.startTime - minStartTime) / totalDuration) * 100;
            const widthPercentage = Math.max((span.duration / totalDuration) * 100, 0.5); // Min width 0.5% for visibility
            const hasError = span.tags?.some((t: SpanTag) => t.key === 'error' && t.value === true);
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
                  background: isSelected ? (t.dark ? 'rgba(255,255,255,0.08)' : 'rgba(0,0,0,0.04)') : 'transparent',
                  borderRadius: '6px', position: 'absolute', top: 0, left: 0
                }} />

                {/* Span Bar */}
                <div style={{
                  position: 'relative',
                  left: `${offsetPercentage}%`,
                  width: `${widthPercentage}%`,
                  height: '28px',
                  background: hasError ? t.red : `linear-gradient(90deg, ${t.accent}, ${t.accent2})`,
                  borderRadius: '6px',
                  display: 'flex',
                  alignItems: 'center',
                  padding: '0 8px',
                  boxShadow: '0 2px 8px rgba(0,0,0,0.2)',
                  transition: 'all 0.2s',
                  border: isSelected ? '2px solid #fff' : '2px solid transparent',
                  overflow: 'hidden'
                }}>
                  <span style={{ color: '#fff', fontSize: '11px', fontWeight: 600, whiteSpace: 'nowrap', textOverflow: 'ellipsis' }}>
                    {serviceName} : {span.operationName}
                  </span>
                </div>
              </div>
            );
          })}
        </div>
      </div>

      {/* Details & Correlated Logs View */}
      <div style={{ ...cardStyle, flex: 1, minWidth: 0, display: 'flex', flexDirection: 'column', overflow: 'hidden' }}>
        {selectedSpan ? (
          <>
            <div style={{ padding: '22px', borderBottom: '1px solid ' + t.panelBorder }}>
              <h4 style={{ fontSize: '18px', fontWeight: 600, marginBottom: '8px', color: t.text1 }}>Span Details</h4>
              <p style={{ fontSize: '14px', color: t.text2, marginBottom: '4px' }}>
                <strong style={{ color: t.text1 }}>Service:</strong> {traceData.processes[selectedSpan.processID]?.serviceName}
              </p>
              <p style={{ fontSize: '14px', color: t.text2, marginBottom: '4px' }}>
                <strong style={{ color: t.text1 }}>Operation:</strong> {selectedSpan.operationName}
              </p>
              <p style={{ fontSize: '14px', color: t.text2, marginBottom: '12px' }}>
                <strong style={{ color: t.text1 }}>Duration:</strong> {formatDuration(selectedSpan.duration)}
              </p>

              <button
                onClick={() => {
                  const svc = traceData.processes[selectedSpan.processID]?.serviceName;
                  router.push(`/profiler?service=${encodeURIComponent(svc || '')}&spanId=${encodeURIComponent(selectedSpan.spanID)}`);
                }}
                style={{
                  display: 'inline-flex',
                  alignItems: 'center',
                  gap: '6px',
                  fontSize: '12px',
                  padding: '7px 13px',
                  background: 'transparent',
                  border: '1px solid ' + t.panelBorder,
                  borderRadius: '8px',
                  color: t.accent,
                  cursor: 'pointer'
                }}
              >
                <span className="material-symbols-outlined" style={{ fontSize: '15px' }}>speed</span>
                View Profile for this Span
              </button>

              <div style={{ marginTop: '16px' }}>
                <h5 style={{ fontSize: '12px', textTransform: 'uppercase', color: t.text2, marginBottom: '8px' }}>Tags</h5>
                <div style={{ display: 'flex', flexWrap: 'wrap', gap: '8px' }}>
                  {selectedSpan.tags?.map((tag: SpanTag, i: number) => (
                    <span key={i} style={{ fontSize: '11px', background: t.dark ? 'rgba(255,255,255,0.08)' : 'rgba(0,0,0,0.05)', padding: '4px 8px', borderRadius: '4px', color: t.text1 }}>
                      <span style={{ color: t.accent }}>{tag.key}:</span> {tag.value?.toString()}
                    </span>
                  ))}
                </div>
              </div>
            </div>

            <div style={{ padding: '22px', flex: 1, overflow: 'auto' }}>
              <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', marginBottom: '16px', gap: '8px' }}>
                <h4 style={{ fontSize: '18px', fontWeight: 600, display: 'flex', alignItems: 'center', gap: '8px', color: t.text1, margin: 0 }}>
                  <span className="material-symbols-outlined" style={{ fontSize: '18px' }}>subject</span> Correlated Logs
                </h4>
                {/* Trace→logs pivot: open the full Explorer scoped to this trace,
                    reusing the shareable-query URL (?q=). */}
                <button
                  onClick={() => router.push(`/explorer?q=${encodeURIComponent(`trace_id:"${traceId}"`)}`)}
                  title="Open these logs in the Explorer"
                  style={{
                    display: 'inline-flex', alignItems: 'center', gap: '6px',
                    fontSize: '12px', padding: '6px 11px', background: 'transparent',
                    border: '1px solid ' + t.panelBorder, borderRadius: '8px', color: t.accent, cursor: 'pointer', whiteSpace: 'nowrap',
                  }}
                >
                  <span className="material-symbols-outlined" style={{ fontSize: '15px' }}>open_in_new</span>
                  Open in Explorer
                </button>
              </div>
              {correlatedLogs.length === 0 ? (
                <p style={{ fontSize: '13px', color: t.text2 }}>No logs found for this exact trace ID in Quickwit.</p>
              ) : (
                <div style={{ display: 'flex', flexDirection: 'column', gap: '8px' }}>
                  {correlatedLogs.map((log: CorrelatedLog, i: number) => (
                    <div key={i} style={{
                      background: t.dark ? 'rgba(0,0,0,0.25)' : 'rgba(0,0,0,0.04)',
                      padding: '10px 12px',
                      borderRadius: '8px',
                      fontSize: '11.5px',
                      fontFamily: 'monospace'
                    }}>
                      <span style={{ color: log.level === 'ERROR' ? t.red : t.green, marginRight: '8px' }}>[{log.level || 'INFO'}]</span>
                      <span style={{ color: t.text2, marginRight: '8px' }}>{new Date(log.timestamp ?? 0).toLocaleTimeString()}</span>
                      <span style={{ color: t.text1 }}>{log.message}</span>
                    </div>
                  ))}
                </div>
              )}
            </div>
          </>
        ) : (
          <div style={{ padding: '48px', textAlign: 'center', color: t.text2 }}>
            Select a span in the flame graph to view details and correlated logs.
          </div>
        )}
      </div>

    </div>
  );
}
