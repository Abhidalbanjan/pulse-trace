"use client";

import React, { useState, useEffect, useCallback } from 'react';
import { useTheme } from '@/context/ThemeContext';
import { api } from '@/lib/api/client';
import { fetchWithAuth } from '@/lib/api';
import type { ThemeTokens } from '@/lib/theme';

// PostmortemPanel (Incidents · E1): generate an AI-drafted postmortem from the
// incident's evidence, edit it, and export it. GET returns the stored draft (or
// null → offer to generate); POST (re)generates; PUT saves an edit.

interface Postmortem {
  incident_id: string;
  content: string;
  model: string;
  generated_at: string;
  edited_at?: string | null;
}

// renderMarkdown is a small, safe, block-level Markdown renderer (text nodes
// only — no dangerouslySetInnerHTML). It covers what the postmortem emits:
// headings, bullet/numbered lists, and task checkboxes.
function renderMarkdown(md: string, t: ThemeTokens): React.ReactNode {
  const lines = md.split('\n');
  const out: React.ReactNode[] = [];
  let list: React.ReactNode[] = [];
  const flush = () => {
    if (list.length) {
      out.push(<ul key={`ul-${out.length}`} style={{ margin: '4px 0 12px', paddingLeft: 20, display: 'flex', flexDirection: 'column', gap: 4 }}>{list}</ul>);
      list = [];
    }
  };
  lines.forEach((raw, i) => {
    const line = raw.replace(/\r$/, '');
    if (/^#\s+/.test(line)) { flush(); out.push(<h2 key={i} style={{ fontSize: 20, fontWeight: 700, color: t.text1, margin: '10px 0 8px' }}>{line.replace(/^#\s+/, '')}</h2>); return; }
    if (/^##\s+/.test(line)) { flush(); out.push(<h3 key={i} style={{ fontSize: 15, fontWeight: 700, color: t.text1, margin: '16px 0 6px' }}>{line.replace(/^##\s+/, '')}</h3>); return; }
    if (/^###\s+/.test(line)) { flush(); out.push(<h4 key={i} style={{ fontSize: 13.5, fontWeight: 700, color: t.text1, margin: '12px 0 4px' }}>{line.replace(/^###\s+/, '')}</h4>); return; }
    const task = line.match(/^-\s+\[([ xX])\]\s+(.*)$/);
    if (task) { list.push(<li key={i} style={{ listStyle: 'none', marginLeft: -16, color: t.text1, fontSize: 13.5 }}><input type="checkbox" checked={task[1].toLowerCase() === 'x'} readOnly style={{ marginRight: 8 }} />{task[2]}</li>); return; }
    if (/^[-*]\s+/.test(line)) { list.push(<li key={i} style={{ color: t.text1, fontSize: 13.5 }}>{line.replace(/^[-*]\s+/, '')}</li>); return; }
    if (/^\d+\.\s+/.test(line)) { list.push(<li key={i} style={{ color: t.text1, fontSize: 13.5, listStyle: 'decimal' }}>{line.replace(/^\d+\.\s+/, '')}</li>); return; }
    flush();
    if (line.trim() === '') { out.push(<div key={i} style={{ height: 6 }} />); return; }
    out.push(<p key={i} style={{ color: t.text1, fontSize: 13.5, lineHeight: 1.6, margin: '4px 0' }}>{line}</p>);
  });
  flush();
  return out;
}

export function PostmortemPanel({ incidentId }: { incidentId: string }) {
  const { tokens: t } = useTheme();
  const [pm, setPm] = useState<Postmortem | null>(null);
  const [loading, setLoading] = useState(true);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [editing, setEditing] = useState(false);
  const [draft, setDraft] = useState('');

  const load = useCallback(() => {
    setLoading(true);
    setEditing(false);
    api.getData<Postmortem | null>(`/api/v1/incidents/${encodeURIComponent(incidentId)}/postmortem`)
      .then((d) => setPm(d ?? null))
      .catch(() => setError('Failed to load postmortem'))
      .finally(() => setLoading(false));
  }, [incidentId]);

  // eslint-disable-next-line react-hooks/set-state-in-effect -- intentional: sync the panel from the API when the selected incident changes; the effect is the right place to fetch and hydrate.
  useEffect(() => { load(); }, [load]);

  const generate = async () => {
    setBusy(true); setError(null);
    try {
      const res = await fetchWithAuth(`/api/v1/incidents/${encodeURIComponent(incidentId)}/postmortem`, { method: 'POST' });
      const json = await res.json();
      if (!res.ok) throw new Error(json.error || 'Generation failed');
      setPm(json.data as Postmortem);
    } catch (e) {
      setError(e instanceof Error ? e.message : 'Generation failed');
    } finally { setBusy(false); }
  };

  const save = async () => {
    setBusy(true); setError(null);
    try {
      const res = await fetchWithAuth(`/api/v1/incidents/${encodeURIComponent(incidentId)}/postmortem`, {
        method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ content: draft }),
      });
      const json = await res.json();
      if (!res.ok) throw new Error(json.error || 'Save failed');
      setPm(json.data as Postmortem);
      setEditing(false);
    } catch (e) {
      setError(e instanceof Error ? e.message : 'Save failed');
    } finally { setBusy(false); }
  };

  const exportMd = () => {
    if (!pm) return;
    const blob = new Blob([pm.content], { type: 'text/markdown' });
    const url = URL.createObjectURL(blob);
    const a = document.createElement('a');
    a.href = url;
    a.download = `postmortem-${incidentId}.md`;
    document.body.appendChild(a); a.click(); a.remove();
    URL.revokeObjectURL(url);
  };

  const btn = (primary: boolean): React.CSSProperties => ({
    padding: '9px 16px', borderRadius: 10, fontSize: 13, fontWeight: 600, cursor: busy ? 'not-allowed' : 'pointer',
    opacity: busy ? 0.6 : 1, border: primary ? 'none' : `1px solid ${t.panelBorder}`,
    background: primary ? `linear-gradient(135deg, ${t.accent}, ${t.accent2})` : 'transparent', color: primary ? '#fff' : t.text1,
  });

  if (loading) return <div style={{ padding: 32, color: t.text2 }}>Loading postmortem…</div>;

  return (
    <div>
      {error && <div style={{ padding: 12, background: t.redSoft, color: t.red, borderRadius: 8, marginBottom: 16 }}>{error}</div>}

      {!pm ? (
        <div style={{ textAlign: 'center', padding: '40px 20px' }}>
          <p style={{ color: t.text2, fontSize: 14, marginBottom: 16, maxWidth: 460, marginInline: 'auto', lineHeight: 1.6 }}>
            No postmortem yet. Generate a blameless retrospective — summary, impact, timeline, root cause, contributing factors, and action items — drafted from this incident’s evidence.
          </p>
          <button onClick={generate} disabled={busy} style={btn(true)}>{busy ? 'Generating…' : '✦ Generate postmortem'}</button>
        </div>
      ) : editing ? (
        <div>
          <textarea
            value={draft}
            onChange={(e) => setDraft(e.target.value)}
            aria-label="Postmortem content"
            style={{ width: '100%', minHeight: 400, padding: 14, borderRadius: 10, border: `1px solid ${t.panelBorder}`, background: t.dark ? 'rgba(0,0,0,0.2)' : 'rgba(0,0,0,0.02)', color: t.text1, fontFamily: 'monospace', fontSize: 13, lineHeight: 1.6, resize: 'vertical' }}
          />
          <div style={{ display: 'flex', gap: 10, marginTop: 12 }}>
            <button onClick={save} disabled={busy} style={btn(true)}>Save</button>
            <button onClick={() => setEditing(false)} disabled={busy} style={btn(false)}>Cancel</button>
          </div>
        </div>
      ) : (
        <div>
          <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', gap: 12, flexWrap: 'wrap', marginBottom: 12 }}>
            <span style={{ fontSize: 11.5, color: t.text2 }}>
              Drafted by {pm.model || 'template'} · {new Date(pm.generated_at).toLocaleString()}{pm.edited_at ? ` · edited ${new Date(pm.edited_at).toLocaleString()}` : ''}
            </span>
            <div style={{ display: 'flex', gap: 8 }}>
              <button onClick={() => { setDraft(pm.content); setEditing(true); }} style={btn(false)}>Edit</button>
              <button onClick={exportMd} style={btn(false)}>Export</button>
              <button onClick={generate} disabled={busy} style={btn(false)}>{busy ? 'Regenerating…' : 'Regenerate'}</button>
            </div>
          </div>
          <div style={{ padding: '18px 20px', borderRadius: 14, background: t.dark ? 'rgba(255,255,255,0.02)' : 'rgba(0,0,0,0.015)', border: `1px solid ${t.panelBorder}` }}>
            {renderMarkdown(pm.content, t)}
          </div>
        </div>
      )}
    </div>
  );
}
