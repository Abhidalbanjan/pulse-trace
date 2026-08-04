"use client";

import React, { useState, useEffect, useCallback } from 'react';
import { errMessage } from '@/lib/errMessage';
import { fetchWithAuth } from '@/lib/api';
import { useTheme } from '@/context/ThemeContext';

interface Policy {
  id: string;
  name: string;
  effect: 'allow' | 'deny';
  resource: string;
  condition: string;
  priority: number;
  enabled: boolean;
}

export function PoliciesPanel() {
  const { tokens: t } = useTheme();
  const [policies, setPolicies] = useState<Policy[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [showForm, setShowForm] = useState(false);
  const [form, setForm] = useState({ name: '', effect: 'deny', resource: '*', condition: '', priority: 100 });
  const [formError, setFormError] = useState<string | null>(null);

  const fetchPolicies = useCallback(() => {
    fetchWithAuth('/api/v1/admin/policies')
      .then(async res => {
        if (!res.ok) throw new Error(await res.text());
        return res.json();
      })
      .then(json => {
        setPolicies(json.data || []);
        setError(null);
      })
      .catch(err => setError(errMessage(err, 'Failed to load policies')))
      .finally(() => setLoading(false));
  }, []);

  useEffect(() => { fetchPolicies(); }, [fetchPolicies]);

  const createPolicy = async (e: React.FormEvent) => {
    e.preventDefault();
    setFormError(null);
    if (!form.name.trim() || !form.condition.trim()) return;
    try {
      const res = await fetchWithAuth('/api/v1/admin/policies', {
        method: 'POST',
        body: JSON.stringify(form),
      });
      if (!res.ok) throw new Error(await res.text());
      setForm({ name: '', effect: 'deny', resource: '*', condition: '', priority: 100 });
      setShowForm(false);
      fetchPolicies();
    } catch (err) {
      setFormError(errMessage(err));
    }
  };

  const toggleEnabled = async (p: Policy) => {
    try {
      const res = await fetchWithAuth(`/api/v1/admin/policies/${p.id}`, {
        method: 'PUT',
        body: JSON.stringify({ enabled: !p.enabled, priority: p.priority }),
      });
      if (!res.ok) throw new Error(await res.text());
      fetchPolicies();
    } catch (err) {
      alert(`Error updating policy: ${errMessage(err)}`);
    }
  };

  const deletePolicy = async (id: string) => {
    if (!confirm('Delete this policy?')) return;
    try {
      const res = await fetchWithAuth(`/api/v1/admin/policies/${id}`, { method: 'DELETE' });
      if (!res.ok) throw new Error(await res.text());
      fetchPolicies();
    } catch (err) {
      alert(`Error deleting policy: ${errMessage(err)}`);
    }
  };

  const primaryBtnStyle: React.CSSProperties = {
    padding: '10px 18px', borderRadius: '10px', border: 'none',
    background: `linear-gradient(135deg, ${t.accent}, ${t.accent2})`, color: '#fff',
    fontWeight: 600, fontSize: '13px', cursor: 'pointer', flexShrink: 0,
  };
  const inputStyle: React.CSSProperties = {
    padding: '10px 12px', background: t.dark ? 'rgba(255,255,255,0.05)' : 'rgba(255,255,255,0.7)',
    border: '1px solid ' + t.panelBorder, borderRadius: '8px', color: t.text1,
  };
  const ghostBtnStyle: React.CSSProperties = {
    padding: '6px 12px', fontSize: '12px', borderRadius: '8px', border: '1px solid ' + t.panelBorder,
    background: 'transparent', color: t.text1, cursor: 'pointer',
  };
  const ghostRedBtnStyle: React.CSSProperties = { ...ghostBtnStyle, border: '1px solid ' + t.red, color: t.red };

  return (
    <div>
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'flex-start', marginBottom: '28px', gap: '16px', flexWrap: 'wrap' }}>
        <div>
          <h3 style={{ fontSize: '19px', fontWeight: 700, margin: '0 0 8px', color: t.text1 }}>ABAC Policies</h3>
          <p style={{ color: t.text2, fontSize: '13.5px', maxWidth: '520px', lineHeight: 1.6 }}>Attribute-based rules layered on top of RBAC, evaluated over subject / resource / action.</p>
        </div>
        <button onClick={() => setShowForm(v => !v)} style={primaryBtnStyle}>
          {showForm ? 'Cancel' : '+ New Policy'}
        </button>
      </div>

      {showForm && (
        <form onSubmit={createPolicy} style={{ background: t.dark ? 'rgba(0,0,0,0.2)' : 'rgba(0,0,0,0.03)', padding: '20px', borderRadius: '12px', border: '1px solid ' + t.panelBorder, marginBottom: '24px', display: 'flex', flexDirection: 'column', gap: '12px' }}>
          <div style={{ display: 'flex', gap: '12px', flexWrap: 'wrap' }}>
            <input
              required
              placeholder="Policy name"
              value={form.name}
              onChange={e => setForm({ ...form, name: e.target.value })}
              style={{ ...inputStyle, flex: 1, minWidth: '160px' }}
            />
            <select
              value={form.effect}
              onChange={e => setForm({ ...form, effect: e.target.value })}
              style={inputStyle}
            >
              <option value="deny">deny</option>
              <option value="allow">allow</option>
            </select>
            <input
              placeholder="Resource type (or *)"
              value={form.resource}
              onChange={e => setForm({ ...form, resource: e.target.value })}
              style={{ ...inputStyle, width: '160px' }}
            />
            <input
              type="number"
              placeholder="Priority"
              value={form.priority}
              onChange={e => setForm({ ...form, priority: parseInt(e.target.value, 10) || 100 })}
              style={{ ...inputStyle, width: '100px' }}
            />
          </div>
          <input
            required
            placeholder='Condition, e.g. subject.role == "viewer" && action != "read"'
            value={form.condition}
            onChange={e => setForm({ ...form, condition: e.target.value })}
            style={{ ...inputStyle, fontFamily: 'monospace', fontSize: '13px' }}
          />
          {formError && <div style={{ color: t.red, fontSize: '13px' }}>{formError}</div>}
          <button type="submit" style={{ ...primaryBtnStyle, alignSelf: 'flex-start' }}>Create Policy</button>
        </form>
      )}

      {error && (
        <div style={{ padding: '16px', background: t.redSoft, color: t.red, borderRadius: '8px', marginBottom: '24px' }}>{error}</div>
      )}

      <table style={{ width: '100%', borderCollapse: 'collapse' }}>
        <thead>
          <tr style={{ borderBottom: '1px solid ' + t.panelBorder, textAlign: 'left' }}>
            <th style={{ padding: '10px 8px', fontWeight: 600, color: t.text2, fontSize: '12px' }}>Priority</th>
            <th style={{ padding: '10px 8px', fontWeight: 600, color: t.text2, fontSize: '12px' }}>Name</th>
            <th style={{ padding: '10px 8px', fontWeight: 600, color: t.text2, fontSize: '12px' }}>Effect</th>
            <th style={{ padding: '10px 8px', fontWeight: 600, color: t.text2, fontSize: '12px' }}>Resource</th>
            <th style={{ padding: '10px 8px', fontWeight: 600, color: t.text2, fontSize: '12px' }}>Condition</th>
            <th style={{ padding: '10px 8px', fontWeight: 600, color: t.text2, fontSize: '12px' }}>Status</th>
            <th style={{ padding: '10px 8px', fontWeight: 600, color: t.text2, fontSize: '12px' }}>Actions</th>
          </tr>
        </thead>
        <tbody>
          {loading ? (
            <tr><td colSpan={7} style={{ padding: '24px', textAlign: 'center', color: t.text2 }}>Loading policies...</td></tr>
          ) : policies.length === 0 ? (
            <tr><td colSpan={7} style={{ padding: '24px', textAlign: 'center', color: t.text2 }}>No policies configured.</td></tr>
          ) : policies.map(p => (
            <tr key={p.id} style={{ borderBottom: '1px solid ' + t.panelBorder, opacity: p.enabled ? 1 : 0.5 }}>
              <td style={{ padding: '14px 8px', color: t.text2, fontSize: '13px' }}>{p.priority}</td>
              <td style={{ padding: '14px 8px', fontWeight: 500, fontSize: '13.5px', color: t.text1 }}>{p.name}</td>
              <td style={{ padding: '14px 8px' }}>
                <span style={{ color: p.effect === 'deny' ? t.red : t.green, fontSize: '12px', fontWeight: 600 }}>{p.effect}</span>
              </td>
              <td style={{ padding: '14px 8px', fontFamily: 'monospace', fontSize: '12px', color: t.text1 }}>{p.resource}</td>
              <td style={{ padding: '14px 8px', fontFamily: 'monospace', fontSize: '12px', maxWidth: '260px', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap', color: t.text1 }} title={p.condition}>{p.condition}</td>
              <td style={{ padding: '14px 8px', fontSize: '13px', color: t.text2 }}>{p.enabled ? 'enabled' : 'disabled'}</td>
              <td style={{ padding: '14px 8px' }}>
                <div style={{ display: 'flex', gap: '8px' }}>
                  <button onClick={() => toggleEnabled(p)} style={ghostBtnStyle}>{p.enabled ? 'Disable' : 'Enable'}</button>
                  <button onClick={() => deletePolicy(p.id)} style={ghostRedBtnStyle}>Delete</button>
                </div>
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}
