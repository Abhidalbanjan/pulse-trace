"use client";

import React, { useState } from 'react';
import { useTheme } from '@/context/ThemeContext';
import { api, ApiError } from '@/lib/api/client';
import type { Role } from '@/lib/api/types';
import { useApiResource } from '@/lib/hooks/useApiResource';
import { StateBoundary, ConfirmDialog, useToast } from '@/components/ui';

// Known resource types (from actual gateway routes) - a helper list, not an
// enforced constraint: the backend derives resourceType from the URL path
// segment, so any string here is exactly what a "<resource>:<action>" grant
// needs to match.
const KNOWN_RESOURCES = [
  'incidents', 'alerts', 'logs', 'errors', 'deployments', 'services',
  'topology', 'search', 'slo', 'synthetics', 'analytics', 'profiler', 'rum',
  'admin', 'settings',
];
const ACTIONS = ['read', 'write', '*'];

function errMsg(err: unknown, fallback: string): string {
  return err instanceof ApiError || err instanceof Error ? err.message : fallback;
}

export function RolesPanel() {
  const { tokens: t } = useTheme();
  const toast = useToast();

  // Typed data + loading/error/refetch via the shared hook — no more
  // useState<any[]> + hand-rolled fetch/loading/error.
  const roles = useApiResource<Role[]>(
    () => api.getData<Role[]>('/api/v1/admin/roles').then((d) => d ?? []),
  );
  const list = roles.data ?? [];

  const [showForm, setShowForm] = useState(false);
  const [name, setName] = useState('');
  const [description, setDescription] = useState('');
  const [permissions, setPermissions] = useState<string[]>([]);
  const [resourcePick, setResourcePick] = useState(KNOWN_RESOURCES[0]);
  const [actionPick, setActionPick] = useState('read');
  const [customPerm, setCustomPerm] = useState('');

  // Destructive delete goes through an accessible confirm dialog, not window.confirm.
  const [pendingDelete, setPendingDelete] = useState<string | null>(null);
  const [deleting, setDeleting] = useState(false);

  const addPermission = (perm: string) => {
    if (perm && !permissions.includes(perm)) setPermissions((p) => [...p, perm]);
  };
  const removePermission = (perm: string) => setPermissions((p) => p.filter((x) => x !== perm));

  const resetForm = () => {
    setName(''); setDescription(''); setPermissions([]); setCustomPerm('');
  };

  const createRole = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!name.trim() || permissions.length === 0) return;
    try {
      await api.post('/api/v1/admin/roles', {
        name: name.trim(),
        description: description.trim(),
        permissions,
      });
      resetForm();
      setShowForm(false);
      toast.success(`Role "${name.trim()}" created`);
      await roles.refetch();
    } catch (err) {
      toast.error(`Error creating role: ${errMsg(err, 'request failed')}`);
    }
  };

  const confirmDelete = async () => {
    if (!pendingDelete) return;
    setDeleting(true);
    try {
      await api.del(`/api/v1/admin/roles/${encodeURIComponent(pendingDelete)}`);
      toast.success(`Role "${pendingDelete}" deleted`);
      setPendingDelete(null);
      await roles.refetch();
    } catch (err) {
      toast.error(`Error deleting role: ${errMsg(err, 'request failed')}`);
    } finally {
      setDeleting(false);
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
  const secondaryBtnStyle: React.CSSProperties = {
    padding: '8px 14px', borderRadius: '8px', border: '1px solid ' + t.panelBorder,
    background: 'transparent', color: t.text1, cursor: 'pointer', fontSize: '13px',
  };
  const ghostRedBtnStyle: React.CSSProperties = {
    padding: '6px 12px', fontSize: '12px', borderRadius: '8px', border: '1px solid ' + t.red,
    background: 'transparent', color: t.red, cursor: 'pointer',
  };

  return (
    <div>
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'flex-start', marginBottom: '28px', gap: '16px', flexWrap: 'wrap' }}>
        <div>
          <h3 style={{ fontSize: '19px', fontWeight: 700, margin: '0 0 8px', color: t.text1 }}>Roles</h3>
          <p style={{ color: t.text2, fontSize: '13.5px', maxWidth: '520px', lineHeight: 1.6 }}>
            Resource-scoped RBAC grants like <code>incidents:write</code>, not a blanket write. Stored centrally, takes effect within seconds.
          </p>
        </div>
        <button onClick={() => setShowForm((v) => !v)} style={primaryBtnStyle}>
          {showForm ? 'Cancel' : '+ New Role'}
        </button>
      </div>

      {showForm && (
        <form onSubmit={createRole} style={{ background: t.dark ? 'rgba(0,0,0,0.2)' : 'rgba(0,0,0,0.03)', padding: '20px', borderRadius: '12px', border: '1px solid ' + t.panelBorder, marginBottom: '24px', display: 'flex', flexDirection: 'column', gap: '14px' }}>
          <div style={{ display: 'flex', gap: '12px', flexWrap: 'wrap' }}>
            <input
              required
              placeholder="Role name (e.g. support)"
              value={name}
              onChange={(e) => setName(e.target.value)}
              style={{ ...inputStyle, flex: 1, minWidth: '160px' }}
            />
            <input
              placeholder="Description"
              value={description}
              onChange={(e) => setDescription(e.target.value)}
              style={{ ...inputStyle, flex: 2, minWidth: '200px' }}
            />
          </div>

          <div>
            <div style={{ fontSize: '13px', color: t.text2, marginBottom: '8px' }}>Grant a permission</div>
            <div style={{ display: 'flex', gap: '10px', alignItems: 'center', flexWrap: 'wrap' }}>
              <select value={resourcePick} onChange={(e) => setResourcePick(e.target.value)} style={inputStyle} aria-label="Resource">
                {KNOWN_RESOURCES.map((r) => <option key={r} value={r}>{r}</option>)}
              </select>
              <span style={{ color: t.text2 }}>:</span>
              <select value={actionPick} onChange={(e) => setActionPick(e.target.value)} style={inputStyle} aria-label="Action">
                {ACTIONS.map((a) => <option key={a} value={a}>{a}</option>)}
              </select>
              <button type="button" style={secondaryBtnStyle} onClick={() => addPermission(`${resourcePick}:${actionPick}`)}>
                + Add
              </button>
              <span style={{ color: t.text2, fontSize: '12px' }}>or</span>
              <input
                placeholder="custom, e.g. *:read"
                value={customPerm}
                onChange={(e) => setCustomPerm(e.target.value)}
                style={{ ...inputStyle, width: '160px' }}
                aria-label="Custom permission"
              />
              <button type="button" style={secondaryBtnStyle} onClick={() => { addPermission(customPerm.trim()); setCustomPerm(''); }}>
                + Add Custom
              </button>
            </div>
          </div>

          {permissions.length > 0 && (
            <div style={{ display: 'flex', gap: '8px', flexWrap: 'wrap' }}>
              {permissions.map((p) => (
                <span key={p} style={{ display: 'flex', alignItems: 'center', gap: '6px', background: t.dark ? 'rgba(255,255,255,0.1)' : 'rgba(0,0,0,0.06)', padding: '4px 10px', borderRadius: '128px', fontSize: '12px', color: t.text1 }}>
                  {p}
                  <button type="button" onClick={() => removePermission(p)} aria-label={`Remove ${p}`} style={{ background: 'none', border: 'none', color: t.red, cursor: 'pointer', padding: 0, fontSize: '14px', lineHeight: 1 }}>×</button>
                </span>
              ))}
            </div>
          )}

          <button type="submit" disabled={permissions.length === 0} style={{ ...primaryBtnStyle, alignSelf: 'flex-start', opacity: permissions.length === 0 ? 0.5 : 1, cursor: permissions.length === 0 ? 'not-allowed' : 'pointer' }}>
            Create Role
          </button>
        </form>
      )}

      <StateBoundary
        loading={roles.loading}
        error={roles.error}
        empty={list.length === 0}
        onRetry={roles.refetch}
        loadingLabel="Loading roles…"
        emptyLabel="No roles defined yet."
      >
        <table style={{ width: '100%', borderCollapse: 'collapse' }}>
          <thead>
            <tr style={{ borderBottom: '1px solid ' + t.panelBorder, textAlign: 'left' }}>
              <th style={{ padding: '10px 8px', fontWeight: 600, color: t.text2, fontSize: '12px' }}>Role</th>
              <th style={{ padding: '10px 8px', fontWeight: 600, color: t.text2, fontSize: '12px' }}>Permissions</th>
              <th style={{ padding: '10px 8px', fontWeight: 600, color: t.text2, fontSize: '12px' }}>Description</th>
              <th style={{ padding: '10px 8px', fontWeight: 600, color: t.text2, fontSize: '12px' }}>Actions</th>
            </tr>
          </thead>
          <tbody>
            {list.map((role) => (
              <tr key={role.name} style={{ borderBottom: '1px solid ' + t.panelBorder }}>
                <td style={{ padding: '14px 8px', fontWeight: 500, fontSize: '13.5px', verticalAlign: 'top', color: t.text1 }}>
                  {role.name} {role.is_system && <span style={{ fontSize: '12px', color: t.text2 }}> (built-in)</span>}
                </td>
                <td style={{ padding: '14px 8px', verticalAlign: 'top' }}>
                  <div style={{ display: 'flex', gap: '6px', flexWrap: 'wrap' }}>
                    {role.permissions.map((p) => (
                      <span key={p} style={{ background: t.dark ? 'rgba(255,255,255,0.08)' : 'rgba(0,0,0,0.05)', padding: '3px 9px', borderRadius: '100px', fontSize: '11px', fontFamily: 'monospace', color: t.text1 }}>{p}</span>
                    ))}
                  </div>
                </td>
                <td style={{ padding: '14px 8px', color: t.text2, fontSize: '13px', verticalAlign: 'top' }}>{role.description}</td>
                <td style={{ padding: '14px 8px', verticalAlign: 'top' }}>
                  {!role.is_system && (
                    <button onClick={() => setPendingDelete(role.name)} style={ghostRedBtnStyle}>Delete</button>
                  )}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </StateBoundary>

      <ConfirmDialog
        open={pendingDelete !== null}
        danger
        busy={deleting}
        title={`Delete role "${pendingDelete ?? ''}"?`}
        body="Users assigned to it will keep the role name but it will no longer resolve any permissions."
        confirmLabel="Delete role"
        onConfirm={confirmDelete}
        onCancel={() => setPendingDelete(null)}
      />
    </div>
  );
}
