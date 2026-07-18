"use client";

import React, { useState, useEffect, useRef } from 'react';
import { fetchWithAuth } from '@/lib/api';
import { BarChart, Bar, Tooltip, ResponsiveContainer } from 'recharts';
import { useTheme } from '@/context/ThemeContext';

interface LogEntry {
  id: string;
  timestamp: string;
  service_name: string;
  level: string;
  message: string;
  trace_id?: string;
  tenant_id: string;
  _raw: any;
}

interface Bucket {
  key: string | number;
  doc_count: number;
}

export function ExplorerView() {
  const { tokens: t } = useTheme();
  const [logs, setLogs] = useState<LogEntry[]>([]);
  const [loading, setLoading] = useState(false);
  const [query, setQuery] = useState("*");
  const [selectedLog, setSelectedLog] = useState<LogEntry | null>(null);

  // Aggregation State
  const [serviceBuckets, setServiceBuckets] = useState<Bucket[]>([]);
  const [levelBuckets, setLevelBuckets] = useState<Bucket[]>([]);
  const [volumeData, setVolumeData] = useState<any[]>([]);

  // Live Tail State
  const [isLiveTail, setIsLiveTail] = useState(false);
  const liveTailRef = useRef<NodeJS.Timeout | null>(null);

  const fetchLogs = () => {
    setLoading(true);
    // Add aggregations to the Quickwit query
    const requestBody = {
      query: query,
      max_hits: 100,
      sort_by_field: "-timestamp",
      aggs: {
        service_counts: {
          terms: { field: "service_name" }
        },
        level_counts: {
          terms: { field: "level" }
        },
        volume_over_time: {
          date_histogram: { field: "timestamp", fixed_interval: "10s" }
        }
      }
    };

    fetchWithAuth('/api/v1/search/pulsetrace-logs/search', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(requestBody)
    })
      .then(res => res.json())
      .then(data => {
        if (data && data.hits) {
          const formattedLogs = data.hits.map((hit: any) => ({
            id: hit.trace_id || Math.random().toString(),
            timestamp: hit.timestamp || new Date().toISOString(),
            service_name: hit.service_name || 'unknown',
            level: hit.level || 'INFO',
            message: hit.message || JSON.stringify(hit),
            trace_id: hit.trace_id,
            tenant_id: hit.tenant_id || 'default',
            _raw: hit
          }));
          setLogs(formattedLogs);
        }

        if (data && data.aggregations) {
          if (data.aggregations.service_counts) setServiceBuckets(data.aggregations.service_counts.buckets || []);
          if (data.aggregations.level_counts) setLevelBuckets(data.aggregations.level_counts.buckets || []);

          if (data.aggregations.volume_over_time && data.aggregations.volume_over_time.buckets) {
            const chartData = data.aggregations.volume_over_time.buckets.map((b: any) => ({
              time: new Date(b.key).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', second:'2-digit' }),
              count: b.doc_count
            }));
            setVolumeData(chartData);
          }
        }
      })
      .catch(err => console.error("Failed to fetch from Quickwit:", err))
      .finally(() => setLoading(false));
  };

  useEffect(() => {
    fetchLogs();
  }, [query]);

  useEffect(() => {
    if (isLiveTail) {
      liveTailRef.current = setInterval(() => {
        fetchLogs();
      }, 3000);
    } else if (liveTailRef.current) {
      clearInterval(liveTailRef.current);
    }
    return () => {
      if (liveTailRef.current) clearInterval(liveTailRef.current);
    };
  }, [isLiveTail, query]);

  const toggleLiveTail = () => setIsLiveTail(!isLiveTail);

  const getLevelColor = (level: string) => {
    switch (level.toUpperCase()) {
      case 'ERROR':
      case 'FATAL': return t.red;
      case 'WARN': return t.amber;
      case 'INFO': return t.green;
      case 'DEBUG': return t.text2;
      default: return t.text1;
    }
  };

  const addFilter = (field: string, value: string) => {
    const newTerm = `${field}:"${value}"`;
    if (query === "*") {
      setQuery(newTerm);
    } else if (!query.includes(newTerm)) {
      setQuery(`${query} AND ${newTerm}`);
    }
  };

  const badgeStyle: React.CSSProperties = {
    color: t.text2,
    fontSize: '11px',
    background: t.dark ? 'rgba(255,255,255,0.08)' : 'rgba(0,0,0,0.06)',
    padding: '2px 7px',
    borderRadius: '100px',
  };

  const glassPanel: React.CSSProperties = {
    background: t.panelBg,
    border: `1px solid ${t.panelBorder}`,
    backdropFilter: 'blur(34px) saturate(180%)',
    WebkitBackdropFilter: 'blur(34px) saturate(180%)',
    boxShadow: t.shadow,
  };

  return (
    <div style={{ display: 'flex', gap: '18px', height: 'calc(100vh - 124px)', minWidth: 0 }}>

      {/* Left Facet Panel */}
      <div style={{ ...glassPanel, width: '260px', flexShrink: 0, padding: '22px', borderRadius: '22px', overflowY: 'auto' }}>
        <div style={{ marginBottom: '24px' }}>
          <div style={{ fontSize: '11px', fontWeight: 700, letterSpacing: '0.06em', color: t.text2, marginBottom: '12px' }}>
            SERVICE NAME
          </div>
          <div style={{ display: 'flex', flexDirection: 'column', gap: '2px' }}>
            {serviceBuckets.length === 0 ? (
              <div style={{ fontSize: '13px', color: t.text2 }}>No data</div>
            ) : null}
            {serviceBuckets.map(b => (
              <div
                key={b.key}
                onClick={() => addFilter("service_name", b.key.toString())}
                style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', padding: '5px 8px', borderRadius: '8px', cursor: 'pointer' }}
                onMouseEnter={(e) => e.currentTarget.style.background = t.dark ? 'rgba(255,255,255,0.06)' : 'rgba(0,0,0,0.04)'}
                onMouseLeave={(e) => e.currentTarget.style.background = 'transparent'}
              >
                <span style={{ color: t.accent, fontSize: '13px', whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis' }}>{b.key}</span>
                <span style={badgeStyle}>{b.doc_count}</span>
              </div>
            ))}
          </div>
        </div>

        <div>
          <div style={{ fontSize: '11px', fontWeight: 700, letterSpacing: '0.06em', color: t.text2, marginBottom: '12px' }}>
            LEVEL
          </div>
          <div style={{ display: 'flex', flexDirection: 'column', gap: '2px' }}>
            {levelBuckets.length === 0 ? (
              <div style={{ fontSize: '13px', color: t.text2 }}>No data</div>
            ) : null}
            {levelBuckets.map(b => (
              <div
                key={b.key}
                onClick={() => addFilter("level", b.key.toString())}
                style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', padding: '5px 8px', borderRadius: '8px', cursor: 'pointer' }}
                onMouseEnter={(e) => e.currentTarget.style.background = t.dark ? 'rgba(255,255,255,0.06)' : 'rgba(0,0,0,0.04)'}
                onMouseLeave={(e) => e.currentTarget.style.background = 'transparent'}
              >
                <span style={{ display: 'flex', alignItems: 'center', gap: '8px' }}>
                  <span style={{ width: '7px', height: '7px', borderRadius: '50%', background: getLevelColor(b.key.toString()), flexShrink: 0 }} />
                  <span style={{ color: getLevelColor(b.key.toString()), fontSize: '13px', fontWeight: 500 }}>{b.key}</span>
                </span>
                <span style={badgeStyle}>{b.doc_count}</span>
              </div>
            ))}
          </div>
        </div>
      </div>

      {/* Center Column */}
      <div style={{ flex: 1, display: 'flex', flexDirection: 'column', gap: '14px', minWidth: 0, overflow: 'hidden' }}>

        {/* Toolbar */}
        <div style={{ display: 'flex', gap: '14px' }}>
          <div style={{ flex: 1, display: 'flex', alignItems: 'center', gap: '8px', background: t.panelBg, border: `1px solid ${t.panelBorder}`, borderRadius: '14px', padding: '11px 16px', backdropFilter: 'blur(20px) saturate(180%)', WebkitBackdropFilter: 'blur(20px) saturate(180%)' }}>
            <span className="material-symbols-outlined" style={{ fontSize: '18px', color: t.text2 }}>search</span>
            <input
              type="text"
              value={query}
              onChange={(e) => setQuery(e.target.value)}
              placeholder="Search logs... (e.g. level:ERROR)"
              style={{ flex: 1, background: 'transparent', border: 'none', color: t.text1, outline: 'none', fontSize: '13.5px' }}
              onKeyDown={(e) => e.key === 'Enter' && fetchLogs()}
            />
            {loading && <span style={{ fontSize: '12px', color: t.text2 }}>Searching...</span>}
          </div>
          <button
            onClick={toggleLiveTail}
            style={{
              display: 'flex',
              alignItems: 'center',
              gap: '9px',
              padding: '11px 20px',
              borderRadius: '14px',
              border: `1px solid ${t.panelBorder}`,
              background: isLiveTail ? t.accent : 'transparent',
              color: isLiveTail ? '#fff' : t.text2,
              fontSize: '13px',
              fontWeight: 600,
              cursor: 'pointer',
              whiteSpace: 'nowrap',
            }}
          >
            <span style={{ width: '8px', height: '8px', borderRadius: '50%', background: isLiveTail ? '#fff' : t.text2 }} />
            Live Tail {isLiveTail ? 'On' : 'Off'}
          </button>
        </div>

        {/* Volume Chart */}
        {volumeData.length > 0 && (
          <div style={{ height: '110px', padding: '16px', borderRadius: '18px', background: t.panelBg, border: `1px solid ${t.panelBorder}`, backdropFilter: 'blur(20px) saturate(180%)', WebkitBackdropFilter: 'blur(20px) saturate(180%)' }}>
            <ResponsiveContainer width="100%" height="100%">
              <BarChart data={volumeData}>
                <defs>
                  <linearGradient id="explorerVolumeGradient" x1="0" y1="0" x2="0" y2="1">
                    <stop offset="0%" stopColor={t.accent} />
                    <stop offset="100%" stopColor={t.accent2} />
                  </linearGradient>
                </defs>
                <Tooltip
                  contentStyle={{ backgroundColor: t.dark ? 'rgba(20,20,26,0.9)' : 'rgba(255,255,255,0.9)', border: `1px solid ${t.panelBorder}`, borderRadius: '8px', fontSize: '12px' }}
                  itemStyle={{ color: t.accent }}
                  labelStyle={{ color: t.text2 }}
                />
                <Bar dataKey="count" fill="url(#explorerVolumeGradient)" radius={[4, 4, 0, 0]} />
              </BarChart>
            </ResponsiveContainer>
          </div>
        )}

        {/* Log List */}
        <div style={{ flex: 1, overflowY: 'auto', padding: '10px', borderRadius: '18px', background: t.panelBg, border: `1px solid ${t.panelBorder}`, backdropFilter: 'blur(20px) saturate(180%)', WebkitBackdropFilter: 'blur(20px) saturate(180%)', display: 'flex', flexDirection: 'column', gap: '2px' }}>
          {logs.length === 0 && !loading ? (
            <div style={{ color: t.text2, textAlign: 'center', padding: '40px', fontSize: '13px' }}>No logs match the query.</div>
          ) : (
            logs.map((log) => {
              const isSelected = selectedLog?.id === log.id;
              return (
                <div
                  key={log.id}
                  onClick={() => setSelectedLog(log)}
                  style={{
                    display: 'flex',
                    gap: '16px',
                    padding: '9px 14px',
                    borderRadius: '8px',
                    cursor: 'pointer',
                    fontFamily: 'monospace',
                    fontSize: '12.5px',
                    borderLeft: `3px solid ${getLevelColor(log.level)}`,
                    background: isSelected ? (t.dark ? 'rgba(255,255,255,0.08)' : 'rgba(0,0,0,0.05)') : 'transparent',
                  }}
                  onMouseEnter={(e) => { if (!isSelected) e.currentTarget.style.background = t.dark ? 'rgba(255,255,255,0.05)' : 'rgba(0,0,0,0.03)'; }}
                  onMouseLeave={(e) => { e.currentTarget.style.background = isSelected ? (t.dark ? 'rgba(255,255,255,0.08)' : 'rgba(0,0,0,0.05)') : 'transparent'; }}
                >
                  <span style={{ color: t.text2, width: '80px', flexShrink: 0 }}>
                    {new Date(log.timestamp).toLocaleTimeString([], { hour12: false })}
                  </span>
                  <span style={{ color: getLevelColor(log.level), width: '48px', fontWeight: 700, flexShrink: 0 }}>
                    {log.level}
                  </span>
                  <span style={{ color: t.accent, width: '140px', flexShrink: 0, whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis' }}>
                    {log.service_name}
                  </span>
                  <span style={{ color: t.text1, flex: 1, whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis' }}>
                    {log.message}
                  </span>
                </div>
              );
            })
          )}
        </div>
      </div>

      {/* Right Drawer - Log Details */}
      {selectedLog && (
        <div style={{ ...glassPanel, width: 'clamp(260px, 26vw, 380px)', flexShrink: 0, display: 'flex', flexDirection: 'column', borderRadius: '22px', overflow: 'hidden' }}>
          <div style={{ padding: '18px 20px', borderBottom: `1px solid ${t.panelBorder}`, display: 'flex', justifyContent: 'space-between', alignItems: 'center' }}>
            <h3 style={{ fontSize: '15px', fontWeight: 600, color: t.text1, margin: 0 }}>Log Details</h3>
            <button
              onClick={() => setSelectedLog(null)}
              style={{ background: 'transparent', border: 'none', color: t.text2, cursor: 'pointer', fontSize: '18px', lineHeight: 1, padding: 0 }}
            >
              ✕
            </button>
          </div>
          <div style={{ padding: '20px', overflowY: 'auto', flex: 1 }}>

            <div style={{ marginBottom: '20px' }}>
              <div style={{ fontSize: '11px', color: t.text2, textTransform: 'uppercase', letterSpacing: '0.04em', marginBottom: '6px' }}>Timestamp</div>
              <div style={{ fontSize: '14px', color: t.text1 }}>{new Date(selectedLog.timestamp).toLocaleString()}</div>
            </div>

            <div style={{ marginBottom: '20px' }}>
              <div style={{ fontSize: '11px', color: t.text2, textTransform: 'uppercase', letterSpacing: '0.04em', marginBottom: '6px' }}>Service</div>
              <div style={{ fontSize: '14px', color: t.accent }}>{selectedLog.service_name}</div>
            </div>

            <div style={{ marginBottom: '20px' }}>
              <div style={{ fontSize: '11px', color: t.text2, textTransform: 'uppercase', letterSpacing: '0.04em', marginBottom: '6px' }}>Message</div>
              <div style={{ fontSize: '12.5px', fontFamily: 'monospace', color: t.text1, background: t.dark ? 'rgba(0,0,0,0.25)' : 'rgba(0,0,0,0.05)', padding: '12px', borderRadius: '10px', wordBreak: 'break-word' }}>
                {selectedLog.message}
              </div>
            </div>

            {selectedLog.trace_id && (
              <div style={{ marginBottom: '20px' }}>
                <div style={{ fontSize: '11px', color: t.text2, textTransform: 'uppercase', letterSpacing: '0.04em', marginBottom: '6px' }}>Distributed Trace</div>
                <button
                  style={{
                    width: '100%',
                    padding: '11px',
                    borderRadius: '10px',
                    border: `1px solid ${t.accent}44`,
                    background: 'transparent',
                    color: t.accent,
                    fontSize: '13px',
                    fontWeight: 600,
                    display: 'flex',
                    justifyContent: 'center',
                    alignItems: 'center',
                    gap: '8px',
                    cursor: 'pointer',
                  }}
                >
                  <span className="material-symbols-outlined" style={{ fontSize: '16px' }}>hub</span>
                  View Trace {selectedLog.trace_id.substring(0, 8)}...
                </button>
              </div>
            )}

            <div>
              <div style={{ fontSize: '11px', color: t.text2, textTransform: 'uppercase', letterSpacing: '0.04em', marginBottom: '6px' }}>Raw JSON</div>
              <pre style={{ margin: 0, fontSize: '11px', color: t.text2, background: t.dark ? 'rgba(0,0,0,0.25)' : 'rgba(0,0,0,0.05)', padding: '12px', borderRadius: '10px', whiteSpace: 'pre-wrap', wordBreak: 'break-all' }}>
                {JSON.stringify(selectedLog._raw, null, 2)}
              </pre>
            </div>

          </div>
        </div>
      )}

    </div>
  );
}
