"use client";

// ConfirmDialog (ROAD_TO_100 · F0.4).
//
// Accessible replacement for the blocking `confirm()` used before destructive
// actions. Controlled component: render it with `open`, wire `onConfirm`/`onCancel`.
// Handles Escape-to-cancel, backdrop click, focus-on-open, role="dialog"/aria-modal,
// and a `busy` state so the confirm button can show progress during the action.

import React, { useEffect, useRef } from 'react';
import { useTheme } from '@/context/ThemeContext';

export interface ConfirmDialogProps {
  open: boolean;
  title: string;
  body?: React.ReactNode;
  confirmLabel?: string;
  cancelLabel?: string;
  /** Style the confirm button as destructive (red). */
  danger?: boolean;
  /** Disable buttons and show progress while the action runs. */
  busy?: boolean;
  onConfirm: () => void;
  onCancel: () => void;
}

export function ConfirmDialog({
  open,
  title,
  body,
  confirmLabel = 'Confirm',
  cancelLabel = 'Cancel',
  danger = false,
  busy = false,
  onConfirm,
  onCancel,
}: ConfirmDialogProps) {
  const { tokens: t } = useTheme();
  const confirmRef = useRef<HTMLButtonElement>(null);

  useEffect(() => {
    if (!open) return;
    // Move focus into the dialog and close on Escape.
    confirmRef.current?.focus();
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape' && !busy) onCancel();
    };
    document.addEventListener('keydown', onKey);
    return () => document.removeEventListener('keydown', onKey);
  }, [open, busy, onCancel]);

  if (!open) return null;

  const accent = danger ? t.red : t.accent;

  return (
    <div
      onClick={() => !busy && onCancel()}
      style={{
        position: 'fixed',
        inset: 0,
        zIndex: 10000,
        background: 'rgba(0,0,0,0.45)',
        display: 'flex',
        alignItems: 'center',
        justifyContent: 'center',
        padding: '20px',
      }}
    >
      <div
        role="dialog"
        aria-modal="true"
        aria-labelledby="confirm-dialog-title"
        onClick={(e) => e.stopPropagation()}
        style={{
          width: 'min(92vw, 440px)',
          background: t.panelBg,
          border: '1px solid ' + t.panelBorder,
          borderRadius: '14px',
          padding: '24px',
          boxShadow: '0 20px 60px rgba(0,0,0,0.35)',
          backdropFilter: 'blur(16px)',
        }}
      >
        <h3 id="confirm-dialog-title" style={{ margin: '0 0 10px', fontSize: '17px', fontWeight: 700, color: t.text1 }}>
          {title}
        </h3>
        {body && <div style={{ color: t.text2, fontSize: '13.5px', lineHeight: 1.6, marginBottom: '20px' }}>{body}</div>}
        <div style={{ display: 'flex', justifyContent: 'flex-end', gap: '10px' }}>
          <button
            onClick={onCancel}
            disabled={busy}
            style={{
              padding: '9px 16px',
              borderRadius: '9px',
              border: '1px solid ' + t.panelBorder,
              background: 'transparent',
              color: t.text1,
              cursor: busy ? 'not-allowed' : 'pointer',
              fontSize: '13px',
            }}
          >
            {cancelLabel}
          </button>
          <button
            ref={confirmRef}
            onClick={onConfirm}
            disabled={busy}
            style={{
              padding: '9px 18px',
              borderRadius: '9px',
              border: 'none',
              background: danger ? accent : `linear-gradient(135deg, ${t.accent}, ${t.accent2})`,
              color: '#fff',
              fontWeight: 600,
              fontSize: '13px',
              cursor: busy ? 'not-allowed' : 'pointer',
              opacity: busy ? 0.7 : 1,
            }}
          >
            {busy ? 'Working…' : confirmLabel}
          </button>
        </div>
      </div>
    </div>
  );
}
