"use client";

import React, { useState, useEffect } from 'react';

export function SettingsView() {
  const [activeTab, setActiveTab] = useState('users');
  const [ssoClientId, setSsoClientId] = useState<string | null>(null);
  const [users, setUsers] = useState<any[]>([]);
  const [loadingUsers, setLoadingUsers] = useState(false);
  const [showInviteModal, setShowInviteModal] = useState(false);
  const [inviteForm, setInviteForm] = useState({ username: '', password: '', role: 'viewer' });
  const [token, setToken] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    // Check URL for SSO token
    const urlParams = new URLSearchParams(window.location.search);
    const ssoToken = urlParams.get('token');
    if (ssoToken) {
      localStorage.setItem('pulse_token', ssoToken);
      window.history.replaceState({}, document.title, window.location.pathname);
      setToken(ssoToken);
    } else {
      setToken(localStorage.getItem('pulse_token'));
    }
  }, []);

  useEffect(() => {
    if (activeTab === 'sso') {
      import('@/lib/api').then(({ fetchWithAuth }) => {
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
          .catch(err => console.error("Failed to fetch SSO config:", err.message || err));
      });
    }
  }, [activeTab]);

  const fetchUsers = () => {
    setLoadingUsers(true);
    fetch('http://localhost:8080/api/v1/admin/users', {
      headers: { 'Authorization': `Bearer ${token}` }
    })
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
        setError(err.message || err.toString());
        setLoadingUsers(false);
      });
  };

  useEffect(() => {
    if (activeTab === 'users' && token) {
      fetchUsers();
    }
  }, [activeTab, token]);

  const handleInvite = async (e: React.FormEvent) => {
    e.preventDefault();
    try {
      const res = await fetch('http://localhost:8080/api/v1/admin/users', {
        method: 'POST',
        headers: {
          'Content-Type': 'application/json',
          'Authorization': `Bearer ${token}`
        },
        body: JSON.stringify(inviteForm)
      });
      if (!res.ok) throw new Error(await res.text());
      setShowInviteModal(false);
      setInviteForm({ username: '', password: '', role: 'viewer' });
      fetchUsers();
    } catch (err: any) {
      alert(`Error inviting user: ${err.message}`);
    }
  };

  const handleRevoke = async (id: string, username: string) => {
    if (username === 'admin') {
      alert("Cannot revoke the root admin user.");
      return;
    }
    if (!confirm(`Are you sure you want to revoke access for ${username}?`)) return;
    
    try {
      const res = await fetch(`http://localhost:8080/api/v1/admin/users?id=${id}`, {
        method: 'DELETE',
        headers: { 'Authorization': `Bearer ${token}` }
      });
      if (!res.ok) throw new Error(await res.text());
      fetchUsers();
    } catch (err: any) {
      alert(`Error revoking user: ${err.message}`);
    }
  };

  return (
    <div style={{ display: 'flex', gap: '40px', padding: '40px', maxWidth: '1200px', margin: '0 auto', width: '100%', height: '100%', position: 'relative' }}>
      
      {/* Invite Modal */}
      {showInviteModal && (
        <div style={{ position: 'fixed', top: 0, left: 0, right: 0, bottom: 0, background: 'rgba(0,0,0,0.8)', zIndex: 1000, display: 'flex', alignItems: 'center', justifyContent: 'center' }}>
          <div style={{ background: '#1e1e1e', padding: '32px', borderRadius: '16px', width: '400px', border: '1px solid var(--border-color)' }}>
            <h3 style={{ fontSize: '20px', fontWeight: 600, marginBottom: '24px' }}>Invite New User</h3>
            <form onSubmit={handleInvite} style={{ display: 'flex', flexDirection: 'column', gap: '16px' }}>
              <div>
                <label style={{ display: 'block', fontSize: '13px', color: 'var(--text-secondary)', marginBottom: '8px' }}>Email / Username</label>
                <input type="text" required value={inviteForm.username} onChange={e => setInviteForm({...inviteForm, username: e.target.value})} style={{ width: '100%', padding: '12px', background: 'rgba(255,255,255,0.05)', border: '1px solid var(--border-color)', borderRadius: '8px', color: '#fff' }} />
              </div>
              <div>
                <label style={{ display: 'block', fontSize: '13px', color: 'var(--text-secondary)', marginBottom: '8px' }}>Temporary Password</label>
                <input type="password" required value={inviteForm.password} onChange={e => setInviteForm({...inviteForm, password: e.target.value})} style={{ width: '100%', padding: '12px', background: 'rgba(255,255,255,0.05)', border: '1px solid var(--border-color)', borderRadius: '8px', color: '#fff' }} />
              </div>
              <div>
                <label style={{ display: 'block', fontSize: '13px', color: 'var(--text-secondary)', marginBottom: '8px' }}>Role</label>
                <select value={inviteForm.role} onChange={e => setInviteForm({...inviteForm, role: e.target.value})} style={{ width: '100%', padding: '12px', background: 'rgba(255,255,255,0.05)', border: '1px solid var(--border-color)', borderRadius: '8px', color: '#fff' }}>
                  <option value="viewer">Viewer</option>
                  <option value="admin">Admin</option>
                </select>
              </div>
              <div style={{ display: 'flex', gap: '12px', marginTop: '16px' }}>
                <button type="button" onClick={() => setShowInviteModal(false)} className="btn-secondary" style={{ flex: 1 }}>Cancel</button>
                <button type="submit" className="btn-primary" style={{ flex: 1 }}>Send Invite</button>
              </div>
            </form>
          </div>
        </div>
      )}

      {/* Settings Navigation */}
      <div style={{ width: '240px', display: 'flex', flexDirection: 'column', gap: '8px' }}>
        <h2 style={{ fontSize: '24px', fontWeight: 600, marginBottom: '24px' }}>Settings</h2>
        
        {['users', 'apikeys', 'sso', 'alerts'].map(tab => (
          <button 
            key={tab}
            onClick={() => setActiveTab(tab)}
            style={{ 
              textAlign: 'left', 
              padding: '12px 16px', 
              borderRadius: '8px',
              background: activeTab === tab ? 'rgba(255,255,255,0.1)' : 'transparent',
              border: 'none',
              color: activeTab === tab ? 'white' : 'var(--text-secondary)',
              cursor: 'pointer',
              fontSize: '14px',
              fontWeight: 500,
              transition: '0.2s',
              textTransform: 'capitalize'
            }}
          >
            {tab === 'users' ? 'Users & Roles' : tab === 'apikeys' ? 'API Keys' : tab === 'sso' ? 'SSO / SAML' : 'Alert Channels'}
          </button>
        ))}
      </div>

      {/* Main Content Area */}
      <div className="glass-panel" style={{ flex: 1, padding: '40px', display: 'flex', flexDirection: 'column' }}>
        
        {activeTab === 'users' && (
          <div>
            <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: '32px' }}>
              <div>
                <h3 style={{ fontSize: '20px', fontWeight: 600, marginBottom: '8px' }}>User Management</h3>
                <p style={{ color: 'var(--text-secondary)' }}>Manage access control and RBAC roles.</p>
              </div>
              <button className="btn-primary" onClick={() => setShowInviteModal(true)} style={{ padding: '8px 16px' }}>+ Invite User</button>
            </div>
            
            {!token && (
               <div style={{ padding: '16px', background: 'rgba(239, 68, 68, 0.1)', color: 'var(--status-red)', borderRadius: '8px', marginBottom: '24px' }}>
                  You are viewing this page without an admin token. Please authenticate via SSO or standard login.
               </div>
            )}
            
            {error && (
               <div style={{ padding: '16px', background: 'rgba(239, 68, 68, 0.1)', color: 'var(--status-red)', borderRadius: '8px', marginBottom: '24px', border: '1px solid rgba(239, 68, 68, 0.3)' }}>
                  <strong>Access Denied:</strong> {error}
               </div>
            )}

            <table style={{ width: '100%', borderCollapse: 'collapse' }}>
              <thead>
                <tr style={{ borderBottom: '1px solid var(--border-color)', color: 'var(--text-secondary)', fontSize: '13px', textAlign: 'left' }}>
                  <th style={{ padding: '12px 0' }}>User</th>
                  <th>Role</th>
                  <th>Joined</th>
                  <th>Actions</th>
                </tr>
              </thead>
              <tbody>
                {loadingUsers ? (
                  <tr>
                    <td colSpan={4} style={{ padding: '24px', textAlign: 'center', color: 'var(--text-secondary)' }}>Loading users...</td>
                  </tr>
                ) : users.length === 0 ? (
                  <tr>
                    <td colSpan={4} style={{ padding: '24px', textAlign: 'center', color: 'var(--text-secondary)' }}>No users found. Are you authenticated?</td>
                  </tr>
                ) : (
                  users.map(user => (
                    <tr key={user.id} style={{ borderBottom: '1px solid var(--border-color)' }}>
                      <td style={{ padding: '16px 0', display: 'flex', alignItems: 'center', gap: '12px' }}>
                        <div style={{ width: '32px', height: '32px', borderRadius: '50%', background: 'var(--accent-purple)', display: 'flex', alignItems: 'center', justifyContent: 'center', fontWeight: 600 }}>
                          {user.username ? user.username[0].toUpperCase() : 'U'}
                        </div>
                        <div>
                          <div style={{ fontWeight: 500 }}>{user.username.split('@')[0]}</div>
                          <div style={{ color: 'var(--text-secondary)', fontSize: '12px' }}>{user.username}</div>
                        </div>
                      </td>
                      <td><span style={{ background: 'rgba(255,255,255,0.1)', padding: '4px 12px', borderRadius: '128px', fontSize: '12px' }}>{user.role}</span></td>
                      <td style={{ color: 'var(--text-secondary)', fontSize: '13px' }}>{user.created_at ? new Date(user.created_at).toLocaleDateString() : 'Unknown'}</td>
                      <td>
                        <button onClick={() => handleRevoke(user.id, user.username)} className="btn-secondary" style={{ padding: '4px 8px', color: 'var(--status-red)', borderColor: 'var(--status-red)' }}>Revoke</button>
                      </td>
                    </tr>
                  ))
                )}
              </tbody>
            </table>
          </div>
        )}

        {activeTab === 'sso' && (
           <div>
             <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: '32px' }}>
               <div>
                 <h3 style={{ fontSize: '20px', fontWeight: 600, marginBottom: '8px' }}>SSO / SAML Configuration</h3>
                 <p style={{ color: 'var(--text-secondary)' }}>Configure enterprise identity providers (OIDC / OAuth2).</p>
               </div>
               <a href="http://localhost:8080/api/v1/auth/sso/login" className="btn-primary" style={{ padding: '8px 16px', textDecoration: 'none' }}>Test SSO Login</a>
             </div>
             
             <div style={{ background: 'rgba(0,0,0,0.2)', padding: '24px', borderRadius: '12px', border: '1px solid var(--border-color)' }}>
                <div style={{ fontWeight: 600, marginBottom: '16px', display: 'flex', alignItems: 'center', gap: '8px' }}>
                  <img src="https://www.google.com/favicon.ico" alt="Google" style={{ width: '16px', height: '16px' }} />
                  Google Workspace (OIDC)
                </div>
                <div style={{ display: 'grid', gridTemplateColumns: '120px 1fr', gap: '12px', fontSize: '14px' }}>
                   <div style={{ color: 'var(--text-secondary)' }}>Status:</div>
                   <div style={{ color: ssoClientId ? 'var(--status-green)' : 'var(--status-orange)' }}>
                     {ssoClientId ? 'Active' : 'Missing Client ID'}
                   </div>
                   <div style={{ color: 'var(--text-secondary)' }}>Client ID:</div>
                   <div style={{ fontFamily: 'monospace' }}>{ssoClientId || '[Not configured]'}</div>
                   <div style={{ color: 'var(--text-secondary)' }}>Redirect URI:</div>
                   <div style={{ fontFamily: 'monospace' }}>http://localhost:8080/api/v1/auth/sso/callback</div>
                </div>
             </div>
           </div>
        )}

        {activeTab === 'apikeys' && (
           <div>
             <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: '32px' }}>
               <div>
                 <h3 style={{ fontSize: '20px', fontWeight: 600, marginBottom: '8px' }}>API Keys</h3>
                 <p style={{ color: 'var(--text-secondary)' }}>Keys for OpenTelemetry agents to send telemetry.</p>
               </div>
               <button className="btn-primary" style={{ padding: '8px 16px' }}>Generate New Key</button>
             </div>
             
             <div style={{ background: 'rgba(0,0,0,0.2)', padding: '24px', borderRadius: '12px', border: '1px solid var(--border-color)', display: 'flex', justifyContent: 'space-between', alignItems: 'center' }}>
               <div>
                 <div style={{ fontWeight: 600, marginBottom: '8px' }}>Production Cluster Key</div>
                 <div style={{ fontFamily: 'monospace', color: 'var(--accent-blue)' }}>pt_live_************************</div>
               </div>
               <button className="btn-secondary" style={{ padding: '6px 12px' }}>Revoke</button>
             </div>
           </div>
        )}

        {activeTab === 'alerts' && (
           <div>
             <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: '32px' }}>
              <div>
                <h3 style={{ fontSize: '20px', fontWeight: 600, marginBottom: '8px' }}>Alert Channels</h3>
                <p style={{ color: 'var(--text-secondary)' }}>Configure where PulseTrace sends incident alerts.</p>
              </div>
              <button className="btn-primary" style={{ padding: '8px 16px' }}>+ Add Channel</button>
            </div>

             <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: '24px' }}>
                <div style={{ background: 'rgba(0,0,0,0.2)', padding: '24px', borderRadius: '12px', border: '1px solid var(--border-color)' }}>
                  <div style={{ display: 'flex', justifyContent: 'space-between', marginBottom: '16px' }}>
                    <div style={{ fontWeight: 600, display: 'flex', alignItems: 'center', gap: '8px' }}>
                      <span style={{ color: '#E01E5A' }}>#</span> Slack
                    </div>
                    <span style={{ color: 'var(--status-green)', fontSize: '12px' }}>Connected</span>
                  </div>
                  <p style={{ color: 'var(--text-secondary)', fontSize: '13px' }}>Sending critical alerts to #eng-oncall</p>
                </div>
                <div style={{ background: 'rgba(0,0,0,0.2)', padding: '24px', borderRadius: '12px', border: '1px solid var(--border-color)' }}>
                  <div style={{ display: 'flex', justifyContent: 'space-between', marginBottom: '16px' }}>
                    <div style={{ fontWeight: 600, display: 'flex', alignItems: 'center', gap: '8px' }}>
                      <span style={{ color: 'var(--status-green)' }}>✉</span> PagerDuty
                    </div>
                    <span style={{ color: 'var(--text-secondary)', fontSize: '12px' }}>Not Configured</span>
                  </div>
                  <p style={{ color: 'var(--text-secondary)', fontSize: '13px' }}>Trigger on-call escalations.</p>
                </div>
             </div>
           </div>
        )}

      </div>
    </div>
  );
}
