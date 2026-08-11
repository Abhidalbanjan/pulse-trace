"use client";

import React, { useState, Suspense } from 'react';
import { useSearchParams, useRouter } from 'next/navigation';
import { useTheme } from '@/context/ThemeContext';

function ResetPasswordInner() {
  const { tokens: t } = useTheme();
  const params = useSearchParams();
  const router = useRouter();
  const token = params.get('token') || '';
  const [password, setPassword] = useState('');
  const [confirm, setConfirm] = useState('');
  const [error, setError] = useState('');
  const [done, setDone] = useState(false);
  const [loading, setLoading] = useState(false);

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    setError('');
    if (password !== confirm) { setError('Passwords do not match'); return; }
    if (password.length < 8) { setError('Password must be at least 8 characters'); return; }
    setLoading(true);
    try {
      const res = await fetch('/api/v1/auth/password/reset', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ token, new_password: password }),
      });
      if (!res.ok) throw new Error((await res.text()) || 'Reset failed');
      setDone(true);
      setTimeout(() => router.push('/login'), 1800);
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Reset failed');
    } finally {
      setLoading(false);
    }
  };

  const inputStyle: React.CSSProperties = {
    width: '100%', padding: '12px 16px', background: t.dark ? 'rgba(0,0,0,0.2)' : 'rgba(0,0,0,0.03)',
    border: '1px solid ' + t.panelBorder, borderRadius: '10px', color: t.text1, fontSize: '15px', outline: 'none',
  };

  return (
    <div style={{ minHeight: '100vh', display: 'flex', alignItems: 'center', justifyContent: 'center', background: t.pageBg }}>
      <div className="glass-panel" style={{ width: '100%', maxWidth: '440px', padding: '40px', borderRadius: '24px', background: t.panelBg, border: '1px solid ' + t.panelBorder, boxShadow: t.shadow }}>
        <h1 style={{ fontSize: '24px', fontWeight: 600, margin: '0 0 8px', color: t.text1 }}>Set a new password</h1>
        <p style={{ color: t.text2, marginBottom: '28px', fontSize: '14px' }}>Choose a new password for your account.</p>

        {done ? (
          <div style={{ padding: '14px 16px', background: t.green + '18', color: t.green, borderRadius: '10px', fontSize: '14px' }}>
            Password reset. Redirecting to sign in…
          </div>
        ) : !token ? (
          <div style={{ padding: '14px 16px', background: t.redSoft, color: t.red, borderRadius: '10px', fontSize: '14px' }}>
            This reset link is missing its token. Request a new link from the sign-in page.
          </div>
        ) : (
          <form onSubmit={submit} style={{ display: 'flex', flexDirection: 'column', gap: '16px' }}>
            {error && <div style={{ padding: '12px', background: t.redSoft, color: t.red, borderRadius: '10px', fontSize: '14px', textAlign: 'center' }}>{error}</div>}
            <input type="password" value={password} onChange={(e) => setPassword(e.target.value)} required aria-label="New password" placeholder="New password" style={inputStyle} />
            <input type="password" value={confirm} onChange={(e) => setConfirm(e.target.value)} required aria-label="Confirm password" placeholder="Confirm password" style={inputStyle} />
            <button type="submit" disabled={loading} style={{ width: '100%', padding: '14px', fontSize: '15px', fontWeight: 600, background: `linear-gradient(135deg, ${t.accent}, ${t.accent2})`, color: '#fff', border: 'none', borderRadius: '10px', cursor: loading ? 'not-allowed' : 'pointer', opacity: loading ? 0.7 : 1 }}>
              {loading ? 'Resetting…' : 'Reset password'}
            </button>
          </form>
        )}
      </div>
    </div>
  );
}

export default function ResetPasswordPage() {
  return (
    <Suspense fallback={null}>
      <ResetPasswordInner />
    </Suspense>
  );
}
