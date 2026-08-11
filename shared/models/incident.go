package models

import "time"

// IncidentStatus represents the lifecycle state of an incident.
type IncidentStatus string

const (
	IncidentStatusOpen     IncidentStatus = "OPEN"
	IncidentStatusResolved IncidentStatus = "RESOLVED"
)

// Incident groups one or more related alerts into a single actionable event.
// The correlation engine creates incidents by clustering alerts that share
// a service dependency graph within a sliding time window.
type Incident struct {
	TenantID     string          `json:"tenant_id,omitempty" db:"tenant_id"`
	ID           string          `json:"id" db:"id"`
	Title        string          `json:"title" db:"title"`
	RootCause    string          `json:"root_cause" db:"root_cause"`
	Status       IncidentStatus  `json:"status" db:"status"`
	Severity     LogLevel        `json:"severity" db:"severity"` // highest alert level in the group
	ServiceNames []string        `json:"services" db:"-"`        // populated from incident_alerts join
	AlertCount   int             `json:"alert_count" db:"alert_count"`
	StartedAt    time.Time       `json:"started_at" db:"started_at"`
	ResolvedAt   *time.Time      `json:"resolved_at,omitempty" db:"resolved_at"`
	CreatedAt    time.Time       `json:"created_at" db:"created_at"`
	UpdatedAt    time.Time       `json:"updated_at" db:"updated_at"`
	Causal       *CausalAnalysis `json:"causal,omitempty" db:"-"` // AI-augmented root-cause inference
}

// CausalLink represents a single inferred causal edge in the incident graph:
// the failure in FromService is hypothesized to have caused the failure in
// ToService. Evidence describes why the link was inferred.
type CausalLink struct {
	FromService string    `json:"from_service"`
	ToService   string    `json:"to_service"`
	Evidence    string    `json:"evidence"`
	At          time.Time `json:"at"`
}

// Playbook lifecycle statuses.
//
// Which of these a playbook lands in is decided by the remediation policy
// (see shared/remediation) rather than by confidence alone: a high-confidence
// hypothesis under a manual-approval policy becomes PENDING_APPROVAL, not
// EXECUTING.
const (
	PlaybookSuggested       = "SUGGESTED"        // recorded only; confidence too low to act on
	PlaybookSuppressed      = "SUPPRESSED"       // remediation is switched off entirely
	PlaybookDryRun          = "DRY_RUN"          // planned and recorded, nothing was changed
	PlaybookPendingApproval = "PENDING_APPROVAL" // waiting on a human
	PlaybookRejected        = "REJECTED"         // a human declined it
	PlaybookExecuting       = "EXECUTING"
	PlaybookExecuted        = "EXECUTED"
	PlaybookFailed          = "FAILED"
)

// PlaybookAction represents a suggested, awaiting-approval, or executed
// recovery action.
type PlaybookAction struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Status      string `json:"status"` // one of the Playbook* constants above
	Output      string `json:"output,omitempty"`

	// DryRun records that this run was a plan, not a change. Kept separate
	// from Status because a dry run can still fail (the plan couldn't even be
	// computed), and "FAILED, and also nothing was touched" is a materially
	// different thing to show an on-call engineer than a failed real run.
	DryRun bool `json:"dry_run,omitempty"`

	// Approval audit trail. Who authorized a change to production, and when,
	// is the question an auditor asks first; recording it on the playbook
	// keeps the answer attached to the incident it belongs to.
	ApprovedBy string     `json:"approved_by,omitempty"`
	ApprovedAt *time.Time `json:"approved_at,omitempty"`
	RejectedBy string     `json:"rejected_by,omitempty"`
	RejectedAt *time.Time `json:"rejected_at,omitempty"`
}

// CausalAnalysis is the structured output of the causal-AI analyzer. It is
// computed asynchronously after incident upsert and stored on the incident
// row as JSONB.
type CausalAnalysis struct {
	Chain      []CausalLink     `json:"chain"`            // ordered causal links, upstream → downstream
	Narrative  string           `json:"narrative"`        // human-readable causal story
	RootCause  string           `json:"root_cause"`       // refined hypothesis (supersedes regex inference)
	Confidence float64          `json:"confidence"`       // 0.0 – 1.0
	Model      string           `json:"model"`            // analyzer identifier (e.g., "claude-opus-4-7", "rule-based")
	AnalyzedAt time.Time        `json:"analyzed_at"`
	Playbook   *PlaybookAction  `json:"playbook,omitempty"`  // suggested recovery playbook
	Grounding  *GroundingReport `json:"grounding,omitempty"` // hallucination-guardrail verdict (nil for pre-guardrail rows)
}

// GroundingReport records the outcome of validating an analyzer's output
// against the incident's actual evidence — the hallucination guardrail.
//
// An LLM can name a service that is not in the incident, invent a causal edge
// between two unrelated services, or report high confidence on a fabricated
// story. Before the narrative is shown to an on-call engineer it is checked
// against the real topology: any causal link touching a service the incident
// never involved is dropped, and confidence is capped when that happens. This
// report is what lets the UI mark a narrative "grounded" (every claim anchored
// to real evidence) versus "adjusted" (hallucinated claims were removed).
type GroundingReport struct {
	// Grounded is true when every causal link the analyzer produced referenced
	// only services present in the incident's evidence — nothing was dropped.
	Grounded bool `json:"grounded"`
	// UnknownServices are service names the analyzer referenced in the causal
	// chain that do not appear anywhere in the incident evidence. Sorted, deduped.
	UnknownServices []string `json:"unknown_services,omitempty"`
	// DroppedLinks is how many causal links were removed for referencing an
	// unknown service.
	DroppedLinks int `json:"dropped_links,omitempty"`
	// ConfidencePenalty is how much confidence was subtracted because the
	// output contained hallucinated references (original − final).
	ConfidencePenalty float64 `json:"confidence_penalty,omitempty"`
}

// IncidentAlert is the join record linking an alert to an incident.
type IncidentAlert struct {
	IncidentID  string    `json:"incident_id" db:"incident_id"`
	AlertID     string    `json:"alert_id" db:"alert_id"`
	ServiceName string    `json:"service" db:"service_name"`
	Level       LogLevel  `json:"level" db:"level"`
	Message     string    `json:"message" db:"message"`
	TriggeredAt time.Time `json:"triggered_at" db:"triggered_at"`
}

// IncidentTimelineEvent is a single entry in an incident's timeline.
type IncidentTimelineEvent struct {
	At          time.Time `json:"at"`
	EventType   string    `json:"event_type"` // alert_triggered, incident_opened, incident_resolved
	ServiceName string    `json:"service,omitempty"`
	Level       string    `json:"level,omitempty"`
	Description string    `json:"description"`
}

// IncidentQueryParams holds filter/pagination options for querying incidents.
type IncidentQueryParams struct {
	TenantID string `form:"tenant_id"`
	Status   string `form:"status"`
	Severity string `form:"severity"`
	Service  string `form:"service"`
	From     string `form:"from"`
	To       string `form:"to"`
	Page     int    `form:"page"`
	PageSize int    `form:"page_size"`
}
