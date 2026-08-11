"use client";

import React, { useEffect, useState } from 'react';
import { errMessage } from '@/lib/errMessage';
import { fetchWithAuth } from '@/lib/api';
import { useTheme } from '@/context/ThemeContext';

interface Tenant { id: string; name: string; plan: string; status: string; }
interface Usage { traces: number; metrics: number; logs: number; rum: number; }
interface PlanLimits { traces: number; metrics: number; logs: number; rum: number; }
type PlanCTA = 'current' | 'upgrade' | 'downgrade' | 'contact';
interface Plan {
  id: string; label: string; price: string; period: string;
  limits: PlanLimits; features: string[]; cta: PlanCTA; self_serve: boolean;
}
interface Catalog { current_plan: string; self_serve: boolean; plans: Plan[]; }
interface Invoice {
  id: string; number: string; amount_due: number; amount_paid: number;
  currency: string; status: string; created: number;
  hosted_invoice_url: string; invoice_pdf: string;
}

const PLAN_LABEL: Record<string, string> = {
  free: 'Free', standard: 'Standard', premium: 'Premium', enterprise: 'Enterprise',
};
const PLANS = ['free', 'standard', 'premium', 'enterprise'];

export function BillingPanel() {
  const { tokens: t } = useTheme();
  const [tenant, setTenant] = useState<Tenant | null>(null);
  const [usage, setUsage] = useState<Usage | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const [planChoice, setPlanChoice] = useState('standard');
  const [catalog, setCatalog] = useState<Catalog | null>(null);
  const [invoices, setInvoices] = useState<Invoice[]>([]);

  const load = async () => {
    try {
      const [tRes, uRes, pRes, iRes] = await Promise.all([
        fetchWithAuth('/api/v1/tenant'),
        fetchWithAuth('/api/v1/usage'),
        fetchWithAuth('/api/v1/billing/plans'),
        fetchWithAuth('/api/v1/billing/invoices'),
      ]);
      if (tRes.ok) setTenant(await tRes.json());
      if (uRes.ok) setUsage((await uRes.json()).usage);
      if (pRes.ok) setCatalog(await pRes.json());
      if (iRes.ok) setInvoices((await iRes.json()).invoices ?? []);
    } catch {
      setError('Failed to load billing information.');
    }
  };

  const money = (cents: number, currency: string) => {
    try {
      return new Intl.NumberFormat(undefined, { style: 'currency', currency: (currency || 'usd').toUpperCase() }).format((cents || 0) / 100);
    } catch {
      return `${((cents || 0) / 100).toFixed(2)} ${currency?.toUpperCase() || ''}`;
    }
  };
  // eslint-disable-next-line react-hooks/set-state-in-effect -- intentional one-shot fetch/hydration on mount; effect is the right place to sync from the API/localStorage
  useEffect(() => { load(); }, []);

  const checkout = async (plan: string) => {
    setBusy(true); setError(null); setNotice(null);
    try {
      const res = await fetchWithAuth('/api/v1/billing/checkout', {
        method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ plan }),
      });
      if (res.status === 501) { setNotice('Self-serve billing is disabled on this deployment — contact your account team to change plans.'); return; }
      if (!res.ok) throw new Error((await res.text()) || 'Checkout failed');
      const { url } = await res.json();
      if (url) window.location.assign(url);
    } catch (e) {
      setError(errMessage(e, 'Checkout failed'));
    } finally { setBusy(false); }
  };

  // Direct admin plan override (POST /api/v1/admin/tenant/plan). Distinct from
  // Stripe checkout: it's how plans change on deployments where self-serve
  // billing is disabled (enterprise / on-prem / manual billing).
  const setPlanDirect = async () => {
    setBusy(true); setError(null); setNotice(null);
    try {
      const res = await fetchWithAuth('/api/v1/admin/tenant/plan', {
        method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ plan: planChoice }),
      });
      if (!res.ok) throw new Error((await res.text()) || 'Failed to set plan');
      setNotice(`Plan set to ${PLAN_LABEL[planChoice] || planChoice}.`);
      await load();
    } catch (e) {
      setError(errMessage(e, 'Failed to set plan'));
    } finally { setBusy(false); }
  };

  const portal = async () => {
    setBusy(true); setError(null); setNotice(null);
    try {
      const res = await fetchWithAuth('/api/v1/billing/portal', { method: 'POST' });
      if (res.status === 501) { setNotice('Self-serve billing is disabled on this deployment.'); return; }
      if (!res.ok) throw new Error((await res.text()) || 'Could not open billing portal');
      const { url } = await res.json();
      if (url) window.location.assign(url);
    } catch (e) {
      setError(errMessage(e, 'Could not open billing portal'));
    } finally { setBusy(false); }
  };

  const card: React.CSSProperties = {
    background: t.dark ? 'rgba(255,255,255,0.03)' : 'rgba(0,0,0,0.02)',
    border: '1px solid ' + t.panelBorder, borderRadius: '14px', padding: '20px',
  };
  const btn = (primary: boolean): React.CSSProperties => ({
    padding: '10px 18px', borderRadius: '10px', fontWeight: 600, fontSize: '13px',
    cursor: busy ? 'not-allowed' : 'pointer', opacity: busy ? 0.6 : 1,
    border: primary ? 'none' : '1px solid ' + t.panelBorder,
    background: primary ? `linear-gradient(135deg, ${t.accent}, ${t.accent2})` : 'transparent',
    color: primary ? '#fff' : t.text1,
  });

  const fmt = (n: number) => n.toLocaleString();

  return (
    <div>
      <h3 style={{ fontSize: '19px', fontWeight: 700, margin: '0 0 8px', color: t.text1 }}>Billing &amp; Usage</h3>
      <p style={{ color: t.text2, fontSize: '13.5px', maxWidth: 520, lineHeight: 1.6, marginBottom: 24 }}>
        Your current plan and this month&apos;s ingested volume.
      </p>

      {error && <div style={{ padding: 12, background: t.redSoft, color: t.red, borderRadius: 8, marginBottom: 16 }}>{error}</div>}
      {notice && <div style={{ padding: 12, background: t.dark ? 'rgba(255,255,255,0.06)' : 'rgba(0,0,0,0.05)', color: t.text1, borderRadius: 8, marginBottom: 16 }}>{notice}</div>}

      {/* Dunning banner (F17): surface a failed payment with a recovery CTA. */}
      {tenant && (tenant.status === 'past_due' || tenant.status === 'suspended') && (
        <div role="alert" style={{
          padding: '14px 16px', borderRadius: 10, marginBottom: 20,
          background: tenant.status === 'suspended' ? t.redSoft : `${t.amber}18`,
          border: `1px solid ${tenant.status === 'suspended' ? t.red : t.amber}55`,
          display: 'flex', justifyContent: 'space-between', alignItems: 'center', gap: 16, flexWrap: 'wrap',
        }}>
          <div style={{ fontSize: 13.5, color: tenant.status === 'suspended' ? t.red : t.text1, lineHeight: 1.5 }}>
            <strong>{tenant.status === 'suspended' ? 'Your subscription is suspended.' : 'Your last payment failed.'}</strong>{' '}
            {tenant.status === 'suspended'
              ? 'Ingestion may be limited. Update your payment method to restore full service.'
              : 'Please update your payment method before your next retry to avoid interruption.'}
          </div>
          <button style={btn(true)} disabled={busy} onClick={portal}>Update payment method</button>
        </div>
      )}

      <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: 16, marginBottom: 24 }}>
        <div style={card}>
          <div style={{ fontSize: 12, color: t.text2, marginBottom: 6 }}>Current plan</div>
          <div style={{ fontSize: 24, fontWeight: 700, color: t.text1 }}>
            {tenant ? (PLAN_LABEL[tenant.plan] || tenant.plan) : '—'}
          </div>
          {tenant && <div style={{ fontSize: 12, color: tenant.status === 'active' ? t.green : tenant.status === 'past_due' ? t.amber : t.red, marginTop: 6 }}>{tenant.status.replace('_', ' ')}</div>}
        </div>
        <div style={card}>
          <div style={{ fontSize: 12, color: t.text2, marginBottom: 10 }}>This month&apos;s usage</div>
          {usage ? (
            <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: '4px 16px', fontSize: 13 }}>
              <span style={{ color: t.text2 }}>Traces</span><span style={{ color: t.text1, textAlign: 'right' }}>{fmt(usage.traces)}</span>
              <span style={{ color: t.text2 }}>Metrics</span><span style={{ color: t.text1, textAlign: 'right' }}>{fmt(usage.metrics)}</span>
              <span style={{ color: t.text2 }}>Logs</span><span style={{ color: t.text1, textAlign: 'right' }}>{fmt(usage.logs)}</span>
              <span style={{ color: t.text2 }}>RUM</span><span style={{ color: t.text1, textAlign: 'right' }}>{fmt(usage.rum)}</span>
            </div>
          ) : <div style={{ color: t.text2, fontSize: 13 }}>—</div>}
        </div>
      </div>

      {/* Plan comparison (F17): live catalog with per-plan upgrade/downgrade CTAs. */}
      <div style={{ fontSize: 15, fontWeight: 700, color: t.text1, margin: '4px 0 14px' }}>Compare plans</div>
      <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(180px, 1fr))', gap: 14, marginBottom: 12 }}>
        {(catalog?.plans ?? []).map((p) => {
          const isCurrent = p.cta === 'current';
          return (
            <div key={p.id} style={{
              ...card,
              borderColor: isCurrent ? t.accent : t.panelBorder,
              boxShadow: isCurrent ? `0 0 0 1px ${t.accent}` : 'none',
              display: 'flex', flexDirection: 'column', gap: 10,
            }}>
              <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'baseline' }}>
                <span style={{ fontSize: 15, fontWeight: 700, color: t.text1 }}>{p.label}</span>
                {isCurrent && <span style={{ fontSize: 10.5, fontWeight: 700, color: t.accent, textTransform: 'uppercase' }}>Current</span>}
              </div>
              <div style={{ fontSize: 20, fontWeight: 700, color: t.text1 }}>
                {p.price}{p.period && <span style={{ fontSize: 12, fontWeight: 500, color: t.text2 }}>/{p.period}</span>}
              </div>
              <ul style={{ listStyle: 'none', padding: 0, margin: 0, display: 'flex', flexDirection: 'column', gap: 6 }}>
                {p.features.map((f) => (
                  <li key={f} style={{ fontSize: 12, color: t.text2, lineHeight: 1.4, display: 'flex', gap: 6 }}>
                    <span style={{ color: t.green }}>✓</span>{f}
                  </li>
                ))}
              </ul>
              <div style={{ marginTop: 'auto', paddingTop: 6 }}>
                {p.cta === 'current' ? (
                  <button style={{ ...btn(false), width: '100%', cursor: 'default', opacity: 0.6 }} disabled>Current plan</button>
                ) : p.cta === 'contact' ? (
                  <a href="mailto:sales@pulsetrace.local?subject=PulseTrace%20Enterprise" style={{ ...btn(false), width: '100%', textAlign: 'center', textDecoration: 'none', display: 'block' }}>Contact sales</a>
                ) : p.cta === 'upgrade' ? (
                  <button style={{ ...btn(true), width: '100%' }} disabled={busy} onClick={() => checkout(p.id)}>Upgrade</button>
                ) : (
                  // downgrade: to a paid tier via checkout (Stripe prorates), to free via the portal
                  <button style={{ ...btn(false), width: '100%' }} disabled={busy} onClick={() => (p.id === 'free' ? portal() : checkout(p.id))}>Downgrade</button>
                )}
              </div>
            </div>
          );
        })}
      </div>
      <div style={{ display: 'flex', gap: 12, flexWrap: 'wrap' }}>
        <button style={btn(false)} disabled={busy} onClick={portal}>Manage billing</button>
      </div>

      {/* Invoice history (F17) */}
      {invoices.length > 0 && (
        <div style={{ marginTop: 28 }}>
          <div style={{ fontSize: 15, fontWeight: 700, color: t.text1, marginBottom: 12 }}>Invoice history</div>
          <table style={{ width: '100%', borderCollapse: 'collapse' }}>
            <thead>
              <tr style={{ borderBottom: '1px solid ' + t.panelBorder, textAlign: 'left' }}>
                <th style={{ padding: '9px 8px', fontSize: 12, fontWeight: 600, color: t.text2 }}>Date</th>
                <th style={{ padding: '9px 8px', fontSize: 12, fontWeight: 600, color: t.text2 }}>Invoice</th>
                <th style={{ padding: '9px 8px', fontSize: 12, fontWeight: 600, color: t.text2 }}>Amount</th>
                <th style={{ padding: '9px 8px', fontSize: 12, fontWeight: 600, color: t.text2 }}>Status</th>
                <th style={{ padding: '9px 8px', fontSize: 12, fontWeight: 600, color: t.text2 }}></th>
              </tr>
            </thead>
            <tbody>
              {invoices.map((inv) => (
                <tr key={inv.id} style={{ borderBottom: '1px solid ' + t.panelBorder }}>
                  <td style={{ padding: '11px 8px', fontSize: 13, color: t.text2, whiteSpace: 'nowrap' }}>{inv.created ? new Date(inv.created * 1000).toLocaleDateString() : '—'}</td>
                  <td style={{ padding: '11px 8px', fontSize: 12.5, fontFamily: 'monospace', color: t.text1 }}>{inv.number || inv.id}</td>
                  <td style={{ padding: '11px 8px', fontSize: 13, color: t.text1 }}>{money(inv.amount_paid || inv.amount_due, inv.currency)}</td>
                  <td style={{ padding: '11px 8px' }}>
                    <span style={{ fontSize: 11.5, fontWeight: 700, textTransform: 'capitalize', color: inv.status === 'paid' ? t.green : inv.status === 'open' ? t.amber : t.text2 }}>{inv.status}</span>
                  </td>
                  <td style={{ padding: '11px 8px', textAlign: 'right' }}>
                    {inv.invoice_pdf ? (
                      <a href={inv.invoice_pdf} target="_blank" rel="noreferrer" style={{ fontSize: 12.5, color: t.accent, textDecoration: 'none' }}>PDF</a>
                    ) : inv.hosted_invoice_url ? (
                      <a href={inv.hosted_invoice_url} target="_blank" rel="noreferrer" style={{ fontSize: 12.5, color: t.accent, textDecoration: 'none' }}>View</a>
                    ) : null}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}

      <div style={{ ...card, marginTop: 24 }}>
        <div style={{ fontSize: 13, fontWeight: 700, color: t.text1, marginBottom: 6 }}>Admin: set plan directly</div>
        <p style={{ fontSize: 12.5, color: t.text2, lineHeight: 1.6, margin: '0 0 12px', maxWidth: 520 }}>
          Change the tenant plan without Stripe — for enterprise / on-prem deployments where billing
          is managed manually. Takes effect immediately.
        </p>
        <div style={{ display: 'flex', gap: 10, alignItems: 'center', flexWrap: 'wrap' }}>
          <select value={planChoice} onChange={(e) => setPlanChoice(e.target.value)} aria-label="Plan"
            style={{ padding: '9px 12px', background: t.dark ? 'rgba(255,255,255,0.05)' : 'rgba(255,255,255,0.7)', border: '1px solid ' + t.panelBorder, borderRadius: 8, color: t.text1 }}>
            {PLANS.map((p) => <option key={p} value={p}>{PLAN_LABEL[p]}</option>)}
          </select>
          <button style={btn(false)} disabled={busy} onClick={setPlanDirect}>Set plan</button>
        </div>
      </div>
    </div>
  );
}
