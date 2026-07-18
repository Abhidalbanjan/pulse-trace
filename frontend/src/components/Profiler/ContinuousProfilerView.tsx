"use client";

import React, { useState } from 'react';
import { useSearchParams } from 'next/navigation';
import { PROFILED_SERVICES } from '@/lib/profiledServices';
import { useTheme } from '@/context/ThemeContext';

const TIME_RANGE_SECONDS: Record<string, number> = {
  'Last 15 minutes': 15 * 60,
  'Last 1 hour': 60 * 60,
  'Last 24 hours': 24 * 60 * 60,
};

export function ContinuousProfilerView() {
  const { tokens: t } = useTheme();
  const searchParams = useSearchParams();
  const spanId = searchParams.get('spanId');
  const requestedService = searchParams.get('service');

  const [profileType, setProfileType] = useState('process_cpu');
  const [service, setService] = useState(
    requestedService && PROFILED_SERVICES.includes(requestedService) ? requestedService : PROFILED_SERVICES[0]
  );
  const [timeRange, setTimeRange] = useState('Last 1 hour');
  const [compareMode, setCompareMode] = useState(false);

  // Profiling <-> trace linkage: when arriving from a trace span, filter the
  // flame graph down to just the samples pprof-labeled with that span_id
  // (see shared/middleware/tracing.go's pyroscope.TagWrapper).
  const selector = spanId ? `{span_id="${spanId}"}` : '{}';
  const query = `${service}.${profileType}${selector}`;

  // We proxy Pyroscope directly via an iframe pointing to our gateway proxy
  // The gateway strips the /api/v1/profiler prefix and routes to Pyroscope's internal UI.
  // We use a relative path. In development, next.config.ts rewrites this to the gateway.
  // In production, the ingress controller or load balancer would route /api/v1/ to the gateway.
  const buildProfilerUrl = () => {
    if (!compareMode) {
      return `/api/v1/profiler/?query=${query}`;
    }

    // Compare mode: current window (right) vs the immediately preceding window of the
    // same length (left) - Pyroscope's built-in diff view, day/hour-over-day/hour comparison.
    const rangeSeconds = TIME_RANGE_SECONDS[timeRange] || 3600;
    const now = Math.floor(Date.now() / 1000);
    const rightUntil = now;
    const rightFrom = now - rangeSeconds;
    const leftUntil = rightFrom;
    const leftFrom = rightFrom - rangeSeconds;

    const params = new URLSearchParams({
      query,
      from: String(leftFrom),
      until: String(rightUntil),
      leftQuery: query,
      leftFrom: String(leftFrom),
      leftUntil: String(leftUntil),
      rightQuery: query,
      rightFrom: String(rightFrom),
      rightUntil: String(rightUntil),
    });
    return `/api/v1/profiler/comparison-diff?${params.toString()}`;
  };

  const pyroscopeUrl = buildProfilerUrl();

  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: '24px', height: '100%' }}>

      {/* Header */}
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center' }}>
        <div>
          <h2 style={{ fontSize: '26px', fontWeight: 700, margin: '0 0 8px' }}>Continuous Profiler</h2>
          <p style={{ color: t.text2, fontSize: '14.5px' }}>
            {spanId ? (
              <>Filtered to span <code style={{ background: t.dark ? 'rgba(255,255,255,0.1)' : 'rgba(0,0,0,0.06)', padding: '2px 6px', borderRadius: '4px' }}>{spanId}</code> — {' '}
                <a href="/profiler" style={{ color: t.accent }}>clear filter</a>
              </>
            ) : (
              'Identify performance bottlenecks in production code down to the line number.'
            )}
          </p>
        </div>
      </div>

      {/* Toolbar */}
      <div style={{
        display: 'flex',
        gap: '12px',
        padding: '16px',
        borderRadius: '18px',
        background: t.panelBg,
        border: '1px solid ' + t.panelBorder,
        backdropFilter: 'blur(30px)',
        marginBottom: '16px',
        flexWrap: 'wrap',
        alignItems: 'center',
      }}>

        <select
          value={service}
          onChange={(e) => setService(e.target.value)}
          style={{
            background: t.dark ? 'rgba(255,255,255,0.06)' : 'rgba(255,255,255,0.7)',
            border: '1px solid ' + t.panelBorder,
            color: t.text1,
            padding: '9px 13px',
            borderRadius: '10px',
            fontSize: '13px',
            minWidth: '180px',
          }}
        >
          {PROFILED_SERVICES.map(s => <option key={s} value={s}>{s}</option>)}
        </select>

        <select
          value={profileType}
          onChange={(e) => setProfileType(e.target.value)}
          style={{
            background: t.dark ? 'rgba(255,255,255,0.06)' : 'rgba(255,255,255,0.7)',
            border: '1px solid ' + t.panelBorder,
            color: t.text1,
            padding: '9px 13px',
            borderRadius: '10px',
            fontSize: '13px',
            minWidth: '180px',
          }}
        >
          <option value="process_cpu">CPU (process_cpu)</option>
          <option value="memory_alloc_objects">Memory Allocations</option>
          <option value="memory_inuse_space">Memory In-Use</option>
        </select>

        <select
          value={timeRange}
          onChange={(e) => setTimeRange(e.target.value)}
          style={{
            background: t.dark ? 'rgba(255,255,255,0.06)' : 'rgba(255,255,255,0.7)',
            border: '1px solid ' + t.panelBorder,
            color: t.text1,
            padding: '9px 13px',
            borderRadius: '10px',
            fontSize: '13px',
            minWidth: '180px',
          }}
        >
          <option>Last 15 minutes</option>
          <option>Last 1 hour</option>
          <option>Last 24 hours</option>
        </select>

        <div style={{ flex: 1 }} />

        <button
          onClick={() => setCompareMode(prev => !prev)}
          style={{
            padding: '10px 22px',
            borderRadius: '10px',
            border: 'none',
            background: `linear-gradient(135deg, ${t.accent}, ${t.accent2})`,
            color: '#fff',
            fontWeight: 600,
            fontSize: '13px',
            cursor: 'pointer',
          }}
        >
          {compareMode ? 'Back to Flame Graph' : 'Compare Profiles'}
        </button>
      </div>

      {/* Interactive Flame Graph (Pyroscope UI Embed) */}
      <div style={{
        flex: 1,
        display: 'flex',
        flexDirection: 'column',
        borderRadius: '20px',
        overflow: 'hidden',
        background: t.panelBg,
        border: '1px solid ' + t.panelBorder,
        backdropFilter: 'blur(30px) saturate(180%)',
        boxShadow: t.shadow,
        minHeight: '420px',
      }}>
        <div style={{ padding: '16px 24px', borderBottom: '1px solid ' + t.panelBorder, fontSize: '14px', fontWeight: 700 }}>
          {compareMode
            ? `Comparing ${service} (${profileType}): ${timeRange} vs. the preceding period`
            : `Flame Graph: ${service} (${profileType})`}
        </div>

        {/* We embed Pyroscope directly. Note: Pyroscope UI is light-themed by default. */}
        <iframe
          key={pyroscopeUrl}
          src={pyroscopeUrl}
          style={{ flex: 1, width: '100%', border: 'none' }}
          title="Continuous Profiler Flamegraph"
          sandbox="allow-same-origin allow-scripts allow-popups allow-forms"
        />
      </div>

    </div>
  );
}
