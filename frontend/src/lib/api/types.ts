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

// ── Incidents & self-healing remediation (ROAD_TO_100 · F1) ───────────────────
// Mirrors shared/models: Incident.Causal.Playbook is the human-in-the-loop
// remediation the UI drives via the playbook endpoints.

/** One inferred causal edge (upstream → downstream). */
export interface CausalLink {
  from_service: string;
  to_service: string;
  evidence: string;
  at?: string;
}

/** Playbook lifecycle — mirrors the models.Playbook* constants. */
export type PlaybookStatus =
  | 'SUGGESTED'
  | 'SUPPRESSED'
  | 'DRY_RUN'
  | 'PENDING_APPROVAL'
  | 'REJECTED'
  | 'EXECUTING'
  | 'EXECUTED'
  | 'FAILED';

/** A suggested / awaiting-approval / executed recovery action, with its audit trail. */
export interface PlaybookAction {
  name: string;
  description: string;
  status: PlaybookStatus;
  output?: string;
  dry_run?: boolean;
  approved_by?: string;
  approved_at?: string;
  rejected_by?: string;
  rejected_at?: string;
}

/**
 * Hallucination-guardrail verdict (mirrors shared/models.GroundingReport).
 * Present on analyses produced after the guardrail shipped; absent on older
 * rows. `grounded` is true when every causal link the analyzer produced
 * referenced a service that actually appears in the incident's evidence.
 */
export interface GroundingReport {
  grounded: boolean;
  unknown_services?: string[];
  dropped_links?: number;
  confidence_penalty?: number;
}

/** Structured causal-AI output attached to an incident. */
export interface CausalAnalysis {
  chain: CausalLink[];
  narrative: string;
  root_cause: string;
  confidence: number;
  model: string;
  analyzed_at?: string;
  playbook?: PlaybookAction;
  grounding?: GroundingReport | null;
}

/** One link in the causal-AI provider chain's health (mirrors causal.ProviderHealth). */
export interface CausalProviderHealth {
  name: string;
  healthy: boolean;
  failures: number;
  cooldown_remaining?: string;
}

/** Response of GET /api/v1/causal/providers — analyzer chain health. */
export interface CausalProviders {
  analyzer: string;
  llm_enabled: boolean;
  providers: CausalProviderHealth[];
}

export type IncidentSeverity = 'CRITICAL' | 'ERROR' | 'WARNING' | 'INFO' | string;

export interface Incident {
  id: string;
  tenant_id?: string;
  title: string;
  root_cause: string;
  status: string; // OPEN | RESOLVED
  severity: IncidentSeverity;
  services: string[];
  alert_count: number;
  started_at?: string;
  resolved_at?: string;
  created_at?: string;
  updated_at?: string;
  causal?: CausalAnalysis | null;
}

export interface IncidentTimelineEvent {
  at: string;
  event_type: string; // alert_triggered | incident_opened | incident_resolved
  service?: string;
  level?: string;
  description: string;
}

/** Current remediation posture — gates whether the UI offers an execute path.
 *  A control that silently does nothing is worse than an absent one. */
export interface RemediationPolicy {
  mode: string; // e.g. off | suggest | manual | auto
  confidence_threshold: number;
  execution_allowed: boolean;
}

// ── SLOs / error budgets / burn rate (ROAD_TO_100 · F2) ───────────────────────
// Mirrors shared/models/slo.go.

export type SLIType = 'availability' | 'latency' | string;
export type SLOStatus = 'healthy' | 'warning' | 'critical' | string;

export interface SLODefinition {
  id: string;
  service_name: string;
  slo_target: number; // e.g. 99.9
  sli_type: SLIType;
  window_days: number; // e.g. 30
  created_at?: string;
  updated_at?: string;
}

export interface SLOTrendPoint {
  at: string;
  sli_value: number;
}

/** Computed dashboard row: definition + current SLI + error budget + burn rate. */
export interface SLODashboardItem {
  definition: SLODefinition;
  current_sli: number;
  total_events: number;
  error_events: number;
  budget_total_min: number;
  budget_used_min: number;
  budget_remaining_pct: number; // 0–100
  burn_rate: number; // 1.0 = on-track
  status: SLOStatus;
  trend: SLOTrendPoint[];
  // Error-budget-burn forecast (SLOs · E4). forecast_burning is true only when
  // the budget is actually declining; the other fields are then present.
  forecast_burning: boolean;
  forecast_exhaust_at?: string;
  forecast_days_left?: number;
}

export interface SLOBudgetAlert {
  id: string;
  service_name: string;
  burn_rate: number;
  budget_remaining_pct: number;
  severity: string;
  message: string;
  triggered_at: string;
}

export interface CreateSLORequest {
  service_name: string;
  slo_target: number;
  sli_type?: SLIType;
  window_days?: number;
}

// ── Alerts (raw signals before correlation into incidents) ────────────────────
export interface Alert {
  id: string;
  tenant_id?: string;
  log_entry_id?: string;
  service: string;
  level: string; // CRITICAL | ERROR | WARNING | …
  message: string;
  trace_id?: string;
  triggered_at: string;
  created_at?: string;
}

// A set of near-identical alerts collapsed into one row (Alerts · E1). Members
// share a group key (service + level + a fingerprint of the message); Count and
// first/last-seen turn an alert storm into one scannable line.
export interface AlertGroup {
  key: string;
  service: string;
  level: string;
  sample: string;
  sample_id: string;
  count: number;
  first_seen: string;
  last_seen: string;
  instances?: Alert[];
}

// ── Anomaly-detection tuning (ROAD_TO_100 · F14) ──────────────────────────────
export interface AnomalyConfig {
  enabled: boolean;
  p99_multiplier: number;        // fire when p99 >= this × baseline
  error_rate_jump_points: number; // absolute % points above baseline
  min_error_rate: number;         // floor: error rate must also clear this %
  throughput_drop_ratio: number;  // fire when throughput <= this × baseline (0–1)
}

// ── Alert delivery channels (ROAD_TO_100 · F3) ────────────────────────────────
export type ChannelType = 'slack' | 'email' | 'pagerduty' | 'opsgenie' | 'webhook';

export interface NotificationChannel {
  id: string;
  tenant_id: string;
  name: string;
  type: ChannelType;
  // Non-secret config in clear; secret values are never returned — instead a
  // `<key>_set: "true"` presence flag indicates a secret is configured.
  config: Record<string, string>;
  enabled: boolean;
  created_at: string;
  updated_at: string;
}

// ── Shift-left deploy gates (ROAD_TO_100 · F5) ────────────────────────────────
export interface DeployGate {
  id: string;
  pr_number: number;
  title: string;
  author: string;
  repo: string;
  sha: string;
  decision: string; // APPROVE | BLOCK
  reason: string;
  pr_url: string;
  created_at: string;
}

// ── Tenant data deletion (ROAD_TO_100 · F19) ──────────────────────────────────
// The per-store result of a purge/close, doubling as a deletion certificate:
// each attempted store lands in steps[] (ok) or errors[].
export interface TenantPurgeResult {
  tenant_id: string;
  full: boolean; // true = account also closed, not just telemetry purged
  steps: string[];
  errors: string[];
}
