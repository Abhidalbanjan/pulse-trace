"use client";

import React, { useState, useEffect, useRef } from 'react';
import { fetchWithAuth } from '@/lib/api';
import { BarChart, Bar, XAxis, YAxis, Tooltip, ResponsiveContainer, Cell } from 'recharts';

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
      case 'FATAL': return 'var(--status-red)';
      case 'WARN': return 'var(--status-orange)';
      case 'INFO': return 'var(--status-green)';
      case 'DEBUG': return 'var(--text-secondary)';
      default: return 'var(--text-primary)';
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

  return (
    <div style={{ display: 'flex', gap: '24px', height: 'calc(100vh - 120px)' }}>
      
      {/* Sidebar - Faceted Search */}
      <div className="glass-panel" style={{ width: '280px', padding: '24px', display: 'flex', flexDirection: 'column', gap: '24px', overflowY: 'auto' }}>
        <div>
          <h3 style={{ fontSize: '16px', fontWeight: 600, marginBottom: '16px', letterSpacing: '0.5px' }}>FACETS</h3>
          
          <div style={{ marginBottom: '24px' }}>
            <label style={{ display: 'block', fontSize: '12px', color: 'var(--text-secondary)', marginBottom: '8px', textTransform: 'uppercase' }}>Service Name</label>
            <div style={{ display: 'flex', flexDirection: 'column', gap: '8px' }}>
              {serviceBuckets.length === 0 ? <div style={{ fontSize: '13px', color: 'var(--text-secondary)' }}>No data</div> : null}
              {serviceBuckets.map(b => (
                <div 
                  key={b.key} 
                  onClick={() => addFilter("service_name", b.key.toString())}
                  style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', fontSize: '13px', cursor: 'pointer', padding: '4px 8px', borderRadius: '4px', transition: 'background 0.2s' }}
                  onMouseEnter={(e) => e.currentTarget.style.background = 'rgba(255,255,255,0.05)'}
                  onMouseLeave={(e) => e.currentTarget.style.background = 'transparent'}
                >
                  <span style={{ color: 'var(--accent-blue)', whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis' }}>{b.key}</span>
                  <span style={{ color: 'var(--text-secondary)', fontSize: '11px', background: 'rgba(255,255,255,0.1)', padding: '2px 6px', borderRadius: '12px' }}>{b.doc_count}</span>
                </div>
              ))}
            </div>
          </div>

          <div style={{ marginBottom: '16px' }}>
            <label style={{ display: 'block', fontSize: '12px', color: 'var(--text-secondary)', marginBottom: '8px', textTransform: 'uppercase' }}>Level</label>
            <div style={{ display: 'flex', flexDirection: 'column', gap: '8px' }}>
              {levelBuckets.length === 0 ? <div style={{ fontSize: '13px', color: 'var(--text-secondary)' }}>No data</div> : null}
              {levelBuckets.map(b => (
                <div 
                  key={b.key} 
                  onClick={() => addFilter("level", b.key.toString())}
                  style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', fontSize: '13px', cursor: 'pointer', padding: '4px 8px', borderRadius: '4px', transition: 'background 0.2s' }}
                  onMouseEnter={(e) => e.currentTarget.style.background = 'rgba(255,255,255,0.05)'}
                  onMouseLeave={(e) => e.currentTarget.style.background = 'transparent'}
                >
                  <span style={{ color: getLevelColor(b.key.toString()), fontWeight: 500 }}>{b.key}</span>
                  <span style={{ color: 'var(--text-secondary)', fontSize: '11px', background: 'rgba(255,255,255,0.1)', padding: '2px 6px', borderRadius: '12px' }}>{b.doc_count}</span>
                </div>
              ))}
            </div>
          </div>
        </div>
      </div>

      {/* Main Content - Log List & Analytics */}
      <div style={{ flex: 1, display: 'flex', gap: '24px', overflow: 'hidden' }}>
        
        {/* Logs + Chart Column */}
        <div className="glass-panel" style={{ flex: selectedLog ? '1' : '100%', display: 'flex', flexDirection: 'column', overflow: 'hidden', transition: 'all 0.3s ease' }}>
          
          {/* Toolbar */}
          <div style={{ padding: '16px 24px', borderBottom: '1px solid var(--border-color)', display: 'flex', justifyContent: 'space-between', alignItems: 'center', gap: '16px' }}>
            <input 
              type="text" 
              value={query}
              onChange={(e) => setQuery(e.target.value)}
              placeholder="Search logs... (e.g. level:ERROR)"
              style={{ flex: 1, background: 'rgba(0,0,0,0.4)', border: '1px solid var(--border-color)', color: 'white', padding: '8px 16px', borderRadius: '8px', outline: 'none' }}
              onKeyDown={(e) => e.key === 'Enter' && fetchLogs()}
            />
            <div style={{ display: 'flex', gap: '12px', alignItems: 'center' }}>
              {loading && <span style={{ fontSize: '12px', color: 'var(--text-secondary)' }}>Searching...</span>}
              <button 
                onClick={toggleLiveTail}
                className={isLiveTail ? "btn-primary" : "btn-secondary"} 
                style={{ padding: '8px 16px', fontSize: '13px', display: 'flex', alignItems: 'center', gap: '8px', transition: 'all 0.2s' }}
              >
                <div style={{ width: '8px', height: '8px', borderRadius: '50%', background: isLiveTail ? 'white' : 'var(--text-secondary)' }} />
                Live Tail {isLiveTail ? 'On' : 'Off'}
              </button>
            </div>
          </div>

          {/* Time Series Volume Chart */}
          {volumeData.length > 0 && (
            <div style={{ height: '120px', padding: '16px 24px 0 24px', borderBottom: '1px solid var(--border-color)' }}>
              <ResponsiveContainer width="100%" height="100%">
                <BarChart data={volumeData}>
                  <Tooltip 
                    contentStyle={{ backgroundColor: 'rgba(10, 10, 15, 0.9)', border: '1px solid var(--border-color)', borderRadius: '8px' }}
                    itemStyle={{ color: 'var(--accent-blue)' }}
                    labelStyle={{ color: 'var(--text-secondary)' }}
                  />
                  <Bar dataKey="count" fill="var(--accent-blue)" radius={[4, 4, 0, 0]} />
                </BarChart>
              </ResponsiveContainer>
            </div>
          )}

          {/* Log Viewer */}
          <div style={{ flex: 1, overflowY: 'auto', padding: '16px', fontFamily: 'monospace', fontSize: '13px' }}>
            {logs.length === 0 && !loading ? (
              <div style={{ color: 'var(--text-secondary)', textAlign: 'center', padding: '40px' }}>No logs match the query.</div>
            ) : (
              <div style={{ display: 'flex', flexDirection: 'column', gap: '4px' }}>
                {logs.map((log) => (
                  <div 
                    key={log.id} 
                    onClick={() => setSelectedLog(log)}
                    style={{ 
                      display: 'flex', 
                      gap: '16px', 
                      padding: '8px 12px', 
                      borderRadius: '6px',
                      cursor: 'pointer',
                      background: selectedLog?.id === log.id ? 'rgba(255,255,255,0.08)' : 'transparent',
                      borderLeft: `3px solid ${getLevelColor(log.level)}`,
                      transition: 'background 0.2s'
                    }}
                    onMouseEnter={(e) => e.currentTarget.style.background = 'rgba(255,255,255,0.08)'}
                    onMouseLeave={(e) => e.currentTarget.style.background = selectedLog?.id === log.id ? 'rgba(255,255,255,0.08)' : 'transparent'}
                  >
                    <span style={{ color: 'var(--text-secondary)', width: '130px', flexShrink: 0 }}>
                      {new Date(log.timestamp).toLocaleTimeString([], { hour12: false })}
                    </span>
                    <span style={{ color: getLevelColor(log.level), width: '50px', fontWeight: 600, flexShrink: 0 }}>
                      {log.level}
                    </span>
                    <span style={{ color: 'var(--accent-blue)', width: '140px', flexShrink: 0, textOverflow: 'ellipsis', overflow: 'hidden', whiteSpace: 'nowrap' }}>
                      {log.service_name}
                    </span>
                    <span style={{ color: 'var(--text-primary)', flex: 1, whiteSpace: 'pre-wrap', wordBreak: 'break-word' }}>
                      {log.message}
                    </span>
                  </div>
                ))}
              </div>
            )}
          </div>
        </div>

        {/* Right Drawer - Log Details */}
        {selectedLog && (
          <div className="glass-panel" style={{ width: '400px', flexShrink: 0, display: 'flex', flexDirection: 'column', overflow: 'hidden' }}>
            <div style={{ padding: '16px', borderBottom: '1px solid var(--border-color)', display: 'flex', justifyContent: 'space-between', alignItems: 'center' }}>
              <h3 style={{ fontSize: '16px', fontWeight: 600 }}>Log Details</h3>
              <button 
                onClick={() => setSelectedLog(null)}
                style={{ background: 'transparent', border: 'none', color: 'var(--text-secondary)', cursor: 'pointer', fontSize: '18px' }}
              >
                ✕
              </button>
            </div>
            <div style={{ flex: 1, overflowY: 'auto', padding: '16px' }}>
              
              <div style={{ marginBottom: '24px' }}>
                <div style={{ fontSize: '12px', color: 'var(--text-secondary)', marginBottom: '4px' }}>Timestamp</div>
                <div style={{ fontSize: '14px' }}>{new Date(selectedLog.timestamp).toLocaleString()}</div>
              </div>

              <div style={{ marginBottom: '24px' }}>
                <div style={{ fontSize: '12px', color: 'var(--text-secondary)', marginBottom: '4px' }}>Service</div>
                <div style={{ fontSize: '14px', color: 'var(--accent-blue)' }}>{selectedLog.service_name}</div>
              </div>

              <div style={{ marginBottom: '24px' }}>
                <div style={{ fontSize: '12px', color: 'var(--text-secondary)', marginBottom: '4px' }}>Message</div>
                <div style={{ fontSize: '13px', fontFamily: 'monospace', background: 'rgba(0,0,0,0.3)', padding: '12px', borderRadius: '8px', wordBreak: 'break-word' }}>
                  {selectedLog.message}
                </div>
              </div>

              {selectedLog.trace_id && (
                <div style={{ marginBottom: '24px' }}>
                  <div style={{ fontSize: '12px', color: 'var(--text-secondary)', marginBottom: '4px' }}>Distributed Trace</div>
                  <button className="btn-secondary" style={{ width: '100%', padding: '8px', fontSize: '13px', display: 'flex', justifyContent: 'center', gap: '8px', color: 'var(--accent-blue)', borderColor: 'rgba(0, 210, 255, 0.3)' }}>
                    <span>⎉</span> View Trace {selectedLog.trace_id.substring(0, 8)}...
                  </button>
                </div>
              )}

              <div>
                <div style={{ fontSize: '12px', color: 'var(--text-secondary)', marginBottom: '8px' }}>Raw JSON</div>
                <pre style={{ margin: 0, fontSize: '11px', color: 'var(--text-secondary)', background: 'rgba(0,0,0,0.3)', padding: '12px', borderRadius: '8px', whiteSpace: 'pre-wrap', wordBreak: 'break-all' }}>
                  {JSON.stringify(selectedLog._raw, null, 2)}
                </pre>
              </div>

            </div>
          </div>
        )}

      </div>
    </div>
  );
}
