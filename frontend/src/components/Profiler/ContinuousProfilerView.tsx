"use client";

import React, { useState } from 'react';

export function ContinuousProfilerView() {
  const [profileType, setProfileType] = useState('process_cpu');
  const [service, setService] = useState('gateway-service');
  const [timeRange, setTimeRange] = useState('Last 1 hour');

  // We proxy Pyroscope directly via an iframe pointing to our gateway proxy
  // The gateway strips the /api/v1/profiler prefix and routes to Pyroscope's internal UI.
  // We use a relative path. In development, next.config.ts rewrites this to the gateway.
  // In production, the ingress controller or load balancer would route /api/v1/ to the gateway.
  const pyroscopeUrl = `/api/v1/profiler/?query=${service}.${profileType}{}`;

  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: '24px', height: '100%' }}>
      
      {/* Header */}
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center' }}>
        <div>
          <h2 style={{ fontSize: '24px', fontWeight: 600, marginBottom: '8px' }}>Continuous Profiler</h2>
          <p style={{ color: 'var(--text-secondary)' }}>Identify performance bottlenecks in production code down to the line number.</p>
        </div>
      </div>

      {/* Toolbar */}
      <div className="glass-panel" style={{ padding: '16px', display: 'flex', gap: '16px', alignItems: 'center' }}>
        
        <div style={{ display: 'flex', gap: '12px' }}>
          <select 
            value={service}
            onChange={(e) => setService(e.target.value)}
            className="input-field" 
            style={{ padding: '8px 12px', minWidth: '180px' }}
          >
            <option value="gateway-service">gateway-service</option>
            <option value="cart-service">cart-service</option>
            <option value="payment-service">payment-service</option>
          </select>
          
          <select 
            value={profileType}
            onChange={(e) => setProfileType(e.target.value)}
            className="input-field" 
            style={{ padding: '8px 12px', minWidth: '180px' }}
          >
            <option value="process_cpu">CPU (process_cpu)</option>
            <option value="memory_alloc_objects">Memory Allocations</option>
            <option value="memory_inuse_space">Memory In-Use</option>
          </select>

          <select 
            value={timeRange}
            onChange={(e) => setTimeRange(e.target.value)}
            className="input-field" 
            style={{ padding: '8px 12px', minWidth: '180px' }}
          >
            <option>Last 15 minutes</option>
            <option>Last 1 hour</option>
            <option>Last 24 hours</option>
          </select>
        </div>

        <div style={{ flex: 1 }} />
        
        <button className="btn-primary" style={{ padding: '10px 24px' }}>
          Compare Profiles
        </button>
      </div>

      {/* Interactive Flame Graph (Pyroscope UI Embed) */}
      <div className="glass-panel" style={{ flex: 1, padding: 0, overflow: 'hidden', display: 'flex', flexDirection: 'column' }}>
        <div style={{ padding: '16px 24px', borderBottom: '1px solid var(--border-color)', background: 'rgba(0,0,0,0.2)' }}>
          <h3 style={{ fontSize: '15px', fontWeight: 600 }}>Flame Graph: {service} ({profileType})</h3>
        </div>
        
        <div style={{ flex: 1, position: 'relative', background: '#ffffff' }}>
          {/* We embed Pyroscope directly. Note: Pyroscope UI is light-themed by default. */}
          <iframe 
            src={pyroscopeUrl} 
            style={{ width: '100%', height: '100%', border: 'none' }}
            title="Continuous Profiler Flamegraph"
            sandbox="allow-same-origin allow-scripts allow-popups allow-forms"
          />
        </div>
      </div>

    </div>
  );
}
