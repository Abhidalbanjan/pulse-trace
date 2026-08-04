"use client";

import React, { useState, useEffect, useCallback } from 'react';
import { errMessage } from '@/lib/errMessage';
import { fetchWithAuth } from '@/lib/api';
import { useTheme } from '@/context/ThemeContext';

interface AlertRule {
  id: string;
  name: string;
  description: string;
  service_name: string;
  condition: string;
  severity: string;
  cooldown_seconds: number;
  enabled: boolean;
}

const SEVERITIES = ['DEBUG', 'INFO', 'WARNING', 'ERROR', 'FATAL', 'CRITICAL'];

const CONDITION_FIELDS = [
  { name: 'error_rate', desc: 'error % over the last 15m' },
  { name: 'p50_latency_ms', desc: 'p50 latency (ms)' },
  { name: 'p90_latency_ms', desc: 'p90 latency (ms)' },
  { name: 'p99_latency_ms', desc: 'p99 latency (ms)' },
  { name: 'request_count', desc: 'requests in window' },
  { name: 'error_count', desc: 'errored requests in window' },
  { name: 'baseline_ratio', desc: 'current p99 vs its own rolling baseline (0 until enough history)' },
];

export function AlertRulesPanel() {
  const { tokens: t } = useTheme();
  const [rules, setRules] = useState<AlertRule[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [showForm, setShowForm] = useState(false);
  const [formError, setFormError] = useState<string | null>(null);
  const [form, setForm] = useState({
    name: '', description: '', serviceName: '*', condition: '', severity: 'WARNING', cooldownSeconds: 900,
  });

  const fetchRules = useCallback(() => {
    fetchWithAuth('/api/v1/admin/alert-rules')
      .then(async res => {
        if (!res.ok) throw new Error(await res.text());
        return res.json();
      })
      .then(json => {
        setRules(json.data || []);
        setError(null);
      })
      .catch(err => setError(errMessage(err, 'Failed to load alert rules')))
      .finally(() => setLoading(false));
  }, []);

  useEffect(() => { fetchRules(); }, [fetchRules]);

  const createRule = async (e: React.FormEvent) => {
    e.preventDefault();
    setFormError(null);
    if (!form.name.trim() || !form.condition.trim()) return;
    try {
      const res = await fetchWithAuth('/api/v1/admin/alert-rules', {
        method: 'POST',
        body: JSON.stringify({
          name: form.name.trim(),
          description: form.description.trim(),
          service_name: form.serviceName.trim() || '*',
          condition: form.condition.trim(),
          severity: form.severity,
          cooldown_seconds: form.cooldownSeconds,
        }),
      });
      if (!res.ok) throw new Error(await res.text());
      setForm({ name: '', description: '', serviceName: '*', condition: '', severity: 'WARNING', cooldownSeconds: 900 });
      setShowForm(false);
      fetchRules();
    } catch (err) {
      setFormError(errMessage(err, 'Failed to create rule'));
    }
  };

  const toggleEnabled = async (rule: AlertRule) => {
    try {
      const res = await fetchWithAuth(`/api/v1/admin/alert-rules/${rule.id}`, {
        method: 'PUT',
        body: JSON.stringify({ enabled: !rule.enabled }),
      });
      if (!res.ok) throw new Error(await res.text());
      fetchRules();
    } catch (err) {
      alert(`Error updating rule: ${errMessage(err)}`);
    }
  };

  const deleteRule = async (id: string) => {
    if (!confirm('Delete this alert rule?')) return;
    try {
      const res = await fetchWithAuth(`/api/v1/admin/alert-rules/${id}`, { method: 'DELETE' });
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
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'flex-start', marginBottom: '20px', gap: '16px', flexWrap: 'wrap' }}>
        <div>
          <h3 style={{ fontSize: '19px', fontWeight: 700, margin: '0 0 8px', color: t.text1 }}>Alert Rules</h3>
          <p style={{ color: t.text2, fontSize: '13.5px', maxWidth: '560px', lineHeight: 1.6 }}>
            Define the conditions that trigger an incident notification, evaluated against live RED metrics every 15s. Changes take effect within a minute — no redeploy.
          </p>
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
              placeholder="Service name (default: * for all services)"
              value={form.serviceName}
              onChange={e => setForm({ ...form, serviceName: e.target.value })}
              style={{ ...inputStyle, flex: 1, minWidth: '160px' }}
            />
            <select value={form.severity} onChange={e => setForm({ ...form, severity: e.target.value })} style={{ ...inputStyle, width: '140px' }}>
              {SEVERITIES.map(s => <option key={s} value={s}>{s}</option>)}
            </select>
          </div>
          <input
            placeholder="Description (optional)"
            value={form.description}
            onChange={e => setForm({ ...form, description: e.target.value })}
            style={inputStyle}
          />
          <input
            required
            placeholder="Condition, e.g. error_rate > 5 && request_count > 20"
            value={form.condition}
            onChange={e => setForm({ ...form, condition: e.target.value })}
            style={{ ...inputStyle, fontFamily: 'monospace' }}
          />
          <div style={{ fontSize: '11.5px', color: t.text2, lineHeight: 1.8 }}>
            Available fields: {CONDITION_FIELDS.map(f => `${f.name} (${f.desc})`).join(' · ')}
          </div>
          <label style={{ display: 'flex', alignItems: 'center', gap: '8px', fontSize: '13px', color: t.text2 }}>
            Cooldown (seconds) — minimum time between repeat firings of this rule per service
            <input type="number" min={0} value={form.cooldownSeconds} onChange={e => setForm({ ...form, cooldownSeconds: parseInt(e.target.value, 10) || 0 })}
              style={{ ...inputStyle, width: '100px', padding: '8px 10px' }} />
          </label>
          {formError && (
            <div style={{ padding: '10px 12px', background: t.redSoft, color: t.red, borderRadius: '8px', fontSize: '12.5px' }}>{formError}</div>
          )}
          <button type="submit" style={{ ...primaryBtnStyle, alignSelf: 'flex-start' }}>Create Rule</button>
        </form>
      )}

      {error && (
        <div style={{ padding: '16px', background: t.redSoft, color: t.red, borderRadius: '8px', marginBottom: '24px' }}>{error}</div>
      )}

      <table style={{ width: '100%', borderCollapse: 'collapse' }}>
        <thead>
          <tr style={{ borderBottom: '1px solid ' + t.panelBorder, textAlign: 'left' }}>
            <th style={{ padding: '10px 8px', fontWeight: 600, color: t.text2, fontSize: '12px' }}>Name</th>
            <th style={{ padding: '10px 8px', fontWeight: 600, color: t.text2, fontSize: '12px' }}>Service</th>
            <th style={{ padding: '10px 8px', fontWeight: 600, color: t.text2, fontSize: '12px' }}>Condition</th>
            <th style={{ padding: '10px 8px', fontWeight: 600, color: t.text2, fontSize: '12px' }}>Severity</th>
            <th style={{ padding: '10px 8px', fontWeight: 600, color: t.text2, fontSize: '12px' }}>Cooldown</th>
            <th style={{ padding: '10px 8px', fontWeight: 600, color: t.text2, fontSize: '12px' }}>Status</th>
            <th style={{ padding: '10px 8px', fontWeight: 600, color: t.text2, fontSize: '12px' }}>Actions</th>
          </tr>
        </thead>
        <tbody>
          {loading ? (
            <tr><td colSpan={7} style={{ padding: '24px', textAlign: 'center', color: t.text2 }}>Loading alert rules...</td></tr>
          ) : rules.length === 0 ? (
            <tr><td colSpan={7} style={{ padding: '24px', textAlign: 'center', color: t.text2 }}>No alert rules configured.</td></tr>
          ) : rules.map(rule => (
            <tr key={rule.id} style={{ borderBottom: '1px solid ' + t.panelBorder, opacity: rule.enabled ? 1 : 0.5 }}>
              <td style={{ padding: '14px 8px', fontWeight: 500, fontSize: '13.5px', color: t.text1 }}>
                {rule.name}
                {rule.description && <div style={{ fontSize: '11.5px', color: t.text2, fontWeight: 400, marginTop: '2px' }}>{rule.description}</div>}
              </td>
              <td style={{ padding: '14px 8px', fontFamily: 'monospace', fontSize: '12px', color: t.text1 }}>{rule.service_name}</td>
              <td style={{ padding: '14px 8px', fontFamily: 'monospace', fontSize: '12px', color: t.text1 }}>{rule.condition}</td>
              <td style={{ padding: '14px 8px', fontSize: '12px', color: t.text2 }}>{rule.severity}</td>
              <td style={{ padding: '14px 8px', fontSize: '13px', color: t.text1 }}>{rule.cooldown_seconds}s</td>
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
