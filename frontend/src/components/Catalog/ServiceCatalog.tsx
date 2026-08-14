"use client";

import React, { useEffect, useState } from 'react';
import { errMessage } from '@/lib/errMessage';
import { fetchWithAuth } from '@/lib/api';
import { useTheme } from '@/context/ThemeContext';

type Lifecycle = '' | 'experimental' | 'production' | 'deprecated';
type Tier = '' | 'tier-1' | 'tier-2' | 'tier-3';
type LinkKey = 'repo' | 'dashboards' | 'runbooks' | 'docs';

interface CatalogNode {
  id: string; state: string; team?: string; repo?: string; slack?: string;
  tier?: Tier; lifecycle?: Lifecycle; links?: Partial<Record<LinkKey, string>>;
}
interface SLOInfo { budgetRemainingPct: number; status: string }

const LIFECYCLES: Lifecycle[] = ['', 'experimental', 'production', 'deprecated'];
const TIERS: Tier[] = ['', 'tier-1', 'tier-2', 'tier-3'];
const LINK_KEYS: Array<{ key: LinkKey; label: string; icon: string }> = [
  { key: 'repo', label: 'Repository', icon: '📦' },
  { key: 'dashboards', label: 'Dashboards', icon: '📊' },
  { key: 'runbooks', label: 'Runbooks', icon: '📘' },
  { key: 'docs', label: 'Docs', icon: '📄' },
];

export function ServiceCatalog() {
  const { tokens: t } = useTheme();
  const [nodes, setNodes] = useState<CatalogNode[]>([]);
  const [slos, setSlos] = useState<Map<string, SLOInfo>>(new Map());
  const [search, setSearch] = useState('');
  const [loading, setLoading] = useState(true);
  const [showModal, setShowModal] = useState(false);
  const [formData, setFormData] = useState({ service_name: '', team: '', repo: '', slack: '' });

  // Dependencies drill-in (Catalog · E4): which service's deps are expanded.
  const [depsOpen, setDepsOpen] = useState<string | null>(null);
  const [deps, setDeps] = useState<{ upstream: string[]; downstream: string[] }>({ upstream: [], downstream: [] });
  const [depsLoading, setDepsLoading] = useState(false);

  const toggleDeps = (id: string) => {
    if (depsOpen === id) { setDepsOpen(null); return; }
    setDepsOpen(id);
    setDeps({ upstream: [], downstream: [] });
    setDepsLoading(true);
    Promise.all([
      fetchWithAuth(`/api/v1/topology/dependencies/upstream/${encodeURIComponent(id)}`).then(r => (r.ok ? r.json() : [])).catch(() => []),
      fetchWithAuth(`/api/v1/topology/dependencies/downstream/${encodeURIComponent(id)}`).then(r => (r.ok ? r.json() : [])).catch(() => []),
    ])
      .then(([up, down]) => setDeps({ upstream: Array.isArray(up) ? up : [], downstream: Array.isArray(down) ? down : [] }))
      .finally(() => setDepsLoading(false));
  };

  // Rich-metadata editor (Catalog · E3): the service being edited + its draft.
  const [metaNode, setMetaNode] = useState<CatalogNode | null>(null);
  const [metaForm, setMetaForm] = useState<{ tier: Tier; lifecycle: Lifecycle; links: Record<LinkKey, string> }>({
    tier: '', lifecycle: '', links: { repo: '', dashboards: '', runbooks: '', docs: '' },
  });

  const fetchCatalog = () => {
    setLoading(true);
    fetchWithAuth('/api/v1/topology/graph')
      .then(res => res.json())
      .then(data => {
        if (data.nodes) {
          const sorted = data.nodes.sort((a: CatalogNode, b: CatalogNode) => a.id.localeCompare(b.id));
          setNodes(sorted);
        }
        setLoading(false);
      })
      .catch(err => {
        console.error("Failed to fetch topology graph:", err);
        setLoading(false);
      });

    // Enrich the catalog with each service's SLO posture so a scorecard carries
    // ownership AND reliability, not just metadata.
    fetchWithAuth('/api/v1/slo/dashboard')
      .then(res => (res.ok ? res.json() : null))
      .then(json => {
        const items = json?.data || [];
        const m = new Map<string, SLOInfo>();
        for (const it of items) {
          const svc = it?.definition?.service_name;
          if (svc) m.set(svc, { budgetRemainingPct: it.budget_remaining_pct ?? 0, status: it.status || 'unknown' });
        }
        setSlos(m);
      })
      .catch(() => { /* SLOs are supplementary; a failure leaves the column as “No SLO”. */ });
  };

  useEffect(() => {
    // eslint-disable-next-line react-hooks/set-state-in-effect -- intentional one-shot fetch/hydration on mount; effect is the right place to sync from the API/localStorage
    fetchCatalog();
  }, []);

  const handleRegister = async (e: React.FormEvent) => {
    e.preventDefault();
    try {
      const res = await fetchWithAuth('/api/v1/topology/catalog', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(formData)
      });
      if (!res.ok) throw new Error(await res.text());
      setShowModal(false);
      setFormData({ service_name: '', team: '', repo: '', slack: '' });
      fetchCatalog(); // Refresh
    } catch (err) {
      alert(`Failed to register service: ${errMessage(err)}`);
    }
  };

  const openMetaModal = (node: CatalogNode) => {
    setMetaNode(node);
    setMetaForm({
      tier: node.tier ?? '',
      lifecycle: node.lifecycle ?? '',
      links: {
        repo: node.links?.repo ?? '',
        dashboards: node.links?.dashboards ?? '',
        runbooks: node.links?.runbooks ?? '',
        docs: node.links?.docs ?? '',
      },
    });
  };

  const handleSaveMetadata = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!metaNode) return;
    // Send only non-empty links so we don't persist blank keys.
    const links: Partial<Record<LinkKey, string>> = {};
    (Object.keys(metaForm.links) as LinkKey[]).forEach(k => {
      const v = metaForm.links[k].trim();
      if (v) links[k] = v;
    });
    try {
      const res = await fetchWithAuth(`/api/v1/topology/catalog/${encodeURIComponent(metaNode.id)}`, {
        method: 'PATCH',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ tier: metaForm.tier, lifecycle: metaForm.lifecycle, links }),
      });
      if (!res.ok) throw new Error(await res.text());
      setMetaNode(null);
      fetchCatalog();
    } catch (err) {
      alert(`Failed to save metadata: ${errMessage(err)}`);
    }
  };

  // Lifecycle badge palette: production is trusted-green, experimental is
  // caution-amber, deprecated is muted (on its way out), unset is neutral.
  const lifecycleStyle = (lc?: Lifecycle): { color: string; label: string } => {
    if (lc === 'production') return { color: t.green, label: 'Production' };
    if (lc === 'experimental') return { color: t.amber, label: 'Experimental' };
    if (lc === 'deprecated') return { color: t.red, label: 'Deprecated' };
    return { color: t.text2, label: 'Unset' };
  };

  const stateColor = (state: string) => {
    if (state === 'HEALTHY') return t.green;
    if (state === 'PREDICTIVE_WARNING' || state?.toLowerCase().includes('degrad') || state?.toLowerCase().includes('warn')) return t.amber;
    return t.red;
  };

  const filtered = nodes.filter(n =>
    n.id.toLowerCase().includes(search.toLowerCase()) ||
    (n.team || '').toLowerCase().includes(search.toLowerCase())
  );

  // SLO scorecard cell: budget-remaining % coloured by objective status.
  const sloColor = (status: string) => {
    if (status === 'healthy') return t.green;
    if (status === 'warning') return t.amber;
    if (status === 'critical' || status === 'breached') return t.red;
    return t.text2;
  };

  const inputStyle: React.CSSProperties = {
    width: '100%',
    padding: '12px',
    background: t.dark ? 'rgba(255,255,255,0.06)' : 'rgba(0,0,0,0.03)',
    border: '1px solid ' + t.panelBorder,
    borderRadius: '8px',
    color: t.text1,
    fontSize: '14px',
  };

  const labelStyle: React.CSSProperties = {
    display: 'block',
    fontSize: '13px',
    color: t.text2,
    marginBottom: '8px',
  };

  return (
    <div style={{ padding: '40px', maxWidth: '1400px', margin: '0 auto', width: '100%', height: '100%', overflowY: 'auto', position: 'relative' }}>

      {showModal && (
        <div style={{ position: 'fixed', top: 0, left: 0, right: 0, bottom: 0, background: 'rgba(0,0,0,0.6)', zIndex: 1000, display: 'flex', alignItems: 'center', justifyContent: 'center', backdropFilter: 'blur(6px)' }}>
          <div style={{
            background: t.panelBg,
            padding: '32px',
            borderRadius: '20px',
            width: '400px',
            border: '1px solid ' + t.panelBorder,
            backdropFilter: 'blur(30px) saturate(180%)',
            boxShadow: t.shadow,
          }}>
            <h3 style={{ fontSize: '20px', fontWeight: 600, marginBottom: '24px', color: t.text1 }}>Register Service Metadata</h3>
            <form onSubmit={handleRegister} style={{ display: 'flex', flexDirection: 'column', gap: '16px' }}>
              <div>
                <label style={labelStyle}>Service Name</label>
                <select required value={formData.service_name} onChange={e => setFormData({...formData, service_name: e.target.value})} style={inputStyle}>
                  <option value="" disabled>Select a discovered service...</option>
                  {nodes.map(n => (
                    <option key={n.id} value={n.id}>{n.id}</option>
                  ))}
                </select>
              </div>
              <div>
                <label style={labelStyle}>Owning Team</label>
                <input type="text" placeholder="e.g. Team Platform" required value={formData.team} onChange={e => setFormData({...formData, team: e.target.value})} style={inputStyle} />
              </div>
              <div>
                <label style={labelStyle}>GitHub Repository</label>
                <input type="text" placeholder="e.g. github.com/org/repo" required value={formData.repo} onChange={e => setFormData({...formData, repo: e.target.value})} style={inputStyle} />
              </div>
              <div>
                <label style={labelStyle}>Slack Channel</label>
                <input type="text" placeholder="e.g. #eng-platform-alerts" required value={formData.slack} onChange={e => setFormData({...formData, slack: e.target.value})} style={inputStyle} />
              </div>
              <div style={{ display: 'flex', gap: '12px', marginTop: '16px' }}>
                <button
                  type="button"
                  onClick={() => setShowModal(false)}
                  style={{
                    flex: 1,
                    padding: '10px 20px',
                    borderRadius: '10px',
                    border: '1px solid ' + t.panelBorder,
                    background: 'transparent',
                    color: t.text1,
                    fontWeight: 600,
                    fontSize: '13.5px',
                    cursor: 'pointer',
                  }}
                >
                  Cancel
                </button>
                <button
                  type="submit"
                  style={{
                    flex: 1,
                    padding: '10px 20px',
                    borderRadius: '10px',
                    border: 'none',
                    background: `linear-gradient(135deg, ${t.accent}, ${t.accent2})`,
                    color: '#fff',
                    fontWeight: 600,
                    fontSize: '13.5px',
                    cursor: 'pointer',
                  }}
                >
                  Save Metadata
                </button>
              </div>
            </form>
          </div>
        </div>
      )}

      {metaNode && (
        <div style={{ position: 'fixed', top: 0, left: 0, right: 0, bottom: 0, background: 'rgba(0,0,0,0.6)', zIndex: 1000, display: 'flex', alignItems: 'center', justifyContent: 'center', backdropFilter: 'blur(6px)' }}>
          <div style={{ background: t.panelBg, padding: '32px', borderRadius: '20px', width: '440px', maxHeight: '86vh', overflowY: 'auto', border: '1px solid ' + t.panelBorder, backdropFilter: 'blur(30px) saturate(180%)', boxShadow: t.shadow }}>
            <h3 style={{ fontSize: '20px', fontWeight: 600, marginBottom: '6px', color: t.text1 }}>Service Metadata</h3>
            <p style={{ color: t.text2, fontSize: '13px', marginBottom: '20px' }}>{metaNode.id}</p>
            <form onSubmit={handleSaveMetadata} style={{ display: 'flex', flexDirection: 'column', gap: '16px' }}>
              <div style={{ display: 'flex', gap: '12px' }}>
                <div style={{ flex: 1 }}>
                  <label style={labelStyle}>Lifecycle</label>
                  <select aria-label="Lifecycle" value={metaForm.lifecycle} onChange={e => setMetaForm({ ...metaForm, lifecycle: e.target.value as Lifecycle })} style={inputStyle}>
                    {LIFECYCLES.map(lc => <option key={lc || 'unset'} value={lc}>{lc ? lc[0].toUpperCase() + lc.slice(1) : 'Unset'}</option>)}
                  </select>
                </div>
                <div style={{ flex: 1 }}>
                  <label style={labelStyle}>Tier</label>
                  <select aria-label="Tier" value={metaForm.tier} onChange={e => setMetaForm({ ...metaForm, tier: e.target.value as Tier })} style={inputStyle}>
                    {TIERS.map(tr => <option key={tr || 'unset'} value={tr}>{tr ? tr.toUpperCase() : 'Unset'}</option>)}
                  </select>
                </div>
              </div>
              {LINK_KEYS.map(({ key, label, icon }) => (
                <div key={key}>
                  <label style={labelStyle}>{icon} {label}</label>
                  <input
                    type="text"
                    placeholder={`https://… (${key})`}
                    value={metaForm.links[key]}
                    onChange={e => setMetaForm({ ...metaForm, links: { ...metaForm.links, [key]: e.target.value } })}
                    style={inputStyle}
                  />
                </div>
              ))}
              <div style={{ display: 'flex', gap: '12px', marginTop: '8px' }}>
                <button type="button" onClick={() => setMetaNode(null)} style={{ flex: 1, padding: '10px 20px', borderRadius: '10px', border: '1px solid ' + t.panelBorder, background: 'transparent', color: t.text1, fontWeight: 600, fontSize: '13.5px', cursor: 'pointer' }}>Cancel</button>
                <button type="submit" style={{ flex: 1, padding: '10px 20px', borderRadius: '10px', border: 'none', background: `linear-gradient(135deg, ${t.accent}, ${t.accent2})`, color: '#fff', fontWeight: 600, fontSize: '13.5px', cursor: 'pointer' }}>Save Metadata</button>
              </div>
            </form>
          </div>
        </div>
      )}

      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'flex-end', marginBottom: '20px', gap: '16px', flexWrap: 'wrap' }}>
        <div>
          <h2 style={{ fontSize: '26px', fontWeight: 700, margin: '0 0 8px', color: t.text1 }}>Service Catalog</h2>
          <p style={{ color: t.text2, fontSize: '14.5px' }}>Inventory of all discovered microservices and ownership teams.</p>
        </div>
        <div style={{ display: 'flex', gap: '12px' }}>
          <input
            type="text"
            placeholder="Search services or teams..."
            value={search}
            onChange={(e) => setSearch(e.target.value)}
            aria-label="Search services"
            style={{
              padding: '10px 16px',
              borderRadius: '10px',
              border: '1px solid ' + t.panelBorder,
              background: t.dark ? 'rgba(255,255,255,0.06)' : 'rgba(255,255,255,0.7)',
              color: t.text1,
              width: '260px',
            }}
          />
          <button
            onClick={() => setShowModal(true)}
            style={{
              padding: '10px 20px',
              borderRadius: '10px',
              border: 'none',
              background: `linear-gradient(135deg, ${t.accent}, ${t.accent2})`,
              color: '#fff',
              fontWeight: 600,
              fontSize: '13.5px',
              cursor: 'pointer',
            }}
          >
            Register Service
          </button>
        </div>
      </div>

      <div style={{
        borderRadius: '20px',
        overflow: 'hidden',
        background: t.panelBg,
        border: '1px solid ' + t.panelBorder,
        backdropFilter: 'blur(30px) saturate(180%)',
        boxShadow: t.shadow,
      }}>
        <table style={{ width: '100%', borderCollapse: 'collapse' }}>
          <thead>
            <tr style={{ background: t.panelTop, borderBottom: '1px solid ' + t.panelBorder, color: t.text2, fontSize: '13px', textAlign: 'left' }}>
              <th style={{ padding: '16px 24px', fontWeight: 500 }}>Service Name</th>
              <th style={{ padding: '16px 24px', fontWeight: 500 }}>Health</th>
              <th style={{ padding: '16px 24px', fontWeight: 500 }}>Lifecycle / Tier</th>
              <th style={{ padding: '16px 24px', fontWeight: 500 }}>SLO Budget</th>
              <th style={{ padding: '16px 24px', fontWeight: 500 }}>Owning Team</th>
              <th style={{ padding: '16px 24px', fontWeight: 500 }}>Repository</th>
              <th style={{ padding: '16px 24px', fontWeight: 500 }}>Slack Channel</th>
            </tr>
          </thead>
          <tbody>
            {loading ? (
              <tr>
                <td colSpan={7} style={{ padding: '40px', textAlign: 'center', color: t.text2 }}>Loading catalog...</td>
              </tr>
            ) : filtered.length === 0 ? (
              <tr>
                <td colSpan={7} style={{ padding: '40px', textAlign: 'center', color: t.text2 }}>{nodes.length === 0 ? 'No services discovered yet.' : 'No services match your search.'}</td>
              </tr>
            ) : (
              filtered.map(node => {
                const color = stateColor(node.state);
                const slo = slos.get(node.id);
                return (
                  <React.Fragment key={node.id}>
                  <tr
                    style={{ borderBottom: depsOpen === node.id ? 'none' : '1px solid ' + t.panelBorder, transition: 'background 0.2s' }}
                    onMouseEnter={(e) => e.currentTarget.style.background = t.dark ? 'rgba(255,255,255,0.03)' : 'rgba(0,0,0,0.02)'}
                    onMouseLeave={(e) => e.currentTarget.style.background = 'transparent'}
                  >
                    <td style={{ padding: '16px 24px', fontWeight: 500, color: t.text1 }}>
                      <button onClick={() => toggleDeps(node.id)} title="Show dependencies" style={{ display: 'inline-flex', alignItems: 'center', background: 'transparent', border: 'none', cursor: 'pointer', color: t.text1, fontWeight: 500, fontSize: '13.5px', padding: 0 }}>
                        <span style={{ color: t.text2, fontSize: '10px', marginRight: '8px' }}>{depsOpen === node.id ? '▾' : '▸'}</span>
                        <div style={{ width: '8px', height: '8px', borderRadius: '50%', background: color, marginRight: '10px' }}></div>
                        {node.id}
                      </button>
                    </td>
                    <td style={{ padding: '16px 24px' }}>
                      <span style={{
                        fontSize: '11.5px',
                        padding: '4px 11px',
                        borderRadius: '100px',
                        background: t.dark ? 'rgba(255,255,255,0.08)' : 'rgba(0,0,0,0.05)',
                        color: color,
                      }}>
                        {node.state.replace('_', ' ')}
                      </span>
                    </td>
                    <td style={{ padding: '16px 24px', fontSize: '13px' }}>
                      {(() => {
                        const lc = lifecycleStyle(node.lifecycle);
                        const linkCount = node.links ? Object.keys(node.links).length : 0;
                        return (
                          <button
                            onClick={() => openMetaModal(node)}
                            // The button wraps visible badges, and text content beats
                            // title in accessible-name computation — so without this
                            // it announced as "Production Tier-1" with no hint that
                            // it opens an editor.
                            aria-label="Edit lifecycle, tier & links"
                            title="Edit lifecycle, tier & links"
                            style={{ display: 'inline-flex', alignItems: 'center', gap: '8px', background: 'transparent', border: 'none', cursor: 'pointer', padding: 0 }}
                          >
                            <span style={{ fontSize: '11.5px', padding: '4px 10px', borderRadius: '100px', border: '1px solid ' + lc.color, color: lc.color, fontWeight: 600 }}>
                              {lc.label}
                            </span>
                            {node.tier && (
                              <span style={{ fontSize: '11px', padding: '3px 8px', borderRadius: '6px', background: t.dark ? 'rgba(255,255,255,0.08)' : 'rgba(0,0,0,0.05)', color: t.text1, textTransform: 'uppercase', letterSpacing: '0.03em' }}>
                                {node.tier}
                              </span>
                            )}
                            {linkCount > 0 && <span style={{ fontSize: '11px', color: t.text2 }}>🔗 {linkCount}</span>}
                            <span style={{ fontSize: '11px', color: t.accent, opacity: 0.7 }}>✎</span>
                          </button>
                        );
                      })()}
                    </td>
                    <td style={{ padding: '16px 24px', fontSize: '13px' }}>
                      {slo ? (
                        <span style={{ display: 'inline-flex', alignItems: 'center', gap: '8px' }}>
                          <span style={{ width: '54px', height: '6px', borderRadius: '100px', background: t.dark ? 'rgba(255,255,255,0.08)' : 'rgba(0,0,0,0.06)', overflow: 'hidden' }}>
                            <span style={{ display: 'block', width: `${Math.max(0, Math.min(100, slo.budgetRemainingPct))}%`, height: '100%', background: sloColor(slo.status) }} />
                          </span>
                          <span style={{ color: sloColor(slo.status), fontWeight: 600 }}>{Math.round(slo.budgetRemainingPct)}%</span>
                        </span>
                      ) : (
                        <span style={{ color: t.text2 }}>No SLO</span>
                      )}
                    </td>
                    <td style={{ padding: '16px 24px', color: t.text2, fontSize: '13.5px' }}>{node.team || 'Unassigned'}</td>
                    <td style={{ padding: '16px 24px', color: t.accent, fontSize: '13px', cursor: 'pointer' }}>{node.repo || 'github.com/pulsetrace/' + node.id}</td>
                    <td style={{ padding: '16px 24px', fontSize: '13.5px' }}>
                       <span style={{ background: t.dark ? 'rgba(255,255,255,0.08)' : 'rgba(0,0,0,0.05)', padding: '4px 10px', borderRadius: '6px', fontSize: '12.5px', color: t.text1 }}>
                          {node.slack || '#eng-general'}
                       </span>
                    </td>
                  </tr>
                  {depsOpen === node.id && (
                    <tr style={{ borderBottom: '1px solid ' + t.panelBorder }}>
                      <td colSpan={7} style={{ padding: '4px 24px 18px 56px' }}>
                        {depsLoading ? (
                          <div style={{ color: t.text2, fontSize: '12.5px', padding: '8px 0' }}>Loading dependencies…</div>
                        ) : (
                          <div style={{ display: 'flex', gap: '32px', flexWrap: 'wrap' }}>
                            <DepColumn title="Depends on (upstream)" services={deps.upstream} empty="No upstream dependencies observed." t={t} />
                            <DepColumn title="Depended on by (downstream)" services={deps.downstream} empty="No downstream dependents observed." t={t} />
                          </div>
                        )}
                      </td>
                    </tr>
                  )}
                  </React.Fragment>
                );
              })
            )}
          </tbody>
        </table>
      </div>
    </div>
  );
}

// DepColumn renders one side of a service's dependency graph (Catalog · E4) as
// a labeled list of chips, reusing the topology dependency endpoints.
function DepColumn({ title, services, empty, t }: { title: string; services: string[]; empty: string; t: ReturnType<typeof useTheme>['tokens'] }) {
  return (
    <div style={{ minWidth: '240px' }}>
      <div style={{ fontSize: '11px', fontWeight: 700, letterSpacing: '0.04em', color: t.text2, textTransform: 'uppercase', marginBottom: '8px' }}>{title}</div>
      {services.length === 0 ? (
        <div style={{ fontSize: '12.5px', color: t.text2 }}>{empty}</div>
      ) : (
        <div style={{ display: 'flex', flexWrap: 'wrap', gap: '6px' }}>
          {services.map((s) => (
            <span key={s} style={{ fontSize: '12px', fontFamily: 'monospace', padding: '3px 10px', borderRadius: '100px', background: t.dark ? 'rgba(255,255,255,0.06)' : 'rgba(0,0,0,0.05)', color: t.text1 }}>{s}</span>
          ))}
        </div>
      )}
    </div>
  );
}
