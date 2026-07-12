export function initRUM() {
  if (typeof window === 'undefined') return;
  if ((window as any).__PULSETRACE_RUM_INIT__) return;
  (window as any).__PULSETRACE_RUM_INIT__ = true;

  const sessionId = Math.random().toString(36).substring(2, 15);
  const events: any[] = [];
  let isFlushing = false;

  const flush = async () => {
    if (events.length === 0 || isFlushing) return;
    isFlushing = true;
    const batch = [...events];
    events.length = 0;
    
    try {
      await fetch('/api/v1/rum/ingest', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
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
    events.push({
      session_id: sessionId,
      path: window.location.pathname,
      user_agent: navigator.userAgent,
      ...event
    });
    if (events.length >= 10) flush();
  };

  // 1. Page Views
  enqueue({ type: 'page_view' });
  let lastPath = window.location.pathname;
  setInterval(() => {
    if (lastPath !== window.location.pathname) {
      lastPath = window.location.pathname;
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

  // 3. Web Vitals (Simulated for this demo, or we can use native PerformanceObserver)
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
