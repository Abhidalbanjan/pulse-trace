"use client";

import React, { useState, useEffect, useCallback } from 'react';
import { fetchWithAuth } from '@/lib/api';
import { useTheme } from '@/context/ThemeContext';

interface Deployment {
  id: string;
  service: string;
  version: string;
  git_sha: string;
  environment: string;
  deployed_by: string;
  notes: string;
  deployed_at: string;
}

export function DeploymentsPanel({ serviceName }: { serviceName: string }) {
  const { tokens: t } = useTheme();
  const [deployments, setDeployments] = useState<Deployment[]>([]);
  const [loading, setLoading] = useState(true);
  const [showForm, setShowForm] = useState(false);
  const [submitting, setSubmitting] = useState(false);
  const [form, setForm] = useState({ version: '', gitSha: '', environment: 'production', deployedBy: '', notes: '' });

  const fetchDeployments = useCallback(() => {
    fetchWithAuth(`/api/v1/deployments?service=${encodeURIComponent(serviceName)}`)
      .then(async res => {
        if (!res.ok) throw new Error(await res.text());
        return res.json();
      })
      .then(json => setDeployments(json.data || []))
      .catch(err => console.error('Failed to load deployments:', err))
      .finally(() => setLoading(false));
  }, [serviceName]);

  useEffect(() => {
    fetchDeployments();
  }, [fetchDeployments]);

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!form.version.trim()) return;
    setSubmitting(true);
    try {
      const res = await fetchWithAuth('/api/v1/deployments', {
        method: 'POST',
        body: JSON.stringify({
          service: serviceName,
          version: form.version.trim(),
          git_sha: form.gitSha.trim(),
          environment: form.environment,
          deployed_by: form.deployedBy.trim(),
          notes: form.notes.trim(),
        }),
      });
      if (!res.ok) throw new Error(await res.text());
      setForm({ version: '', gitSha: '', environment: 'production', deployedBy: '', notes: '' });
      setShowForm(false);
      await fetchDeployments();
    } catch (err) {
      console.error('Failed to record deployment:', err);
    } finally {
      setSubmitting(false);
    }
  };

  const inputStyle: React.CSSProperties = {
    background: t.dark ? 'rgba(255,255,255,0.05)' : 'rgba(0,0,0,0.03)',
    border: '1px solid ' + t.panelBorder,
    borderRadius: '10px',
    color: t.text1,
    fontSize: '13px',
    outline: 'none',
  };

  const cardStyle: React.CSSProperties = {
    padding: 0,
    overflow: 'auto',
    borderRadius: '20px',
    background: t.panelBg,
    border: '1px solid ' + t.panelBorder,
    backdropFilter: 'blur(30px) saturate(180%)',
    WebkitBackdropFilter: 'blur(30px) saturate(180%)',
    boxShadow: t.shadow,
  };

  return (
    <div style={cardStyle}>
      <div style={{ padding: '16px 20px', borderBottom: '1px solid ' + t.panelBorder, display: 'flex', justifyContent: 'space-between', alignItems: 'center' }}>
        <h3 style={{ fontSize: '14px', fontWeight: 600, color: t.text1, margin: 0 }}>Deployments</h3>
        <button
          onClick={() => setShowForm(v => !v)}
          style={{
            fontSize: '12.5px',
            fontWeight: 600,
            padding: '7px 14px',
            borderRadius: '8px',
            cursor: 'pointer',
            ...(showForm
              ? { background: 'transparent', border: '1px solid ' + t.panelBorder, color: t.text1 }
              : { background: `linear-gradient(135deg, ${t.accent}, ${t.accent2})`, border: 'none', color: '#fff' }),
          }}
        >
          {showForm ? 'Cancel' : '+ Record Deployment'}
        </button>
      </div>

      {showForm && (
        <form onSubmit={submit} style={{ padding: '16px 20px', borderBottom: '1px solid ' + t.panelBorder, display: 'flex', flexDirection: 'column', gap: '10px' }}>
          <div style={{ display: 'flex', gap: '10px', flexWrap: 'wrap' }}>
            <input
              required
              placeholder="Version (e.g. v1.4.2 or git sha)"
              value={form.version}
              onChange={e => setForm({ ...form, version: e.target.value })}
              style={{ ...inputStyle, flex: '1 1 180px', padding: '8px 12px' }}
            />
            <input
              placeholder="Git SHA (optional)"
              value={form.gitSha}
              onChange={e => setForm({ ...form, gitSha: e.target.value })}
              style={{ ...inputStyle, flex: '1 1 140px', padding: '8px 12px' }}
            />
            <select
              value={form.environment}
              onChange={e => setForm({ ...form, environment: e.target.value })}
              style={{ ...inputStyle, flex: '1 1 140px', padding: '8px 12px' }}
            >
              <option value="production">production</option>
              <option value="staging">staging</option>
              <option value="development">development</option>
            </select>
            <input
              placeholder="Deployed by"
              value={form.deployedBy}
              onChange={e => setForm({ ...form, deployedBy: e.target.value })}
              style={{ ...inputStyle, flex: '1 1 140px', padding: '8px 12px' }}
            />
          </div>
          <input
            placeholder="Notes (optional)"
            value={form.notes}
            onChange={e => setForm({ ...form, notes: e.target.value })}
            style={{ ...inputStyle, padding: '8px 12px' }}
          />
          <button
            type="submit"
            disabled={submitting}
            style={{
              alignSelf: 'flex-start',
              padding: '9px 22px',
              borderRadius: '10px',
              border: 'none',
              fontSize: '13px',
              fontWeight: 600,
              color: '#fff',
              cursor: submitting ? 'default' : 'pointer',
              background: `linear-gradient(135deg, ${t.accent}, ${t.accent2})`,
              opacity: submitting ? 0.6 : 1,
            }}
          >
            {submitting ? 'Recording...' : 'Record Deployment'}
          </button>
        </form>
      )}

      {loading ? (
        <div style={{ padding: '32px', textAlign: 'center', color: t.text2 }}>Loading deployments...</div>
      ) : deployments.length === 0 ? (
        <div style={{ padding: '32px', textAlign: 'center', color: t.text2 }}>No deployments recorded for this service yet.</div>
      ) : (
        <table style={{ width: '100%', borderCollapse: 'collapse', textAlign: 'left' }}>
          <thead>
            <tr style={{ borderBottom: '1px solid ' + t.panelBorder, background: t.dark ? 'rgba(0,0,0,0.15)' : 'rgba(0,0,0,0.03)' }}>
              <th style={{ padding: '16px', fontWeight: 600, color: t.text2, fontSize: '12.5px' }}>Version</th>
              <th style={{ padding: '16px', fontWeight: 600, color: t.text2, fontSize: '12.5px' }}>Environment</th>
              <th style={{ padding: '16px', fontWeight: 600, color: t.text2, fontSize: '12.5px' }}>Deployed By</th>
              <th style={{ padding: '16px', fontWeight: 600, color: t.text2, fontSize: '12.5px' }}>Deployed At</th>
              <th style={{ padding: '16px', fontWeight: 600, color: t.text2, fontSize: '12.5px' }}>Notes</th>
            </tr>
          </thead>
          <tbody>
            {deployments.map(d => (
              <tr key={d.id} style={{ borderBottom: '1px solid ' + t.panelBorder }}>
                <td style={{ padding: '15px 16px', fontFamily: 'monospace', fontSize: '13px', color: t.accent }}>{d.version}</td>
                <td style={{ padding: '15px 16px', fontSize: '13.5px', color: t.text1 }}>{d.environment}</td>
                <td style={{ padding: '15px 16px', fontSize: '13.5px', color: t.text1 }}>{d.deployed_by || '—'}</td>
                <td style={{ padding: '15px 16px', fontSize: '13.5px', color: t.text2 }}>{new Date(d.deployed_at).toLocaleString()}</td>
                <td style={{ padding: '15px 16px', fontSize: '13.5px', color: t.text2 }}>{d.notes || '—'}</td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </div>
  );
}
