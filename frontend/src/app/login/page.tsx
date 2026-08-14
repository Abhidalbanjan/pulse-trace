"use client";

import React, { useState } from 'react';
import Image from 'next/image';
import { useAuth } from '@/context/AuthContext';
import { useTheme } from '@/context/ThemeContext';

export default function LoginPage() {
  const { tokens: t } = useTheme();
  const [isLogin, setIsLogin] = useState(true);
  const [orgName, setOrgName] = useState('');
  const [email, setEmail] = useState('');
  const [password, setPassword] = useState('');
  const [confirmPassword, setConfirmPassword] = useState('');
  const [error, setError] = useState('');
  const [loading, setLoading] = useState(false);
  // Two-step MFA: once the password is accepted for an MFA-enabled account, the
  // server returns a short-lived challenge instead of a session. We hold it here
  // and switch the form to a code prompt.
  const [mfaToken, setMfaToken] = useState<string | null>(null);
  const [mfaCode, setMfaCode] = useState('');
  // Forgot-password: an inline sub-flow that posts the username and shows a
  // uniform "check your email" acknowledgement (the API never reveals whether
  // the account exists).
  const [forgotMode, setForgotMode] = useState(false);
  const [forgotSent, setForgotSent] = useState(false);
  const { login } = useAuth();

  const handleForgot = async (e: React.FormEvent) => {
    e.preventDefault();
    setError('');
    setLoading(true);
    try {
      await fetch('/api/v1/auth/password/forgot', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ username: email }),
      });
      setForgotSent(true);
    } catch {
      // Even on a network error we show the same acknowledgement to avoid
      // leaking anything about the account.
      setForgotSent(true);
    } finally {
      setLoading(false);
    }
  };

  const finishLogin = (data: { token: string; role?: string; user?: unknown }) => {
    const userData = data.user || { id: 'temp-id', email: email, role: data.role || 'admin' };
    login(data.token, userData as never);
  };

  // Second factor: exchange the challenge + a TOTP/recovery code for a session.
  const handleMfaSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    setError('');
    setLoading(true);
    try {
      const res = await fetch('/api/v1/auth/mfa/login', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ mfa_token: mfaToken, code: mfaCode.trim() }),
      });
      const data = await res.json();
      if (!res.ok) throw new Error(data.error || 'Verification failed');
      finishLogin(data);
    } catch (err) {
      setError(err instanceof Error ? err.message : 'An unexpected error occurred');
    } finally {
      setLoading(false);
    }
  };

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    setError('');

    if (!isLogin && password !== confirmPassword) {
      setError('Passwords do not match');
      return;
    }

    setLoading(true);

    try {
      // Sign-in uses /login; sign-up uses the self-serve /signup, which creates a
      // brand-new tenant with this user as its admin and returns a token directly.
      const endpoint = isLogin ? '/api/v1/auth/login' : '/api/v1/auth/signup';
      const body = isLogin
        ? { username: email, password }
        : { org_name: orgName, username: email, password };

      const res = await fetch(endpoint, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(body)
      });

      let data;
      const contentType = res.headers.get("content-type");
      if (contentType && contentType.includes("application/json")) {
        data = await res.json();
      } else {
        const text = await res.text();
        throw new Error(text || 'Authentication failed');
      }

      if (!res.ok) {
        throw new Error(data.error || data.message || 'Authentication failed');
      }

      // MFA gate: correct password, but a second factor is still required.
      if (data.mfa_required && data.mfa_token) {
        setMfaToken(data.mfa_token);
        setMfaCode('');
        return;
      }

      finishLogin(data);

    } catch (err) {
      setError(err instanceof Error ? err.message : 'An unexpected error occurred');
    } finally {
      setLoading(false);
    }
  };

  const handleSSO = () => {
    window.location.href = '/api/v1/auth/sso/login';
  };

  const handleSAML = () => {
    window.location.href = '/api/v1/auth/saml/login';
  };

  const inputStyle: React.CSSProperties = {
    width: '100%', padding: '12px 16px', background: t.dark ? 'rgba(0,0,0,0.2)' : 'rgba(0,0,0,0.03)',
    border: '1px solid ' + t.panelBorder, borderRadius: '10px',
    color: t.text1, fontSize: '15px', outline: 'none'
  };

  const labelStyle: React.CSSProperties = {
    display: 'block', fontSize: '13px', color: t.text2, marginBottom: '8px'
  };

  return (
    <div style={{
      minHeight: '100vh',
      display: 'flex',
      alignItems: 'center',
      justifyContent: 'center',
      background: t.pageBg,
      position: 'relative',
      overflow: 'hidden'
    }}>
      <div className="glass-panel" style={{
        width: '100%',
        maxWidth: '440px',
        padding: '40px',
        position: 'relative',
        zIndex: 1,
        display: 'flex',
        flexDirection: 'column',
        alignItems: 'center',
        borderRadius: '24px',
        background: t.panelBg,
        border: '1px solid ' + t.panelBorder,
        backdropFilter: 'blur(30px) saturate(180%)',
        boxShadow: t.shadow
      }}>
        {/* Logo */}
        <div style={{ display: 'flex', alignItems: 'center', gap: '12px', marginBottom: '32px' }}>
          <div style={{ width: '40px', height: '40px' }}>
            <Image src="/logo.png" alt="PulseTrace" width={40} height={40} style={{ width: '100%', height: '100%' }} />
          </div>
          <span style={{ fontSize: '24px', fontWeight: 700, letterSpacing: '-0.02em', color: t.text1 }}>
            Pulse<span style={{ color: t.accent }}>Trace</span>
          </span>
        </div>

        <h1 style={{ fontSize: '24px', fontWeight: 600, margin: '0 0 8px', color: t.text1 }}>
          {forgotMode ? 'Reset your password' : mfaToken ? 'Two-factor authentication' : isLogin ? 'Welcome back' : 'Create your account'}
        </h1>
        <p style={{ color: t.text2, marginBottom: '32px', textAlign: 'center', fontSize: '14px' }}>
          {forgotMode
            ? 'Enter your username and we’ll send a reset link if the account exists.'
            : mfaToken
            ? 'Enter the 6-digit code from your authenticator app, or a recovery code.'
            : 'Enter your credentials to access the enterprise observability platform.'}
        </p>

        {error && (
          <div style={{
            width: '100%', padding: '12px', background: t.redSoft,
            border: '1px solid ' + t.redSoft, color: t.red,
            borderRadius: '10px', marginBottom: '24px', fontSize: '14px', textAlign: 'center'
          }}>
            {error}
          </div>
        )}

        {forgotMode ? (
          forgotSent ? (
            <div style={{ width: '100%', display: 'flex', flexDirection: 'column', gap: '16px' }}>
              <div style={{ padding: '14px 16px', background: t.green + '18', color: t.green, borderRadius: '10px', fontSize: '14px', textAlign: 'center' }}>
                If an account exists for that username, a reset link is on its way.
              </div>
              <button type="button" onClick={() => { setForgotMode(false); setForgotSent(false); }} style={{ background: 'none', border: 'none', color: t.accent, fontSize: '14px', cursor: 'pointer' }}>← Back to sign in</button>
            </div>
          ) : (
            <form onSubmit={handleForgot} style={{ width: '100%', display: 'flex', flexDirection: 'column', gap: '16px' }}>
              <div>
                <label style={labelStyle}>Email or Username</label>
                <input type="text" value={email} onChange={(e) => setEmail(e.target.value)} required aria-label="Email or Username" style={inputStyle} placeholder="admin" />
              </div>
              <button type="submit" disabled={loading} style={{ width: '100%', padding: '14px', marginTop: '4px', fontSize: '15px', fontWeight: 600, background: `linear-gradient(135deg, ${t.accent}, ${t.accent2})`, color: '#fff', border: 'none', borderRadius: '10px', cursor: loading ? 'not-allowed' : 'pointer', opacity: loading ? 0.7 : 1 }}>
                {loading ? 'Sending…' : 'Send reset link'}
              </button>
              <button type="button" onClick={() => { setForgotMode(false); setError(''); }} style={{ background: 'none', border: 'none', color: t.text2, fontSize: '13px', cursor: 'pointer' }}>← Back to sign in</button>
            </form>
          )
        ) : mfaToken ? (
          <form onSubmit={handleMfaSubmit} style={{ width: '100%', display: 'flex', flexDirection: 'column', gap: '16px' }}>
            <div>
              <label style={labelStyle}>Authentication code</label>
              <input
                type="text"
                inputMode="text"
                autoFocus
                value={mfaCode}
                onChange={(e) => setMfaCode(e.target.value)}
                required
                aria-label="Authentication code"
                style={{ ...inputStyle, letterSpacing: '0.3em', textAlign: 'center', fontSize: '18px' }}
                placeholder="123456"
              />
            </div>
            <button
              type="submit"
              disabled={loading}
              style={{
                width: '100%', padding: '14px', marginTop: '8px', fontSize: '15px', fontWeight: 600,
                background: `linear-gradient(135deg, ${t.accent}, ${t.accent2})`, color: '#fff',
                border: 'none', borderRadius: '10px', cursor: loading ? 'not-allowed' : 'pointer', opacity: loading ? 0.7 : 1,
              }}
            >
              {loading ? 'Verifying…' : 'Verify'}
            </button>
            <button
              type="button"
              onClick={() => { setMfaToken(null); setMfaCode(''); setError(''); }}
              style={{ background: 'none', border: 'none', color: t.text2, fontSize: '13px', cursor: 'pointer' }}
            >
              ← Back to sign in
            </button>
          </form>
        ) : (
        <form onSubmit={handleSubmit} style={{ width: '100%', display: 'flex', flexDirection: 'column', gap: '16px' }}>
          {!isLogin && (
            <div>
              <label htmlFor="org-name" style={labelStyle}>Organization name</label>
              <input
                id="org-name"
                type="text"
                value={orgName}
                onChange={(e) => setOrgName(e.target.value)}
                required
                style={inputStyle}
                placeholder="Acme Inc"
              />
            </div>
          )}

          <div>
            <label htmlFor="login-email" style={labelStyle}>Email or Username</label>
            <input
              id="login-email"
              type="text"
              value={email}
              onChange={(e) => setEmail(e.target.value)}
              required
              style={inputStyle}
              placeholder="admin"
            />
          </div>

          <div>
            <label htmlFor="login-password" style={labelStyle}>Password</label>
            <input
              id="login-password"
              type="password"
              value={password}
              onChange={(e) => setPassword(e.target.value)}
              required
              style={inputStyle}
              placeholder="••••••••"
            />
          </div>

          {!isLogin && (
            <div>
              <label htmlFor="confirm-password" style={labelStyle}>Confirm Password</label>
              <input
                id="confirm-password"
                type="password"
                value={confirmPassword}
                onChange={(e) => setConfirmPassword(e.target.value)}
                required
                style={inputStyle}
                placeholder="••••••••"
              />
            </div>
          )}

          <button
            type="submit"
            disabled={loading}
            style={{
              width: '100%',
              padding: '14px',
              marginTop: '8px',
              fontSize: '15px',
              fontWeight: 600,
              background: `linear-gradient(135deg, ${t.accent}, ${t.accent2})`,
              color: '#fff',
              border: 'none',
              borderRadius: '10px',
              cursor: loading ? 'not-allowed' : 'pointer',
              opacity: loading ? 0.7 : 1
            }}
          >
            {loading ? 'Authenticating...' : (isLogin ? 'Sign In' : 'Sign Up')}
          </button>
          {isLogin && (
            <button type="button" onClick={() => { setForgotMode(true); setError(''); }} style={{ background: 'none', border: 'none', color: t.text2, fontSize: '13px', cursor: 'pointer', alignSelf: 'center' }}>
              Forgot password?
            </button>
          )}
        </form>
        )}

        {!mfaToken && !forgotMode && (
        <div style={{
          width: '100%', display: 'flex', alignItems: 'center', gap: '16px', margin: '24px 0'
        }}>
          <div style={{ flex: 1, height: '1px', background: t.panelBorder }} />
          <span style={{ fontSize: '12px', color: t.text2 }}>OR</span>
          <div style={{ flex: 1, height: '1px', background: t.panelBorder }} />
        </div>
        )}

        {!mfaToken && !forgotMode && (
        <button
          type="button"
          onClick={handleSSO}
          style={{
            width: '100%', padding: '14px', background: t.dark ? 'rgba(255,255,255,0.9)' : '#fff', color: '#1c1e26',
            borderRadius: '10px', border: '1px solid ' + t.panelBorder, fontSize: '15px', fontWeight: 600,
            display: 'flex', alignItems: 'center', justifyContent: 'center', gap: '12px',
            cursor: 'pointer'
          }}
        >
          {/* eslint-disable-next-line @next/next/no-img-element -- next/image on a
              third-party host requires adding svgrepo.com to next.config
              remotePatterns, which lets the optimizer proxy arbitrary images from
              it. Not worth that surface for a 20px icon. The better fix is
              vendoring the official Google mark into /public, tracked separately
              because it carries brand-asset licensing questions. */}
          <img src="https://www.svgrepo.com/show/475656/google-color.svg" alt="Google" style={{ width: '20px' }} />
          Continue with Google
        </button>
        )}

        {!mfaToken && !forgotMode && (
        <button
          type="button"
          onClick={handleSAML}
          style={{
            width: '100%', marginTop: '12px', padding: '14px', background: 'transparent', color: t.text1,
            borderRadius: '10px', border: '1px solid ' + t.panelBorder, fontSize: '15px', fontWeight: 600,
            display: 'flex', alignItems: 'center', justifyContent: 'center', gap: '12px', cursor: 'pointer',
          }}
        >
          Continue with SAML SSO
        </button>
        )}

        {!mfaToken && !forgotMode && (
        <p style={{ marginTop: '32px', fontSize: '14px', color: t.text2 }}>
          {isLogin ? "Don't have an account? " : "Already have an account? "}
          <span
            onClick={() => {
              setIsLogin(!isLogin);
              setError('');
              setConfirmPassword('');
              setOrgName('');
            }}
            style={{ color: t.accent, cursor: 'pointer', fontWeight: 500 }}
          >
            {isLogin ? 'Sign up' : 'Sign in'}
          </span>
        </p>
        )}
      </div>
    </div>
  );
}
