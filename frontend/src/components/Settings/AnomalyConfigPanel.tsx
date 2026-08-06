"use client";

// Anomaly-detection tuning (ROAD_TO_100 · F14).
//
// The EWMA anomaly detector's sensitivity was hardcoded; this panel exposes the
// per-tenant thresholds and an on/off switch. Changes are picked up by the
// running detector within its config-cache TTL (~30s).

import React, { useState } from 'react';
import { useTheme } from '@/context/ThemeContext';
import { api, ApiError } from '@/lib/api/client';
import type { AnomalyConfig } from '@/lib/api/types';
import { useApiResource } from '@/lib/hooks/useApiResource';
import { StateBoundary, useToast } from '@/components/ui';

function errMsg(err: unknown, fallback: string): string {
  return err instanceof ApiError || err instanceof Error ? err.message : fallback;
}

const FIELDS: { key: keyof AnomalyConfig; label: string; help: string; min: number; max: number; step: number }[] = [
  { key: 'p99_multiplier', label: 'Latency spike (× baseline)', help: 'Flag when p99 latency reaches this multiple of the service’s own baseline.', min: 1, max: 10, step: 0.1 },
  { key: 'error_rate_jump_points', label: 'Error-rate jump (% points)', help: 'Flag when the error rate rises this many percentage points above baseline.', min: 0, max: 100, step: 0.5 },
  { key: 'min_error_rate', label: 'Error-rate floor (%)', help: 'The error rate must also exceed this absolute floor, so near-zero services don’t page on noise.', min: 0, max: 100, step: 0.5 },
  { key: 'throughput_drop_ratio', label: 'Throughput drop (× baseline)', help: 'Flag when throughput falls to this fraction of baseline (e.g. 0.4 = down to 40%).', min: 0.05, max: 1, step: 0.05 },
];

export function AnomalyConfigPanel() {
  const { tokens: t } = useTheme();
  const toast = useToast();

  const cfg = useApiResource<AnomalyConfig | null>(
    () => api.getData<AnomalyConfig>('/api/v1/anomaly/config').then((d) => d ?? null),
  );

  const [draft, setDraft] = useState<AnomalyConfig | null>(null);
  const [saving, setSaving] = useState(false);
  // Seed the editable draft from the fetched config once it arrives, without an
  // effect: derive it lazily on first render where data exists.
  const current = draft ?? cfg.data;

  const update = (patch: Partial<AnomalyConfig>) => {
    if (!current) return;
    setDraft({ ...current, ...patch });
  };

  const save = async () => {
    if (!current || saving) return;
    setSaving(true);
    try {
      await api.put('/api/v1/anomaly/config', current);
      toast.success('Anomaly detection updated');
      setDraft(null);
      await cfg.refetch();
    } catch (err) {
      toast.error(`Error saving: ${errMsg(err, 'request failed')}`);
    } finally {
      setSaving(false);
    }
  };

  const input: React.CSSProperties = { padding: '9px 12px', background: t.dark ? 'rgba(255,255,255,0.05)' : 'rgba(255,255,255,0.7)', border: '1px solid ' + t.panelBorder, borderRadius: '8px', color: t.text1, width: '120px' };
  const primaryBtn: React.CSSProperties = { padding: '10px 18px', borderRadius: '10px', border: 'none', background: `linear-gradient(135deg, ${t.accent}, ${t.accent2})`, color: '#fff', fontWeight: 600, fontSize: '13px', cursor: 'pointer' };

  return (
    <div>
      <h3 style={{ fontSize: '19px', fontWeight: 700, margin: '0 0 8px', color: t.text1 }}>Anomaly Detection</h3>
      <p style={{ color: t.text2, fontSize: '13.5px', maxWidth: '560px', lineHeight: 1.6, marginBottom: '24px' }}>
        Tune how aggressively PulseTrace flags a service as degrading. Each service is compared to
        its own rolling baseline; these thresholds decide how far off-baseline is “anomalous”.
        Changes take effect within about 30 seconds.
      </p>

      <StateBoundary loading={cfg.loading && !current} error={cfg.error} onRetry={cfg.refetch} loadingLabel="Loading config…">
        {current && (
          <div style={{ maxWidth: '620px', display: 'flex', flexDirection: 'column', gap: '18px' }}>
            <label style={{ display: 'flex', alignItems: 'center', gap: '10px', cursor: 'pointer' }}>
              <input type="checkbox" checked={current.enabled} onChange={(e) => update({ enabled: e.target.checked })} />
              <span style={{ color: t.text1, fontWeight: 600, fontSize: '14px' }}>Anomaly detection enabled</span>
            </label>

            {FIELDS.map((f) => (
              <div key={f.key} style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', gap: '16px', opacity: current.enabled ? 1 : 0.5 }}>
                <div>
                  <div style={{ color: t.text1, fontSize: '13.5px', fontWeight: 600 }}>{f.label}</div>
                  <div style={{ color: t.text2, fontSize: '12px', maxWidth: '420px', lineHeight: 1.5 }}>{f.help}</div>
                </div>
                <input
                  type="number"
                  value={current[f.key] as number}
                  min={f.min}
                  max={f.max}
                  step={f.step}
                  disabled={!current.enabled}
                  onChange={(e) => update({ [f.key]: parseFloat(e.target.value) } as Partial<AnomalyConfig>)}
                  style={input}
                  aria-label={f.label}
                />
              </div>
            ))}

            <button onClick={save} disabled={saving || !draft} style={{ ...primaryBtn, alignSelf: 'flex-start', opacity: saving || !draft ? 0.5 : 1, cursor: saving || !draft ? 'not-allowed' : 'pointer' }}>
              {saving ? 'Saving…' : 'Save changes'}
            </button>
          </div>
        )}
      </StateBoundary>
    </div>
  );
}
