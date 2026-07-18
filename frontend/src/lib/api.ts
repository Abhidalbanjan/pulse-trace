import { getTraceparentHeader } from './traceContext';

// Generic fetch wrapper to inject JWT auth tokens
export async function fetchWithAuth(url: string, options: RequestInit = {}): Promise<Response> {
  const token = localStorage.getItem('pulse_token');

  const headers = new Headers(options.headers || {});

  if (token) {
    headers.set('Authorization', `Bearer ${token}`);
  }

  if (!headers.has('Content-Type') && !(options.body instanceof FormData)) {
    headers.set('Content-Type', 'application/json');
  }

  // W3C trace context so this request's backend span shares the RUM session's trace_id.
  if (!headers.has('traceparent')) {
    headers.set('traceparent', getTraceparentHeader());
  }

  const response = await fetch(url, {
    ...options,
    headers,
  });

  if (response.status === 401) {
    // Unauthorized: Token expired or invalid
    localStorage.removeItem('pulse_token');
    localStorage.removeItem('pulse_user');
    // Delete cookie as well by setting it to expire
    document.cookie = 'pulse_token=; expires=Thu, 01 Jan 1970 00:00:00 UTC; path=/;';
    window.location.href = '/login';
  }

  return response;
}
