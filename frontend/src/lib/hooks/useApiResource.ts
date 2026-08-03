"use client";

// useApiResource (ROAD_TO_100 · F0.4).
//
// The shared data-fetching + live-update primitive. Replaces the repeated
// `useState<any[]>() + useEffect(fetch) + loading/error` boilerplate across
// screens with one typed hook, and gives every screen optional polling (the
// "shared live-update primitive" the platform needs for tail/streaming views).
//
// Pair it with the typed client (`api.*`) as the fetcher and <StateBoundary> for
// rendering the loading/error/empty states it returns.

import { useCallback, useEffect, useRef, useState } from 'react';
import { ApiError } from '../api/client';

export interface ApiResource<T> {
  data: T | null;
  error: string | null;
  loading: boolean;
  /** Re-run the fetcher on demand (e.g. after a mutation); shows the loading state. */
  refetch: () => Promise<void>;
  /** Optimistically set local data (e.g. after an inline edit). */
  setData: (next: T | null) => void;
}

export interface UseApiResourceOptions {
  /** Re-fetch whenever this changes — e.g. an id/route param the fetcher closes over. */
  key?: string | number;
  /** Poll every N ms while mounted (the live-update primitive). Omit for one-shot. */
  pollMs?: number;
  /** Skip fetching until true (e.g. waiting on an id/param). */
  enabled?: boolean;
}

/**
 * Fetch a resource with typed data, error, and loading state.
 *
 * @param fetcher returns the resource; typically `() => api.getData<T>(path)`.
 * @param options re-fetch key, polling interval, enabled gate.
 */
export function useApiResource<T>(
  fetcher: () => Promise<T>,
  options: UseApiResourceOptions = {},
): ApiResource<T> {
  const { key, pollMs, enabled = true } = options;
  const [data, setData] = useState<T | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState<boolean>(enabled);

  // Always call the latest fetcher without making it a dependency (callers pass an
  // inline arrow that changes each render). The ref is updated after render, not
  // during, to stay pure.
  const fetcherRef = useRef(fetcher);
  useEffect(() => {
    fetcherRef.current = fetcher;
  });

  // Guard against setting state after unmount / out-of-order responses.
  const mountedRef = useRef(false);
  useEffect(() => {
    mountedRef.current = true;
    return () => {
      mountedRef.current = false;
    };
  }, []);

  // load manages its own loading flag so a poll tick can refresh silently and we
  // never call setState synchronously in an effect body.
  const load = useCallback(
    async (silent = false) => {
      if (!enabled) return;
      if (!silent && mountedRef.current) setLoading(true);
      try {
        const result = await fetcherRef.current();
        if (!mountedRef.current) return;
        setData(result);
        setError(null);
      } catch (err) {
        if (!mountedRef.current) return;
        setError(err instanceof ApiError || err instanceof Error ? err.message : 'Request failed');
      } finally {
        if (mountedRef.current) setLoading(false);
      }
    },
    // `key` is a deliberate cache-buster: it isn't read in the body, but changing
    // it must re-create load so the effect re-fetches (e.g. a route param changed).
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [enabled, key],
  );

  useEffect(() => {
    if (!enabled) return;
    void load();
    if (!pollMs) return;
    // Poll silently so streaming/tail views don't flash their loading state.
    const id = setInterval(() => void load(true), pollMs);
    return () => clearInterval(id);
  }, [load, pollMs, enabled]);

  const refetch = useCallback(() => load(false), [load]);

  return { data, error, loading, refetch, setData };
}
