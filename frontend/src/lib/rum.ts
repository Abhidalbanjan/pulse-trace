import { getRUMTraceContext, rotateRUMTrace, getTraceparentHeader } from './traceContext';

export interface RUMConfig {
  // Fraction of sessions to capture, 0.0-1.0. Defaults to NEXT_PUBLIC_RUM_SAMPLE_RATE
  // if set, else 1.0 (every session) - matching Datadog's sessionSampleRate model:
  // the sampling decision is made once per session, not per event, so a sampled-in
  // session sends all its data and a sampled-out session sends none at all. Without
  // this lever, RUM ingest cost/volume scales linearly with traffic with no way to
  // control it short of removing the SDK entirely.
  sampleRate?: number;
}

export function initRUM(config: RUMConfig = {}) {
  if (typeof window === 'undefined') return;
  if ((window as any).__PULSETRACE_RUM_INIT__) return;
  (window as any).__PULSETRACE_RUM_INIT__ = true;

  const envRate = process.env.NEXT_PUBLIC_RUM_SAMPLE_RATE;
  const rawRate = config.sampleRate ?? (envRate !== undefined ? parseFloat(envRate) : 1.0);
  const sampleRate = Number.isFinite(rawRate) ? Math.min(Math.max(rawRate, 0), 1) : 1.0;

  // Sampled-out sessions register nothing: no listeners, no timers, no network
  // calls - genuinely zero overhead/ingest, not just "collect but don't send."
  if (Math.random() >= sampleRate) return;

  const sessionId = Math.random().toString(36).substring(2, 15);
  const events: any[] = [];
  let isFlushing = false;

  // Public, RUM-only client token (scope 'rum'). Safe to embed in the bundle: it
  // can only attribute browser RUM to a tenant, never read data or ingest server
  // telemetry (enforced gateway-side). When unset, RUM lands in the default tenant.
  const clientToken = process.env.NEXT_PUBLIC_RUM_CLIENT_TOKEN;

  const flush = async () => {
    if (events.length === 0 || isFlushing) return;
    isFlushing = true;
    const batch = [...events];
    events.length = 0;

    try {
      const headers: Record<string, string> = {
        'Content-Type': 'application/json',
        'traceparent': getTraceparentHeader(),
      };
      if (clientToken) headers['Authorization'] = `Bearer ${clientToken}`;
      await fetch('/api/v1/rum/ingest', {
        method: 'POST',
        headers,
        body: JSON.stringify(batch),
        keepalive: true
      });
    } catch (e) {
      console.error("PulseTrace RUM failed to ingest:", e);
    } finally {
      isFlushing = false;
    }
  };

  const enqueue = (event: any) => {
    const { traceId, spanId } = getRUMTraceContext();
    events.push({
      session_id: sessionId,
      path: window.location.pathname,
      user_agent: navigator.userAgent,
      trace_id: traceId,
      span_id: spanId,
      ...event
    });
    if (events.length >= 10) flush();
  };

  // 1. Page Views - each page view gets a fresh trace_id, shared with every API
  // call fetchWithAuth makes while that page is active (see lib/traceContext.ts).
  enqueue({ type: 'page_view' });
  let lastPath = window.location.pathname;
  setInterval(() => {
    if (lastPath !== window.location.pathname) {
      lastPath = window.location.pathname;
      rotateRUMTrace();
      enqueue({ type: 'page_view' });
    }
  }, 1000);

  // 2. Unhandled Errors
  window.addEventListener('error', (event) => {
    enqueue({
      type: 'error',
      error_msg: event.message,
      error_stack: event.error?.stack || 'No stack trace available'
    });
  });
  window.addEventListener('unhandledrejection', (event) => {
    enqueue({
      type: 'error',
      error_msg: event.reason?.message || String(event.reason),
      error_stack: event.reason?.stack || 'No stack trace available'
    });
  });

  // 3. Web Vitals via the native PerformanceObserver API
  try {
    const observer = new PerformanceObserver((list) => {
      for (const entry of list.getEntries()) {
        if (entry.entryType === 'largest-contentful-paint') {
          enqueue({ type: 'web_vitals', metric_name: 'LCP', metric_value: entry.startTime });
        }
        if (entry.entryType === 'layout-shift') {
          // CLS
          enqueue({ type: 'web_vitals', metric_name: 'CLS', metric_value: (entry as any).value });
        }
      }
    });
    observer.observe({ type: 'largest-contentful-paint', buffered: true });
    observer.observe({ type: 'layout-shift', buffered: true });
  } catch (e) {
    // Ignore if PerformanceObserver unsupported
  }

  // Periodic flush
  setInterval(flush, 5000);
  window.addEventListener('visibilitychange', () => {
    if (document.visibilityState === 'hidden') flush();
  });
}
