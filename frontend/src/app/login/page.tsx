"use client";

import React, { useState } from 'react';
import { useAuth } from '@/context/AuthContext';

export default function LoginPage() {
  const [isLogin, setIsLogin] = useState(true);
  const [email, setEmail] = useState('');
  const [password, setPassword] = useState('');
  const [confirmPassword, setConfirmPassword] = useState('');
  const [error, setError] = useState('');
  const [loading, setLoading] = useState(false);
  const { login } = useAuth();

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    setError('');
    
    if (!isLogin && password !== confirmPassword) {
      setError('Passwords do not match');
      return;
    }

    setLoading(true);

    try {
      const endpoint = isLogin ? '/api/v1/auth/login' : '/api/v1/auth/register';
      
      const res = await fetch(endpoint, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ username: email, password })
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

      // If we just registered, the backend returns a success message but no token.
      // We must auto-login to get the token.
      if (!isLogin) {
        const loginRes = await fetch('/api/v1/auth/login', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ username: email, password })
        });
        if (!loginRes.ok) {
          const errText = await loginRes.text();
          throw new Error(errText || 'Login after registration failed');
        }
        data = await loginRes.json();
      }

      const userData = data.user || { id: 'temp-id', email: email, role: data.role || 'ADMIN' };
      login(data.token, userData);

    } catch (err: any) {
      setError(err.message || 'An unexpected error occurred');
    } finally {
      setLoading(false);
    }
  };

  const handleSSO = () => {
    window.location.href = '/api/v1/auth/sso/login';
  };

  return (
    <div style={{
      minHeight: '100vh',
      display: 'flex',
      alignItems: 'center',
      justifyContent: 'center',
      background: 'var(--bg-dark)',
      position: 'relative',
      overflow: 'hidden'
    }}>
      {/* Background Effects */}
      <div style={{
        position: 'absolute',
        top: '50%', left: '50%',
        transform: 'translate(-50%, -50%)',
        width: '800px', height: '800px',
        background: 'radial-gradient(circle, rgba(59, 130, 246, 0.15) 0%, rgba(0,0,0,0) 70%)',
        zIndex: 0
      }} />

      <div className="glass-panel" style={{
        width: '100%',
        maxWidth: '440px',
        padding: '48px',
        position: 'relative',
        zIndex: 1,
        display: 'flex',
        flexDirection: 'column',
        alignItems: 'center'
      }}>
        {/* Logo */}
        <div style={{ display: 'flex', alignItems: 'center', gap: '12px', marginBottom: '32px' }}>
          <div style={{ width: '40px', height: '40px' }}>
            <img src="/logo.png" alt="PulseTrace" style={{ width: '100%', height: '100%' }} />
          </div>
          <span style={{ fontSize: '24px', fontWeight: 700, letterSpacing: '-0.02em' }}>
            Pulse<span style={{ color: 'var(--accent-blue)' }}>Trace</span>
          </span>
        </div>

        <h1 style={{ fontSize: '24px', fontWeight: 600, marginBottom: '8px' }}>
          {isLogin ? 'Welcome back' : 'Create your account'}
        </h1>
        <p style={{ color: 'var(--text-secondary)', marginBottom: '32px', textAlign: 'center', fontSize: '14px' }}>
          Enter your credentials to access the enterprise observability platform.
        </p>

        {error && (
          <div style={{ 
            width: '100%', padding: '12px', background: 'rgba(239, 68, 68, 0.1)', 
            border: '1px solid rgba(239, 68, 68, 0.2)', color: 'var(--status-red)', 
            borderRadius: '8px', marginBottom: '24px', fontSize: '14px', textAlign: 'center' 
          }}>
            {error}
          </div>
        )}

        <form onSubmit={handleSubmit} style={{ width: '100%', display: 'flex', flexDirection: 'column', gap: '16px' }}>
          <div>
            <label style={{ display: 'block', fontSize: '13px', color: 'var(--text-secondary)', marginBottom: '8px' }}>Email</label>
            <input 
              type="email" 
              value={email}
              onChange={(e) => setEmail(e.target.value)}
              required
              style={{
                width: '100%', padding: '12px 16px', background: 'rgba(0,0,0,0.2)',
                border: '1px solid var(--border-color)', borderRadius: '8px',
                color: 'white', fontSize: '15px', outline: 'none'
              }}
              placeholder="admin@pulsetrace.ai"
            />
          </div>
          
          <div>
            <label style={{ display: 'block', fontSize: '13px', color: 'var(--text-secondary)', marginBottom: '8px' }}>Password</label>
            <input 
              type="password" 
              value={password}
              onChange={(e) => setPassword(e.target.value)}
              required
              style={{
                width: '100%', padding: '12px 16px', background: 'rgba(0,0,0,0.2)',
                border: '1px solid var(--border-color)', borderRadius: '8px',
                color: 'white', fontSize: '15px', outline: 'none'
              }}
              placeholder="••••••••"
            />
          </div>

          {!isLogin && (
            <div>
              <label style={{ display: 'block', fontSize: '13px', color: 'var(--text-secondary)', marginBottom: '8px' }}>Confirm Password</label>
              <input 
                type="password" 
                value={confirmPassword}
                onChange={(e) => setConfirmPassword(e.target.value)}
                required
                style={{
                  width: '100%', padding: '12px 16px', background: 'rgba(0,0,0,0.2)',
                  border: '1px solid var(--border-color)', borderRadius: '8px',
                  color: 'white', fontSize: '15px', outline: 'none'
                }}
                placeholder="••••••••"
              />
            </div>
          )}

          <button 
            type="submit" 
            className="btn-primary" 
            disabled={loading}
            style={{ width: '100%', padding: '14px', marginTop: '8px', fontSize: '15px', fontWeight: 600 }}
          >
            {loading ? 'Authenticating...' : (isLogin ? 'Sign In' : 'Sign Up')}
          </button>
        </form>

        <div style={{ 
          width: '100%', display: 'flex', alignItems: 'center', gap: '16px', margin: '24px 0' 
        }}>
          <div style={{ flex: 1, height: '1px', background: 'var(--border-color)' }} />
          <span style={{ fontSize: '12px', color: 'var(--text-secondary)' }}>OR</span>
          <div style={{ flex: 1, height: '1px', background: 'var(--border-color)' }} />
        </div>

        <button 
          type="button"
          onClick={handleSSO}
          style={{ 
            width: '100%', padding: '14px', background: 'white', color: 'black', 
            borderRadius: '8px', border: 'none', fontSize: '15px', fontWeight: 600,
            display: 'flex', alignItems: 'center', justifyContent: 'center', gap: '12px',
            cursor: 'pointer'
          }}
        >
          <img src="https://www.svgrepo.com/show/475656/google-color.svg" alt="Google" style={{ width: '20px' }} />
          Continue with Google
        </button>

        <p style={{ marginTop: '32px', fontSize: '14px', color: 'var(--text-secondary)' }}>
          {isLogin ? "Don't have an account? " : "Already have an account? "}
          <span 
            onClick={() => {
              setIsLogin(!isLogin);
              setError('');
              setConfirmPassword('');
            }}
            style={{ color: 'var(--accent-blue)', cursor: 'pointer', fontWeight: 500 }}
          >
            {isLogin ? 'Sign up' : 'Sign in'}
          </span>
        </p>
      </div>
    </div>
  );
}
