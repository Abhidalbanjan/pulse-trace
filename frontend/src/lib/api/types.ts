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
