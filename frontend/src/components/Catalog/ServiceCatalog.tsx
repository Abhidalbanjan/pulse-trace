"use client";

import React, { useEffect, useState } from 'react';
import { fetchWithAuth } from '@/lib/api';

export function ServiceCatalog() {
  const [nodes, setNodes] = useState<any[]>([]);
  const [loading, setLoading] = useState(true);
  const [showModal, setShowModal] = useState(false);
  const [formData, setFormData] = useState({ service_name: '', team: '', repo: '', slack: '' });

  const fetchCatalog = () => {
    setLoading(true);
    fetchWithAuth('/api/v1/topology/graph')
      .then(res => res.json())
      .then(data => {
        if (data.nodes) {
          const sorted = data.nodes.sort((a: any, b: any) => a.id.localeCompare(b.id));
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
    } catch (err: any) {
      alert(`Failed to register service: ${err.message}`);
    }
  };

  return (
    <div style={{ padding: '40px', maxWidth: '1400px', margin: '0 auto', width: '100%', height: '100%', overflowY: 'auto', position: 'relative' }}>
      
      {showModal && (
        <div style={{ position: 'fixed', top: 0, left: 0, right: 0, bottom: 0, background: 'rgba(0,0,0,0.8)', zIndex: 1000, display: 'flex', alignItems: 'center', justifyContent: 'center' }}>
          <div style={{ background: '#1e1e1e', padding: '32px', borderRadius: '16px', width: '400px', border: '1px solid var(--border-color)' }}>
            <h3 style={{ fontSize: '20px', fontWeight: 600, marginBottom: '24px' }}>Register Service Metadata</h3>
            <form onSubmit={handleRegister} style={{ display: 'flex', flexDirection: 'column', gap: '16px' }}>
              <div>
                <label style={{ display: 'block', fontSize: '13px', color: 'var(--text-secondary)', marginBottom: '8px' }}>Service Name</label>
                <select required value={formData.service_name} onChange={e => setFormData({...formData, service_name: e.target.value})} style={{ width: '100%', padding: '12px', background: 'rgba(255,255,255,0.05)', border: '1px solid var(--border-color)', borderRadius: '8px', color: '#fff' }}>
                  <option value="" disabled>Select a discovered service...</option>
                  {nodes.map(n => (
                    <option key={n.id} value={n.id}>{n.id}</option>
                  ))}
                </select>
              </div>
              <div>
                <label style={{ display: 'block', fontSize: '13px', color: 'var(--text-secondary)', marginBottom: '8px' }}>Owning Team</label>
                <input type="text" placeholder="e.g. Team Platform" required value={formData.team} onChange={e => setFormData({...formData, team: e.target.value})} style={{ width: '100%', padding: '12px', background: 'rgba(255,255,255,0.05)', border: '1px solid var(--border-color)', borderRadius: '8px', color: '#fff' }} />
              </div>
              <div>
                <label style={{ display: 'block', fontSize: '13px', color: 'var(--text-secondary)', marginBottom: '8px' }}>GitHub Repository</label>
                <input type="text" placeholder="e.g. github.com/org/repo" required value={formData.repo} onChange={e => setFormData({...formData, repo: e.target.value})} style={{ width: '100%', padding: '12px', background: 'rgba(255,255,255,0.05)', border: '1px solid var(--border-color)', borderRadius: '8px', color: '#fff' }} />
              </div>
              <div>
                <label style={{ display: 'block', fontSize: '13px', color: 'var(--text-secondary)', marginBottom: '8px' }}>Slack Channel</label>
                <input type="text" placeholder="e.g. #eng-platform-alerts" required value={formData.slack} onChange={e => setFormData({...formData, slack: e.target.value})} style={{ width: '100%', padding: '12px', background: 'rgba(255,255,255,0.05)', border: '1px solid var(--border-color)', borderRadius: '8px', color: '#fff' }} />
              </div>
              <div style={{ display: 'flex', gap: '12px', marginTop: '16px' }}>
                <button type="button" onClick={() => setShowModal(false)} className="btn-secondary" style={{ flex: 1 }}>Cancel</button>
                <button type="submit" className="btn-primary" style={{ flex: 1 }}>Save Metadata</button>
              </div>
            </form>
          </div>
        </div>
      )}

      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'flex-end', marginBottom: '32px' }}>
        <div>
          <h2 style={{ fontSize: '28px', fontWeight: 600, marginBottom: '8px' }}>Service Catalog</h2>
          <p style={{ color: 'var(--text-secondary)' }}>Inventory of all discovered microservices and ownership teams.</p>
        </div>
        <div style={{ display: 'flex', gap: '12px' }}>
          <input 
            type="text" 
            placeholder="Search services..." 
            style={{ padding: '10px 16px', borderRadius: '8px', border: '1px solid var(--border-color)', background: 'rgba(0,0,0,0.2)', color: 'white', width: '300px' }}
          />
          <button onClick={() => setShowModal(true)} className="btn-primary" style={{ padding: '10px 20px' }}>Register Service</button>
        </div>
      </div>

      <div className="glass-panel" style={{ padding: '0', overflow: 'hidden' }}>
        <table style={{ width: '100%', borderCollapse: 'collapse' }}>
          <thead>
            <tr style={{ background: 'rgba(255,255,255,0.02)', borderBottom: '1px solid var(--border-color)', color: 'var(--text-secondary)', fontSize: '13px', textAlign: 'left' }}>
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
                <td colSpan={5} style={{ padding: '40px', textAlign: 'center', color: 'var(--text-secondary)' }}>Loading catalog...</td>
              </tr>
            ) : nodes.length === 0 ? (
              <tr>
                <td colSpan={5} style={{ padding: '40px', textAlign: 'center', color: 'var(--text-secondary)' }}>No services discovered yet.</td>
              </tr>
            ) : (
              nodes.map(node => (
                <tr key={node.id} style={{ borderBottom: '1px solid var(--border-color)', transition: 'background 0.2s' }} onMouseEnter={(e) => e.currentTarget.style.background = 'rgba(255,255,255,0.02)'} onMouseLeave={(e) => e.currentTarget.style.background = 'transparent'}>
                  <td style={{ padding: '16px 24px', fontWeight: 500, display: 'flex', alignItems: 'center', gap: '12px' }}>
                    <div style={{ width: '8px', height: '8px', borderRadius: '50%', background: node.state === 'HEALTHY' ? 'var(--status-green)' : node.state === 'PREDICTIVE_WARNING' ? 'var(--status-yellow)' : 'var(--status-red)' }}></div>
                    {node.id}
                  </td>
                  <td style={{ padding: '16px 24px' }}>
                    <span style={{ 
                      fontSize: '12px', 
                      padding: '4px 10px', 
                      borderRadius: '128px', 
                      background: node.state === 'HEALTHY' ? 'rgba(16, 185, 129, 0.1)' : node.state === 'PREDICTIVE_WARNING' ? 'rgba(245, 158, 11, 0.1)' : 'rgba(239, 68, 68, 0.1)',
                      color: node.state === 'HEALTHY' ? 'var(--status-green)' : node.state === 'PREDICTIVE_WARNING' ? 'var(--status-yellow)' : 'var(--status-red)',
                      border: `1px solid ${node.state === 'HEALTHY' ? 'rgba(16, 185, 129, 0.2)' : node.state === 'PREDICTIVE_WARNING' ? 'rgba(245, 158, 11, 0.2)' : 'rgba(239, 68, 68, 0.2)'}`
                    }}>
                      {node.state.replace('_', ' ')}
                    </span>
                  </td>
                  <td style={{ padding: '16px 24px', fontSize: '14px', color: 'var(--text-secondary)' }}>{node.team || 'Unassigned'}</td>
                  <td style={{ padding: '16px 24px', fontSize: '14px', color: 'var(--accent-blue)', textDecoration: 'underline', cursor: 'pointer' }}>{node.repo || 'github.com/pulsetrace/' + node.id}</td>
                  <td style={{ padding: '16px 24px', fontSize: '14px' }}>
                     <span style={{ background: 'rgba(255,255,255,0.1)', padding: '4px 8px', borderRadius: '4px' }}>
                        {node.slack || '#eng-general'}
                     </span>
                  </td>
                </tr>
              ))
            )}
          </tbody>
        </table>
      </div>
    </div>
  );
}
