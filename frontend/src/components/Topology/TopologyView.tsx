"use client";

import React, { useState, useEffect, useCallback, useMemo } from 'react';
import { errMessage } from '@/lib/errMessage';
import dagre from 'dagre';
import ReactFlow, {
  Node,
  Edge,
  Background,
  Controls,
  MiniMap,
  useNodesState,
  useEdgesState,
  Handle,
  Position
} from 'reactflow';
import 'reactflow/dist/style.css';
import { fetchWithAuth } from '@/lib/api';
import { useTheme } from '@/context/ThemeContext';

// Custom Node matching the Enterprise UI Redesign glass-node style
interface NodeData { label: string; state: string; highlighted?: boolean; }
interface TopoNode { id: string; state: string; }
interface TopoEdge { source: string; target: string; request_count?: number; error_count?: number; avg_latency_ms?: number; is_causal?: boolean; reason?: string; }
const CustomNode = ({ data }: { data: NodeData }) => {
  const { tokens: t } = useTheme();

  const getStateColor = (state: string) => {
    switch (state) {
      case 'HEALTHY': return t.green;
      case 'DEGRADED': return t.amber;
      case 'UNHEALTHY': return t.red;
      default: return t.panelBorder;
    }
  };

  const isHighlighted = !!data.highlighted;
  const stateColor = getStateColor(data.state);
  // Anomaly overlay (Topology · E2): a service in a warning/unhealthy state
  // pulses so it draws the eye on a large map, distinct from the gold root-cause
  // highlight applied to the causal chain.
  const isAnomalous = data.state === 'DEGRADED' || data.state === 'UNHEALTHY' || data.state === 'PREDICTIVE_WARNING';

  return (
    <div style={{
      background: t.panelBg,
      backdropFilter: 'blur(24px) saturate(180%)',
      WebkitBackdropFilter: 'blur(24px) saturate(180%)',
      border: `${isHighlighted ? '2px' : '1px'} solid ${isHighlighted ? t.gold : stateColor}`,
      borderRadius: '14px',
      padding: '14px',
      minWidth: '170px',
      minHeight: '78px',
      boxShadow: isHighlighted ? `0 0 26px ${t.gold}80` : t.shadow,
      color: t.text1,
      textAlign: 'center',
      transition: 'all 0.3s ease',
      ...(isAnomalous && !isHighlighted ? { ['--pulse-color' as string]: stateColor + '99', animation: 'topoPulse 1.8s ease-in-out infinite' } : {}),
    }}>
      <Handle type="target" position={Position.Top} style={{ background: t.text2 }} />
      <div style={{ fontWeight: 700, fontSize: '13.5px', marginBottom: '6px' }}>{data.label}</div>
      <div style={{
        fontSize: '10px',
        color: stateColor,
        background: t.dark ? 'rgba(255,255,255,0.08)' : 'rgba(0,0,0,0.05)',
        padding: '2px 9px',
        borderRadius: '100px',
        display: 'inline-block'
      }}>
        {data.state}
      </div>
      {isHighlighted && (
        <div style={{ fontSize: '10px', color: t.gold, marginTop: '4px' }}>⚡ root cause path</div>
      )}
      <Handle type="source" position={Position.Bottom} style={{ background: t.text2 }} />
    </div>
  );
};

const nodeTypes = {
  custom: CustomNode,
};

const NODE_WIDTH = 180;
const NODE_HEIGHT = 90;

// Real graph layout via dagre (layered, dependency-aware) instead of a naive grid.
function layoutWithDagre(nodes: Node[], edges: Edge[]): Node[] {
  const g = new dagre.graphlib.Graph();
  g.setGraph({ rankdir: 'TB', nodesep: 60, ranksep: 100 });
  g.setDefaultEdgeLabel(() => ({}));

  nodes.forEach((n) => g.setNode(n.id, { width: NODE_WIDTH, height: NODE_HEIGHT }));
  edges.forEach((e) => g.setEdge(e.source, e.target));

  dagre.layout(g);

  return nodes.map((n) => {
    const pos = g.node(n.id);
    return {
      ...n,
      position: { x: pos.x - NODE_WIDTH / 2, y: pos.y - NODE_HEIGHT / 2 },
    };
  });
}

interface RootCauseResult {
  unhealthyService: string;
  rootCause: string;
  chain: string[];
  narrative?: string;
  confidence?: number;
  model?: string;
  source: 'ai' | 'topology'; // 'ai' = correlation-service's causal engine, 'topology' = fallback graph walk
}

interface CausalLink {
  from_service: string;
  to_service: string;
  evidence: string;
}

interface Incident {
  id: string;
  services: string[];
  causal?: {
    chain: CausalLink[];
    narrative: string;
    root_cause: string;
    confidence: number;
    model: string;
  };
}

export function TopologyView() {
  const { tokens: t } = useTheme();
  const [nodes, setNodes, onNodesChange] = useNodesState([]);
  const [edges, setEdges, onEdgesChange] = useEdgesState([]);
  // The laid-out graph as loaded, kept separate from the displayed nodes/edges so
  // filter/focus/search can re-derive the view (opacity, highlight) without
  // re-fetching or re-running dagre — the key to staying responsive at 100s of nodes.
  const [baseNodes, setBaseNodes] = useState<Node[]>([]);
  const [baseEdges, setBaseEdges] = useState<Edge[]>([]);
  const [loading, setLoading] = useState(true);
  const [analyzing, setAnalyzing] = useState(false);
  const [rootCauseResults, setRootCauseResults] = useState<RootCauseResult[] | null>(null);
  const [rootCauseError, setRootCauseError] = useState<string | null>(null);
  // Downstream "blast radius": what depends on a clicked service.
  const [impact, setImpact] = useState<{ service: string; downstream: string[] } | null>(null);
  const [impactLoading, setImpactLoading] = useState(false);

  // Large-graph controls (F13): search, single-node focus (dim all but its 1-hop
  // neighbours), an unhealthy-only filter, and the AI root-cause highlight set.
  const [search, setSearch] = useState('');
  const [focusNode, setFocusNode] = useState<string | null>(null);
  const [onlyUnhealthy, setOnlyUnhealthy] = useState(false);
  const [highlightIds, setHighlightIds] = useState<Set<string>>(new Set());

  const UNHEALTHY = useMemo(() => new Set(['UNHEALTHY', 'DEGRADED', 'PREDICTIVE_WARNING']), []);

  // Adjacency (both directions) so "focus" and unhealthy-context can include a
  // node's immediate neighbours.
  const neighbours = useMemo(() => {
    const m = new Map<string, Set<string>>();
    const add = (a: string, b: string) => {
      if (!m.has(a)) m.set(a, new Set());
      m.get(a)!.add(b);
    };
    for (const e of baseEdges) { add(e.source, e.target); add(e.target, e.source); }
    return m;
  }, [baseEdges]);

  const showDownstreamImpact = useCallback(async (service: string) => {
    setImpact({ service, downstream: [] });
    setImpactLoading(true);
    try {
      const res = await fetchWithAuth(`/api/v1/topology/dependencies/downstream/${encodeURIComponent(service)}`);
      const deps: string[] = res.ok ? (await res.json()) || [] : [];
      setImpact({ service, downstream: Array.isArray(deps) ? deps : [] });
    } catch {
      setImpact({ service, downstream: [] });
    } finally {
      setImpactLoading(false);
    }
  }, []);

  const loadTopology = useCallback(() => {
    setLoading(true);
    fetchWithAuth('/api/v1/topology/graph')
      .then(res => res.json())
      .then(data => {
        if (data && data.nodes) {
          const rfNodes: Node[] = data.nodes.map((n: TopoNode) => ({
            id: n.id,
            type: 'custom',
            data: { label: n.id, state: n.state, highlighted: false },
            position: { x: 0, y: 0 }, // real position assigned by dagre below
          }));

          const rfEdges: Edge[] = data.edges ? data.edges.map((e: TopoEdge, idx: number) => {
            // Real fix: the backend's JSON fields are `source`/`target`, not
            // `from`/`to` - this mismatch previously meant every edge resolved to
            // source=undefined/target=undefined and ReactFlow silently dropped it,
            // so the topology graph rendered nodes with zero visible edges.
            const requests: number = e.request_count || 0;
            const errors: number = e.error_count || 0;
            const errorRate = requests > 0 ? errors / requests : 0;
            // Incident overlay (Topology · E2): an edge on the causal path of a
            // live incident (from Neo4j causal_entries) is highlighted gold and
            // labeled with the inferred reason — the "this is how the failure
            // propagated" story drawn straight on the map.
            const isCausal = !!e.is_causal;
            const strokeColor = isCausal ? t.gold : errorRate > 0.1 ? t.red : errorRate > 0 ? t.amber : t.accent;
            // Width scales with recent traffic so a busy dependency reads as a
            // thicker line, not visually identical to a quiet one.
            const strokeWidth = isCausal ? 3.5 : requests === 0 ? 1.5 : Math.min(2 + Math.log10(requests + 1) * 1.5, 8);

            return {
              id: `e${idx}`,
              source: e.source,
              target: e.target,
              animated: isCausal || requests > 0,
              label: isCausal ? `⚠ ${e.reason || 'causal path'}` : requests > 0 ? `${requests}${errors > 0 ? ` (${errors} err)` : ''}` : undefined,
              labelStyle: { fill: isCausal ? t.gold : t.text2, fontSize: isCausal ? 11 : 10, fontWeight: isCausal ? 700 : 400 },
              style: { stroke: strokeColor, strokeWidth, ...(isCausal ? { strokeDasharray: '6 3' } : {}) },
              data: { requestCount: requests, errorCount: errors, avgLatencyMs: e.avg_latency_ms || 0 },
            };
          }) : [];

          const laidOut = layoutWithDagre(rfNodes, rfEdges);
          setBaseNodes(laidOut);
          setBaseEdges(rfEdges);
          setNodes(laidOut);
          setEdges(rfEdges);
        }
        setLoading(false);
      })
      .catch(err => {
        console.error("Failed to fetch topology:", err);
        setLoading(false);
      });
  }, [setNodes, setEdges, t]);

  useEffect(() => {
    // eslint-disable-next-line react-hooks/set-state-in-effect -- intentional one-shot fetch/hydration on mount; effect is the right place to sync from the API/localStorage
    loadTopology();
  }, [loadTopology]);

  // Derive the visible graph from the base graph + the active filters. A node is
  // "shown" when it clears the unhealthy filter and the focus scope; search dims
  // (not hides) non-matches so you keep spatial context. Edges fade unless both
  // endpoints are shown. Positions come from the base layout, so filtering never
  // re-runs dagre.
  useEffect(() => {
    if (baseNodes.length === 0) return;
    const q = search.trim().toLowerCase();

    const focusScope = (id: string) => !focusNode || id === focusNode || neighbours.get(focusNode)?.has(id);
    const healthScope = (n: Node) => {
      if (!onlyUnhealthy) return true;
      if (UNHEALTHY.has(n.data.state)) return true;
      // keep a healthy node only if it neighbours an unhealthy one (context).
      for (const nb of neighbours.get(n.id) || []) {
        const nbState = baseNodes.find(x => x.id === nb)?.data.state;
        if (nbState && UNHEALTHY.has(nbState)) return true;
      }
      return false;
    };

    const shown = new Set<string>();
    const displayNodes = baseNodes.map(n => {
      const isShown = !!focusScope(n.id) && healthScope(n);
      const matches = q === '' || n.id.toLowerCase().includes(q);
      if (isShown) shown.add(n.id);
      const opacity = !isShown ? 0.1 : (q && !matches ? 0.28 : 1);
      return {
        ...n,
        data: { ...n.data, highlighted: highlightIds.has(n.id) },
        style: { ...n.style, opacity },
      };
    });

    const displayEdges = baseEdges.map(e => {
      const visible = shown.has(e.source) && shown.has(e.target);
      const touchesFocus = !focusNode || e.source === focusNode || e.target === focusNode;
      return { ...e, style: { ...e.style, opacity: visible && touchesFocus ? 1 : 0.08 } };
    });

    setNodes(displayNodes);
    setEdges(displayEdges);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [search, focusNode, onlyUnhealthy, highlightIds, baseNodes, baseEdges, neighbours]);

  // Root-cause analysis for every unhealthy/degraded service. Prefers the real
  // causal AI engine (correlation-service's LLM/rule-based analyzer, which already
  // runs asynchronously per-incident with a confidence score and narrative) and
  // only falls back to a plain topology-graph walk if no analyzed incident exists
  // yet for that service (analysis is async and can take up to ~45s to land).
  const findRootCause = async () => {
    setAnalyzing(true);
    setRootCauseError(null);
    setRootCauseResults(null);
    try {
      const stateById = new Map(baseNodes.map(n => [n.id, n.data.state]));
      const unhealthy = baseNodes.filter(n => n.data.state === 'UNHEALTHY' || n.data.state === 'DEGRADED');

      if (unhealthy.length === 0) {
        setRootCauseResults([]);
        return;
      }

      const results: RootCauseResult[] = [];
      const highlightSet = new Set<string>();

      for (const svc of unhealthy) {
        // 1. Try the real causal AI engine: most recent open incident touching this service.
        let aiResult: RootCauseResult | null = null;
        try {
          const res = await fetchWithAuth(`/api/v1/incidents?service=${encodeURIComponent(svc.id)}&status=OPEN&page_size=1`);
          if (res.ok) {
            const json = await res.json();
            const incident: Incident | undefined = json.data?.[0];
            const causal = incident?.causal;
            if (causal && causal.chain && causal.chain.length > 0) {
              const chain = [causal.chain[0].from_service, ...causal.chain.map(l => l.to_service)];
              aiResult = {
                unhealthyService: svc.id,
                rootCause: causal.root_cause || causal.chain[0].from_service,
                chain,
                narrative: causal.narrative,
                confidence: causal.confidence,
                model: causal.model,
                source: 'ai',
              };
            }
          }
        } catch (e) {
          console.error(`Causal engine lookup failed for ${svc.id}:`, e);
        }

        if (aiResult) {
          results.push(aiResult);
          aiResult.chain.forEach(id => highlightSet.add(id));
          continue;
        }

        // 2. Fallback: no analyzed incident yet - walk the real upstream dependency
        // chain (topology-service/Neo4j) to find the deepest unhealthy ancestor.
        const chain: string[] = [svc.id];
        const visited = new Set<string>([svc.id]);
        let current = svc.id;
        let rootCause = svc.id;

        for (let hop = 0; hop < 10; hop++) {
          const res = await fetchWithAuth(`/api/v1/topology/dependencies/upstream/${encodeURIComponent(current)}`);
          if (!res.ok) break;
          const upstreamDeps: string[] = await res.json();
          if (!upstreamDeps || upstreamDeps.length === 0) break;

          const badUpstream = upstreamDeps.find(u => !visited.has(u) && (stateById.get(u) === 'UNHEALTHY' || stateById.get(u) === 'DEGRADED'));
          if (!badUpstream) break;

          chain.push(badUpstream);
          visited.add(badUpstream);
          rootCause = badUpstream;
          current = badUpstream;
        }

        results.push({ unhealthyService: svc.id, rootCause, chain, source: 'topology' });
        chain.forEach(id => highlightSet.add(id));
      }

      setRootCauseResults(results);
      setHighlightIds(highlightSet);
    } catch (err) {
      setRootCauseError(errMessage(err, 'Root cause analysis failed'));
    } finally {
      setAnalyzing(false);
    }
  };

  return (
    <div style={{ display: 'flex', flexDirection: 'column', height: 'calc(100vh - 124px)' }}>
      {/* Anomaly-node pulse keyframes (Topology · E2). */}
      <style>{`@keyframes topoPulse { 0%,100% { box-shadow: 0 0 0 0 var(--pulse-color); } 50% { box-shadow: 0 0 15px 3px var(--pulse-color); } }`}</style>

      {/* Toolbar */}
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'flex-start', marginBottom: '18px' }}>
        <div>
          <h2 style={{ fontSize: '26px', fontWeight: 700, margin: '0 0 8px' }}>Interactive Service Topology</h2>
          <p style={{ color: t.text2, margin: 0, fontSize: '14.5px' }}>Live dependency map derived from distributed traces.</p>
        </div>
        <button
          onClick={findRootCause}
          disabled={analyzing || loading}
          style={{
            display: 'flex', alignItems: 'center', gap: '8px', padding: '11px 20px', borderRadius: '12px',
            border: 'none', background: `linear-gradient(135deg, ${t.accent}, ${t.accent2})`, color: '#fff',
            fontWeight: 600, fontSize: '13.5px', cursor: analyzing || loading ? 'default' : 'pointer',
            opacity: analyzing ? 0.6 : 1,
          }}
        >
          <span className="material-symbols-outlined" style={{ fontSize: '16px' }}>bolt</span>
          {analyzing ? 'Analyzing…' : 'AI Root Cause'}
        </button>
      </div>

      {/* Large-graph controls: search, focus reset, unhealthy filter, legend */}
      <div style={{ display: 'flex', alignItems: 'center', gap: '12px', marginBottom: '16px', flexWrap: 'wrap' }}>
        <div style={{ display: 'flex', alignItems: 'center', gap: '8px', background: t.panelBg, border: `1px solid ${t.panelBorder}`, borderRadius: '12px', padding: '8px 12px', minWidth: '240px' }}>
          <span className="material-symbols-outlined" style={{ fontSize: '17px', color: t.text2 }}>search</span>
          <input
            value={search}
            onChange={(e) => setSearch(e.target.value)}
            placeholder="Search services…"
            aria-label="Search services"
            style={{ flex: 1, background: 'transparent', border: 'none', outline: 'none', color: t.text1, fontSize: '13px' }}
          />
        </div>

        <button
          onClick={() => setOnlyUnhealthy(v => !v)}
          style={{
            padding: '9px 15px', borderRadius: '12px', cursor: 'pointer', fontSize: '13px', fontWeight: 600,
            border: `1px solid ${onlyUnhealthy ? t.red : t.panelBorder}`,
            background: onlyUnhealthy ? (t.dark ? 'rgba(241,107,99,0.14)' : 'rgba(224,82,75,0.1)') : 'transparent',
            color: onlyUnhealthy ? t.red : t.text2,
          }}
        >
          Only unhealthy
        </button>

        {(focusNode || search || onlyUnhealthy) && (
          <button
            onClick={() => { setFocusNode(null); setSearch(''); setOnlyUnhealthy(false); }}
            style={{ padding: '9px 15px', borderRadius: '12px', cursor: 'pointer', fontSize: '13px', fontWeight: 600, border: `1px solid ${t.panelBorder}`, background: 'transparent', color: t.text2 }}
          >
            Reset view{focusNode ? ` · focused on ${focusNode}` : ''}
          </button>
        )}

        <div style={{ flex: 1 }} />

        {/* Health overlay legend */}
        <div style={{ display: 'flex', alignItems: 'center', gap: '14px', fontSize: '12px', color: t.text2 }}>
          {[['Healthy', t.green], ['Degraded', t.amber], ['Unhealthy', t.red]].map(([label, color]) => (
            <span key={label as string} style={{ display: 'flex', alignItems: 'center', gap: '6px' }}>
              <span style={{ width: '9px', height: '9px', borderRadius: '50%', background: color as string }} />
              {label}
            </span>
          ))}
        </div>
      </div>

      {rootCauseError && (
        <div style={{ marginBottom: '16px', padding: '12px 16px', background: t.redSoft, color: t.red, borderRadius: '10px', border: `1px solid ${t.red}33` }}>
          {rootCauseError}
        </div>
      )}

      {rootCauseResults && (
        <div style={{
          padding: '18px 22px', borderRadius: '16px', background: t.panelBg, border: `1px solid ${t.panelBorder}`,
          backdropFilter: 'blur(24px)', marginBottom: '16px', fontSize: '13.5px',
        }}>
          {rootCauseResults.length === 0 ? (
            <div style={{ color: t.green }}>No unhealthy or degraded services detected — nothing to root-cause.</div>
          ) : (
            rootCauseResults.map((r, idx) => (
              <div key={idx} style={{
                marginBottom: idx < rootCauseResults.length - 1 ? '16px' : 0,
                paddingBottom: idx < rootCauseResults.length - 1 ? '16px' : 0,
                borderBottom: idx < rootCauseResults.length - 1 ? `1px solid ${t.panelBorder}` : 'none',
              }}>
                <div style={{ display: 'flex', alignItems: 'center', gap: '10px', marginBottom: r.narrative ? '8px' : 0 }}>
                  <strong style={{ color: t.accent }}>{r.unhealthyService}</strong>
                  {r.source === 'ai' ? (
                    <span style={{ fontSize: '11px', padding: '3px 10px', borderRadius: '100px', background: 'rgba(232,184,75,0.15)', color: t.gold }}>
                      AI · {r.model} · {r.confidence !== undefined ? `${Math.round(r.confidence * 100)}% confidence` : ''}
                    </span>
                  ) : (
                    <span style={{ fontSize: '11px', padding: '3px 10px', borderRadius: '100px', background: t.dark ? 'rgba(255,255,255,0.06)' : 'rgba(0,0,0,0.05)', color: t.text2 }}>
                      topology walk (AI analysis pending)
                    </span>
                  )}
                </div>
                {r.narrative && (
                  <p style={{ color: t.text2, margin: '4px 0 0', lineHeight: 1.6 }}>{r.narrative}</p>
                )}
                {r.rootCause === r.unhealthyService ? (
                  <span style={{ color: t.text2 }}>No unhealthy upstream dependency found — <strong style={{ color: t.gold }}>{r.rootCause}</strong> is likely the root cause itself.</span>
                ) : (
                  <span style={{ color: t.text2 }}>Traces back to <strong style={{ color: t.gold }}>{r.rootCause}</strong> via {r.chain.join(' → ')}.</span>
                )}
              </div>
            ))
          )}
        </div>
      )}

      {/* React Flow Graph Container */}
      <div style={{
        flex: 1, position: 'relative', borderRadius: '22px', overflow: 'hidden', background: t.panelBg,
        border: `1px solid ${t.panelBorder}`, borderTop: `1px solid ${t.panelTop}`,
        backdropFilter: 'blur(30px) saturate(180%)', WebkitBackdropFilter: 'blur(30px) saturate(180%)',
        boxShadow: t.shadow, minHeight: '560px',
      }}>
        {loading ? (
          <div style={{ display: 'flex', height: '100%', justifyContent: 'center', alignItems: 'center', color: t.text2 }}>
            Discovering topology...
          </div>
        ) : (
          <ReactFlow
            nodes={nodes}
            edges={edges}
            onNodesChange={onNodesChange}
            onEdgesChange={onEdgesChange}
            onNodeClick={(_, node) => { setFocusNode(node.id); showDownstreamImpact(node.id); }}
            nodeTypes={nodeTypes}
            fitView
            style={{ background: 'transparent' }}
          >
            <Background color={t.dark ? 'rgba(255,255,255,0.08)' : 'rgba(0,0,0,0.06)'} gap={16} />
            <Controls />
            <MiniMap
              nodeColor={(n) => {
                if (n.data?.state === 'UNHEALTHY') return t.red;
                if (n.data?.state === 'DEGRADED') return t.amber;
                return t.green;
              }}
              maskColor={t.dark ? 'rgba(0,0,0,0.5)' : 'rgba(255,255,255,0.5)'}
              style={{ background: t.panelBg, border: `1px solid ${t.panelBorder}` }}
            />
          </ReactFlow>
        )}

        {impact && (
          <div style={{ position: 'absolute', top: '16px', right: '16px', width: '280px', maxHeight: 'calc(100% - 32px)', overflowY: 'auto', background: t.panelBg, border: `1px solid ${t.panelBorder}`, borderRadius: '14px', padding: '16px', boxShadow: t.shadow, zIndex: 5 }}>
            <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: '10px' }}>
              <h4 style={{ fontSize: '14px', fontWeight: 700, margin: 0, color: t.text1 }}>Downstream impact</h4>
              <button onClick={() => setImpact(null)} aria-label="Close impact panel" style={{ background: 'none', border: 'none', color: t.text2, cursor: 'pointer', fontSize: '18px', lineHeight: 1 }}>×</button>
            </div>
            <div style={{ fontSize: '12.5px', color: t.text2, marginBottom: '12px', lineHeight: 1.5 }}>
              If <span style={{ color: t.text1, fontWeight: 600 }}>{impact.service}</span> fails, these
              services depend on it and are in the blast radius.
            </div>
            {impactLoading ? (
              <div style={{ color: t.text2, fontSize: '13px' }}>Loading…</div>
            ) : impact.downstream.length === 0 ? (
              <div style={{ color: t.text2, fontSize: '13px' }}>No downstream dependents — nothing else depends on this service.</div>
            ) : (
              <div style={{ display: 'flex', flexWrap: 'wrap', gap: '6px' }}>
                {impact.downstream.map((d) => (
                  <span key={d} style={{ background: t.dark ? 'rgba(255,255,255,0.06)' : 'rgba(0,0,0,0.05)', border: `1px solid ${t.panelBorder}`, padding: '4px 10px', borderRadius: '100px', fontSize: '12px', color: t.text1 }}>{d}</span>
                ))}
              </div>
            )}
          </div>
        )}
      </div>

    </div>
  );
}
