package handler

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// ErrorTrackingHandler groups trace errors (StatusCode = STATUS_CODE_ERROR) from ClickHouse
// into fingerprinted issues, and persists a resolve/mute triage workflow for them in Postgres.
type ErrorTrackingHandler struct {
	ch *clickHouseClient
	db *sql.DB
}

func NewErrorTrackingHandler(clickhouseURL string, db *sql.DB) *ErrorTrackingHandler {
	return &ErrorTrackingHandler{ch: &clickHouseClient{URL: clickhouseURL}, db: db}
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

	resp, err := h.ch.query(query, map[string]string{"tenant": tenantFromRequest(r)})
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
			for i := range groups {
				if s, ok := statusByFP[groups[i].Fingerprint]; ok {
					groups[i].Status = s.status
					groups[i].ResolvedBy = s.resolvedBy
					groups[i].ResolvedAt = s.resolvedAt
				}
			}
		}
	}

	_ = json.NewEncoder(w).Encode(map[string]interface{}{"data": groups})
}

type triageState struct {
	status     string
	resolvedBy string
	resolvedAt string
}

func (h *ErrorTrackingHandler) loadTriageStatus(fingerprints []string) (map[string]triageState, error) {
	placeholders := make([]string, len(fingerprints))
	args := make([]interface{}, len(fingerprints))
	for i, fp := range fingerprints {
		placeholders[i] = "$" + itoa(i+1)
		args[i] = fp
	}

	rows, err := h.db.Query(
		"SELECT fingerprint, status, COALESCE(resolved_by, ''), COALESCE(resolved_at::text, '') FROM error_groups WHERE fingerprint IN ("+strings.Join(placeholders, ",")+")",
		args...,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make(map[string]triageState)
	for rows.Next() {
		var fp, status, resolvedBy, resolvedAt string
		if err := rows.Scan(&fp, &status, &resolvedBy, &resolvedAt); err != nil {
			continue
		}
		out[fp] = triageState{status: status, resolvedBy: resolvedBy, resolvedAt: resolvedAt}
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
		INSERT INTO error_groups (fingerprint, service, operation, message, status, resolved_by, resolved_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, now())
		ON CONFLICT (fingerprint) DO UPDATE SET
			status = EXCLUDED.status,
			resolved_by = EXCLUDED.resolved_by,
			resolved_at = EXCLUDED.resolved_at,
			updated_at = now()
	`, fp, req.Service, req.Operation, req.Message, status, resolvedBy, resolvedAt)
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
