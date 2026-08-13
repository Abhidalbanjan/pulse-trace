"use client";

import React, { useState, useEffect } from 'react';
import { errMessage } from '@/lib/errMessage';
import { fetchWithAuth } from '@/lib/api';
import { RolesPanel } from './RolesPanel';
import { PoliciesPanel } from './PoliciesPanel';
import { AuditLogPanel } from './AuditLogPanel';
import { SecurityPanel } from './SecurityPanel';
import { RateLimitsPanel } from './RateLimitsPanel';
import { AlertRulesPanel } from './AlertRulesPanel';
import { BillingPanel } from './BillingPanel';
import { IngestionKeysPanel } from './IngestionKeysPanel';
import { DataPrivacyPanel } from './DataPrivacyPanel';
import { ChannelsPanel } from './ChannelsPanel';
import { AnomalyConfigPanel } from './AnomalyConfigPanel';
import { UsagePanel } from './UsagePanel';
import { useTheme } from '@/context/ThemeContext';

interface User { id: string; username: string; role: string; created_at?: string; }

export function SettingsView() {
  const { tokens: t } = useTheme();
  const [activeTab, setActiveTab] = useState('users');
  const [ssoClientId, setSsoClientId] = useState<string | null>(null);
  const [users, setUsers] = useState<User[]>([]);
  const [loadingUsers, setLoadingUsers] = useState(false);
  const [showInviteModal, setShowInviteModal] = useState(false);
  const [inviteForm, setInviteForm] = useState({ username: '', password: '', role: 'viewer' });
  const [availableRoles, setAvailableRoles] = useState<string[]>(['viewer', 'admin']);
  const [token, setToken] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    // Check URL for SSO token
    const urlParams = new URLSearchParams(window.location.search);
    const ssoToken = urlParams.get('token');
    if (ssoToken) {
      localStorage.setItem('pulse_token', ssoToken);
      window.history.replaceState({}, document.title, window.location.pathname);
      // eslint-disable-next-line react-hooks/set-state-in-effect -- intentional one-shot fetch/hydration on mount; effect is the right place to sync from the API/localStorage
      setToken(ssoToken);
    } else {
      setToken(localStorage.getItem('pulse_token'));
    }
  }, []);

  useEffect(() => {
    if (activeTab === 'sso') {
      fetchWithAuth('/api/v1/auth/sso/config')
        .then(async res => {
          if (!res.ok) throw new Error(await res.text());
          return res.json();
        })
        .then(data => {
          if (data && data.client_id) {
            setSsoClientId(data.client_id);
          }
        })
        .catch(err => console.error("Failed to fetch SSO config:", errMessage(err) || err));
    }
  }, [activeTab]);

  const fetchUsers = () => {
    setLoadingUsers(true);
    fetchWithAuth('/api/v1/admin/users')
      .then(async res => {
        if (!res.ok) throw new Error(await res.text());
        return res.json();
      })
      .then(data => {
        if (data.data && Array.isArray(data.data)) {
          setUsers(data.data);
        } else if (Array.isArray(data)) {
          setUsers(data);
        }
        setLoadingUsers(false);
      })
      .catch(err => {
        setError(errMessage(err) || err.toString());
        setLoadingUsers(false);
      });
  };

  const fetchAvailableRoles = () => {
    fetchWithAuth('/api/v1/admin/roles')
      .then(async res => {
        if (!res.ok) throw new Error(await res.text());
        return res.json();
      })
      .then(data => {
        const names = (data.data || []).map((r: { name: string }) => r.name);
        if (names.length > 0) setAvailableRoles(names);
      })
      .catch(err => console.error('Failed to load roles for invite form:', err));
  };

  useEffect(() => {
    if (activeTab === 'users' && token) {
      // eslint-disable-next-line react-hooks/set-state-in-effect -- intentional one-shot fetch/hydration on mount; effect is the right place to sync from the API/localStorage
      fetchUsers();
      fetchAvailableRoles();
    }
  }, [activeTab, token]);

  const handleInvite = async (e: React.FormEvent) => {
    e.preventDefault();
    try {
      const res = await fetchWithAuth('/api/v1/admin/users', {
        method: 'POST',
        body: JSON.stringify(inviteForm)
      });
      if (!res.ok) throw new Error(await res.text());
      setShowInviteModal(false);
      setInviteForm({ username: '', password: '', role: 'viewer' });
      fetchUsers();
    } catch (err) {
      alert(`Error inviting user: ${errMessage(err)}`);
    }
  };

  const handleRevoke = async (id: string, username: string) => {
    if (username === 'admin') {
      alert("Cannot revoke the root admin user.");
      return;
    }
    if (!confirm(`Are you sure you want to revoke access for ${username}?`)) return;

    try {
      const res = await fetchWithAuth(`/api/v1/admin/users?id=${id}`, { method: 'DELETE' });
      if (!res.ok) throw new Error(await res.text());
      fetchUsers();
    } catch (err) {
      alert(`Error revoking user: ${errMessage(err)}`);
    }
  };

  // Inline role change (PUT /api/v1/admin/users/role?id=<id>). Optimistic, with
  // revert on failure — the server enforces that the role exists and is tenant-scoped.
  const updateUserRole = async (id: string, role: string) => {
    const previous = users;
    setUsers((us) => us.map((u) => (u.id === id ? { ...u, role } : u)));
    setError(null);
    try {
      const res = await fetchWithAuth(`/api/v1/admin/users/role?id=${encodeURIComponent(id)}`, {
        method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ role }),
      });
      if (!res.ok) throw new Error((await res.text()) || 'Failed to update role');
    } catch (err) {
      setUsers(previous); // revert the optimistic change
      setError(errMessage(err, 'Failed to update role'));
    }
  };

  const primaryBtnStyle: React.CSSProperties = {
    padding: '10px 18px',
    borderRadius: '10px',
    border: 'none',
    background: `linear-gradient(135deg, ${t.accent}, ${t.accent2})`,
    color: '#fff',
    fontWeight: 600,
    fontSize: '13px',
    flexShrink: 0,
    cursor: 'pointer',
  };

  const ghostRedBtnStyle: React.CSSProperties = {
    padding: '6px 12px',
    fontSize: '12px',
    borderRadius: '8px',
    border: '1px solid ' + t.red,
    background: 'transparent',
    color: t.red,
    cursor: 'pointer',
  };

  const inputStyle: React.CSSProperties = {
    width: '100%',
    padding: '10px 12px',
    background: t.dark ? 'rgba(255,255,255,0.05)' : 'rgba(255,255,255,0.7)',
    border: '1px solid ' + t.panelBorder,
    borderRadius: '8px',
    color: t.text1,
  };

  return (
    <div style={{ display: 'flex', gap: '28px', minWidth: 0, padding: '40px', maxWidth: '1200px', margin: '0 auto', width: '100%', height: '100%', position: 'relative' }}>

      {/* Invite Modal */}
      {showInviteModal && (
        <div style={{ position: 'fixed', top: 0, left: 0, right: 0, bottom: 0, background: 'rgba(0,0,0,0.6)', zIndex: 1000, display: 'flex', alignItems: 'center', justifyContent: 'center', backdropFilter: 'blur(4px)' }}>
          <div style={{ background: t.panelBg, backdropFilter: 'blur(30px) saturate(180%)', padding: '32px', borderRadius: '20px', width: '400px', border: '1px solid ' + t.panelBorder, boxShadow: t.shadow }}>
            <h3 style={{ fontSize: '19px', fontWeight: 700, margin: '0 0 24px', color: t.text1 }}>Invite New User</h3>
            <form onSubmit={handleInvite} style={{ display: 'flex', flexDirection: 'column', gap: '16px' }}>
              <div>
                <label style={{ display: 'block', fontSize: '13px', color: t.text2, marginBottom: '8px' }}>Email / Username</label>
                <input type="text" required value={inviteForm.username} onChange={e => setInviteForm({...inviteForm, username: e.target.value})} style={inputStyle} />
              </div>
              <div>
                <label style={{ display: 'block', fontSize: '13px', color: t.text2, marginBottom: '8px' }}>Temporary Password</label>
                <input type="password" required value={inviteForm.password} onChange={e => setInviteForm({...inviteForm, password: e.target.value})} style={inputStyle} />
              </div>
              <div>
                <label style={{ display: 'block', fontSize: '13px', color: t.text2, marginBottom: '8px' }}>Role</label>
                <select value={inviteForm.role} onChange={e => setInviteForm({...inviteForm, role: e.target.value})} style={inputStyle}>
                  {availableRoles.map(r => (
                    <option key={r} value={r}>{r.charAt(0).toUpperCase() + r.slice(1)}</option>
                  ))}
                </select>
              </div>
              <div style={{ display: 'flex', gap: '12px', marginTop: '16px' }}>
                <button type="button" onClick={() => setShowInviteModal(false)} style={{ flex: 1, padding: '10px 18px', borderRadius: '10px', border: '1px solid ' + t.panelBorder, background: 'transparent', color: t.text1, fontWeight: 600, fontSize: '13px', cursor: 'pointer' }}>Cancel</button>
                <button type="submit" style={{ ...primaryBtnStyle, flex: 1 }}>Send Invite</button>
              </div>
            </form>
          </div>
        </div>
      )}

      {/* Settings Navigation */}
      <div style={{ width: '230px', flexShrink: 0, display: 'flex', flexDirection: 'column', gap: '4px' }}>
        <h2 style={{ fontSize: '24px', fontWeight: 700, margin: '0 0 20px', color: t.text1 }}>Settings</h2>

        {['users', 'security', 'billing', 'usage', 'roles', 'policies', 'ratelimits', 'audit', 'apikeys', 'sso', 'alerts', 'anomalies', 'privacy'].map(tab => (
          <button
            key={tab}
            onClick={() => setActiveTab(tab)}
            style={{
              textAlign: 'left',
              padding: '11px 16px',
              borderRadius: '10px',
              background: activeTab === tab ? (t.dark ? 'rgba(255,255,255,0.1)' : 'rgba(255,255,255,0.6)') : 'transparent',
              border: 'none',
              color: activeTab === tab ? t.text1 : t.text2,
              cursor: 'pointer',
              fontSize: '13.5px',
              fontWeight: 600,
              transition: '0.2s',
              textTransform: 'capitalize'
            }}
          >
            {tab === 'users' ? 'Users'
              : tab === 'security' ? 'Security (MFA)'
              : tab === 'billing' ? 'Billing & Usage'
              : tab === 'usage' ? 'Usage & Quota'
              : tab === 'roles' ? 'Roles (RBAC)'
              : tab === 'policies' ? 'Policies (ABAC)'
              : tab === 'ratelimits' ? 'Rate Limits'
              : tab === 'audit' ? 'Audit Log'
              : tab === 'apikeys' ? 'API Keys'
              : tab === 'sso' ? 'SSO / SAML'
              : tab === 'alerts' ? 'Alert Channels'
              : tab === 'anomalies' ? 'Anomalies'
              : 'Data & Privacy'}
          </button>
        ))}
      </div>

      {/* Main Content Area */}
      <div style={{ flex: 1, minWidth: 0, padding: 'clamp(20px,3vw,40px)', borderRadius: '24px', background: t.panelBg, border: '1px solid ' + t.panelBorder, backdropFilter: 'blur(30px) saturate(180%)', boxShadow: t.shadow, display: 'flex', flexDirection: 'column' }}>

        {activeTab === 'users' && (
          <div>
            <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'flex-start', marginBottom: '28px', gap: '16px', flexWrap: 'wrap' }}>
              <div>
                <h3 style={{ fontSize: '19px', fontWeight: 700, margin: '0 0 8px', color: t.text1 }}>User Management</h3>
                <p style={{ color: t.text2, fontSize: '13.5px', maxWidth: '520px', lineHeight: 1.6 }}>Manage access control and RBAC roles.</p>
              </div>
              <button onClick={() => setShowInviteModal(true)} style={primaryBtnStyle}>+ Invite User</button>
            </div>

            {!token && (
               <div style={{ padding: '16px', background: t.redSoft, color: t.red, borderRadius: '8px', marginBottom: '24px' }}>
                  You are viewing this page without an admin token. Please authenticate via SSO or standard login.
               </div>
            )}

            {error && (
               <div style={{ padding: '16px', background: t.redSoft, color: t.red, borderRadius: '8px', marginBottom: '24px', border: '1px solid ' + t.red }}>
                  <strong>Access Denied:</strong> {error}
               </div>
            )}

            <table style={{ width: '100%', borderCollapse: 'collapse' }}>
              <thead>
                <tr style={{ borderBottom: '1px solid ' + t.panelBorder, textAlign: 'left' }}>
                  <th style={{ padding: '10px 8px', fontWeight: 600, color: t.text2, fontSize: '12px' }}>User</th>
                  <th style={{ padding: '10px 8px', fontWeight: 600, color: t.text2, fontSize: '12px' }}>Role</th>
                  <th style={{ padding: '10px 8px', fontWeight: 600, color: t.text2, fontSize: '12px' }}>Joined</th>
                  <th style={{ padding: '10px 8px', fontWeight: 600, color: t.text2, fontSize: '12px' }}>Actions</th>
                </tr>
              </thead>
              <tbody>
                {loadingUsers ? (
                  <tr>
                    <td colSpan={4} style={{ padding: '24px', textAlign: 'center', color: t.text2 }}>Loading users...</td>
                  </tr>
                ) : users.length === 0 ? (
                  <tr>
                    <td colSpan={4} style={{ padding: '24px', textAlign: 'center', color: t.text2 }}>No users found. Are you authenticated?</td>
                  </tr>
                ) : (
                  users.map(user => (
                    <tr key={user.id} style={{ borderBottom: '1px solid ' + t.panelBorder }}>
                      <td style={{ padding: '14px 8px', display: 'flex', alignItems: 'center', gap: '12px' }}>
                        <div style={{ width: '32px', height: '32px', borderRadius: '50%', background: `linear-gradient(135deg, ${t.accent}, ${t.accent2})`, color: '#fff', display: 'flex', alignItems: 'center', justifyContent: 'center', fontWeight: 700, fontSize: '13px' }}>
                          {user.username ? user.username[0].toUpperCase() : 'U'}
                        </div>
                        <div>
                          <div style={{ fontWeight: 600, fontSize: '13.5px', color: t.text1 }}>{user.username.split('@')[0]}</div>
                          <div style={{ color: t.text2, fontSize: '12px' }}>{user.username}</div>
                        </div>
                      </td>
                      <td style={{ padding: '14px 8px', fontSize: '13.5px' }}>
                        <select
                          value={user.role}
                          onChange={(e) => updateUserRole(user.id, e.target.value)}
                          disabled={user.username === 'admin'}
                          aria-label={`Role for ${user.username}`}
                          style={{ padding: '5px 10px', borderRadius: '100px', fontSize: '12px', background: t.dark ? 'rgba(255,255,255,0.08)' : 'rgba(0,0,0,0.05)', color: t.text1, border: '1px solid ' + t.panelBorder, cursor: user.username === 'admin' ? 'not-allowed' : 'pointer' }}
                        >
                          {Array.from(new Set([user.role, ...availableRoles])).map((r) => (
                            <option key={r} value={r}>{r}</option>
                          ))}
                        </select>
                      </td>
                      <td style={{ padding: '14px 8px', color: t.text2, fontSize: '13.5px' }}>{user.created_at ? new Date(user.created_at).toLocaleDateString() : 'Unknown'}</td>
                      <td style={{ padding: '14px 8px', fontSize: '13.5px' }}>
                        <button onClick={() => handleRevoke(user.id, user.username)} style={ghostRedBtnStyle}>Revoke</button>
                      </td>
                    </tr>
                  ))
                )}
              </tbody>
            </table>
          </div>
        )}

        {activeTab === 'security' && <SecurityPanel />}
        {activeTab === 'billing' && <BillingPanel />}
        {activeTab === 'usage' && <UsagePanel />}
        {activeTab === 'roles' && <RolesPanel />}

        {activeTab === 'policies' && <PoliciesPanel />}

        {activeTab === 'ratelimits' && <RateLimitsPanel />}

        {activeTab === 'audit' && <AuditLogPanel />}

        {activeTab === 'sso' && (
           <div>
             <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'flex-start', marginBottom: '28px', gap: '16px', flexWrap: 'wrap' }}>
               <div>
                 <h3 style={{ fontSize: '19px', fontWeight: 700, margin: '0 0 8px', color: t.text1 }}>SSO / SAML Configuration</h3>
                 <p style={{ color: t.text2, fontSize: '13.5px', maxWidth: '520px', lineHeight: 1.6 }}>Configure enterprise identity providers (OIDC / OAuth2).</p>
               </div>
               <a href="/api/v1/auth/sso/login" style={{ ...primaryBtnStyle, textDecoration: 'none', display: 'inline-block' }}>Test SSO Login</a>
             </div>

             <div style={{ background: t.dark ? 'rgba(0,0,0,0.2)' : 'rgba(0,0,0,0.03)', padding: '24px', borderRadius: '12px', border: '1px solid ' + t.panelBorder }}>
                <div style={{ fontWeight: 600, marginBottom: '16px', display: 'flex', alignItems: 'center', gap: '8px', color: t.text1 }}>
                  <img src="https://www.google.com/favicon.ico" alt="Google" style={{ width: '16px', height: '16px' }} />
                  Google Workspace (OIDC)
                </div>
                <div style={{ display: 'grid', gridTemplateColumns: '120px 1fr', gap: '12px', fontSize: '14px' }}>
                   <div style={{ color: t.text2 }}>Status:</div>
                   <div style={{ color: ssoClientId ? t.green : t.amber }}>
                     {ssoClientId ? 'Active' : 'Missing Client ID'}
                   </div>
                   <div style={{ color: t.text2 }}>Client ID:</div>
                   <div style={{ fontFamily: 'monospace', color: t.text1 }}>{ssoClientId || '[Not configured]'}</div>
                   <div style={{ color: t.text2 }}>Redirect URI:</div>
                   <div style={{ fontFamily: 'monospace', color: t.text1 }}>http://localhost:8080/api/v1/auth/sso/callback</div>
                </div>
             </div>
           </div>
        )}

        {activeTab === 'apikeys' && <IngestionKeysPanel />}

        {activeTab === 'alerts' && (
           <div>
             <AlertRulesPanel />

             <div style={{ marginTop: '40px', paddingTop: '32px', borderTop: '1px solid ' + t.panelBorder }}>
               <ChannelsPanel />
             </div>
           </div>
        )}

        {activeTab === 'anomalies' && <AnomalyConfigPanel />}

        {activeTab === 'privacy' && <DataPrivacyPanel />}

      </div>
    </div>
  );
}
