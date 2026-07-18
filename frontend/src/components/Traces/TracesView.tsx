"use client";

import React, { useState, useEffect } from 'react';
import { useSearchParams } from 'next/navigation';
import { fetchWithAuth } from '@/lib/api';
import { TraceWaterfall } from './TraceWaterfall';
import { TraceAnalyticsView } from './TraceAnalyticsView';
import { useTheme } from '@/context/ThemeContext';

interface Trace {
  traceID: string;
  spans: any[];
  duration: number; // calculated
  startTime: number; // calculated
  rootServiceName: string;
  rootOperationName: string;
  error: boolean;
}

export function TracesView() {
  const { tokens: t } = useTheme();
  const searchParams = useSearchParams();
  const [traces, setTraces] = useState<Trace[]>([]);
  const [services, setServices] = useState<string[]>([]);
  const [selectedService, setSelectedService] = useState<string>('');
  const [selectedTraceId, setSelectedTraceId] = useState<string | null>(() => searchParams.get('trace'));
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [viewMode, setViewMode] = useState<'list' | 'analytics'>('list');
  const [tagFilter, setTagFilter] = useState('');

  useEffect(() => {
    // Fetch available services from Jaeger
    fetchWithAuth('/api/services')
      .then(async res => {
        if (!res.ok) throw new Error(await res.text());
        return res.json();
      })
      .then(data => {
        if (data && data.data && data.data.length > 0) {
          setServices(data.data);
          setSelectedService(prev => prev || data.data[0]);
        }
      })
      .catch(err => console.error("Failed to fetch Jaeger services:", err));
  }, []);

  const parseTagFilter = (raw: string): string | null => {
    const pairs = raw
      .split(',')
      .map(p => p.trim())
      .filter(Boolean)
      .map(p => p.split('='))
      .filter(([k, v]) => k && v !== undefined) as [string, string][];
    if (pairs.length === 0) return null;
    const tags: Record<string, string> = {};
    for (const [k, v] of pairs) tags[k.trim()] = v.trim();
    return JSON.stringify(tags);
  };

  const fetchTraces = () => {
    if (!selectedService) return;
    setLoading(true);
    setError(null);
    const tags = parseTagFilter(tagFilter);
    const tagsParam = tags ? `&tags=${encodeURIComponent(tags)}` : '';
    fetchWithAuth(`/api/traces?service=${selectedService}&limit=20${tagsParam}`)
      .then(async res => {
        if (!res.ok) throw new Error(await res.text());
        return res.json();
      })
      .then(data => {
        if (data && data.data) {
          const parsedTraces = data.data.map((t: any) => {
            // Find root span (no references or refType childOf)
            const rootSpan = t.spans.find((s: any) => !s.references || s.references.length === 0) || t.spans[0];
            const rootServiceName = rootSpan?.processID && t.processes[rootSpan.processID] ? t.processes[rootSpan.processID].serviceName : 'Unknown';
            const hasError = t.spans.some((s: any) => s.tags?.some((tag: any) => tag.key === 'error' && tag.value === true));

            return {
              traceID: t.traceID,
              spans: t.spans,
              duration: rootSpan?.duration || 0,
              startTime: rootSpan?.startTime || 0,
              rootServiceName,
              rootOperationName: rootSpan?.operationName || 'Unknown',
              error: hasError
            };
          });
          setTraces(parsedTraces);
        } else {
          setTraces([]);
        }
      })
      .catch(err => setError("Failed to load traces. Ensure Jaeger is running and generating traffic."))
      .finally(() => setLoading(false));
  };

  useEffect(() => {
    fetchTraces();
    // Refresh every 10s
    const interval = setInterval(fetchTraces, 10000);
    return () => clearInterval(interval);
  }, [selectedService]);

  const handleTagFilterKeyDown = (e: React.KeyboardEvent<HTMLInputElement>) => {
    if (e.key === 'Enter') fetchTraces();
  };

  const formatDuration = (micros: number) => {
    if (micros > 1000000) return `${(micros / 1000000).toFixed(2)}s`;
    if (micros > 1000) return `${(micros / 1000).toFixed(2)}ms`;
    return `${micros}µs`;
  };

  const inputStyle: React.CSSProperties = {
    background: t.dark ? 'rgba(255,255,255,0.06)' : 'rgba(255,255,255,0.7)',
    border: '1px solid ' + t.panelBorder,
    color: t.text1,
    padding: '10px 14px',
    borderRadius: '10px',
    outline: 'none',
  };

  if (selectedTraceId) {
    return (
      <div style={{ display: 'flex', flexDirection: 'column', height: '100%', minHeight: 0 }}>
        <div style={{ marginBottom: '16px' }}>
          <button
            onClick={() => setSelectedTraceId(null)}
            style={{
              display: 'flex',
              alignItems: 'center',
              gap: '8px',
              padding: '9px 16px',
              borderRadius: '10px',
              border: '1px solid ' + t.panelBorder,
              background: t.panelBg,
              color: t.text1,
              fontSize: '13px',
              cursor: 'pointer',
            }}
          >
            <span className="material-symbols-outlined" style={{ fontSize: '18px' }}>arrow_back</span>
            Back to Traces
          </button>
        </div>
        <TraceWaterfall traceId={selectedTraceId} />
      </div>
    );
  }

  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: '16px', height: '100%' }}>

      {/* View Switcher */}
      <div style={{ display: 'flex', gap: '8px', marginBottom: '8px' }}>
        <button
          onClick={() => setViewMode('list')}
          style={{
            padding: '8px 16px',
            borderRadius: '10px',
            border: '1px solid ' + t.panelBorder,
            background: viewMode === 'list' ? t.accentSoft : 'transparent',
            color: viewMode === 'list' ? t.accent : t.text2,
            fontWeight: 500,
            cursor: 'pointer'
          }}
        >
          Trace Explorer
        </button>
        <button
          onClick={() => setViewMode('analytics')}
          style={{
            padding: '8px 16px',
            borderRadius: '10px',
            border: '1px solid ' + t.panelBorder,
            background: viewMode === 'analytics' ? t.accentSoft : 'transparent',
            color: viewMode === 'analytics' ? t.accent : t.text2,
            fontWeight: 500,
            cursor: 'pointer'
          }}
        >
          Analytics (ClickHouse)
        </button>
      </div>

      {viewMode === 'analytics' ? (
        <TraceAnalyticsView />
      ) : (
        <>
          {/* Filters Toolbar */}
          <div style={{
            display: 'flex',
            gap: '14px',
            padding: '16px',
            borderRadius: '18px',
            background: t.panelBg,
            border: '1px solid ' + t.panelBorder,
            backdropFilter: 'blur(30px) saturate(180%)',
            alignItems: 'center',
          }}>
            <select
              value={selectedService}
              onChange={(e) => setSelectedService(e.target.value)}
              disabled={services.length === 0}
              style={inputStyle}
            >
              {services.length === 0 ? (
                <option value="">No services reporting yet</option>
              ) : (
                services.map(s => <option key={s} value={s}>{s}</option>)
              )}
            </select>

            <input
              type="text"
              value={tagFilter}
              onChange={(e) => setTagFilter(e.target.value)}
              onKeyDown={handleTagFilterKeyDown}
              placeholder="Filter by Tags (e.g. error=true)"
              style={{ ...inputStyle, flex: 1 }}
            />

            <button
              onClick={fetchTraces}
              style={{
                padding: '10px 24px',
                borderRadius: '10px',
                border: 'none',
                background: `linear-gradient(135deg, ${t.accent}, ${t.accent2})`,
                color: '#fff',
                fontWeight: 600,
                cursor: 'pointer',
              }}
            >
              Search Traces
            </button>
          </div>

          {/* Traces List */}
          <div style={{
            flex: 1,
            overflow: 'auto',
            borderRadius: '20px',
            background: t.panelBg,
            border: '1px solid ' + t.panelBorder,
            backdropFilter: 'blur(30px) saturate(180%)',
            boxShadow: t.shadow,
          }}>
            {loading && traces.length === 0 ? (
              <div style={{ padding: '48px', textAlign: 'center', color: t.text2 }}>Searching traces in Jaeger...</div>
            ) : error ? (
              <div style={{ padding: '48px', textAlign: 'center', color: t.red }}>{error}</div>
            ) : traces.length === 0 ? (
              <div style={{ padding: '48px', textAlign: 'center', color: t.text2 }}>No traces found for this service.</div>
            ) : (
              <table style={{ width: '100%', borderCollapse: 'collapse', textAlign: 'left' }}>
                <thead>
                  <tr style={{ borderBottom: '1px solid ' + t.panelBorder, background: t.dark ? 'rgba(0,0,0,0.15)' : 'rgba(0,0,0,0.03)' }}>
                    <th style={{ padding: '15px 16px', fontWeight: 600, color: t.text2, fontSize: '12.5px' }}>Status</th>
                    <th style={{ padding: '15px 16px', fontWeight: 600, color: t.text2, fontSize: '12.5px' }}>Trace ID</th>
                    <th style={{ padding: '15px 16px', fontWeight: 600, color: t.text2, fontSize: '12.5px' }}>Root Operation</th>
                    <th style={{ padding: '15px 16px', fontWeight: 600, color: t.text2, fontSize: '12.5px' }}>Spans</th>
                    <th style={{ padding: '15px 16px', fontWeight: 600, color: t.text2, fontSize: '12.5px' }}>Duration</th>
                    <th style={{ padding: '15px 16px', fontWeight: 600, color: t.text2, fontSize: '12.5px' }}>Timestamp</th>
                  </tr>
                </thead>
                <tbody>
                  {traces.map((trace) => (
                    <tr
                      key={trace.traceID}
                      onClick={() => setSelectedTraceId(trace.traceID)}
                      style={{
                        borderBottom: '1px solid ' + t.panelBorder,
                        cursor: 'pointer',
                        transition: 'background 0.2s'
                      }}
                      onMouseEnter={(e) => e.currentTarget.style.background = t.dark ? 'rgba(255,255,255,0.03)' : 'rgba(0,0,0,0.02)'}
                      onMouseLeave={(e) => e.currentTarget.style.background = 'transparent'}
                    >
                      <td style={{ padding: '15px 16px', fontSize: '13.5px' }}>
                        <span style={{
                          color: trace.error ? t.red : t.green,
                          fontSize: '11.5px',
                          padding: '4px 9px',
                          background: trace.error
                            ? (t.dark ? 'rgba(241,107,99,0.15)' : 'rgba(224,82,75,0.1)')
                            : (t.dark ? 'rgba(52,199,126,0.15)' : 'rgba(37,169,107,0.1)'),
                          borderRadius: '100px',
                          fontWeight: 600,
                        }}>
                          {trace.error ? 'Error' : 'OK'}
                        </span>
                      </td>
                      <td style={{ padding: '15px 16px', fontSize: '13.5px', fontFamily: 'monospace', color: t.text1 }}>
                        {trace.traceID.substring(0, 12)}...
                      </td>
                      <td style={{ padding: '15px 16px', fontSize: '13.5px', fontWeight: 500, color: t.text1 }}>
                        <span style={{ color: t.accent, marginRight: '8px' }}>{trace.rootServiceName}</span>
                        {trace.rootOperationName}
                      </td>
                      <td style={{ padding: '15px 16px', fontSize: '13.5px', color: t.text2 }}>{trace.spans.length} spans</td>
                      <td style={{ padding: '15px 16px', fontSize: '13.5px', fontWeight: 500, color: t.text1 }}>{formatDuration(trace.duration)}</td>
                      <td style={{ padding: '15px 16px', color: t.text2, fontSize: '13.5px' }}>
                        {new Date(trace.startTime / 1000).toLocaleString()}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            )}
          </div>
        </>
      )}

    </div>
  );
}
