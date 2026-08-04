"use client";

// Tenant data deletion (ROAD_TO_100 · F19).
//
// GDPR/SOC2 "delete my data": the gateway can purge a tenant's telemetry across
// every store (ClickHouse / Quickwit / Neo4j / derived Postgres) and, for a full
// offboarding, close the account too — but there was no UI. This panel drives
// both, behind a type-your-tenant-id confirmation (matching the server's
// `{confirm:"<tenant>"}` contract), and renders the per-store result as a
// deletion certificate.

import React, { useEffect, useRef, useState } from 'react';
import { useTheme } from '@/context/ThemeContext';
import { api, ApiError } from '@/lib/api/client';
import type { TenantPurgeResult } from '@/lib/api/types';
import { useToast } from '@/components/ui';

function errMsg(err: unknown, fallback: string): string {
  return err instanceof ApiError || err instanceof Error ? err.message : fallback;
}

type Action = 'purge' | 'close';

export function DataPrivacyPanel() {
  const { tokens: t } = useTheme();
  const toast = useToast();

  const [action, setAction] = useState<Action | null>(null);
  const [busy, setBusy] = useState(false);
  const [result, setResult] = useState<TenantPurgeResult | null>(null);

  const run = async (which: Action, confirm: string) => {
    setBusy(true);
    try {
      const path = which === 'purge' ? '/api/v1/admin/tenant/purge-data' : '/api/v1/admin/tenant/close';
      // Non-enveloped: the purger writes its Result struct directly.
      const res = await api.post<TenantPurgeResult>(path, { confirm });
      setResult(res);
      setAction(null);
      if (res.errors && res.errors.length > 0) {
        toast.error(`${which === 'purge' ? 'Purge' : 'Close'} completed with ${res.errors.length} error(s)`);
      } else {
        toast.success(which === 'purge' ? 'Telemetry data purged' : 'Account closed and data purged');
      }
    } catch (err) {
      toast.error(errMsg(err, 'request failed'));
    } finally {
      setBusy(false);
    }
  };

  const card: React.CSSProperties = {
    background: t.dark ? 'rgba(255,60,60,0.06)' : 'rgba(255,60,60,0.04)',
    border: '1px solid ' + t.red + '44',
    borderRadius: '14px',
    padding: '20px',
    marginBottom: '16px',
  };
  const dangerBtn: React.CSSProperties = {
    padding: '10px 16px', borderRadius: '10px', border: '1px solid ' + t.red,
    background: 'transparent', color: t.red, fontWeight: 600, fontSize: '13px', cursor: 'pointer',
  };

  return (
    <div>
      <h3 style={{ fontSize: '19px', fontWeight: 700, margin: '0 0 8px', color: t.text1 }}>Data &amp; Privacy</h3>
      <p style={{ color: t.text2, fontSize: '13.5px', maxWidth: '560px', lineHeight: 1.6, marginBottom: '24px' }}>
        Erase this tenant&apos;s data for a data-subject request (GDPR) or compliance offboarding.
        These actions are irreversible and delete data across every store — telemetry, logs, traces,
        metrics, RUM, and the service topology.
      </p>

      <div style={card}>
        <div style={{ fontWeight: 700, color: t.text1, marginBottom: '6px' }}>Purge telemetry data</div>
        <p style={{ color: t.text2, fontSize: '13px', lineHeight: 1.6, margin: '0 0 14px' }}>
          Permanently deletes all telemetry for this tenant (logs, traces, metrics, RUM, synthetics,
          topology). <strong style={{ color: t.text1 }}>Your account, users, and keys are kept.</strong>
        </p>
        <button style={dangerBtn} onClick={() => setAction('purge')}>Purge data…</button>
      </div>

      <div style={card}>
        <div style={{ fontWeight: 700, color: t.text1, marginBottom: '6px' }}>Close account</div>
        <p style={{ color: t.text2, fontSize: '13px', lineHeight: 1.6, margin: '0 0 14px' }}>
          Full offboarding: purges all telemetry <strong style={{ color: t.text1 }}>and</strong> deletes
          the account itself — users, ingestion keys, alert rules, and the tenant record.
        </p>
        <button style={{ ...dangerBtn, background: t.red, color: '#fff', border: 'none' }} onClick={() => setAction('close')}>Close account…</button>
      </div>

      {result && (
        <div style={{ marginTop: '20px', background: t.panelBg, border: '1px solid ' + t.panelBorder, borderRadius: '12px', padding: '16px' }}>
          <div style={{ fontWeight: 700, color: t.text1, marginBottom: '10px' }}>
            Deletion certificate — tenant <span style={{ fontFamily: 'monospace' }}>{result.tenant_id}</span>
            {result.full ? ' (account closed)' : ' (telemetry purged)'}
          </div>
          <div style={{ display: 'flex', flexDirection: 'column', gap: '5px' }}>
            {result.steps.map((s, i) => (
              <div key={`s${i}`} style={{ fontSize: '12.5px', color: t.green }}>✓ {s}</div>
            ))}
            {result.errors.map((e, i) => (
              <div key={`e${i}`} style={{ fontSize: '12.5px', color: t.red }}>✗ {e}</div>
            ))}
            {result.steps.length === 0 && result.errors.length === 0 && (
              <div style={{ fontSize: '12.5px', color: t.text2 }}>No stores reported changes.</div>
            )}
          </div>
        </div>
      )}

      {action !== null && (
        <TypeToConfirmModal
          busy={busy}
          danger
          title={action === 'close' ? 'Close this account?' : 'Purge all telemetry data?'}
          body={
            action === 'close'
              ? 'This deletes ALL telemetry and the account itself (users, keys, alert rules). This cannot be undone.'
              : 'This permanently deletes all telemetry for this tenant. Your account is kept. This cannot be undone.'
          }
          confirmLabel={action === 'close' ? 'Close account' : 'Purge data'}
          onConfirm={(value) => run(action, value)}
          onCancel={() => setAction(null)}
        />
      )}
    </div>
  );
}

// TypeToConfirmModal gates a destructive action behind typing the tenant id,
// exactly what the server requires ({confirm:"<tenant id>"}). Accessible:
// role=dialog, aria-modal, focus management, Escape to cancel.
function TypeToConfirmModal({
  busy, danger, title, body, confirmLabel, onConfirm, onCancel,
}: {
  busy: boolean;
  danger?: boolean;
  title: string;
  body: string;
  confirmLabel: string;
  onConfirm: (confirmValue: string) => void;
  onCancel: () => void;
}) {
  const { tokens: t } = useTheme();
  const inputRef = useRef<HTMLInputElement>(null);
  // Mounted only while open (parent gates), so state initializes fresh each time.
  const [value, setValue] = useState('');

  useEffect(() => {
    inputRef.current?.focus();
    const onKey = (e: KeyboardEvent) => { if (e.key === 'Escape' && !busy) onCancel(); };
    document.addEventListener('keydown', onKey);
    return () => document.removeEventListener('keydown', onKey);
  }, [busy, onCancel]);

  const accent = danger ? t.red : t.accent;
  const canConfirm = value.trim().length > 0 && !busy;

  return (
    <div onClick={() => !busy && onCancel()} style={{ position: 'fixed', inset: 0, zIndex: 10000, background: 'rgba(0,0,0,0.45)', display: 'flex', alignItems: 'center', justifyContent: 'center', padding: '20px' }}>
      <div role="dialog" aria-modal="true" aria-labelledby="ttc-title" onClick={(e) => e.stopPropagation()} style={{ width: 'min(92vw, 460px)', background: t.panelBg, border: '1px solid ' + t.panelBorder, borderRadius: '14px', padding: '24px', boxShadow: '0 20px 60px rgba(0,0,0,0.35)', backdropFilter: 'blur(16px)' }}>
        <h3 id="ttc-title" style={{ margin: '0 0 10px', fontSize: '17px', fontWeight: 700, color: t.text1 }}>{title}</h3>
        <p style={{ color: t.text2, fontSize: '13.5px', lineHeight: 1.6, margin: '0 0 16px' }}>{body}</p>
        <label style={{ display: 'block', fontSize: '12px', color: t.text2, marginBottom: '6px' }}>
          Type your <strong style={{ color: t.text1 }}>tenant ID</strong> to confirm
        </label>
        <input
          ref={inputRef}
          value={value}
          onChange={(e) => setValue(e.target.value)}
          onKeyDown={(e) => { if (e.key === 'Enter' && canConfirm) onConfirm(value.trim()); }}
          placeholder="tenant id"
          aria-label="Tenant ID confirmation"
          style={{ width: '100%', padding: '10px 12px', background: t.dark ? 'rgba(255,255,255,0.05)' : 'rgba(255,255,255,0.7)', border: '1px solid ' + t.panelBorder, borderRadius: '8px', color: t.text1, marginBottom: '20px', boxSizing: 'border-box' }}
        />
        <div style={{ display: 'flex', justifyContent: 'flex-end', gap: '10px' }}>
          <button onClick={onCancel} disabled={busy} style={{ padding: '9px 16px', borderRadius: '9px', border: '1px solid ' + t.panelBorder, background: 'transparent', color: t.text1, cursor: busy ? 'not-allowed' : 'pointer', fontSize: '13px' }}>Cancel</button>
          <button onClick={() => canConfirm && onConfirm(value.trim())} disabled={!canConfirm} style={{ padding: '9px 18px', borderRadius: '9px', border: 'none', background: accent, color: '#fff', fontWeight: 600, fontSize: '13px', cursor: canConfirm ? 'pointer' : 'not-allowed', opacity: canConfirm ? 1 : 0.5 }}>
            {busy ? 'Working…' : confirmLabel}
          </button>
        </div>
      </div>
    </div>
  );
}
