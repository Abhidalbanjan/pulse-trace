"use client";

// Self-healing remediation panel (ROAD_TO_100 · F1, flagship).
//
// The human half of human-in-the-loop remediation. The correlation backend can
// propose a recovery playbook per incident and gate it behind approval by risk
// tier, but there was no UI — the incident detail showed hardcoded fake
// runbooks. This panel makes the real lifecycle drivable: see the proposed
// playbook, DRY-RUN it (compute the plan without touching anything), then
// APPROVE (execute) or REJECT (with a reason) — with the policy posture and the
// approver/audit trail shown, and every degraded state (no playbook, execution
// disabled, terminal) rendered honestly rather than as a dead button.
//
// Mount it keyed by incident id (`<RemediationPanel key={id} .../>`) so it
// remounts per incident and seeds its local state cleanly.

import React, { useState } from 'react';
import { useTheme } from '@/context/ThemeContext';
import { api, ApiError } from '@/lib/api/client';
import type { PlaybookAction, PlaybookStatus, RemediationPolicy } from '@/lib/api/types';
import { useApiResource } from '@/lib/hooks/useApiResource';
import { ConfirmDialog, useToast } from '@/components/ui';

function errMsg(err: unknown, fallback: string): string {
  return err instanceof ApiError || err instanceof Error ? err.message : fallback;
}

function fmt(ts?: string): string {
  if (!ts) return '';
  const d = new Date(ts);
  return Number.isNaN(d.getTime()) ? '' : d.toLocaleString();
}

interface PlaybookResp {
  playbook: PlaybookAction;
}

export function RemediationPanel({
  incidentId,
  playbook,
  primaryService,
  onChanged,
}: {
  incidentId: string;
  playbook: PlaybookAction | null;
  primaryService?: string;
  onChanged?: () => void | Promise<void>;
}) {
  const { tokens: t } = useTheme();
  const toast = useToast();

  // Seeded from the prop; mutation responses update it in place so the panel
  // reflects the new status immediately (the component is keyed by incidentId,
  // so this initializer re-runs per incident).
  const [current, setCurrent] = useState<PlaybookAction | null>(playbook);

  // Policy posture gates whether an execute path is offered at all.
  const policy = useApiResource<RemediationPolicy | null>(
    () => api.get<RemediationPolicy>('/api/v1/remediation/policy').catch(() => null),
  );
  const executionAllowed = policy.data?.execution_allowed ?? false;

  const [busy, setBusy] = useState<'dry-run' | 'approve' | 'reject' | null>(null);
  const [confirmApprove, setConfirmApprove] = useState(false);
  const [confirmReject, setConfirmReject] = useState(false);
  const [rejectReason, setRejectReason] = useState('');

  const run = async (
    kind: 'dry-run' | 'approve' | 'reject',
    path: string,
    body?: unknown,
    successMsg?: string,
  ) => {
    setBusy(kind);
    try {
      const res = await api.post<PlaybookResp>(path, body);
      if (res?.playbook) setCurrent(res.playbook);
      if (successMsg) toast.success(successMsg);
      await onChanged?.();
    } catch (err) {
      toast.error(errMsg(err, `${kind} failed`));
    } finally {
      setBusy(null);
    }
  };

  const id = encodeURIComponent(incidentId);
  const doDryRun = () => run('dry-run', `/api/v1/incidents/${id}/playbook/dry-run`, undefined, 'Dry-run complete — plan below');
  const doApprove = async () => {
    setConfirmApprove(false);
    await run('approve', `/api/v1/incidents/${id}/playbook/approve`, undefined, 'Remediation approved and executing');
  };
  const doReject = async () => {
    setConfirmReject(false);
    await run('reject', `/api/v1/incidents/${id}/playbook/reject`, { reason: rejectReason.trim() }, 'Remediation rejected');
    setRejectReason('');
  };

  const heading = <h3 style={{ fontSize: '17px', fontWeight: 700, margin: '0 0 16px', color: t.text1 }}>Remediation</h3>;

  if (!current) {
    return (
      <div>
        {heading}
        <div style={{ background: t.dark ? 'rgba(255,255,255,0.04)' : 'rgba(0,0,0,0.03)', border: '1px dashed ' + t.panelBorder, borderRadius: '12px', padding: '18px', color: t.text2, fontSize: '13px', lineHeight: 1.6 }}>
          No remediation has been proposed for this incident yet. The causal engine suggests a
          playbook once it has a confident root cause.
        </div>
      </div>
    );
  }

  const st = current.status;
  const isPending = st === 'PENDING_APPROVAL';
  const isTerminal = st === 'EXECUTED' || st === 'FAILED' || st === 'REJECTED' || st === 'SUPPRESSED';
  const isExecuting = st === 'EXECUTING';
  // Dry-run is read-only planning — allowed whenever the plan isn't mid-flight or done.
  const canDryRun = !isExecuting && !isTerminal;

  const statusMeta: Record<PlaybookStatus, { label: string; color: string }> = {
    SUGGESTED: { label: 'Suggested', color: t.text2 },
    SUPPRESSED: { label: 'Suppressed', color: t.text2 },
    DRY_RUN: { label: 'Dry-run', color: t.accent },
    PENDING_APPROVAL: { label: 'Awaiting approval', color: t.amber },
    REJECTED: { label: 'Rejected', color: t.red },
    EXECUTING: { label: 'Executing…', color: t.amber },
    EXECUTED: { label: 'Executed', color: t.green },
    FAILED: { label: 'Failed', color: t.red },
  };
  const meta = statusMeta[st] ?? { label: st, color: t.text2 };

  const btn = (disabled: boolean): React.CSSProperties => ({
    padding: '10px 16px', borderRadius: '10px', fontSize: '13px', fontWeight: 600,
    cursor: disabled ? 'not-allowed' : 'pointer', opacity: disabled ? 0.5 : 1,
  });

  return (
    <div>
      {heading}
      <div style={{ background: t.dark ? 'rgba(255,255,255,0.04)' : 'rgba(0,0,0,0.03)', border: '1px solid ' + t.panelBorder, borderRadius: '14px', padding: '18px' }}>
        <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'flex-start', gap: '12px', marginBottom: '10px' }}>
          <div style={{ fontWeight: 700, fontSize: '14.5px', color: t.text1 }}>{current.name || 'Recovery playbook'}</div>
          <span style={{ background: meta.color + '22', color: meta.color, padding: '3px 10px', borderRadius: '100px', fontSize: '11px', fontWeight: 700, whiteSpace: 'nowrap' }}>
            {meta.label}{current.dry_run && st !== 'DRY_RUN' ? ' · planned' : ''}
          </span>
        </div>
        {current.description && (
          <p style={{ color: t.text2, fontSize: '13px', lineHeight: 1.6, margin: '0 0 12px' }}>{current.description}</p>
        )}
        {primaryService && (
          <div style={{ fontSize: '12px', color: t.text2, marginBottom: '12px' }}>
            Target service: <span style={{ color: t.text1, fontFamily: 'monospace' }}>{primaryService}</span>
          </div>
        )}

        {/* Policy posture — why an execute button may be absent. */}
        {!policy.loading && !executionAllowed && (
          <div style={{ background: t.dark ? 'rgba(255,255,255,0.05)' : 'rgba(0,0,0,0.04)', borderRadius: '9px', padding: '10px 12px', fontSize: '12px', color: t.text2, lineHeight: 1.5, marginBottom: '12px' }}>
            Automated execution is disabled by the current remediation policy
            {policy.data?.mode ? ` (mode: ${policy.data.mode})` : ''}. You can still dry-run the plan.
          </div>
        )}

        {/* Actions */}
        <div style={{ display: 'flex', gap: '10px', flexWrap: 'wrap' }}>
          {canDryRun && (
            <button
              onClick={doDryRun}
              disabled={busy !== null}
              style={{ ...btn(busy !== null), border: '1px solid ' + t.panelBorder, background: 'transparent', color: t.text1 }}
            >
              {busy === 'dry-run' ? 'Planning…' : 'Dry-run'}
            </button>
          )}
          {isPending && (
            <>
              <button
                onClick={() => setConfirmApprove(true)}
                disabled={busy !== null || !executionAllowed}
                title={!executionAllowed ? 'Execution is disabled by policy' : undefined}
                style={{ ...btn(busy !== null || !executionAllowed), border: 'none', background: `linear-gradient(135deg, ${t.accent}, ${t.accent2})`, color: '#fff' }}
              >
                {busy === 'approve' ? 'Approving…' : 'Approve & run'}
              </button>
              <button
                onClick={() => setConfirmReject(true)}
                disabled={busy !== null}
                style={{ ...btn(busy !== null), border: '1px solid ' + t.red, background: 'transparent', color: t.red }}
              >
                Reject
              </button>
            </>
          )}
          {isExecuting && <span style={{ color: t.text2, fontSize: '13px', alignSelf: 'center' }}>Execution in progress…</span>}
        </div>

        {/* Output: dry-run plan or execution output. */}
        {current.output && (
          <pre style={{ marginTop: '14px', background: t.dark ? 'rgba(0,0,0,0.35)' : 'rgba(0,0,0,0.05)', border: '1px solid ' + t.panelBorder, borderRadius: '10px', padding: '12px', fontSize: '12px', color: t.text1, whiteSpace: 'pre-wrap', wordBreak: 'break-word', maxHeight: '220px', overflow: 'auto' }}>
            {current.output}
          </pre>
        )}

        {/* Audit trail. */}
        {(current.approved_by || current.rejected_by) && (
          <div style={{ marginTop: '12px', fontSize: '12px', color: t.text2, lineHeight: 1.6 }}>
            {current.approved_by && <div>Approved by <span style={{ color: t.text1 }}>{current.approved_by}</span>{current.approved_at ? ` · ${fmt(current.approved_at)}` : ''}</div>}
            {current.rejected_by && <div>Rejected by <span style={{ color: t.text1 }}>{current.rejected_by}</span>{current.rejected_at ? ` · ${fmt(current.rejected_at)}` : ''}</div>}
          </div>
        )}
      </div>

      <ConfirmDialog
        open={confirmApprove}
        busy={busy === 'approve'}
        title="Approve and execute this remediation?"
        body={
          <span>
            This runs <strong style={{ color: t.text1 }}>{current.name}</strong>
            {primaryService ? <> against <strong style={{ color: t.text1 }}>{primaryService}</strong></> : null}. High-risk
            actions additionally require an elevated role and will be rejected server-side otherwise.
          </span>
        }
        confirmLabel="Approve & run"
        onConfirm={doApprove}
        onCancel={() => setConfirmApprove(false)}
      />

      <ConfirmDialog
        open={confirmReject}
        danger
        busy={busy === 'reject'}
        title="Reject this remediation?"
        body={
          <div style={{ display: 'flex', flexDirection: 'column', gap: '10px' }}>
            <span>Decline the proposed playbook. Recording why helps the next on-call engineer.</span>
            <textarea
              value={rejectReason}
              onChange={(e) => setRejectReason(e.target.value)}
              placeholder="Reason (optional)"
              rows={3}
              aria-label="Rejection reason"
              style={{ padding: '10px 12px', background: t.dark ? 'rgba(255,255,255,0.05)' : 'rgba(255,255,255,0.7)', border: '1px solid ' + t.panelBorder, borderRadius: '8px', color: t.text1, resize: 'vertical', fontFamily: 'inherit' }}
            />
          </div>
        }
        confirmLabel="Reject"
        onConfirm={doReject}
        onCancel={() => { setConfirmReject(false); setRejectReason(''); }}
      />
    </div>
  );
}
