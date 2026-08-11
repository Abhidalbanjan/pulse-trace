"use client";

import React, { useState, useEffect, useCallback } from 'react';
import { fetchWithAuth } from '@/lib/api';
import { useTheme } from '@/context/ThemeContext';

type Stage = 'idle' | 'enrolling' | 'done';

// SecurityPanel (F18) — self-service TOTP MFA management: enrol an authenticator,
// confirm with a code, capture one-time recovery codes, and disable.
export function SecurityPanel() {
  const { tokens: t } = useTheme();
  const [enabled, setEnabled] = useState<boolean | null>(null);
  const [stage, setStage] = useState<Stage>('idle');
  const [secret, setSecret] = useState('');
  const [otpauth, setOtpauth] = useState('');
  const [code, setCode] = useState('');
  const [recoveryCodes, setRecoveryCodes] = useState<string[]>([]);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [disableCode, setDisableCode] = useState('');

  const loadStatus = useCallback(() => {
    fetchWithAuth('/api/v1/auth/mfa/status')
      .then(async (res) => {
        if (!res.ok) throw new Error(await res.text());
        return res.json();
      })
      .then((j) => setEnabled(!!j.enabled))
      .catch(() => setEnabled(false));
  }, []);

  useEffect(() => { loadStatus(); }, [loadStatus]);

  const startEnroll = async () => {
    setBusy(true); setError(null);
    try {
      const res = await fetchWithAuth('/api/v1/auth/mfa/enroll', { method: 'POST' });
      const j = await res.json();
      if (!res.ok) throw new Error(j.error || 'Failed to start enrolment');
      setSecret(j.secret);
      setOtpauth(j.otpauth_url);
      setStage('enrolling');
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to start enrolment');
    } finally {
      setBusy(false);
    }
  };

  const confirmEnroll = async () => {
    setBusy(true); setError(null);
    try {
      const res = await fetchWithAuth('/api/v1/auth/mfa/verify', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ code: code.trim() }),
      });
      const j = await res.json();
      if (!res.ok) throw new Error(j.error || 'Verification failed');
      setRecoveryCodes(j.recovery_codes || []);
      setStage('done');
      setEnabled(true);
      setCode('');
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Verification failed');
    } finally {
      setBusy(false);
    }
  };

  const disable = async () => {
    setBusy(true); setError(null);
    try {
      const res = await fetchWithAuth('/api/v1/auth/mfa/disable', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ code: disableCode.trim() }),
      });
      const j = await res.json();
      if (!res.ok) throw new Error(j.error || 'Failed to disable MFA');
      setEnabled(false);
      setStage('idle');
      setDisableCode('');
      setSecret(''); setOtpauth(''); setRecoveryCodes([]);
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to disable MFA');
    } finally {
      setBusy(false);
    }
  };

  const input: React.CSSProperties = {
    padding: '10px 14px', borderRadius: '8px', border: `1px solid ${t.panelBorder}`,
    background: t.dark ? 'rgba(0,0,0,0.2)' : 'rgba(0,0,0,0.03)', color: t.text1, fontSize: '14px', outline: 'none',
  };
  const primaryBtn: React.CSSProperties = {
    padding: '10px 18px', borderRadius: '8px', border: 'none', fontWeight: 600, fontSize: '14px',
    background: `linear-gradient(135deg, ${t.accent}, ${t.accent2})`, color: '#fff', cursor: busy ? 'not-allowed' : 'pointer', opacity: busy ? 0.7 : 1,
  };
  const ghostBtn: React.CSSProperties = {
    padding: '10px 18px', borderRadius: '8px', border: `1px solid ${t.panelBorder}`,
    background: 'transparent', color: t.text1, fontWeight: 600, fontSize: '14px', cursor: busy ? 'not-allowed' : 'pointer',
  };

  return (
    <div>
      <div style={{ marginBottom: '28px' }}>
        <h3 style={{ fontSize: '19px', fontWeight: 700, margin: '0 0 8px', color: t.text1 }}>Two-Factor Authentication</h3>
        <p style={{ color: t.text2, fontSize: '13.5px', maxWidth: '560px', lineHeight: 1.6 }}>
          Protect your account with a time-based one-time password (TOTP) from an authenticator app.
          When enabled, sign-in requires your password and a 6-digit code.
        </p>
      </div>

      {error && (
        <div style={{ padding: '14px 16px', background: t.redSoft, color: t.red, borderRadius: '8px', marginBottom: '20px', fontSize: '13.5px' }}>{error}</div>
      )}

      {/* Status pill */}
      <div style={{ display: 'flex', alignItems: 'center', gap: '10px', marginBottom: '24px' }}>
        <span style={{
          display: 'inline-flex', alignItems: 'center', gap: '8px', padding: '5px 12px', borderRadius: '100px', fontSize: '12.5px', fontWeight: 700,
          background: enabled ? t.green + '18' : (t.dark ? 'rgba(255,255,255,0.06)' : 'rgba(0,0,0,0.05)'),
          color: enabled ? t.green : t.text2,
        }}>
          <span style={{ width: '8px', height: '8px', borderRadius: '50%', background: enabled ? t.green : t.text2 }} />
          {enabled === null ? 'Checking…' : enabled ? 'Enabled' : 'Not enabled'}
        </span>
      </div>

      {/* Not enabled → offer enrolment */}
      {enabled === false && stage === 'idle' && (
        <button onClick={startEnroll} disabled={busy} style={primaryBtn}>Set up authenticator</button>
      )}

      {/* Enrolling → show secret + confirm */}
      {stage === 'enrolling' && (
        <div style={{ maxWidth: '520px' }}>
          <p style={{ color: t.text1, fontSize: '14px', marginBottom: '12px' }}>
            1. Add this secret to your authenticator app (or scan its QR from the provisioning URI):
          </p>
          <div style={{ display: 'flex', flexDirection: 'column', gap: '10px', marginBottom: '20px' }}>
            <code style={{ fontFamily: 'monospace', fontSize: '15px', letterSpacing: '0.15em', padding: '12px', borderRadius: '8px', background: t.dark ? 'rgba(0,0,0,0.25)' : 'rgba(0,0,0,0.04)', color: t.text1, wordBreak: 'break-all' }}>{secret}</code>
            <code style={{ fontFamily: 'monospace', fontSize: '11px', padding: '10px', borderRadius: '8px', background: t.dark ? 'rgba(0,0,0,0.25)' : 'rgba(0,0,0,0.04)', color: t.text2, wordBreak: 'break-all' }}>{otpauth}</code>
          </div>
          <p style={{ color: t.text1, fontSize: '14px', marginBottom: '12px' }}>2. Enter the current 6-digit code to confirm:</p>
          <div style={{ display: 'flex', gap: '12px', alignItems: 'center' }}>
            <input value={code} onChange={(e) => setCode(e.target.value)} aria-label="Verification code" placeholder="123456" style={{ ...input, letterSpacing: '0.3em', width: '140px', textAlign: 'center' }} />
            <button onClick={confirmEnroll} disabled={busy} style={primaryBtn}>Verify & enable</button>
            <button onClick={() => { setStage('idle'); setError(null); }} disabled={busy} style={ghostBtn}>Cancel</button>
          </div>
        </div>
      )}

      {/* Just enabled → show one-time recovery codes */}
      {stage === 'done' && recoveryCodes.length > 0 && (
        <div style={{ maxWidth: '520px', marginTop: '8px' }}>
          <div style={{ padding: '16px', borderRadius: '10px', background: t.amber + '14', border: `1px solid ${t.amber}55`, marginBottom: '16px' }}>
            <strong style={{ color: t.text1, fontSize: '14px' }}>Save your recovery codes</strong>
            <p style={{ color: t.text2, fontSize: '13px', margin: '6px 0 0', lineHeight: 1.5 }}>
              Each code works once if you lose your authenticator. They are shown only now — store them somewhere safe.
            </p>
          </div>
          <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: '8px', fontFamily: 'monospace', fontSize: '14px' }}>
            {recoveryCodes.map((c) => (
              <span key={c} style={{ padding: '8px 12px', borderRadius: '6px', background: t.dark ? 'rgba(0,0,0,0.25)' : 'rgba(0,0,0,0.04)', color: t.text1, textAlign: 'center' }}>{c}</span>
            ))}
          </div>
          <button onClick={() => setStage('idle')} style={{ ...ghostBtn, marginTop: '16px' }}>I&apos;ve saved them</button>
        </div>
      )}

      {/* Enabled → offer disable (requires a code) */}
      {enabled === true && stage !== 'enrolling' && stage !== 'done' && (
        <div style={{ maxWidth: '520px', marginTop: '4px' }}>
          <p style={{ color: t.text2, fontSize: '13.5px', marginBottom: '12px' }}>To turn off two-factor authentication, confirm a current code or a recovery code.</p>
          <div style={{ display: 'flex', gap: '12px', alignItems: 'center' }}>
            <input value={disableCode} onChange={(e) => setDisableCode(e.target.value)} aria-label="Code to disable MFA" placeholder="123456" style={{ ...input, width: '160px', textAlign: 'center' }} />
            <button onClick={disable} disabled={busy || !disableCode.trim()} style={{ ...ghostBtn, color: t.red, borderColor: t.red + '66' }}>Disable MFA</button>
          </div>
        </div>
      )}
    </div>
  );
}
