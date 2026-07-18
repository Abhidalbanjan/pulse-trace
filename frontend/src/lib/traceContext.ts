// Generates real W3C trace-context ids for the browser session so that RUM events
// and the API calls made during the same page view share one trace_id. Backend
// services already extract an incoming `traceparent` header (shared/middleware/tracing.go),
// so this trace_id becomes the actual OTel trace id recorded for those requests -
// giving genuine RUM <-> APM correlation, not a fabricated link.

function randomHex(byteLength: number): string {
  const arr = new Uint8Array(byteLength);
  if (typeof crypto !== 'undefined' && crypto.getRandomValues) {
    crypto.getRandomValues(arr);
  } else {
    for (let i = 0; i < byteLength; i++) arr[i] = Math.floor(Math.random() * 256);
  }
  return Array.from(arr).map(b => b.toString(16).padStart(2, '0')).join('');
}

let currentTraceId = randomHex(16); // 32 hex chars
let currentSpanId = randomHex(8); // 16 hex chars

export function getRUMTraceContext(): { traceId: string; spanId: string } {
  return { traceId: currentTraceId, spanId: currentSpanId };
}

// Call when a new "unit of user activity" starts (e.g. page navigation) so
// requests/events from here on get a fresh trace, matching how a new page view
// is a new root operation from the backend's perspective.
export function rotateRUMTrace(): void {
  currentTraceId = randomHex(16);
  currentSpanId = randomHex(8);
}

export function getTraceparentHeader(): string {
  return `00-${currentTraceId}-${currentSpanId}-01`;
}
