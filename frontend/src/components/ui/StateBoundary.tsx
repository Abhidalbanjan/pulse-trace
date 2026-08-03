"use client";

// StateBoundary (ROAD_TO_100 · F0.4).
//
// One place that renders the loading / error / empty / content states every
// screen was re-implementing inline (`loading ? ... : error ? ... : rows.length
// ? ... : ...`). Screens pass the flags from useApiResource and their content;
// this keeps those states consistent and accessible (aria-live, a retry button).

import React from 'react';
import { useTheme } from '@/context/ThemeContext';

export interface StateBoundaryProps {
  loading: boolean;
  error: string | null;
  /** True when there's no data to show (renders the empty state instead of children). */
  empty?: boolean;
  onRetry?: () => void;
  loadingLabel?: string;
  emptyLabel?: string;
  children: React.ReactNode;
}

export function StateBoundary({
  loading,
  error,
  empty = false,
  onRetry,
  loadingLabel = 'Loading…',
  emptyLabel = 'Nothing to show yet.',
  children,
}: StateBoundaryProps) {
  const { tokens: t } = useTheme();

  const wrap: React.CSSProperties = {
    padding: '32px 20px',
    textAlign: 'center',
    color: t.text2,
    fontSize: '13.5px',
  };

  if (loading) {
    return (
      <div style={wrap} role="status" aria-live="polite">
        {loadingLabel}
      </div>
    );
  }

  if (error) {
    return (
      <div
        role="alert"
        style={{
          padding: '16px',
          background: t.redSoft,
          color: t.red,
          borderRadius: '8px',
          display: 'flex',
          alignItems: 'center',
          justifyContent: 'space-between',
          gap: '12px',
          flexWrap: 'wrap',
        }}
      >
        <span>{error}</span>
        {onRetry && (
          <button
            onClick={onRetry}
            style={{
              padding: '6px 14px',
              borderRadius: '8px',
              border: '1px solid ' + t.red,
              background: 'transparent',
              color: t.red,
              cursor: 'pointer',
              fontSize: '12px',
              fontWeight: 600,
            }}
          >
            Retry
          </button>
        )}
      </div>
    );
  }

  if (empty) {
    return (
      <div style={wrap} aria-live="polite">
        {emptyLabel}
      </div>
    );
  }

  return <>{children}</>;
}
