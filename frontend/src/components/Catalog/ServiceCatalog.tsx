"use client";

import React, { useEffect, useState } from 'react';
import { errMessage } from '@/lib/errMessage';
import { fetchWithAuth } from '@/lib/api';
import { useTheme } from '@/context/ThemeContext';

interface CatalogNode { id: string; state: string; team?: string; repo?: string; slack?: string; }

export function ServiceCatalog() {
  const { tokens: t } = useTheme();
  const [nodes, setNodes] = useState<CatalogNode[]>([]);
  const [loading, setLoading] = useState(true);
  const [showModal, setShowModal] = useState(false);
  const [formData, setFormData] = useState({ service_name: '', team: '', repo: '', slack: '' });

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

  const stateColor = (state: string) => {
    if (state === 'HEALTHY') return t.green;
    if (state === 'PREDICTIVE_WARNING' || state?.toLowerCase().includes('degrad') || state?.toLowerCase().includes('warn')) return t.amber;
    return t.red;
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

      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'flex-end', marginBottom: '20px', gap: '16px', flexWrap: 'wrap' }}>
        <div>
          <h2 style={{ fontSize: '26px', fontWeight: 700, margin: '0 0 8px', color: t.text1 }}>Service Catalog</h2>
          <p style={{ color: t.text2, fontSize: '14.5px' }}>Inventory of all discovered microservices and ownership teams.</p>
        </div>
        <div style={{ display: 'flex', gap: '12px' }}>
          <input
            type="text"
            placeholder="Search services..."
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
              <th style={{ padding: '16px 24px', fontWeight: 500 }}>Owning Team</th>
              <th style={{ padding: '16px 24px', fontWeight: 500 }}>Repository</th>
              <th style={{ padding: '16px 24px', fontWeight: 500 }}>Slack Channel</th>
            </tr>
          </thead>
          <tbody>
            {loading ? (
              <tr>
                <td colSpan={5} style={{ padding: '40px', textAlign: 'center', color: t.text2 }}>Loading catalog...</td>
              </tr>
            ) : nodes.length === 0 ? (
              <tr>
                <td colSpan={5} style={{ padding: '40px', textAlign: 'center', color: t.text2 }}>No services discovered yet.</td>
              </tr>
            ) : (
              nodes.map(node => {
                const color = stateColor(node.state);
                return (
                  <tr
                    key={node.id}
                    style={{ borderBottom: '1px solid ' + t.panelBorder, transition: 'background 0.2s' }}
                    onMouseEnter={(e) => e.currentTarget.style.background = t.dark ? 'rgba(255,255,255,0.03)' : 'rgba(0,0,0,0.02)'}
                    onMouseLeave={(e) => e.currentTarget.style.background = 'transparent'}
                  >
                    <td style={{ padding: '16px 24px', fontWeight: 500, display: 'flex', alignItems: 'center', color: t.text1 }}>
                      <div style={{ width: '8px', height: '8px', borderRadius: '50%', background: color, marginRight: '10px' }}></div>
                      {node.id}
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
                    <td style={{ padding: '16px 24px', color: t.text2, fontSize: '13.5px' }}>{node.team || 'Unassigned'}</td>
                    <td style={{ padding: '16px 24px', color: t.accent, fontSize: '13px', cursor: 'pointer' }}>{node.repo || 'github.com/pulsetrace/' + node.id}</td>
                    <td style={{ padding: '16px 24px', fontSize: '13.5px' }}>
                       <span style={{ background: t.dark ? 'rgba(255,255,255,0.08)' : 'rgba(0,0,0,0.05)', padding: '4px 10px', borderRadius: '6px', fontSize: '12.5px', color: t.text1 }}>
                          {node.slack || '#eng-general'}
                       </span>
                    </td>
                  </tr>
                );
              })
            )}
          </tbody>
        </table>
      </div>
    </div>
  );
}
