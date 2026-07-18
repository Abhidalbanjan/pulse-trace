package auth

import (
	"database/sql"
	"encoding/json"
	"log"
	"net/http"
)

// WriteAudit appends one row to the audit_log for a role/policy/user/rate-limit-rule
// mutation. Best-effort: a logging failure never blocks the mutation itself, but it
// is logged loudly since a silently-failing audit trail is worse than none.
// Exported so other handler packages (e.g. rate limit rule CRUD) can reuse the same
// audit table without duplicating the insert logic.
func WriteAudit(db *sql.DB, actor, action, targetType, targetID string, before, after interface{}) {
	if db == nil {
		return
	}
	if actor == "" {
		actor = "unknown"
	}
	beforeJSON, _ := json.Marshal(before)
	afterJSON, _ := json.Marshal(after)
	_, err := db.Exec(
		"INSERT INTO audit_log (actor, action, target_type, target_id, before_state, after_state) VALUES ($1, $2, $3, $4, $5, $6)",
		actor, action, targetType, targetID, beforeJSON, afterJSON,
	)
	if err != nil {
		log.Printf("audit: FAILED to record %s %s %s (actor=%s): %v", action, targetType, targetID, actor, err)
	}
}

func actorFromRequest(r *http.Request) string {
	return r.Header.Get("X-User-Subject")
}

type AuditEntry struct {
	ID          int64           `json:"id"`
	Actor       string          `json:"actor"`
	Action      string          `json:"action"`
	TargetType  string          `json:"target_type"`
	TargetID    string          `json:"target_id"`
	BeforeState json.RawMessage `json:"before_state,omitempty"`
	AfterState  json.RawMessage `json:"after_state,omitempty"`
	CreatedAt   string          `json:"created_at"`
}

// AuditLogHandler exposes the audit trail for the Settings UI.
type AuditLogHandler struct {
	db *sql.DB
}

func NewAuditLogHandler(db *sql.DB) *AuditLogHandler {
	return &AuditLogHandler{db: db}
}

// ListAuditLog handles GET /api/v1/admin/audit-log
func (h *AuditLogHandler) ListAuditLog(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if h.db == nil {
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"data": []AuditEntry{}})
		return
	}

	rows, err := h.db.Query(`
		SELECT id, actor, action, target_type, target_id,
		       COALESCE(before_state, 'null'::jsonb), COALESCE(after_state, 'null'::jsonb),
		       created_at::text
		FROM audit_log
		ORDER BY created_at DESC
		LIMIT 200
	`)
	if err != nil {
		log.Printf("audit: failed to list audit log: %v", err)
		http.Error(w, "failed to list audit log", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	out := []AuditEntry{}
	for rows.Next() {
		var e AuditEntry
		if err := rows.Scan(&e.ID, &e.Actor, &e.Action, &e.TargetType, &e.TargetID, &e.BeforeState, &e.AfterState, &e.CreatedAt); err != nil {
			continue
		}
		out = append(out, e)
	}
	_ = json.NewEncoder(w).Encode(map[string]interface{}{"data": out})
}
