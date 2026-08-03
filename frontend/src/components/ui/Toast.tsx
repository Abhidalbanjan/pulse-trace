"use client";

// Toast (ROAD_TO_100 · F0.4).
//
// Replaces the `alert()` calls scattered across screens with a non-blocking,
// accessible notification stack. Mount <ToastProvider> once at the app root, then
// call `const toast = useToast()` and `toast.success(...) / .error(...) / .info(...)`.

import React, { createContext, useCallback, useContext, useMemo, useRef, useState } from 'react';
import { useTheme } from '@/context/ThemeContext';

type ToastKind = 'success' | 'error' | 'info';

interface ToastItem {
  id: number;
  kind: ToastKind;
  message: string;
}

interface ToastApi {
  success: (message: string) => void;
  error: (message: string) => void;
  info: (message: string) => void;
}

const ToastContext = createContext<ToastApi | null>(null);

/** Access the toast API. Throws if used outside <ToastProvider>. */
export function useToast(): ToastApi {
  const ctx = useContext(ToastContext);
  if (!ctx) throw new Error('useToast must be used within a <ToastProvider>');
  return ctx;
}

export function ToastProvider({ children }: { children: React.ReactNode }) {
  const [items, setItems] = useState<ToastItem[]>([]);
  const idRef = useRef(0);

  const remove = useCallback((id: number) => {
    setItems((prev) => prev.filter((t) => t.id !== id));
  }, []);

  const push = useCallback(
    (kind: ToastKind, message: string) => {
      const id = ++idRef.current;
      setItems((prev) => [...prev, { id, kind, message }]);
      // Errors linger longer than confirmations so they aren't missed.
      const ttl = kind === 'error' ? 7000 : 4000;
      setTimeout(() => remove(id), ttl);
    },
    [remove],
  );

  const api = useMemo<ToastApi>(
    () => ({
      success: (m) => push('success', m),
      error: (m) => push('error', m),
      info: (m) => push('info', m),
    }),
    [push],
  );

  return (
    <ToastContext.Provider value={api}>
      {children}
      <ToastStack items={items} onDismiss={remove} />
    </ToastContext.Provider>
  );
}

function ToastStack({ items, onDismiss }: { items: ToastItem[]; onDismiss: (id: number) => void }) {
  const { tokens: t } = useTheme();
  if (items.length === 0) return null;

  const color = (k: ToastKind) => (k === 'success' ? t.green : k === 'error' ? t.red : t.accent);

  return (
    <div
      aria-live="polite"
      aria-atomic="false"
      style={{
        position: 'fixed',
        bottom: '20px',
        right: '20px',
        zIndex: 9999,
        display: 'flex',
        flexDirection: 'column',
        gap: '10px',
        maxWidth: 'min(92vw, 380px)',
      }}
    >
      {items.map((item) => (
        <div
          key={item.id}
          role={item.kind === 'error' ? 'alert' : 'status'}
          style={{
            display: 'flex',
            alignItems: 'flex-start',
            gap: '10px',
            padding: '12px 14px',
            borderRadius: '10px',
            background: t.panelBg,
            border: `1px solid ${color(item.kind)}`,
            borderLeft: `4px solid ${color(item.kind)}`,
            color: t.text1,
            fontSize: '13px',
            boxShadow: '0 8px 24px rgba(0,0,0,0.18)',
            backdropFilter: 'blur(12px)',
          }}
        >
          <span style={{ flex: 1, lineHeight: 1.5 }}>{item.message}</span>
          <button
            onClick={() => onDismiss(item.id)}
            aria-label="Dismiss notification"
            style={{
              background: 'none',
              border: 'none',
              color: t.text2,
              cursor: 'pointer',
              fontSize: '16px',
              lineHeight: 1,
              padding: 0,
            }}
          >
            ×
          </button>
        </div>
      ))}
    </div>
  );
}
