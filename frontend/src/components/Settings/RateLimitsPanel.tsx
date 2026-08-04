"use client";

import React, { useState, useEffect, useCallback } from 'react';
import { errMessage } from '@/lib/errMessage';
import { fetchWithAuth } from '@/lib/api';
import { useTheme } from '@/context/ThemeContext';

interface RateLimitRule {
  id: string;
  name: string;
  path_prefixes: string[];
  limit_count: number;
  window_seconds: number;
  priority: number;
  enabled: boolean;
}

export function RateLimitsPanel() {
  const { tokens: t } = useTheme();
  const [rules, setRules] = useState<RateLimitRule[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [showForm, setShowForm] = useState(false);
  const [form, setForm] = useState({ name: '', pathPrefixes: '', limitCount: 100, windowSeconds: 60, priority: 100 });

  const fetchRules = useCallback(() => {
    fetchWithAuth('/api/v1/admin/rate-limits')
      .then(async res => {
        if (!res.ok) throw new Error(await res.text());
        return res.json();
      })
      .then(json => {
        setRules(json.data || []);
        setError(null);
      })
      .catch(err => setError(errMessage(err, 'Failed to load rate limit rules')))
      .finally(() => setLoading(false));
  }, []);

  useEffect(() => { fetchRules(); }, [fetchRules]);

  const createRule = async (e: React.FormEvent) => {
    e.preventDefault();
    const prefixes = form.pathPrefixes.split(',').map(p => p.trim()).filter(Boolean);
    if (!form.name.trim() || prefixes.length === 0) return;
    try {
      const res = await fetchWithAuth('/api/v1/admin/rate-limits', {
        method: 'POST',
        body: JSON.stringify({
          name: form.name.trim(),
          path_prefixes: prefixes,
          limit_count: form.limitCount,
          window_seconds: form.windowSeconds,
          priority: form.priority,
        }),
      });
      if (!res.ok) throw new Error(await res.text());
      setForm({ name: '', pathPrefixes: '', limitCount: 100, windowSeconds: 60, priority: 100 });
      setShowForm(false);
      fetchRules();
    } catch (err) {
      alert(`Error creating rule: ${errMessage(err)}`);
    }
  };

  const toggleEnabled = async (rule: RateLimitRule) => {
    try {
      const res = await fetchWithAuth(`/api/v1/admin/rate-limits/${rule.id}`, {
        method: 'PUT',
        body: JSON.stringify({ enabled: !rule.enabled, priority: rule.priority, path_prefixes: [] }),
      });
      if (!res.ok) throw new Error(await res.text());
      fetchRules();
    } catch (err) {
      alert(`Error updating rule: ${errMessage(err)}`);
    }
  };

  const deleteRule = async (id: string) => {
    if (!confirm('Delete this rate limit rule?')) return;
    try {
      const res = await fetchWithAuth(`/api/v1/admin/rate-limits/${id}`, { method: 'DELETE' });
      if (!res.ok) throw new Error(await res.text());
      fetchRules();
    } catch (err) {
      alert(`Error deleting rule: ${errMessage(err)}`);
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
          <h3 style={{ fontSize: '19px', fontWeight: 700, margin: '0 0 8px', color: t.text1 }}>Rate Limits</h3>
          <p style={{ color: t.text2, fontSize: '13.5px', maxWidth: '520px', lineHeight: 1.6 }}>Distributed request budgets per path prefix. Changes apply within ~5s, no redeploy.</p>
        </div>
        <button onClick={() => setShowForm(v => !v)} style={primaryBtnStyle}>
          {showForm ? 'Cancel' : '+ New Rule'}
        </button>
      </div>

      {showForm && (
        <form onSubmit={createRule} style={{ background: t.dark ? 'rgba(0,0,0,0.2)' : 'rgba(0,0,0,0.03)', padding: '20px', borderRadius: '12px', border: '1px solid ' + t.panelBorder, marginBottom: '24px', display: 'flex', flexDirection: 'column', gap: '12px' }}>
          <div style={{ display: 'flex', gap: '12px', flexWrap: 'wrap' }}>
            <input
              required
              placeholder="Rule name"
              value={form.name}
              onChange={e => setForm({ ...form, name: e.target.value })}
              style={{ ...inputStyle, flex: 1, minWidth: '160px' }}
            />
            <input
              required
              placeholder="Path prefixes, comma-separated (e.g. /api/v1/search)"
              value={form.pathPrefixes}
              onChange={e => setForm({ ...form, pathPrefixes: e.target.value })}
              style={{ ...inputStyle, flex: 2, minWidth: '220px' }}
            />
          </div>
          <div style={{ display: 'flex', gap: '12px', flexWrap: 'wrap' }}>
            <label style={{ display: 'flex', alignItems: 'center', gap: '8px', fontSize: '13px', color: t.text2 }}>
              Limit
              <input type="number" min={1} value={form.limitCount} onChange={e => setForm({ ...form, limitCount: parseInt(e.target.value, 10) || 1 })}
                style={{ ...inputStyle, width: '90px', padding: '8px 10px' }} />
            </label>
            <label style={{ display: 'flex', alignItems: 'center', gap: '8px', fontSize: '13px', color: t.text2 }}>
              per (seconds)
              <input type="number" min={1} value={form.windowSeconds} onChange={e => setForm({ ...form, windowSeconds: parseInt(e.target.value, 10) || 1 })}
                style={{ ...inputStyle, width: '90px', padding: '8px 10px' }} />
            </label>
            <label style={{ display: 'flex', alignItems: 'center', gap: '8px', fontSize: '13px', color: t.text2 }}>
              Priority (lower = matched first)
              <input type="number" value={form.priority} onChange={e => setForm({ ...form, priority: parseInt(e.target.value, 10) || 0 })}
                style={{ ...inputStyle, width: '90px', padding: '8px 10px' }} />
            </label>
          </div>
          <button type="submit" style={{ ...primaryBtnStyle, alignSelf: 'flex-start' }}>Create Rule</button>
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
            <th style={{ padding: '10px 8px', fontWeight: 600, color: t.text2, fontSize: '12px' }}>Path Prefixes</th>
            <th style={{ padding: '10px 8px', fontWeight: 600, color: t.text2, fontSize: '12px' }}>Budget</th>
            <th style={{ padding: '10px 8px', fontWeight: 600, color: t.text2, fontSize: '12px' }}>Status</th>
            <th style={{ padding: '10px 8px', fontWeight: 600, color: t.text2, fontSize: '12px' }}>Actions</th>
          </tr>
        </thead>
        <tbody>
          {loading ? (
            <tr><td colSpan={6} style={{ padding: '24px', textAlign: 'center', color: t.text2 }}>Loading rate limit rules...</td></tr>
          ) : rules.length === 0 ? (
            <tr><td colSpan={6} style={{ padding: '24px', textAlign: 'center', color: t.text2 }}>No rate limit rules configured.</td></tr>
          ) : rules.map(rule => (
            <tr key={rule.id} style={{ borderBottom: '1px solid ' + t.panelBorder, opacity: rule.enabled ? 1 : 0.5 }}>
              <td style={{ padding: '14px 8px', color: t.text2, fontSize: '13px' }}>{rule.priority}</td>
              <td style={{ padding: '14px 8px', fontWeight: 500, fontSize: '13.5px', color: t.text1 }}>{rule.name}</td>
              <td style={{ padding: '14px 8px', fontFamily: 'monospace', fontSize: '12px', color: t.text1 }}>{rule.path_prefixes.join(', ')}</td>
              <td style={{ padding: '14px 8px', fontSize: '13.5px', color: t.text1 }}>{rule.limit_count} / {rule.window_seconds}s</td>
              <td style={{ padding: '14px 8px', fontSize: '13px', color: t.text2 }}>{rule.enabled ? 'enabled' : 'disabled'}</td>
              <td style={{ padding: '14px 8px' }}>
                <div style={{ display: 'flex', gap: '8px' }}>
                  <button onClick={() => toggleEnabled(rule)} style={ghostBtnStyle}>{rule.enabled ? 'Disable' : 'Enable'}</button>
                  <button onClick={() => deleteRule(rule.id)} style={ghostRedBtnStyle}>Delete</button>
                </div>
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}
