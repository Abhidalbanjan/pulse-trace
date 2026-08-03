// Typed API client (ROAD_TO_100 · F0.4).
//
// A thin, typed layer over fetchWithAuth that:
//   • parses JSON and returns it typed as T (no more `res.json()` returning any),
//   • throws a typed ApiError (with HTTP status + server message) on non-2xx, so
//     callers have one consistent error to surface — no more `await res.text()`
//     ad hoc at every call site,
//   • unwraps the standard { success, data, error } envelope via the *Data helpers.
//
// Transport (auth header, 401→login redirect, trace context) stays in fetchWithAuth;
// this only adds typing and error/envelope handling on top.

import { fetchWithAuth } from '../api';
import type { ApiEnvelope, PaginationMeta } from './types';

export class ApiError extends Error {
  readonly status: number;
  constructor(message: string, status: number) {
    super(message);
    this.name = 'ApiError';
    this.status = status;
  }
}

type Body = unknown;

async function request<T>(method: string, path: string, body?: Body): Promise<T> {
  const init: RequestInit = { method };
  if (body !== undefined) init.body = JSON.stringify(body);

  const res = await fetchWithAuth(path, init);
  const text = await res.text();

  // Tolerate empty bodies (204/DELETE) and non-JSON error pages.
  let json: unknown;
  if (text) {
    try {
      json = JSON.parse(text);
    } catch {
      json = undefined;
    }
  }

  if (!res.ok) {
    const msg = errorMessage(json) ?? (text || `HTTP ${res.status}`);
    throw new ApiError(msg, res.status);
  }
  return json as T;
}

function errorMessage(json: unknown): string | undefined {
  if (json && typeof json === 'object') {
    const o = json as Record<string, unknown>;
    if (typeof o.error === 'string') return o.error;
    if (typeof o.message === 'string') return o.message;
  }
  return undefined;
}

/** Unwrap the standard envelope: returns data (or a fallback for empty lists). */
async function requestData<T>(method: string, path: string, body?: Body): Promise<T | undefined> {
  const env = await request<ApiEnvelope<T>>(method, path, body);
  // Endpoints that don't use the envelope (e.g. ClickHouse-backed { data }) still
  // fit ApiEnvelope, so this stays correct for both shapes.
  return env?.data;
}

export interface ListResult<T> {
  items: T[];
  meta?: PaginationMeta;
}

/** Unwrap a paginated envelope into a non-null list plus its meta. */
async function requestList<T>(method: string, path: string, body?: Body): Promise<ListResult<T>> {
  const env = await request<ApiEnvelope<T[]>>(method, path, body);
  return { items: env?.data ?? [], meta: env?.meta };
}

export const api = {
  // Raw: returns the parsed JSON typed as T (use for non-enveloped endpoints).
  get: <T>(path: string) => request<T>('GET', path),
  post: <T>(path: string, body?: Body) => request<T>('POST', path, body),
  put: <T>(path: string, body?: Body) => request<T>('PUT', path, body),
  patch: <T>(path: string, body?: Body) => request<T>('PATCH', path, body),
  del: <T>(path: string, body?: Body) => request<T>('DELETE', path, body),

  // Envelope-aware: unwrap { data } (single) or { data: [], meta } (list).
  getData: <T>(path: string) => requestData<T>('GET', path),
  postData: <T>(path: string, body?: Body) => requestData<T>('POST', path, body),
  list: <T>(path: string) => requestList<T>('GET', path),
} as const;
