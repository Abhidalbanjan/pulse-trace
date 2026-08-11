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

// Attribute catalog for the guided builder. These mirror the env the backend
// evaluates a condition against (subject.role/tier/tenant_id, action,
// resource.type — see auth.abacValidationEnv), so a rule built here maps 1:1
// onto what actually runs. The backend /validate endpoint is the correctness
// gate; this catalog is authoring convenience.
const ATTRIBUTES: { key: string; label: string; suggestions: string[] }[] = [
  { key: 'subject.role', label: 'User role', suggestions: [] },
  { key: 'subject.tier', label: 'Tenant tier', suggestions: ['standard', 'pro', 'enterprise'] },
  { key: 'subject.tenant_id', label: 'Tenant ID', suggestions: [] },
  { key: 'action', label: 'Action', suggestions: ['read', 'create', 'update', 'delete'] },
  { key: 'resource.type', label: 'Resource type', suggestions: ['incidents', 'topology', 'admin', 'settings'] },
];
const OPERATORS: { key: string; label: string }[] = [
  { key: '==', label: 'is' },
  { key: '!=', label: 'is not' },
];

interface Clause { attr: string; op: string; value: string }

// buildCondition turns the guided clauses into an expr-lang string. String
// values are JSON-quoted so operator-typed input can't break out of the literal.
function buildCondition(clauses: Clause[], join: string): string {
  return clauses
    .filter((c) => c.value.trim() !== '')
    .map((c) => `${c.attr} ${c.op} ${JSON.stringify(c.value)}`)
    .join(` ${join} `);
}

export function PoliciesPanel() {
  const { tokens: t } = useTheme();
  const [policies, setPolicies] = useState<Policy[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [showForm, setShowForm] = useState(false);
  const [form, setForm] = useState({ name: '', effect: 'deny', resource: '*', condition: '', priority: 100 });
  const [formError, setFormError] = useState<string | null>(null);

  // Guided builder state.
  const [mode, setMode] = useState<'guided' | 'advanced'>('guided');
  const [clauses, setClauses] = useState<Clause[]>([{ attr: 'subject.role', op: '==', value: '' }]);
  const [join, setJoin] = useState('&&');
  const [roleSuggestions, setRoleSuggestions] = useState<string[]>([]);
  const [validity, setValidity] = useState<{ valid: boolean; error?: string } | null>(null);

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

  // Pull real role names so the role clause offers the tenant's actual roles.
  useEffect(() => {
    fetchWithAuth('/api/v1/admin/roles')
      .then(res => (res.ok ? res.json() : null))
      .then(json => {
        if (json?.data) setRoleSuggestions((json.data as { name: string }[]).map(r => r.name).filter(Boolean));
      })
      .catch(() => { /* suggestions are optional */ });
  }, []);

  // Keep the condition in sync with the builder while in guided mode.
  const condition = mode === 'guided' ? buildCondition(clauses, join) : form.condition;

  // Live-validate the condition against the backend compiler (debounced), so the
  // operator sees the exact error before committing.
  useEffect(() => {
    const c = condition.trim();
    let cancelled = false;
    const timer = setTimeout(() => {
      if (!c) { if (!cancelled) setValidity(null); return; }
      fetchWithAuth('/api/v1/admin/policies/validate', {
        method: 'POST',
        body: JSON.stringify({ condition: c }),
      })
        .then(res => res.json())
        .then(j => { if (!cancelled) setValidity({ valid: !!j.valid, error: j.error }); })
        .catch(() => { if (!cancelled) setValidity(null); });
    }, 400);
    return () => { cancelled = true; clearTimeout(timer); };
  }, [condition]);

  const suggestionsFor = (attr: string): string[] => {
    if (attr === 'subject.role' && roleSuggestions.length) return roleSuggestions;
    return ATTRIBUTES.find(a => a.key === attr)?.suggestions ?? [];
  };

  const createPolicy = async (e: React.FormEvent) => {
    e.preventDefault();
    setFormError(null);
    const finalCondition = condition.trim();
    if (!form.name.trim() || !finalCondition) {
      setFormError('A name and at least one condition are required.');
      return;
    }
    if (validity && !validity.valid) {
      setFormError(`Condition is invalid: ${validity.error}`);
      return;
    }
    try {
      const res = await fetchWithAuth('/api/v1/admin/policies', {
        method: 'POST',
        body: JSON.stringify({ ...form, condition: finalCondition }),
      });
      if (!res.ok) throw new Error(await res.text());
      setForm({ name: '', effect: 'deny', resource: '*', condition: '', priority: 100 });
      setClauses([{ attr: 'subject.role', op: '==', value: '' }]);
      setValidity(null);
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
  const smallSelect: React.CSSProperties = { ...inputStyle, padding: '8px 10px', fontSize: '13px' };

  const setClause = (i: number, patch: Partial<Clause>) =>
    setClauses(cs => cs.map((c, idx) => (idx === i ? { ...c, ...patch } : c)));

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
        <form onSubmit={createPolicy} style={{ background: t.dark ? 'rgba(0,0,0,0.2)' : 'rgba(0,0,0,0.03)', padding: '20px', borderRadius: '12px', border: '1px solid ' + t.panelBorder, marginBottom: '24px', display: 'flex', flexDirection: 'column', gap: '14px' }}>
          <div style={{ display: 'flex', gap: '12px', flexWrap: 'wrap' }}>
            <input
              required
              placeholder="Policy name"
              value={form.name}
              onChange={e => setForm({ ...form, name: e.target.value })}
              style={{ ...inputStyle, flex: 1, minWidth: '160px' }}
            />
            <select value={form.effect} onChange={e => setForm({ ...form, effect: e.target.value })} style={inputStyle}>
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

          {/* Guided / Advanced toggle */}
          <div style={{ display: 'flex', gap: '4px', background: t.dark ? 'rgba(0,0,0,0.25)' : 'rgba(0,0,0,0.05)', borderRadius: '8px', padding: '4px', alignSelf: 'flex-start' }}>
            {(['guided', 'advanced'] as const).map(m => (
              <button
                key={m}
                type="button"
                onClick={() => {
                  if (m === 'advanced' && mode === 'guided') setForm(f => ({ ...f, condition: buildCondition(clauses, join) }));
                  setMode(m);
                }}
                style={{
                  padding: '6px 14px', borderRadius: '6px', border: 'none', fontSize: '12.5px', fontWeight: 600, cursor: 'pointer',
                  background: mode === m ? (t.dark ? 'rgba(255,255,255,0.12)' : '#fff') : 'transparent',
                  color: mode === m ? t.text1 : t.text2,
                }}
              >
                {m === 'guided' ? 'Guided builder' : 'Advanced (expr)'}
              </button>
            ))}
          </div>

          {mode === 'guided' ? (
            <div style={{ display: 'flex', flexDirection: 'column', gap: '10px' }}>
              {clauses.length > 1 && (
                <div style={{ display: 'flex', alignItems: 'center', gap: '10px', fontSize: '12.5px', color: t.text2 }}>
                  Match
                  <select value={join} onChange={e => setJoin(e.target.value)} style={smallSelect}>
                    <option value="&&">ALL clauses (AND)</option>
                    <option value="||">ANY clause (OR)</option>
                  </select>
                </div>
              )}
              {clauses.map((c, i) => (
                <div key={i} style={{ display: 'flex', gap: '8px', alignItems: 'center', flexWrap: 'wrap' }}>
                  <select value={c.attr} onChange={e => setClause(i, { attr: e.target.value })} style={smallSelect} aria-label="Attribute">
                    {ATTRIBUTES.map(a => <option key={a.key} value={a.key}>{a.label}</option>)}
                  </select>
                  <select value={c.op} onChange={e => setClause(i, { op: e.target.value })} style={smallSelect} aria-label="Operator">
                    {OPERATORS.map(o => <option key={o.key} value={o.key}>{o.label}</option>)}
                  </select>
                  <input
                    list={`clause-suggest-${i}`}
                    value={c.value}
                    onChange={e => setClause(i, { value: e.target.value })}
                    placeholder="value"
                    aria-label="Value"
                    style={{ ...smallSelect, flex: 1, minWidth: '140px' }}
                  />
                  <datalist id={`clause-suggest-${i}`}>
                    {suggestionsFor(c.attr).map(s => <option key={s} value={s} />)}
                  </datalist>
                  {clauses.length > 1 && (
                    <button type="button" onClick={() => setClauses(cs => cs.filter((_, idx) => idx !== i))} style={ghostRedBtnStyle} aria-label="Remove clause">✕</button>
                  )}
                </div>
              ))}
              <button
                type="button"
                onClick={() => setClauses(cs => [...cs, { attr: 'action', op: '!=', value: '' }])}
                style={{ ...ghostBtnStyle, alignSelf: 'flex-start' }}
              >
                + Add clause
              </button>
            </div>
          ) : (
            <input
              placeholder='Condition, e.g. subject.role == "viewer" && action != "read"'
              value={form.condition}
              onChange={e => setForm({ ...form, condition: e.target.value })}
              style={{ ...inputStyle, fontFamily: 'monospace', fontSize: '13px' }}
            />
          )}

          {/* Generated expression + live validity */}
          <div style={{ display: 'flex', alignItems: 'center', gap: '10px', flexWrap: 'wrap' }}>
            <code style={{ flex: 1, minWidth: '220px', fontFamily: 'monospace', fontSize: '12.5px', padding: '10px 12px', borderRadius: '8px', background: t.dark ? 'rgba(0,0,0,0.3)' : 'rgba(0,0,0,0.04)', color: condition.trim() ? t.text1 : t.text2, wordBreak: 'break-all' }}>
              {condition.trim() || 'Your condition will appear here.'}
            </code>
            {condition.trim() && validity && (
              <span style={{ fontSize: '12.5px', fontWeight: 700, color: validity.valid ? t.green : t.red, whiteSpace: 'nowrap' }}>
                {validity.valid ? '✓ Valid' : '✗ Invalid'}
              </span>
            )}
          </div>
          {validity && !validity.valid && validity.error && (
            <div style={{ color: t.red, fontSize: '12.5px', fontFamily: 'monospace' }}>{validity.error}</div>
          )}

          {formError && <div style={{ color: t.red, fontSize: '13px' }}>{formError}</div>}
          <button type="submit" disabled={!!(validity && !validity.valid)} style={{ ...primaryBtnStyle, alignSelf: 'flex-start', opacity: validity && !validity.valid ? 0.5 : 1 }}>Create Policy</button>
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
