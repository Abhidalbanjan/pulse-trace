"use client";

import React, { useState, useEffect } from 'react';
import { fetchWithAuth } from '@/lib/api';
import { TraceWaterfall } from './TraceWaterfall';
import { TraceAnalyticsView } from './TraceAnalyticsView';

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
  const [traces, setTraces] = useState<Trace[]>([]);
  const [services, setServices] = useState<string[]>(['cart-service', 'payment-service', 'gateway-service']);
  const [selectedService, setSelectedService] = useState<string>('cart-service');
  const [selectedTraceId, setSelectedTraceId] = useState<string | null>(null);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [viewMode, setViewMode] = useState<'list' | 'analytics'>('list');

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
        }
      })
      .catch(err => console.error("Failed to fetch Jaeger services:", err));
  }, []);

  const fetchTraces = () => {
    setLoading(true);
    setError(null);
    fetchWithAuth(`/api/traces?service=${selectedService}&limit=20`)
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

  const formatDuration = (micros: number) => {
    if (micros > 1000000) return `${(micros / 1000000).toFixed(2)}s`;
    if (micros > 1000) return `${(micros / 1000).toFixed(2)}ms`;
    return `${micros}µs`;
  };

  if (selectedTraceId) {
    return (
      <div style={{ display: 'flex', flexDirection: 'column', height: '100%' }}>
        <div style={{ marginBottom: '16px' }}>
          <button 
            className="btn-secondary" 
            onClick={() => setSelectedTraceId(null)}
            style={{ padding: '8px 16px', background: 'rgba(255,255,255,0.1)', border: '1px solid var(--border-color)', borderRadius: '8px', color: 'white', cursor: 'pointer' }}
          >
            ← Back to Traces
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
            borderRadius: '8px', 
            border: '1px solid var(--border-color)', 
            background: viewMode === 'list' ? 'rgba(0, 210, 255, 0.1)' : 'transparent',
            color: viewMode === 'list' ? 'var(--accent-blue)' : 'var(--text-secondary)',
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
            borderRadius: '8px', 
            border: '1px solid var(--border-color)', 
            background: viewMode === 'analytics' ? 'rgba(0, 210, 255, 0.1)' : 'transparent',
            color: viewMode === 'analytics' ? 'var(--accent-blue)' : 'var(--text-secondary)',
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
      <div className="glass-panel" style={{ padding: '16px', display: 'flex', gap: '16px', alignItems: 'center' }}>
        <div style={{ flex: 1, display: 'flex', gap: '12px' }}>
          <select 
            value={selectedService}
            onChange={(e) => setSelectedService(e.target.value)}
            style={{ background: 'rgba(0,0,0,0.5)', border: '1px solid var(--border-color)', color: 'white', padding: '10px 16px', borderRadius: '8px', outline: 'none' }}
          >
            {services.map(s => <option key={s} value={s}>{s}</option>)}
          </select>
          
          <input 
            type="text" 
            placeholder="Filter by Tags (e.g. error=true)"
            style={{ flex: 1, background: 'rgba(0,0,0,0.5)', border: '1px solid var(--border-color)', color: 'white', padding: '10px 16px', borderRadius: '8px', outline: 'none' }}
          />
        </div>
        <button className="btn-primary" onClick={fetchTraces} style={{ padding: '10px 24px' }}>
          Search Traces
        </button>
      </div>

      {/* Traces List */}
      <div className="glass-panel" style={{ flex: 1, overflow: 'auto', padding: '0' }}>
        {loading && traces.length === 0 ? (
          <div style={{ padding: '48px', textAlign: 'center', color: 'var(--text-secondary)' }}>Searching traces in Jaeger...</div>
        ) : error ? (
          <div style={{ padding: '48px', textAlign: 'center', color: 'var(--status-red)' }}>{error}</div>
        ) : traces.length === 0 ? (
          <div style={{ padding: '48px', textAlign: 'center', color: 'var(--text-secondary)' }}>No traces found for this service.</div>
        ) : (
          <table style={{ width: '100%', borderCollapse: 'collapse', textAlign: 'left' }}>
            <thead>
              <tr style={{ borderBottom: '1px solid var(--border-color)', background: 'rgba(0,0,0,0.2)' }}>
                <th style={{ padding: '16px', fontWeight: 500, color: 'var(--text-secondary)', fontSize: '13px' }}>Status</th>
                <th style={{ padding: '16px', fontWeight: 500, color: 'var(--text-secondary)', fontSize: '13px' }}>Trace ID</th>
                <th style={{ padding: '16px', fontWeight: 500, color: 'var(--text-secondary)', fontSize: '13px' }}>Root Operation</th>
                <th style={{ padding: '16px', fontWeight: 500, color: 'var(--text-secondary)', fontSize: '13px' }}>Spans</th>
                <th style={{ padding: '16px', fontWeight: 500, color: 'var(--text-secondary)', fontSize: '13px' }}>Duration</th>
                <th style={{ padding: '16px', fontWeight: 500, color: 'var(--text-secondary)', fontSize: '13px' }}>Timestamp</th>
              </tr>
            </thead>
            <tbody>
              {traces.map((trace) => (
                <tr 
                  key={trace.traceID} 
                  onClick={() => setSelectedTraceId(trace.traceID)}
                  style={{ 
                    borderBottom: '1px solid var(--border-color)',
                    cursor: 'pointer',
                    transition: 'background 0.2s'
                  }}
                  onMouseEnter={(e) => e.currentTarget.style.background = 'rgba(255,255,255,0.02)'}
                  onMouseLeave={(e) => e.currentTarget.style.background = 'transparent'}
                >
                  <td style={{ padding: '16px' }}>
                    {trace.error ? (
                      <span style={{ color: 'var(--status-red)', fontSize: '12px', padding: '4px 8px', background: 'rgba(239, 68, 68, 0.1)', borderRadius: '12px' }}>Error</span>
                    ) : (
                      <span style={{ color: 'var(--status-green)', fontSize: '12px', padding: '4px 8px', background: 'rgba(16, 185, 129, 0.1)', borderRadius: '12px' }}>OK</span>
                    )}
                  </td>
                  <td style={{ padding: '16px', fontSize: '13px', fontFamily: 'monospace' }}>
                    {trace.traceID.substring(0, 12)}...
                  </td>
                  <td style={{ padding: '16px', fontWeight: 500 }}>
                    <span style={{ color: 'var(--accent-blue)', marginRight: '8px' }}>{trace.rootServiceName}</span>
                    {trace.rootOperationName}
                  </td>
                  <td style={{ padding: '16px', color: 'var(--text-secondary)' }}>{trace.spans.length} spans</td>
                  <td style={{ padding: '16px', fontWeight: 500 }}>{formatDuration(trace.duration)}</td>
                  <td style={{ padding: '16px', color: 'var(--text-secondary)', fontSize: '13px' }}>
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
