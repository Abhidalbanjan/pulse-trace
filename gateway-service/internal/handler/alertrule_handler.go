package handler

import (
	"database/sql"
	"encoding/json"
	"log"
	"net/http"
	"strings"

	"github.com/expr-lang/expr"

	"github.com/pulsetrace/gateway-service/internal/auth"
)

// AlertRuleHandler manages user-defined alert rules in Postgres. Rules are
// evaluated by correlation-service's AlertRuleEvaluator (which polls this
// same table directly — see correlation-service/internal/repository/
// alertrule_repository.go), not by gateway-service; this handler is purely
// the CRUD surface, following the same DB-backed/no-redeploy pattern as
// RBAC roles/policies and rate limit rules.
type AlertRuleHandler struct {
	db *sql.DB
}

func NewAlertRuleHandler(db *sql.DB) *AlertRuleHandler {
	return &AlertRuleHandler{db: db}
}

type alertRuleRow struct {
	ID              string `json:"id"`
	TenantID        string `json:"tenant_id"`
	Name            string `json:"name"`
	Description     string `json:"description"`
	ServiceName     string `json:"service_name"`
	Condition       string `json:"condition"`
	Severity        string `json:"severity"`
	CooldownSeconds int    `json:"cooldown_seconds"`
	Enabled         bool   `json:"enabled"`
}

// validateCondition compiles the expr-lang condition against a representative
// env (the same fields AlertRuleEvaluator provides at evaluation time) so a
// typo or unknown field is rejected at creation time, not silently skipped
// every poll cycle in correlation-service's logs.
func validateCondition(condition string) error {
	env := map[string]interface{}{
		"error_rate":     0.0,
		"p50_latency_ms": 0.0,
		"p90_latency_ms": 0.0,
		"p99_latency_ms": 0.0,
		"request_count":  int64(0),
		"error_count":    int64(0),
		"baseline_ratio": 0.0,
	}
	_, err := expr.Compile(condition, expr.Env(env), expr.AsBool())
	return err
}

var validSeverities = map[string]bool{
	"DEBUG": true, "INFO": true, "WARNING": true, "ERROR": true, "FATAL": true, "CRITICAL": true,
}

// ListAlertRules handles GET /api/v1/admin/alert-rules
func (h *AlertRuleHandler) ListAlertRules(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	rows, err := h.db.Query(`SELECT id, tenant_id, name, COALESCE(description, ''), service_name, condition, severity, cooldown_seconds, enabled
		FROM alert_rules WHERE tenant_id = $1 ORDER BY name ASC`, tenantFromRequest(r))
	if err != nil {
		log.Printf("alertrules: failed to list: %v", err)
		http.Error(w, "failed to list alert rules", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	out := []alertRuleRow{}
	for rows.Next() {
		var ar alertRuleRow
		if err := rows.Scan(&ar.ID, &ar.TenantID, &ar.Name, &ar.Description, &ar.ServiceName, &ar.Condition, &ar.Severity, &ar.CooldownSeconds, &ar.Enabled); err != nil {
			continue
		}
		out = append(out, ar)
	}
	_ = json.NewEncoder(w).Encode(map[string]interface{}{"data": out})
}

type upsertAlertRuleRequest struct {
	Name            string `json:"name"`
	Description     string `json:"description"`
	ServiceName     string `json:"service_name"`
	Condition       string `json:"condition"`
	Severity        string `json:"severity"`
	CooldownSeconds int    `json:"cooldown_seconds"`
	Enabled         *bool  `json:"enabled"`
}

// CreateAlertRule handles POST /api/v1/admin/alert-rules
func (h *AlertRuleHandler) CreateAlertRule(w http.ResponseWriter, r *http.Request) {
	var req upsertAlertRuleRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if req.Name == "" || req.Condition == "" {
		http.Error(w, "name and condition are required", http.StatusBadRequest)
		return
	}
	if req.ServiceName == "" {
		req.ServiceName = "*"
	}
	if req.Severity == "" {
		req.Severity = "WARNING"
	}
	if !validSeverities[strings.ToUpper(req.Severity)] {
		http.Error(w, "severity must be one of DEBUG, INFO, WARNING, ERROR, FATAL, CRITICAL", http.StatusBadRequest)
		return
	}
	if req.CooldownSeconds <= 0 {
		req.CooldownSeconds = 900
	}
	if err := validateCondition(req.Condition); err != nil {
		http.Error(w, "invalid condition: "+err.Error(), http.StatusBadRequest)
		return
	}

	tenantID := r.Header.Get("X-Tenant-ID")
	if tenantID == "" {
		tenantID = "default"
	}

	_, err := h.db.Exec(
		`INSERT INTO alert_rules (tenant_id, name, description, service_name, condition, severity, cooldown_seconds)
		 VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		tenantID, req.Name, req.Description, req.ServiceName, req.Condition, strings.ToUpper(req.Severity), req.CooldownSeconds,
	)
	if err != nil {
		if strings.Contains(err.Error(), "duplicate key") {
			http.Error(w, "an alert rule with that name already exists for this tenant", http.StatusConflict)
			return
		}
		log.Printf("alertrules: failed to create: %v", err)
		http.Error(w, "failed to create alert rule", http.StatusInternalServerError)
		return
	}
	auth.WriteAudit(h.db, actorFromHeader(r), "create", "alert_rule", req.Name, nil, req)
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "created"})
}

// UpdateAlertRule handles PUT /api/v1/admin/alert-rules/{id}
func (h *AlertRuleHandler) UpdateAlertRule(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req upsertAlertRuleRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if req.Condition != "" {
		if err := validateCondition(req.Condition); err != nil {
			http.Error(w, "invalid condition: "+err.Error(), http.StatusBadRequest)
			return
		}
	}
	if req.Severity != "" && !validSeverities[strings.ToUpper(req.Severity)] {
		http.Error(w, "severity must be one of DEBUG, INFO, WARNING, ERROR, FATAL, CRITICAL", http.StatusBadRequest)
		return
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}

	res, err := h.db.Exec(
		`UPDATE alert_rules SET
			description = COALESCE(NULLIF($1, ''), description),
			service_name = COALESCE(NULLIF($2, ''), service_name),
			condition = COALESCE(NULLIF($3, ''), condition),
			severity = COALESCE(NULLIF($4, ''), severity),
			cooldown_seconds = COALESCE(NULLIF($5, 0), cooldown_seconds),
			enabled = $6,
			updated_at = now()
		WHERE id = $7 AND tenant_id = $8`,
		req.Description, req.ServiceName, req.Condition, strings.ToUpper(req.Severity), req.CooldownSeconds, enabled, id, tenantFromRequest(r),
	)
	if err != nil {
		log.Printf("alertrules: failed to update: %v", err)
		http.Error(w, "failed to update alert rule", http.StatusInternalServerError)
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		http.Error(w, "alert rule not found", http.StatusNotFound)
		return
	}
	auth.WriteAudit(h.db, actorFromHeader(r), "update", "alert_rule", id, nil, req)
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "updated"})
}

// DeleteAlertRule handles DELETE /api/v1/admin/alert-rules/{id}
func (h *AlertRuleHandler) DeleteAlertRule(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	res, err := h.db.Exec("DELETE FROM alert_rules WHERE id = $1 AND tenant_id = $2", id, tenantFromRequest(r))
	if err != nil {
		log.Printf("alertrules: failed to delete: %v", err)
		http.Error(w, "failed to delete alert rule", http.StatusInternalServerError)
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		http.Error(w, "alert rule not found", http.StatusNotFound)
		return
	}
	auth.WriteAudit(h.db, actorFromHeader(r), "delete", "alert_rule", id, nil, nil)
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "deleted"})
}
