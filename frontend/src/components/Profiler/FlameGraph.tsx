"use client";

// Interactive flame graph (Profiler · E1). Renders the positioned frames the
// gateway returns (each frame carries a normalized x/width in [0,1] of the root
// total, plus raw self/total samples). Supports click-to-zoom, hover detail, and
// search highlighting — the things that make a flame graph explorable rather than
// just a picture. Pure presentation over pre-computed layout; no data fetching.

import React, { useState, useMemo } from 'react';
import type { ThemeTokens } from '@/lib/theme';

export interface FlameFrame {
  depth: number;
  x: number;      // normalized left edge in [0,1] of root total
  width: number;  // normalized width in [0,1]
  self: number;
  total: number;
  name: string;
}

const ROW_H = 18;

// frameColor gives each function a stable warm hue (the classic flame palette)
// from a hash of its name, so the same function reads the same color run to run.
function frameColor(name: string): string {
  let h = 0;
  for (let i = 0; i < name.length; i++) h = (h * 31 + name.charCodeAt(i)) | 0;
  const hue = 8 + (Math.abs(h) % 42); // 8–50°: red → orange → yellow
  return `hsl(${hue}, 68%, 52%)`;
}

// diffColor paints a frame by how its share of the profile moved vs baseline
// (Profiler · E2): red grew, green shrank, neutral if flat. The ± band avoids
// lighting up every frame over profiling noise.
function diffColor(delta: number | undefined, t: ThemeTokens): string {
  if (delta == null || Math.abs(delta) < 0.5) return t.dark ? 'rgba(255,255,255,0.14)' : 'rgba(0,0,0,0.14)';
  const mag = Math.min(1, Math.abs(delta) / 10); // saturate by 10pp
  const alpha = 0.35 + mag * 0.5;
  return delta > 0 ? `rgba(224,82,75,${alpha})` : `rgba(37,169,107,${alpha})`;
}

export function FlameGraph({ frames, rootTotal, t, deltaByName }: { frames: FlameFrame[]; rootTotal: number; t: ThemeTokens; deltaByName?: Record<string, number> }) {
  // focus is the currently zoomed x-domain [x, x+width]; clicking a frame zooms
  // to it, and the root frame (or the Reset button) zooms back out.
  const [focus, setFocus] = useState<{ x: number; width: number }>({ x: 0, width: 1 });
  const [search, setSearch] = useState('');
  const [hover, setHover] = useState<{ f: FlameFrame; left: number; top: number } | null>(null);

  const maxDepth = useMemo(() => frames.reduce((m, f) => Math.max(m, f.depth), 0), [frames]);
  const height = (maxDepth + 1) * ROW_H;
  const isZoomed = focus.x !== 0 || focus.width !== 1;
  const q = search.trim().toLowerCase();
  const isDiff = deltaByName != null;

  if (frames.length === 0) {
    return <div style={{ padding: '32px', textAlign: 'center', color: t.text2, fontSize: '13px' }}>No flame data in this window.</div>;
  }

  return (
    <div style={{ position: 'relative' }}>
      <div style={{ display: 'flex', gap: '10px', alignItems: 'center', marginBottom: '10px' }}>
        <input
          value={search}
          onChange={(e) => setSearch(e.target.value)}
          placeholder="Search frames…"
          aria-label="Search flame graph"
          style={{ flex: 1, maxWidth: '260px', padding: '7px 11px', fontSize: '12.5px', background: t.dark ? 'rgba(255,255,255,0.05)' : '#fff', border: '1px solid ' + t.panelBorder, borderRadius: '8px', color: t.text1 }}
        />
        {isZoomed && (
          <button
            onClick={() => setFocus({ x: 0, width: 1 })}
            style={{ padding: '7px 13px', fontSize: '12.5px', fontWeight: 600, background: 'transparent', border: '1px solid ' + t.panelBorder, borderRadius: '8px', color: t.text2, cursor: 'pointer' }}
          >
            ⤢ Reset zoom
          </button>
        )}
        <span style={{ fontSize: '11.5px', color: t.text2 }}>{frames.length} frames</span>
      </div>

      <div style={{ position: 'relative', width: '100%', height, background: t.dark ? 'rgba(255,255,255,0.03)' : 'rgba(0,0,0,0.02)', borderRadius: '8px', overflow: 'hidden' }}>
        {frames.map((f, i) => {
          // Position the frame within the focus domain; skip anything outside it
          // or too thin to see (keeps the DOM small on deep profiles).
          const relX = (f.x - focus.x) / focus.width;
          const relW = f.width / focus.width;
          if (relX + relW <= 0 || relX >= 1 || relW <= 0.0015) return null;
          const clampedX = Math.max(0, relX);
          const clampedW = Math.min(1, relX + relW) - clampedX;
          const matched = q === '' || f.name.toLowerCase().includes(q);
          const pct = rootTotal > 0 ? (f.total / rootTotal) * 100 : 0;
          return (
            <div
              key={i}
              onClick={() => setFocus({ x: f.x, width: f.width || 1 })}
              onMouseEnter={(e) => setHover({ f, left: e.clientX, top: e.clientY })}
              onMouseMove={(e) => setHover({ f, left: e.clientX, top: e.clientY })}
              onMouseLeave={() => setHover(null)}
              title={`${f.name} — ${pct.toFixed(2)}%`}
              style={{
                position: 'absolute',
                left: `${clampedX * 100}%`,
                width: `${clampedW * 100}%`,
                top: f.depth * ROW_H,
                height: ROW_H - 1,
                background: isDiff ? diffColor(deltaByName?.[f.name], t) : frameColor(f.name),
                opacity: matched ? 1 : 0.22,
                border: '1px solid rgba(0,0,0,0.18)',
                borderRadius: '2px',
                fontSize: '10.5px',
                lineHeight: `${ROW_H - 1}px`,
                color: 'rgba(0,0,0,0.82)',
                paddingLeft: '4px',
                overflow: 'hidden',
                whiteSpace: 'nowrap',
                cursor: 'pointer',
                boxSizing: 'border-box',
              }}
            >
              {clampedW > 0.04 ? f.name : ''}
            </div>
          );
        })}
      </div>

      {hover && (
        <div
          style={{
            position: 'fixed', left: Math.min(hover.left + 14, (typeof window !== 'undefined' ? window.innerWidth : 9999) - 320),
            top: hover.top + 14, zIndex: 1200, pointerEvents: 'none',
            background: t.panelBg, border: '1px solid ' + t.panelBorder, borderRadius: '8px',
            padding: '8px 11px', fontSize: '12px', color: t.text1, maxWidth: '320px', boxShadow: t.shadow,
          }}
        >
          <div style={{ fontFamily: 'monospace', marginBottom: '4px', wordBreak: 'break-all' }}>{hover.f.name}</div>
          <div style={{ color: t.text2 }}>
            {rootTotal > 0 ? ((hover.f.total / rootTotal) * 100).toFixed(2) : '0'}% of total · {hover.f.total.toLocaleString()} samples · {hover.f.self.toLocaleString()} self
          </div>
          {isDiff && deltaByName?.[hover.f.name] != null && (
            <div style={{ marginTop: '3px', fontWeight: 700, color: deltaByName[hover.f.name] > 0 ? '#e0524b' : deltaByName[hover.f.name] < 0 ? '#25a96b' : t.text2 }}>
              {deltaByName[hover.f.name] > 0 ? '▲ +' : deltaByName[hover.f.name] < 0 ? '▼ ' : ''}{deltaByName[hover.f.name].toFixed(2)} pp vs baseline
            </div>
          )}
        </div>
      )}
    </div>
  );
}
