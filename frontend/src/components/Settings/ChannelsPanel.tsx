"use client";

// Alert delivery channels (ROAD_TO_100 · F3).
//
// Moves channel config out of notification-service env vars into a per-tenant,
// UI-managed store: add/edit/remove Slack / Email / PagerDuty / Opsgenie /
// generic-webhook channels, and Send a test to verify delivery. Secrets are
// write-only — the API never returns them, so an existing secret is shown as
// "configured" and left blank on edit unless you replace it.

import React, { useState } from 'react';
import { useTheme } from '@/context/ThemeContext';
import { api, ApiError } from '@/lib/api/client';
import type { ChannelType, NotificationChannel } from '@/lib/api/types';
import { useApiResource } from '@/lib/hooks/useApiResource';
import { StateBoundary, ConfirmDialog, useToast } from '@/components/ui';

function errMsg(err: unknown, fallback: string): string {
  return err instanceof ApiError || err instanceof Error ? err.message : fallback;
}

// Per-type config fields. `secret` fields are write-only (never returned).
const FIELDS: Record<ChannelType, { key: string; label: string; secret?: boolean; placeholder?: string }[]> = {
  slack: [{ key: 'webhook_url', label: 'Incoming webhook URL', secret: true, placeholder: 'https://hooks.slack.com/services/…' }],
  email: [
    { key: 'host', label: 'SMTP host', placeholder: 'smtp.example.com' },
    { key: 'port', label: 'Port', placeholder: '587' },
    { key: 'username', label: 'Username' },
    { key: 'password', label: 'Password', secret: true },
    { key: 'from', label: 'From', placeholder: 'alerts@example.com' },
    { key: 'to', label: 'To (comma-separated)', placeholder: 'oncall@example.com' },
  ],
  pagerduty: [{ key: 'routing_key', label: 'Events API v2 routing key', secret: true }],
  opsgenie: [
    { key: 'api_key', label: 'API key', secret: true },
    { key: 'api_url', label: 'API URL (optional)', placeholder: 'https://api.opsgenie.com/v2/alerts' },
  ],
  webhook: [
    { key: 'url', label: 'Endpoint URL', secret: true, placeholder: 'https://example.com/hook' },
    { key: 'secret', label: 'HMAC secret (optional)', secret: true },
  ],
};
const TYPES: ChannelType[] = ['slack', 'email', 'pagerduty', 'opsgenie', 'webhook'];

export function ChannelsPanel() {
  const { tokens: t } = useTheme();
  const toast = useToast();

  const channels = useApiResource<NotificationChannel[]>(
    () => api.getData<NotificationChannel[]>('/api/v1/notification-channels').then((d) => d ?? []),
  );
  const list = channels.data ?? [];

  const [showForm, setShowForm] = useState(false);
  const [editing, setEditing] = useState<NotificationChannel | null>(null);
  const [name, setName] = useState('');
  const [type, setType] = useState<ChannelType>('slack');
  const [config, setConfig] = useState<Record<string, string>>({});
  const [saving, setSaving] = useState(false);
  const [testingId, setTestingId] = useState<string | null>(null);
  const [pendingDelete, setPendingDelete] = useState<NotificationChannel | null>(null);
  const [deleting, setDeleting] = useState(false);

  const resetForm = () => { setEditing(null); setName(''); setType('slack'); setConfig({}); setShowForm(false); };

  const openEdit = (ch: NotificationChannel) => {
    setEditing(ch);
    setName(ch.name);
    setType(ch.type);
    // Secrets aren't returned; start their fields blank (leave blank = keep).
    const cfg: Record<string, string> = {};
    for (const f of FIELDS[ch.type]) if (!f.secret) cfg[f.key] = ch.config[f.key] ?? '';
    setConfig(cfg);
    setShowForm(true);
  };

  const save = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!name.trim() || saving) return;
    setSaving(true);
    try {
      if (editing) {
        await api.put(`/api/v1/notification-channels/${encodeURIComponent(editing.id)}`, { name: name.trim(), enabled: editing.enabled, config });
        toast.success(`Channel "${name.trim()}" updated`);
      } else {
        await api.post('/api/v1/notification-channels', { name: name.trim(), type, config, enabled: true });
        toast.success(`Channel "${name.trim()}" created`);
      }
      resetForm();
      await channels.refetch();
    } catch (err) {
      toast.error(errMsg(err, 'request failed'));
    } finally {
      setSaving(false);
    }
  };

  const sendTest = async (ch: NotificationChannel) => {
    setTestingId(ch.id);
    try {
      await api.post(`/api/v1/notification-channels/${encodeURIComponent(ch.id)}/test`);
      toast.success(`Test sent to "${ch.name}"`);
    } catch (err) {
      toast.error(`Test failed: ${errMsg(err, 'delivery failed')}`);
    } finally {
      setTestingId(null);
    }
  };

  const toggleEnabled = async (ch: NotificationChannel) => {
    try {
      await api.put(`/api/v1/notification-channels/${encodeURIComponent(ch.id)}`, { name: ch.name, enabled: !ch.enabled, config: {} });
      await channels.refetch();
    } catch (err) {
      toast.error(errMsg(err, 'failed to update channel'));
    }
  };

  const confirmDelete = async () => {
    if (!pendingDelete) return;
    setDeleting(true);
    try {
      await api.del(`/api/v1/notification-channels/${encodeURIComponent(pendingDelete.id)}`);
      toast.success(`Channel "${pendingDelete.name}" deleted`);
      setPendingDelete(null);
      await channels.refetch();
    } catch (err) {
      toast.error(errMsg(err, 'failed to delete channel'));
    } finally {
      setDeleting(false);
    }
  };

  const primaryBtn: React.CSSProperties = { padding: '10px 18px', borderRadius: '10px', border: 'none', background: `linear-gradient(135deg, ${t.accent}, ${t.accent2})`, color: '#fff', fontWeight: 600, fontSize: '13px', cursor: 'pointer', flexShrink: 0 };
  const input: React.CSSProperties = { padding: '10px 12px', background: t.dark ? 'rgba(255,255,255,0.05)' : 'rgba(255,255,255,0.7)', border: '1px solid ' + t.panelBorder, borderRadius: '8px', color: t.text1, width: '100%', boxSizing: 'border-box' };
  const ghost: React.CSSProperties = { padding: '6px 12px', fontSize: '12px', borderRadius: '8px', border: '1px solid ' + t.panelBorder, background: 'transparent', color: t.text1, cursor: 'pointer' };
  const ghostRed: React.CSSProperties = { ...ghost, border: '1px solid ' + t.red, color: t.red };

  return (
    <div>
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'flex-start', marginBottom: '20px', gap: '16px', flexWrap: 'wrap' }}>
        <div>
          <h3 style={{ fontSize: '16px', fontWeight: 700, margin: '0 0 8px', color: t.text1 }}>Alert Channels</h3>
          <p style={{ color: t.text2, fontSize: '13px', maxWidth: '560px', lineHeight: 1.6 }}>
            Where matched alerts are delivered for this tenant. Add a channel, send a test to verify
            it, and enable/disable without redeploying. Secrets are stored encrypted and never shown again.
          </p>
        </div>
        <button style={primaryBtn} onClick={() => (showForm ? resetForm() : setShowForm(true))}>{showForm ? 'Cancel' : '+ Add Channel'}</button>
      </div>

      {showForm && (
        <form onSubmit={save} style={{ background: t.dark ? 'rgba(0,0,0,0.2)' : 'rgba(0,0,0,0.03)', padding: '20px', borderRadius: '12px', border: '1px solid ' + t.panelBorder, marginBottom: '24px', display: 'flex', flexDirection: 'column', gap: '12px', maxWidth: '520px' }}>
          <div style={{ display: 'flex', gap: '12px', flexWrap: 'wrap' }}>
            <label style={{ flex: 1, minWidth: '160px', display: 'flex', flexDirection: 'column', gap: '6px' }}>
              <span style={{ fontSize: '12px', color: t.text2 }}>Name</span>
              <input required placeholder="e.g. oncall-slack" value={name} onChange={(e) => setName(e.target.value)} style={input} />
            </label>
            <label style={{ display: 'flex', flexDirection: 'column', gap: '6px' }}>
              <span style={{ fontSize: '12px', color: t.text2 }}>Type</span>
              <select value={type} onChange={(e) => { setType(e.target.value as ChannelType); setConfig({}); }} disabled={!!editing} style={{ ...input, width: 'auto' }} aria-label="Channel type">
                {TYPES.map((ty) => <option key={ty} value={ty}>{ty}</option>)}
              </select>
            </label>
          </div>
          {FIELDS[type].map((f) => {
            const isSet = editing?.config[`${f.key}_set`] === 'true';
            return (
              <label key={f.key} style={{ display: 'flex', flexDirection: 'column', gap: '6px' }}>
                <span style={{ fontSize: '12px', color: t.text2 }}>
                  {f.label}{f.secret && isSet ? ' — configured (leave blank to keep)' : ''}
                </span>
                <input
                  type={f.secret ? 'password' : 'text'}
                  value={config[f.key] ?? ''}
                  placeholder={f.placeholder}
                  onChange={(e) => setConfig((c) => ({ ...c, [f.key]: e.target.value }))}
                  style={input}
                />
              </label>
            );
          })}
          <button type="submit" disabled={!name.trim() || saving} style={{ ...primaryBtn, alignSelf: 'flex-start', opacity: !name.trim() || saving ? 0.5 : 1 }}>
            {saving ? 'Saving…' : editing ? 'Save changes' : 'Create channel'}
          </button>
        </form>
      )}

      <StateBoundary
        loading={channels.loading}
        error={channels.error}
        empty={list.length === 0}
        onRetry={channels.refetch}
        loadingLabel="Loading channels…"
        emptyLabel="No delivery channels configured yet. Add one to route alerts to Slack, email, PagerDuty, Opsgenie, or a webhook."
      >
        <div style={{ display: 'flex', flexDirection: 'column', gap: '10px' }}>
          {list.map((ch) => (
            <div key={ch.id} style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', gap: '12px', flexWrap: 'wrap', background: t.panelBg, border: '1px solid ' + t.panelBorder, borderRadius: '12px', padding: '14px 16px', opacity: ch.enabled ? 1 : 0.6 }}>
              <div>
                <div style={{ fontWeight: 600, color: t.text1, fontSize: '13.5px' }}>
                  {ch.name} <span style={{ fontSize: '11px', color: t.text2, fontWeight: 400 }}>· {ch.type}</span>
                  {!ch.enabled && <span style={{ fontSize: '11px', color: t.text2 }}> · disabled</span>}
                </div>
              </div>
              <div style={{ display: 'flex', gap: '8px', flexWrap: 'wrap' }}>
                <button style={ghost} onClick={() => sendTest(ch)} disabled={testingId === ch.id}>{testingId === ch.id ? 'Sending…' : 'Send test'}</button>
                <button style={ghost} onClick={() => toggleEnabled(ch)}>{ch.enabled ? 'Disable' : 'Enable'}</button>
                <button style={ghost} onClick={() => openEdit(ch)}>Edit</button>
                <button style={ghostRed} onClick={() => setPendingDelete(ch)}>Delete</button>
              </div>
            </div>
          ))}
        </div>
      </StateBoundary>

      <ConfirmDialog
        open={pendingDelete !== null}
        danger
        busy={deleting}
        title={`Delete channel "${pendingDelete?.name ?? ''}"?`}
        body="Alerts will stop being delivered to this destination. This cannot be undone."
        confirmLabel="Delete channel"
        onConfirm={confirmDelete}
        onCancel={() => setPendingDelete(null)}
      />
    </div>
  );
}
