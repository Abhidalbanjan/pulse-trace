package handler

// RUM session timeline (RUM · E1).
//
// The sessions list answers "which sessions were bad"; this answers "what
// happened in one". GET /api/v1/rum/sessions/{id} returns the session's events
// (navigations, web-vitals, JS errors) in order, with offsets from session
// start, so support/eng can reconstruct a user's journey up to the moment it
// broke — a lightweight replay without recording the DOM. The ordering/summary
// logic is pure so it's unit-tested without ClickHouse.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"sort"
	"time"
)

// SessionEventInput is one raw rum_events row for a session.
type SessionEventInput struct {
	Type        string
	Path        string
	MetricName  string
	MetricValue float64
	ErrorMsg    string
	TraceID     string
	Timestamp   string // ClickHouse datetime text
}

// TimelineEvent is one normalized, human-readable step in the session.
type TimelineEvent struct {
	At       string `json:"at"`        // RFC3339
	OffsetMs int64  `json:"offset_ms"` // since session start
	Kind     string `json:"kind"`      // page_view | web_vitals | error | other
	Label    string `json:"label"`
	Path     string `json:"path,omitempty"`
	TraceID  string `json:"trace_id,omitempty"`
}

// SessionTimeline is the assembled session: summary + ordered events.
type SessionTimeline struct {
	SessionID  string          `json:"session_id"`
	StartedAt  string          `json:"started_at"`
	EndedAt    string          `json:"ended_at"`
	DurationMs int64           `json:"duration_ms"`
	PageViews  int             `json:"page_views"`
	Errors     int             `json:"errors"`
	EventCount int             `json:"event_count"`
	Events     []TimelineEvent `json:"events"`
}

// assembleSessionTimeline orders a session's raw events by time and derives the
// summary (start/end/duration, page-view & error counts) plus a normalized,
// labeled event list with offsets from session start. Pure: no DB, no clock.
// Events with an unparseable timestamp sort to the front (offset 0) rather than
// being dropped, so nothing silently disappears from the replay.
func assembleSessionTimeline(sessionID string, raw []SessionEventInput) SessionTimeline {
	type parsed struct {
		ev SessionEventInput
		at time.Time
	}
	items := make([]parsed, 0, len(raw))
	for _, e := range raw {
		items = append(items, parsed{ev: e, at: parseCHTime(e.Timestamp)})
	}
	sort.SliceStable(items, func(i, j int) bool { return items[i].at.Before(items[j].at) })

	out := SessionTimeline{SessionID: sessionID, EventCount: len(raw), Events: make([]TimelineEvent, 0, len(raw))}
	if len(items) == 0 {
		return out
	}

	var start, end time.Time
	for _, it := range items {
		if it.at.IsZero() {
			continue
		}
		if start.IsZero() || it.at.Before(start) {
			start = it.at
		}
		if it.at.After(end) {
			end = it.at
		}
	}
	if !start.IsZero() {
		out.StartedAt = start.UTC().Format(time.RFC3339)
		out.EndedAt = end.UTC().Format(time.RFC3339)
		out.DurationMs = end.Sub(start).Milliseconds()
	}

	for _, it := range items {
		offset := int64(0)
		if !it.at.IsZero() && !start.IsZero() {
			offset = it.at.Sub(start).Milliseconds()
		}
		kind, label := classifyRUMEvent(it.ev)
		switch kind {
		case "page_view":
			out.PageViews++
		case "error":
			out.Errors++
		}
		te := TimelineEvent{OffsetMs: offset, Kind: kind, Label: label, Path: it.ev.Path, TraceID: it.ev.TraceID}
		if !it.at.IsZero() {
			te.At = it.at.UTC().Format(time.RFC3339)
		}
		out.Events = append(out.Events, te)
	}
	return out
}

// classifyRUMEvent maps a raw event to a (kind, human label). Pure helper.
func classifyRUMEvent(e SessionEventInput) (kind, label string) {
	switch e.Type {
	case "page_view":
		p := e.Path
		if p == "" {
			p = "(unknown page)"
		}
		return "page_view", "Viewed " + p
	case "web_vitals":
		return "web_vitals", fmt.Sprintf("%s = %s", e.MetricName, formatVitalValue(e.MetricName, e.MetricValue))
	case "error":
		msg := e.ErrorMsg
		if msg == "" {
			msg = "JavaScript error"
		}
		if len(msg) > 140 {
			msg = msg[:140] + "…"
		}
		return "error", "Error: " + msg
	default:
		return "other", e.Type
	}
}

// formatVitalValue renders a web-vital value with the right unit (CLS is unitless,
// everything else is milliseconds).
func formatVitalValue(metric string, v float64) string {
	if metric == "CLS" {
		return fmt.Sprintf("%.3f", v)
	}
	return fmt.Sprintf("%.0fms", v)
}

// GetSession returns the ordered event timeline for a single RUM session.
//
//	GET /api/v1/rum/sessions/{id}
func (h *RUMHandler) GetSession(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	sessionID := r.PathValue("id")
	if sessionID == "" {
		http.Error(w, "missing session id", http.StatusBadRequest)
		return
	}

	tenantID := r.Header.Get("X-Tenant-ID")
	if tenantID == "" {
		tenantID = "default"
	}

	// Both tenant and the user-supplied session id are bound params, never
	// concatenated into SQL.
	query := `
		SELECT Type, Path, MetricName, MetricValue, ErrorMsg, TraceID, toString(Timestamp) AS ts
		FROM pulsetrace.rum_events
		WHERE TenantID = {tenant:String} AND SessionID = {session:String}
		ORDER BY Timestamp ASC
		LIMIT 2000
		FORMAT JSON`
	reqURL := h.ClickHouseURL + "?param_tenant=" + url.QueryEscape(tenantID) + "&param_session=" + url.QueryEscape(sessionID)
	req, _ := http.NewRequest("POST", reqURL, bytes.NewBufferString(query))
	req.SetBasicAuth(clickhouseUser, clickhousePassword)

	resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		log.Printf("[RUMHandler] session query failed: %v", err)
		http.Error(w, "failed to query analytics engine", http.StatusInternalServerError)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		_ = json.NewEncoder(w).Encode(assembleSessionTimeline(sessionID, nil))
		return
	}

	var result struct {
		Data []struct {
			Type        string  `json:"Type"`
			Path        string  `json:"Path"`
			MetricName  string  `json:"MetricName"`
			MetricValue float64 `json:"MetricValue"`
			ErrorMsg    string  `json:"ErrorMsg"`
			TraceID     string  `json:"TraceID"`
			TS          string  `json:"ts"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		log.Printf("[RUMHandler] failed to decode session response: %v", err)
		http.Error(w, "failed to decode analytics response", http.StatusInternalServerError)
		return
	}

	events := make([]SessionEventInput, 0, len(result.Data))
	for _, d := range result.Data {
		events = append(events, SessionEventInput{
			Type: d.Type, Path: d.Path, MetricName: d.MetricName, MetricValue: d.MetricValue,
			ErrorMsg: d.ErrorMsg, TraceID: d.TraceID, Timestamp: d.TS,
		})
	}
	_ = json.NewEncoder(w).Encode(assembleSessionTimeline(sessionID, events))
}
