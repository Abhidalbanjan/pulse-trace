// Shared API types (ROAD_TO_100 · F0.4).
//
// The backend wraps most responses in a standard envelope (shared/models.APIResponse):
//   { success, data?, error?, meta? }
// These types make that contract explicit on the frontend so screens stop typing
// their state as `any[]` and stop hand-rolling `json.data || []`.

export interface PaginationMeta {
  page: number;
  page_size: number;
  total: number;
  total_pages: number;
}

/** The standard success/error envelope returned by most gateway endpoints. */
export interface ApiEnvelope<T> {
  success?: boolean;
  data?: T;
  error?: string;
  meta?: PaginationMeta;
}

/** A paginated envelope (data is a list, meta is present). */
export interface PaginatedEnvelope<T> extends ApiEnvelope<T[]> {
  meta?: PaginationMeta;
}

// ── Core domain types ───────────────────────────────────────────────────────────
// Grown as screens migrate onto the typed client; each replaces an inline `any`.

export interface Role {
  name: string;
  description: string;
  permissions: string[];
  is_system: boolean;
}

// ── Ingestion keys (ROAD_TO_100 · F4) ─────────────────────────────────────────
// Per-tenant keys agents/SDKs use to send telemetry. The server never returns a
// key's plaintext or hash after creation — only this non-secret metadata.

/** A key's scope. `ingest` is a secret server-side key; `rum` is a public,
 *  browser-embeddable token (see gateway auth.ScopeIngest/ScopeRUM). */
export type IngestionKeyScope = 'ingest' | 'rum';

export interface IngestionKey {
  id: string;
  name: string;
  /** Non-secret display prefix, e.g. `pt_ingest_ab12cd`. */
  key_prefix: string;
  tenant_id: string;
  tier: string;
  scope: IngestionKeyScope;
  created_at: string;
  last_used_at: string | null;
  /** May be in the *future*: a rotation schedules the old key's revocation after
   *  a grace window, during which it stays valid. Null = never revoked. */
  revoked_at: string | null;
  /** Set on a rotated-out key: the id of the successor key that replaced it. */
  replaced_by: string | null;
}

/** The one-time create/rotate response. `key` (plaintext) is returned exactly
 *  once and can never be retrieved again — the UI must reveal it immediately. */
export interface MintedIngestionKey {
  id: string;
  name: string;
  tenant_id: string;
  tier: string;
  scope: IngestionKeyScope;
  key_prefix: string;
  key: string;
  warning: string;
  // Present only on rotation:
  rotated_from?: string;
  grace_period?: string;
  old_key_valid_until?: string;
}
