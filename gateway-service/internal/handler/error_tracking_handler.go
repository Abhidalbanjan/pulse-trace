package handler

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/pulsetrace/shared/models"
)

// ErrorTrackingHandler groups trace errors (StatusCode = STATUS_CODE_ERROR) from ClickHouse
// into fingerprinted issues, and persists a resolve/mute triage workflow for them in Postgres.
type ErrorTrackingHandler struct {
	ch *clickHouseClient
	db *sql.DB

	// alerts is optional; when set, the regression worker pages via the logs
	// topic (→ alert → correlation → notification), reusing the same pipeline an
	// application ERROR uses. nil disables paging (results are still computed).
	alerts LogPublisher
	// alerted is edge-trigger state (tenant\x00fingerprint) so a new/regressed
	// group pages once, not every scan. Only the worker goroutine touches it.
	alerted map[string]bool
}

func NewErrorTrackingHandler(clickhouseURL string, db *sql.DB) *ErrorTrackingHandler {
	return &ErrorTrackingHandler{ch: &clickHouseClient{URL: clickhouseURL}, db: db, alerted: make(map[string]bool)}
}

// WithAlertPublisher wires the error-regression→alert path. Call before StartRegressionWorker.
func (h *ErrorTrackingHandler) WithAlertPublisher(p LogPublisher) *ErrorTrackingHandler {
	h.alerts = p
	return h
}

// fingerprint derives a stable 16-char id for an error group from its identity
// fields. tenant is part of the hash so two tenants that happen to share a
// service/operation/message never collide onto the same error_groups row — which
// would otherwise leak one tenant's triage state (resolved/muted) to another.
func fingerprint(tenant, service, operation, message string) string {
	sum := sha256.Sum256([]byte(tenant + "|" + service + "|" + operation + "|" + message))
	return hex.EncodeToString(sum[:])[:16]
}

type errorGroupRow struct {
	Fingerprint   string `json:"fingerprint"`
	Service       string `json:"service"`
	Operation     string `json:"operation"`
	Message       string `json:"message"`        // normalized template - stable grouping key
	SampleMessage string `json:"sample_message"` // most recent raw message, for display
	Occurrences   int64  `json:"occurrences"`
	FirstSeen     string `json:"first_seen"`
	LastSeen      string `json:"last_seen"`
	SampleTraceID string `json:"sample_trace_id"`
	Status        string `json:"status"`
	ResolvedBy    string `json:"resolved_by,omitempty"`
	ResolvedAt    string `json:"resolved_at,omitempty"`
	Assignee      string `json:"assignee,omitempty"`
	SnoozedUntil  string `json:"snoozed_until,omitempty"`
}

// triageStatuses is the closed set a client may set a group to. Anything else is
// rejected so the status column stays a small, meaningful enum.
var triageStatuses = map[string]bool{"open": true, "resolved": true, "muted": true, "snoozed": true}

// effectiveStatus resolves a group's stored status into what it actually is right
// now. A 'snoozed' group whose snooze window has elapsed (or was never given an
// expiry) silently reads as 'open' again, so an operator's "remind me later"
// auto-returns to the queue without a background job flipping the row. Pure so
// the snooze-expiry rule is unit-tested without a clock or DB. All other statuses
// pass through unchanged.
func effectiveStatus(status string, snoozedUntil, now time.Time) string {
	if status == "snoozed" && (snoozedUntil.IsZero() || !snoozedUntil.After(now)) {
		return "open"
	}
	return status
}

// normalizedMessageExpr strips high-cardinality dynamic values (UUIDs, quoted literals,
// numbers - user IDs, order IDs, timestamps embedded in the message, etc.) out of the raw
// error message before grouping. Without this, two errors that differ only by e.g. a record
// ID ("user 4821 not found" vs "user 9053 not found") would never fingerprint together,
// flooding Error Tracking with one "issue" per occurrence instead of one per root cause.
const normalizedMessageExpr = `
	replaceRegexpAll(
		replaceRegexpAll(
			replaceRegexpAll(
				replaceRegexpAll(StatusMessage,
					'[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}', '<uuid>'),
				'''[^'']*''', '<str>'),
			'"[^"]*"', '<str>'),
		'[0-9]+', '<num>'
	)
`

// ListErrorGroups groups error spans from the last 7 days by (service, operation, normalized
// message template), then merges in each group's triage status from Postgres.
func (h *ErrorTrackingHandler) ListErrorGroups(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	query := `
		SELECT
			ServiceName as service,
			SpanName as operation,
			` + normalizedMessageExpr + ` as message,
			argMax(StatusMessage, Timestamp) as sample_message,
			count() as occurrences,
			min(Timestamp) as first_seen,
			max(Timestamp) as last_seen,
			argMax(TraceId, Timestamp) as sample_trace_id
		FROM pulsetrace.otel_traces
		WHERE ` + tenantClause + ` AND StatusCode = 'STATUS_CODE_ERROR' AND Timestamp >= now() - INTERVAL 7 DAY
		GROUP BY service, operation, message
		ORDER BY last_seen DESC
		LIMIT 200
		FORMAT JSON
	`

	resp, err := h.ch.queryScoped(tenantFromRequest(r), query, nil)
	if err != nil {
		log.Printf("[ErrorTrackingHandler] ClickHouse query failed: %v", err)
		http.Error(w, "failed to query analytics engine", http.StatusInternalServerError)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"data": []errorGroupRow{}})
		return
	}
	if resp.StatusCode != http.StatusOK {
		http.Error(w, "analytics engine returned error", http.StatusInternalServerError)
		return
	}

	type chResult struct {
		Data []struct {
			Service       string `json:"service"`
			Operation     string `json:"operation"`
			Message       string `json:"message"`
			SampleMessage string `json:"sample_message"`
			// ClickHouse's native JSON format serializes UInt64 as a JSON string
			// (avoids precision loss for values >2^53 in JS clients), not a number
			// literal - decoding straight into int64 fails the instant any error
			// data exists at all.
			Occurrences   string `json:"occurrences"`
			FirstSeen     string `json:"first_seen"`
			LastSeen      string `json:"last_seen"`
			SampleTraceID string `json:"sample_trace_id"`
		} `json:"data"`
	}
	var result chResult
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		log.Printf("[ErrorTrackingHandler] failed to decode ClickHouse response: %v", err)
		http.Error(w, "failed to decode analytics response", http.StatusInternalServerError)
		return
	}

	groups := make([]errorGroupRow, 0, len(result.Data))
	fingerprints := make([]string, 0, len(result.Data))
	for _, d := range result.Data {
		fp := fingerprint(tenantFromRequest(r), d.Service, d.Operation, d.Message)
		fingerprints = append(fingerprints, fp)
		occurrences, _ := strconv.ParseInt(d.Occurrences, 10, 64)
		groups = append(groups, errorGroupRow{
			Fingerprint:   fp,
			Service:       d.Service,
			Operation:     d.Operation,
			Message:       d.Message,
			SampleMessage: d.SampleMessage,
			Occurrences:   occurrences,
			FirstSeen:     d.FirstSeen,
			LastSeen:      d.LastSeen,
			SampleTraceID: d.SampleTraceID,
			Status:        "open",
		})
	}

	if h.db != nil && len(fingerprints) > 0 {
		statusByFP, err := h.loadTriageStatus(fingerprints)
		if err != nil {
			log.Printf("[ErrorTrackingHandler] failed to load triage status: %v", err)
		} else {
			now := time.Now().UTC()
			for i := range groups {
				if s, ok := statusByFP[groups[i].Fingerprint]; ok {
					// Present the effective status (an expired snooze reads as open)
					// so the list and its filters agree with what the operator sees.
					groups[i].Status = effectiveStatus(s.status, s.snoozedUntil, now)
					groups[i].ResolvedBy = s.resolvedBy
					groups[i].ResolvedAt = s.resolvedAt
					groups[i].Assignee = s.assignee
					if !s.snoozedUntil.IsZero() {
						groups[i].SnoozedUntil = s.snoozedUntil.UTC().Format(time.RFC3339)
					}
				}
			}
		}
	}

	// Optional triage filters, applied over the effective status so they agree
	// with what the operator sees (an expired snooze filters as 'open'). Empty
	// filters are no-ops, keeping the default listing unchanged.
	if fStatus := r.URL.Query().Get("status"); fStatus != "" {
		groups = filterGroups(groups, func(g errorGroupRow) bool { return g.Status == fStatus })
	}
	if fAssignee := r.URL.Query().Get("assignee"); fAssignee != "" {
		groups = filterGroups(groups, func(g errorGroupRow) bool { return g.Assignee == fAssignee })
	}

	_ = json.NewEncoder(w).Encode(map[string]interface{}{"data": groups})
}

// filterGroups returns the groups satisfying keep, preserving order. Returns a
// non-nil empty slice so the JSON response stays `[]`, never `null`.
func filterGroups(groups []errorGroupRow, keep func(errorGroupRow) bool) []errorGroupRow {
	out := make([]errorGroupRow, 0, len(groups))
	for _, g := range groups {
		if keep(g) {
			out = append(out, g)
		}
	}
	return out
}

type triageState struct {
	status       string
	resolvedBy   string
	resolvedAt   string
	assignee     string
	snoozedUntil time.Time
}

func (h *ErrorTrackingHandler) loadTriageStatus(fingerprints []string) (map[string]triageState, error) {
	placeholders := make([]string, len(fingerprints))
	args := make([]interface{}, len(fingerprints))
	for i, fp := range fingerprints {
		placeholders[i] = "$" + itoa(i+1)
		args[i] = fp
	}

	rows, err := h.db.Query(
		"SELECT fingerprint, status, COALESCE(resolved_by, ''), COALESCE(resolved_at::text, ''), COALESCE(assignee, ''), snoozed_until FROM error_groups WHERE fingerprint IN ("+strings.Join(placeholders, ",")+")",
		args...,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make(map[string]triageState)
	for rows.Next() {
		var fp, status, resolvedBy, resolvedAt, assignee string
		var snoozedUntil sql.NullTime
		if err := rows.Scan(&fp, &status, &resolvedBy, &resolvedAt, &assignee, &snoozedUntil); err != nil {
			continue
		}
		out[fp] = triageState{status: status, resolvedBy: resolvedBy, resolvedAt: resolvedAt, assignee: assignee, snoozedUntil: snoozedUntil.Time}
	}
	return out, nil
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	digits := ""
	for n > 0 {
		digits = string(rune('0'+n%10)) + digits
		n /= 10
	}
	return digits
}

type triageRequest struct {
	Service    string `json:"service"`
	Operation  string `json:"operation"`
	Message    string `json:"message"`
	ResolvedBy string `json:"resolved_by"`
}

func (h *ErrorTrackingHandler) setStatus(w http.ResponseWriter, r *http.Request, status string) {
	if h.db == nil {
		http.Error(w, "database unavailable", http.StatusServiceUnavailable)
		return
	}
	fp := r.PathValue("fingerprint")
	if fp == "" {
		http.Error(w, "missing fingerprint", http.StatusBadRequest)
		return
	}

	var req triageRequest
	_ = json.NewDecoder(r.Body).Decode(&req)

	now := time.Now().UTC()
	var resolvedAt interface{}
	var resolvedBy interface{}
	if status == "resolved" {
		resolvedAt = now
		if req.ResolvedBy != "" {
			resolvedBy = req.ResolvedBy
		}
	}

	_, err := h.db.Exec(`
		INSERT INTO error_groups (fingerprint, tenant_id, service, operation, message, status, resolved_by, resolved_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, now())
		ON CONFLICT (fingerprint) DO UPDATE SET
			status = EXCLUDED.status,
			resolved_by = EXCLUDED.resolved_by,
			resolved_at = EXCLUDED.resolved_at,
			updated_at = now()
	`, fp, tenantFromRequest(r), req.Service, req.Operation, req.Message, status, resolvedBy, resolvedAt)
	if err != nil {
		log.Printf("[ErrorTrackingHandler] failed to update triage status: %v", err)
		http.Error(w, "failed to update error group", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"fingerprint": fp, "status": status})
}

func (h *ErrorTrackingHandler) ResolveErrorGroup(w http.ResponseWriter, r *http.Request) {
	h.setStatus(w, r, "resolved")
}

func (h *ErrorTrackingHandler) MuteErrorGroup(w http.ResponseWriter, r *http.Request) {
	h.setStatus(w, r, "muted")
}

func (h *ErrorTrackingHandler) ReopenErrorGroup(w http.ResponseWriter, r *http.Request) {
	h.setStatus(w, r, "open")
}

type triagePatchRequest struct {
	Service   string `json:"service"`
	Operation string `json:"operation"`
	Message   string `json:"message"`
	// Only the fields present are applied. Status must be one of triageStatuses.
	// SnoozedUntil (RFC3339) is required when Status == "snoozed"; ignored
	// otherwise (and cleared when moving to any non-snoozed status).
	Status       *string `json:"status,omitempty"`
	Assignee     *string `json:"assignee,omitempty"`
	SnoozedUntil *string `json:"snoozed_until,omitempty"`
	ResolvedBy   string  `json:"resolved_by,omitempty"`
}

// UpdateErrorGroup is the unified triage mutation: set status, assign an owner,
// and/or snooze — in one PATCH, only touching the fields the client sends. It
// completes the workflow the resolve/mute/reopen POSTs started (those remain for
// backward compatibility). Upserts the row so a first-ever action on a group works.
//
//	PATCH /api/v1/errors/groups/{fingerprint}
func (h *ErrorTrackingHandler) UpdateErrorGroup(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		http.Error(w, "database unavailable", http.StatusServiceUnavailable)
		return
	}
	fp := r.PathValue("fingerprint")
	if fp == "" {
		http.Error(w, "missing fingerprint", http.StatusBadRequest)
		return
	}

	var req triagePatchRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if req.Status == nil && req.Assignee == nil && req.SnoozedUntil == nil {
		http.Error(w, "no fields to update", http.StatusBadRequest)
		return
	}

	// Build a targeted UPSERT. The VALUES row seeds a fresh group with sensible
	// defaults; the ON CONFLICT SET lists ONLY the columns this request touches —
	// and references the very same bind params ($6..$10) as VALUES, so nothing is
	// string-interpolated and an assignee change never clobbers status (or vice
	// versa). Columns absent from SET keep their existing value on an update.
	now := time.Now().UTC()
	sets := []string{"updated_at = now()"}

	insStatus := "open"                                              // $6
	var insResolvedBy, insResolvedAt, insAssignee, insSnooze interface{} // $7 $8 $9 $10

	if req.Status != nil {
		status := *req.Status
		if !triageStatuses[status] {
			http.Error(w, "invalid status", http.StatusBadRequest)
			return
		}
		insStatus = status
		sets = append(sets, "status = $6")

		switch status {
		case "resolved":
			insResolvedAt = now
			sets = append(sets, "resolved_at = $8", "snoozed_until = $10") // clear snooze ($10 nil)
			if req.ResolvedBy != "" {
				insResolvedBy = req.ResolvedBy
				sets = append(sets, "resolved_by = $7")
			}
		case "snoozed":
			until, ok := parseSnoozeUntil(req.SnoozedUntil, now)
			if !ok {
				http.Error(w, "snoozed status requires a future snoozed_until (RFC3339)", http.StatusBadRequest)
				return
			}
			insSnooze = until
			sets = append(sets, "snoozed_until = $10")
		default: // open | muted → clear any snooze ($10 nil)
			sets = append(sets, "snoozed_until = $10")
		}
	}

	if req.Assignee != nil {
		if *req.Assignee != "" {
			insAssignee = *req.Assignee
		}
		sets = append(sets, "assignee = $9") // $9 nil when cleared
	}

	// Standalone snooze extension without a status change (e.g. "give me one more day").
	if req.SnoozedUntil != nil && req.Status == nil {
		until, ok := parseSnoozeUntil(req.SnoozedUntil, now)
		if !ok {
			http.Error(w, "snoozed_until must be a future RFC3339 timestamp", http.StatusBadRequest)
			return
		}
		insStatus = "snoozed"
		insSnooze = until
		sets = append(sets, "status = $6", "snoozed_until = $10")
	}

	query := `
		INSERT INTO error_groups (fingerprint, tenant_id, service, operation, message, status, resolved_by, resolved_at, assignee, snoozed_until, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, now())
		ON CONFLICT (fingerprint) DO UPDATE SET ` + strings.Join(sets, ", ")
	_, err := h.db.Exec(query,
		fp, tenantFromRequest(r), req.Service, req.Operation, req.Message,
		insStatus, insResolvedBy, insResolvedAt, insAssignee, insSnooze)
	if err != nil {
		log.Printf("[ErrorTrackingHandler] failed to patch error group: %v", err)
		http.Error(w, "failed to update error group", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"fingerprint": fp,
		"status":      effectiveStatus(insStatus, timeOrZero(insSnooze), now),
	})
}

// parseSnoozeUntil parses an RFC3339 snooze expiry and requires it to be in the
// future — a snooze that's already expired is meaningless (it would read as open
// immediately) and almost certainly a client bug, so we reject it rather than
// silently no-op.
func parseSnoozeUntil(raw *string, now time.Time) (time.Time, bool) {
	if raw == nil || *raw == "" {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339, *raw)
	if err != nil || !t.After(now) {
		return time.Time{}, false
	}
	return t.UTC(), true
}

func timeOrZero(v interface{}) time.Time {
	if t, ok := v.(time.Time); ok {
		return t
	}
	return time.Time{}
}

// GetErrorGroupTimeline returns the time-bucketed occurrence count for one error
// group, so the UI can render "when and how often" — is this issue spiking,
// steady, or already tailing off? The group is identified by its service /
// operation / normalized-message triple (passed as query params); the path
// fingerprint must match that triple, which both scopes the query and prevents a
// fabricated fingerprint from being paired with a mismatched identity.
//
//	GET /api/v1/errors/groups/{fingerprint}/timeline?service=&operation=&message=&interval=24h|7d
func (h *ErrorTrackingHandler) GetErrorGroupTimeline(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	fp := r.PathValue("fingerprint")
	service := r.URL.Query().Get("service")
	operation := r.URL.Query().Get("operation")
	message := r.URL.Query().Get("message")
	if service == "" || operation == "" {
		http.Error(w, "service and operation are required", http.StatusBadRequest)
		return
	}

	tenant := tenantFromRequest(r)
	if fp != fingerprint(tenant, service, operation, message) {
		http.Error(w, "fingerprint does not match the supplied group identity", http.StatusBadRequest)
		return
	}

	interval := r.URL.Query().Get("interval")
	bucketExpr, ok := metricIntervalToBucket[interval]
	if !ok {
		interval = "24h"
		bucketExpr = metricIntervalToBucket[interval]
	}
	sqlInterval := intervalToSQL[interval]

	query := `
		SELECT
			` + strings.ReplaceAll(bucketExpr, "TimeUnix", "Timestamp") + ` as time_bucket,
			count() as count
		FROM pulsetrace.otel_traces
		WHERE ` + tenantClause + ` AND StatusCode = 'STATUS_CODE_ERROR'
			AND ServiceName = {service:String} AND SpanName = {operation:String}
			AND ` + normalizedMessageExpr + ` = {message:String}
			AND Timestamp >= now() - INTERVAL ` + sqlInterval + `
		GROUP BY time_bucket
		ORDER BY time_bucket ASC
		FORMAT JSON
	`

	resp, err := h.ch.queryScoped(tenant, query, map[string]string{
		"service":   stringParam(service),
		"operation": stringParam(operation),
		"message":   stringParam(message),
	})
	if err != nil {
		log.Printf("[ErrorTrackingHandler] timeline query failed: %v", err)
		http.Error(w, "failed to query analytics engine", http.StatusInternalServerError)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		_, _ = w.Write([]byte(`{"data": []}`))
		return
	}
	if resp.StatusCode != http.StatusOK {
		http.Error(w, "analytics engine returned error", http.StatusInternalServerError)
		return
	}
	_, _ = io.Copy(w, resp.Body)
}

// classifyErrorGroup decides whether an observed error group warrants a page:
//   - "regression": a group a human marked resolved is occurring again after the
//     moment it was resolved — the single most important thing to be told about.
//   - "new": a group with no triage history that first appeared within the scan
//     window (so a long-standing group doesn't page the whole backlog on boot).
//   - "": muted groups, healthy resolved groups, and already-known open groups.
//
// It is pure so the paging decision — the heart of "notifies on regression" — is
// unit-tested without a live DB or ClickHouse.
func classifyErrorGroup(status string, resolvedAt, firstSeen, lastSeen, now time.Time, scanWindow time.Duration) string {
	switch status {
	case "muted":
		return ""
	case "resolved":
		if !resolvedAt.IsZero() && lastSeen.After(resolvedAt) {
			return "regression"
		}
		return ""
	case "": // no triage row yet
		if firstSeen.After(now.Add(-scanWindow)) {
			return "new"
		}
		return ""
	default: // "open" or any human-acknowledged state — already known
		return ""
	}
}

const (
	// regressionScanInterval is how often the worker looks for new/regressed
	// groups; regressionWindow bounds both the "recent errors" query and the
	// "first appeared recently" test for a new group.
	regressionScanInterval = 60 * time.Second
	regressionWindow       = 15 * time.Minute
)

// errGroupObservation is one error group seen in the recent-errors scan.
type errGroupObservation struct {
	service   string
	operation string
	message   string // normalized template
	sample    string // latest raw message, for the alert text
	firstSeen time.Time
	lastSeen  time.Time
	traceID   string
}

// StartRegressionWorker periodically scans each tenant's recent error groups and
// pages (once, edge-triggered) when a new group appears or a resolved group
// regresses. A regression additionally auto-reopens the group so it re-enters the
// triage queue. Safe to start without an alert publisher (it just won't page).
func (h *ErrorTrackingHandler) StartRegressionWorker() {
	if h.db == nil {
		log.Println("[ErrorRegressionWorker] no DB; regression detection disabled")
		return
	}
	go func() {
		ticker := time.NewTicker(regressionScanInterval)
		defer ticker.Stop()
		for range ticker.C {
			h.runRegressionScan()
		}
	}()
}

func (h *ErrorTrackingHandler) runRegressionScan() {
	tenants, err := h.distinctTriageTenants()
	if err != nil {
		log.Printf("[ErrorRegressionWorker] failed to list tenants: %v", err)
		return
	}
	now := time.Now().UTC()
	for _, tenant := range tenants {
		observations, err := h.recentErrorGroups(tenant)
		if err != nil {
			log.Printf("[ErrorRegressionWorker] recent-groups query failed for tenant %q: %v", tenant, err)
			continue
		}
		triage := h.triageStates(tenant)
		for _, o := range observations {
			fp := fingerprint(tenant, o.service, o.operation, o.message)
			st := triage[fp] // zero value (status "") when no triage row
			kind := classifyErrorGroup(st.status, st.resolvedAt, o.firstSeen, o.lastSeen, now, regressionWindow)
			key := tenant + "\x00" + fp
			if kind == "" {
				delete(h.alerted, key) // condition cleared → re-arm for the next time
				continue
			}
			if h.alerted[key] {
				continue
			}
			h.alerted[key] = true
			h.pageErrorGroup(tenant, o, kind)
			if kind == "regression" {
				h.autoReopen(tenant, fp, o)
			}
		}
	}
}

// distinctTriageTenants lists tenants that have any triage history — the set the
// regression worker fans out over (a resolved group necessarily has a row here).
func (h *ErrorTrackingHandler) distinctTriageTenants() ([]string, error) {
	rows, err := h.db.Query("SELECT DISTINCT tenant_id FROM error_groups")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var tenant string
		if err := rows.Scan(&tenant); err == nil && tenant != "" {
			out = append(out, tenant)
		}
	}
	return out, nil
}

// recentErrorGroups returns the error groups seen in the recent window for one
// tenant, with their first/last-seen bounds — enough to classify new/regression.
func (h *ErrorTrackingHandler) recentErrorGroups(tenant string) ([]errGroupObservation, error) {
	query := `
		SELECT
			ServiceName as service,
			SpanName as operation,
			` + normalizedMessageExpr + ` as message,
			argMax(StatusMessage, Timestamp) as sample_message,
			min(Timestamp) as first_seen,
			max(Timestamp) as last_seen,
			argMax(TraceId, Timestamp) as sample_trace_id
		FROM pulsetrace.otel_traces
		WHERE ` + tenantClause + ` AND StatusCode = 'STATUS_CODE_ERROR' AND Timestamp >= now() - INTERVAL 7 DAY
		GROUP BY service, operation, message
		ORDER BY last_seen DESC
		LIMIT 500
		FORMAT JSON
	`
	resp, err := h.ch.queryScoped(tenant, query, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, nil
	}

	var result struct {
		Data []struct {
			Service       string `json:"service"`
			Operation     string `json:"operation"`
			Message       string `json:"message"`
			SampleMessage string `json:"sample_message"`
			FirstSeen     string `json:"first_seen"`
			LastSeen      string `json:"last_seen"`
			SampleTraceID string `json:"sample_trace_id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}
	out := make([]errGroupObservation, 0, len(result.Data))
	for _, d := range result.Data {
		out = append(out, errGroupObservation{
			service: d.Service, operation: d.Operation, message: d.Message, sample: d.SampleMessage,
			firstSeen: parseCHTime(d.FirstSeen), lastSeen: parseCHTime(d.LastSeen), traceID: d.SampleTraceID,
		})
	}
	return out, nil
}

// triageStates loads the typed triage state (status + resolved_at) for a tenant.
func (h *ErrorTrackingHandler) triageStates(tenant string) map[string]struct {
	status     string
	resolvedAt time.Time
} {
	out := map[string]struct {
		status     string
		resolvedAt time.Time
	}{}
	rows, err := h.db.Query("SELECT fingerprint, status, resolved_at FROM error_groups WHERE tenant_id = $1", tenant)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var fp, status string
		var resolvedAt sql.NullTime
		if err := rows.Scan(&fp, &status, &resolvedAt); err != nil {
			continue
		}
		out[fp] = struct {
			status     string
			resolvedAt time.Time
		}{status: status, resolvedAt: resolvedAt.Time}
	}
	return out
}

// autoReopen flips a regressed group back to 'open' so it re-enters the triage
// queue (a resolved issue that came back is, by definition, not resolved).
func (h *ErrorTrackingHandler) autoReopen(tenant, fp string, o errGroupObservation) {
	_, err := h.db.Exec(`
		INSERT INTO error_groups (fingerprint, tenant_id, service, operation, message, status, resolved_by, resolved_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, 'open', NULL, NULL, now())
		ON CONFLICT (fingerprint) DO UPDATE SET status = 'open', resolved_by = NULL, resolved_at = NULL, updated_at = now()
	`, fp, tenant, o.service, o.operation, o.message)
	if err != nil {
		log.Printf("[ErrorRegressionWorker] auto-reopen failed for %s: %v", fp, err)
	}
}

// pageErrorGroup emits an ERROR log for the group, routed through the standard
// logs→alert→correlation→notification pipeline (same path an app error takes).
func (h *ErrorTrackingHandler) pageErrorGroup(tenant string, o errGroupObservation, kind string) {
	if h.alerts == nil {
		return
	}
	headline := "New error group"
	if kind == "regression" {
		headline = "Error regression (resolved issue recurred)"
	}
	msg := o.sample
	if msg == "" {
		msg = o.message
	}
	entry := &models.LogEntry{
		ID:          uuid.NewString(),
		TenantID:    tenant,
		ServiceName: o.service,
		Level:       models.LogLevelError,
		Message:     headline + ": " + o.operation + " — " + msg,
		TraceID:     o.traceID,
		Timestamp:   time.Now().UTC(),
		CreatedAt:   time.Now().UTC(),
	}
	if err := h.alerts.PublishBatch(context.Background(), "logs", []*models.LogEntry{entry}); err != nil {
		log.Printf("[ErrorRegressionWorker] failed to publish %s alert: %v", kind, err)
	}
}

// parseCHTime parses ClickHouse's default datetime rendering ("2006-01-02
// 15:04:05[.000]") as UTC. A parse failure yields the zero time, which
// classifyErrorGroup treats conservatively (never a spurious page).
func parseCHTime(s string) time.Time {
	s = strings.TrimSpace(s)
	for _, layout := range []string{"2006-01-02 15:04:05.000", "2006-01-02 15:04:05"} {
		if ts, err := time.Parse(layout, s); err == nil {
			return ts.UTC()
		}
	}
	return time.Time{}
}
