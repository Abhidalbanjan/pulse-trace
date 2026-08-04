"use client";

// Ingestion-key management (ROAD_TO_100 · F4).
//
// Closes the F4 parity gap: the backend has full key lifecycle
// (list/create/rotate/revoke with a rotation grace window and replaced_by
// lineage) but the UI only ever *created* a key (onboarding wizard) and the
// Settings tab was a hardcoded placeholder. This panel makes the whole
// lifecycle UI-drivable on the F0.4 platform (typed client + useApiResource +
// StateBoundary/ConfirmDialog/Toast), with a one-time plaintext reveal that
// matches the "shown once, never retrievable" server contract.

import React, { useEffect, useRef, useState } from 'react';
import { useTheme } from '@/context/ThemeContext';
import { api, ApiError } from '@/lib/api/client';
import type { IngestionKey, IngestionKeyScope, MintedIngestionKey } from '@/lib/api/types';
import { useApiResource } from '@/lib/hooks/useApiResource';
import { StateBoundary, ConfirmDialog, useToast } from '@/components/ui';

function errMsg(err: unknown, fallback: string): string {
  return err instanceof ApiError || err instanceof Error ? err.message : fallback;
}

// Plan tiers a key can be minted for — the same set the billing handler accepts.
const TIERS = ['free', 'standard', 'premium', 'enterprise'];

// Rotation grace presets (Go duration strings the backend parses). "0" revokes
// the old key immediately; the rest keep it valid until the window expires so an
// embedded RUM token / running agent isn't cut off mid-flight. 720h = the
// backend's 30-day maximum.
const GRACE_PRESETS: { label: string; value: string }[] = [
  { label: 'Immediately (revoke old key now)', value: '0' },
  { label: '1 hour', value: '1h' },
  { label: '24 hours (recommended)', value: '24h' },
  { label: '7 days', value: '168h' },
  { label: '30 days (maximum)', value: '720h' },
];
const DEFAULT_GRACE = '24h';

// Absolute, locale-formatted timestamp; deterministic given the string (no
// wall-clock read), so it's safe to call during render.
function fmt(ts: string | null): string {
  if (!ts) return '—';
  const d = new Date(ts);
  return Number.isNaN(d.getTime()) ? '—' : d.toLocaleString();
}

type KeyState =
  | { kind: 'active'; label: string }
  | { kind: 'grace'; label: string } // revocation scheduled in the future
  | { kind: 'revoked'; label: string };

// Derive a key's lifecycle state. `now` is passed in (captured once per render)
// so this helper stays pure and testable.
function keyState(k: IngestionKey, now: number): KeyState {
  if (!k.revoked_at) return { kind: 'active', label: 'Active' };
  const revokedAt = new Date(k.revoked_at).getTime();
  if (!Number.isNaN(revokedAt) && revokedAt > now) {
    return { kind: 'grace', label: `Retiring ${fmt(k.revoked_at)}` };
  }
  return { kind: 'revoked', label: `Revoked ${fmt(k.revoked_at)}` };
}

export function IngestionKeysPanel() {
  const { tokens: t } = useTheme();
  const toast = useToast();

  const keys = useApiResource<IngestionKey[]>(
    () => api.getData<IngestionKey[]>('/api/v1/admin/ingestion-keys').then((d) => d ?? []),
  );
  const list = keys.data ?? [];

  // Create form.
  const [showForm, setShowForm] = useState(false);
  const [name, setName] = useState('');
  const [scope, setScope] = useState<IngestionKeyScope>('ingest');
  const [tier, setTier] = useState('standard');
  const [creating, setCreating] = useState(false);

  // One-time plaintext reveal (create *and* rotate feed this).
  const [minted, setMinted] = useState<MintedIngestionKey | null>(null);

  // Rotate flow.
  const [rotating, setRotating] = useState<IngestionKey | null>(null);
  const [graceChoice, setGraceChoice] = useState(DEFAULT_GRACE);
  const [rotateBusy, setRotateBusy] = useState(false);

  // Revoke flow.
  const [pendingRevoke, setPendingRevoke] = useState<IngestionKey | null>(null);
  const [revokeBusy, setRevokeBusy] = useState(false);

  const resetForm = () => {
    setName('');
    setScope('ingest');
    setTier('standard');
  };

  const createKey = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!name.trim() || creating) return;
    setCreating(true);
    try {
      const res = await api.post<MintedIngestionKey>('/api/v1/admin/ingestion-keys', {
        name: name.trim(),
        scope,
        tier,
      });
      resetForm();
      setShowForm(false);
      setMinted(res); // reveal the plaintext once
      toast.success(`Key "${res.name}" created`);
      await keys.refetch();
    } catch (err) {
      toast.error(`Error creating key: ${errMsg(err, 'request failed')}`);
    } finally {
      setCreating(false);
    }
  };

  const doRotate = async () => {
    if (!rotating || rotateBusy) return;
    setRotateBusy(true);
    try {
      const res = await api.post<MintedIngestionKey>(
        `/api/v1/admin/ingestion-keys/${encodeURIComponent(rotating.id)}/rotate`,
        { grace_period: graceChoice },
      );
      setRotating(null);
      setGraceChoice(DEFAULT_GRACE);
      setMinted(res); // reveal the new plaintext once
      toast.success(`Key "${res.name}" rotated`);
      await keys.refetch();
    } catch (err) {
      toast.error(`Error rotating key: ${errMsg(err, 'request failed')}`);
    } finally {
      setRotateBusy(false);
    }
  };

  const confirmRevoke = async () => {
    if (!pendingRevoke || revokeBusy) return;
    setRevokeBusy(true);
    try {
      await api.del(`/api/v1/admin/ingestion-keys/${encodeURIComponent(pendingRevoke.id)}`);
      toast.success(`Key "${pendingRevoke.name}" revoked`);
      setPendingRevoke(null);
      await keys.refetch();
    } catch (err) {
      toast.error(`Error revoking key: ${errMsg(err, 'request failed')}`);
    } finally {
      setRevokeBusy(false);
    }
  };

  // Single wall-clock read for grace-window status across the whole render.
  // eslint-disable-next-line react-hooks/purity -- deliberate current-time read to classify active/grace/revoked keys
  const now = Date.now();

  const primaryBtnStyle: React.CSSProperties = {
    padding: '10px 18px', borderRadius: '10px', border: 'none',
    background: `linear-gradient(135deg, ${t.accent}, ${t.accent2})`, color: '#fff',
    fontWeight: 600, fontSize: '13px', cursor: 'pointer', flexShrink: 0,
  };
  const inputStyle: React.CSSProperties = {
    padding: '10px 12px', background: t.dark ? 'rgba(255,255,255,0.05)' : 'rgba(255,255,255,0.7)',
    border: '1px solid ' + t.panelBorder, borderRadius: '8px', color: t.text1,
  };
  const ghostRedBtnStyle: React.CSSProperties = {
    padding: '6px 12px', fontSize: '12px', borderRadius: '8px', border: '1px solid ' + t.red,
    background: 'transparent', color: t.red, cursor: 'pointer',
  };
  const ghostBtnStyle: React.CSSProperties = {
    padding: '6px 12px', fontSize: '12px', borderRadius: '8px', border: '1px solid ' + t.panelBorder,
    background: 'transparent', color: t.text1, cursor: 'pointer',
  };
  const badge = (bg: string, fg: string): React.CSSProperties => ({
    background: bg, color: fg, padding: '2px 9px', borderRadius: '100px',
    fontSize: '11px', fontWeight: 600, whiteSpace: 'nowrap',
  });

  return (
    <div>
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'flex-start', marginBottom: '28px', gap: '16px', flexWrap: 'wrap' }}>
        <div>
          <h3 style={{ fontSize: '19px', fontWeight: 700, margin: '0 0 8px', color: t.text1 }}>API Keys</h3>
          <p style={{ color: t.text2, fontSize: '13.5px', maxWidth: '560px', lineHeight: 1.6 }}>
            Per-tenant ingestion keys for OpenTelemetry agents and SDKs. Rotate to issue a
            replacement while the old key keeps working through a grace window; revoke to kill
            one immediately. Secret keys are shown once at creation and never again.
          </p>
        </div>
        <button onClick={() => setShowForm((v) => !v)} style={primaryBtnStyle}>
          {showForm ? 'Cancel' : '+ Generate Key'}
        </button>
      </div>

      {showForm && (
        <form onSubmit={createKey} style={{ background: t.dark ? 'rgba(0,0,0,0.2)' : 'rgba(0,0,0,0.03)', padding: '20px', borderRadius: '12px', border: '1px solid ' + t.panelBorder, marginBottom: '24px', display: 'flex', gap: '12px', flexWrap: 'wrap', alignItems: 'flex-end' }}>
          <label style={{ display: 'flex', flexDirection: 'column', gap: '6px', flex: 1, minWidth: '200px' }}>
            <span style={{ fontSize: '12px', color: t.text2 }}>Name</span>
            <input
              required
              placeholder="e.g. production-agents"
              value={name}
              onChange={(e) => setName(e.target.value)}
              style={inputStyle}
            />
          </label>
          <label style={{ display: 'flex', flexDirection: 'column', gap: '6px' }}>
            <span style={{ fontSize: '12px', color: t.text2 }}>Scope</span>
            <select value={scope} onChange={(e) => setScope(e.target.value as IngestionKeyScope)} style={inputStyle} aria-label="Scope">
              <option value="ingest">ingest (secret server key)</option>
              <option value="rum">rum (public browser token)</option>
            </select>
          </label>
          <label style={{ display: 'flex', flexDirection: 'column', gap: '6px' }}>
            <span style={{ fontSize: '12px', color: t.text2 }}>Tier</span>
            <select value={tier} onChange={(e) => setTier(e.target.value)} style={inputStyle} aria-label="Tier">
              {TIERS.map((tr) => <option key={tr} value={tr}>{tr}</option>)}
            </select>
          </label>
          <button type="submit" disabled={!name.trim() || creating} style={{ ...primaryBtnStyle, opacity: !name.trim() || creating ? 0.5 : 1, cursor: !name.trim() || creating ? 'not-allowed' : 'pointer' }}>
            {creating ? 'Generating…' : 'Generate'}
          </button>
        </form>
      )}

      <StateBoundary
        loading={keys.loading}
        error={keys.error}
        empty={list.length === 0}
        onRetry={keys.refetch}
        loadingLabel="Loading keys…"
        emptyLabel="No ingestion keys yet. Generate one to start sending telemetry."
      >
        <table style={{ width: '100%', borderCollapse: 'collapse' }}>
          <thead>
            <tr style={{ borderBottom: '1px solid ' + t.panelBorder, textAlign: 'left' }}>
              {['Name', 'Key', 'Scope', 'Tier', 'Status', 'Last used', 'Actions'].map((h) => (
                <th key={h} style={{ padding: '10px 8px', fontWeight: 600, color: t.text2, fontSize: '12px' }}>{h}</th>
              ))}
            </tr>
          </thead>
          <tbody>
            {list.map((k) => {
              const st = keyState(k, now);
              const isActive = st.kind === 'active' || st.kind === 'grace';
              const statusColor = st.kind === 'active' ? t.green : st.kind === 'grace' ? t.accent : t.text2;
              return (
                <tr key={k.id} style={{ borderBottom: '1px solid ' + t.panelBorder }}>
                  <td style={{ padding: '14px 8px', fontWeight: 500, fontSize: '13.5px', color: t.text1, verticalAlign: 'top' }}>
                    {k.name}
                    {k.replaced_by && <div style={{ fontSize: '11px', color: t.text2, marginTop: '3px' }}>→ rotated to a successor key</div>}
                  </td>
                  <td style={{ padding: '14px 8px', fontFamily: 'monospace', color: t.accent, fontSize: '12.5px', verticalAlign: 'top' }}>
                    {k.key_prefix}<span style={{ color: t.text2 }}>…••••</span>
                  </td>
                  <td style={{ padding: '14px 8px', verticalAlign: 'top' }}>
                    {k.scope === 'rum'
                      ? <span style={badge(t.dark ? 'rgba(255,255,255,0.08)' : 'rgba(0,0,0,0.05)', t.text2)}>rum · public</span>
                      : <span style={badge(t.dark ? 'rgba(52,199,126,0.15)' : 'rgba(37,169,107,0.1)', t.green)}>ingest · secret</span>}
                  </td>
                  <td style={{ padding: '14px 8px', color: t.text2, fontSize: '13px', verticalAlign: 'top' }}>{k.tier}</td>
                  <td style={{ padding: '14px 8px', fontSize: '12.5px', color: statusColor, verticalAlign: 'top' }}>{st.label}</td>
                  <td style={{ padding: '14px 8px', color: t.text2, fontSize: '12.5px', verticalAlign: 'top' }}>{fmt(k.last_used_at)}</td>
                  <td style={{ padding: '14px 8px', verticalAlign: 'top' }}>
                    {isActive ? (
                      <div style={{ display: 'flex', gap: '8px', flexWrap: 'wrap' }}>
                        <button onClick={() => { setGraceChoice(DEFAULT_GRACE); setRotating(k); }} style={ghostBtnStyle}>Rotate</button>
                        <button onClick={() => setPendingRevoke(k)} style={ghostRedBtnStyle}>Revoke</button>
                      </div>
                    ) : (
                      <span style={{ color: t.text2, fontSize: '12px' }}>—</span>
                    )}
                  </td>
                </tr>
              );
            })}
          </tbody>
        </table>
      </StateBoundary>

      {/* Rotate: grace-window picker in a ConfirmDialog body. */}
      <ConfirmDialog
        open={rotating !== null}
        busy={rotateBusy}
        title={`Rotate key "${rotating?.name ?? ''}"?`}
        body={
          <div style={{ display: 'flex', flexDirection: 'column', gap: '12px' }}>
            <span>
              A new key is issued immediately. The current key keeps working until the grace
              window below expires, then is revoked automatically — so agents can pick up the
              replacement without downtime.
            </span>
            <label style={{ display: 'flex', flexDirection: 'column', gap: '6px' }}>
              <span style={{ fontSize: '12px', color: t.text2 }}>Old key stays valid for</span>
              <select value={graceChoice} onChange={(e) => setGraceChoice(e.target.value)} style={inputStyle} aria-label="Grace period">
                {GRACE_PRESETS.map((g) => <option key={g.value} value={g.value}>{g.label}</option>)}
              </select>
            </label>
          </div>
        }
        confirmLabel="Rotate key"
        onConfirm={doRotate}
        onCancel={() => { if (!rotateBusy) { setRotating(null); setGraceChoice(DEFAULT_GRACE); } }}
      />

      {/* Revoke: immediate, destructive. */}
      <ConfirmDialog
        open={pendingRevoke !== null}
        danger
        busy={revokeBusy}
        title={`Revoke key "${pendingRevoke?.name ?? ''}"?`}
        body="This takes effect immediately — any agent still using this key will start being rejected. This cannot be undone; issue a new key or rotate instead if you need continuity."
        confirmLabel="Revoke key"
        onConfirm={confirmRevoke}
        onCancel={() => { if (!revokeBusy) setPendingRevoke(null); }}
      />

      {minted && <KeyRevealModal minted={minted} onClose={() => setMinted(null)} />}
    </div>
  );
}

// One-time plaintext reveal. The server returns a key's plaintext exactly once
// (create/rotate) and can never return it again, so this modal is the single
// chance to copy it — hence the explicit copy affordance and warning.
function KeyRevealModal({ minted, onClose }: { minted: MintedIngestionKey; onClose: () => void }) {
  const { tokens: t } = useTheme();
  const toast = useToast();
  const doneRef = useRef<HTMLButtonElement>(null);
  const [copied, setCopied] = useState(false);

  useEffect(() => {
    doneRef.current?.focus();
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') onClose();
    };
    document.addEventListener('keydown', onKey);
    return () => document.removeEventListener('keydown', onKey);
  }, [onClose]);

  const copy = async () => {
    try {
      await navigator.clipboard.writeText(minted.key);
      setCopied(true);
      toast.success('Key copied to clipboard');
    } catch {
      // Clipboard can be unavailable (insecure context / permissions) — the key
      // is still visible for manual copy, so degrade quietly rather than fail.
      toast.info('Copy unavailable — select and copy the key manually');
    }
  };

  const rotated = Boolean(minted.rotated_from);

  return (
    <div
      onClick={onClose}
      style={{ position: 'fixed', inset: 0, zIndex: 10000, background: 'rgba(0,0,0,0.45)', display: 'flex', alignItems: 'center', justifyContent: 'center', padding: '20px' }}
    >
      <div
        role="dialog"
        aria-modal="true"
        aria-labelledby="key-reveal-title"
        onClick={(e) => e.stopPropagation()}
        style={{ width: 'min(94vw, 520px)', background: t.panelBg, border: '1px solid ' + t.panelBorder, borderRadius: '14px', padding: '24px', boxShadow: '0 20px 60px rgba(0,0,0,0.35)', backdropFilter: 'blur(16px)' }}
      >
        <h3 id="key-reveal-title" style={{ margin: '0 0 8px', fontSize: '17px', fontWeight: 700, color: t.text1 }}>
          {rotated ? `Key "${minted.name}" rotated` : `Key "${minted.name}" created`}
        </h3>
        <p style={{ color: t.text2, fontSize: '13px', lineHeight: 1.6, margin: '0 0 16px' }}>
          Copy this key now — <strong style={{ color: t.text1 }}>it cannot be retrieved again</strong>. Store it in your
          agent configuration or secret manager.
        </p>

        <div style={{ display: 'flex', gap: '8px', alignItems: 'stretch', marginBottom: '16px' }}>
          <code style={{ flex: 1, fontFamily: 'monospace', fontSize: '13px', color: t.accent, background: t.dark ? 'rgba(0,0,0,0.3)' : 'rgba(0,0,0,0.05)', padding: '12px 14px', borderRadius: '9px', border: '1px solid ' + t.panelBorder, wordBreak: 'break-all' }}>
            {minted.key}
          </code>
          <button
            onClick={copy}
            style={{ padding: '0 16px', borderRadius: '9px', border: 'none', background: copied ? t.green : `linear-gradient(135deg, ${t.accent}, ${t.accent2})`, color: '#fff', fontWeight: 600, fontSize: '13px', cursor: 'pointer', whiteSpace: 'nowrap' }}
          >
            {copied ? 'Copied ✓' : 'Copy'}
          </button>
        </div>

        {rotated && minted.old_key_valid_until && (
          <div style={{ background: t.dark ? 'rgba(52,199,126,0.1)' : 'rgba(37,169,107,0.07)', color: t.text2, fontSize: '12.5px', lineHeight: 1.6, padding: '12px 14px', borderRadius: '10px', marginBottom: '16px' }}>
            The previous key keeps working until <strong style={{ color: t.text1 }}>{fmt(minted.old_key_valid_until)}</strong>
            {minted.grace_period ? ` (grace ${minted.grace_period})` : ''}, then is revoked automatically. Roll out this
            new key to your agents before then.
          </div>
        )}

        <div style={{ display: 'flex', justifyContent: 'flex-end' }}>
          <button
            ref={doneRef}
            onClick={onClose}
            style={{ padding: '9px 18px', borderRadius: '9px', border: '1px solid ' + t.panelBorder, background: 'transparent', color: t.text1, fontWeight: 600, fontSize: '13px', cursor: 'pointer' }}
          >
            Done
          </button>
        </div>
      </div>
    </div>
  );
}
