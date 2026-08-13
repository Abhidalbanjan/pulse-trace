package handler

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"
)

// parseDeployTime parses an optional RFC3339 timestamp query param. Returns
// (zero, false) for an empty or malformed value so the caller can treat it as
// "no bound" rather than failing the whole request. Pure and unit-tested.
func parseDeployTime(raw string) (time.Time, bool) {
	if raw == "" {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}

// DeploymentHandler records and lists deployment markers (Deployment Tracking):
// which version of a service went out, when, and by whom.
type DeploymentHandler struct {
	db *sql.DB
}

func NewDeploymentHandler(db *sql.DB) *DeploymentHandler {
	return &DeploymentHandler{db: db}
}

type deploymentRequest struct {
	Service     string `json:"service"`
	Version     string `json:"version"`
	GitSHA      string `json:"git_sha"`
	Environment string `json:"environment"`
	DeployedBy  string `json:"deployed_by"`
	Notes       string `json:"notes"`
}

// RecordDeployment stores a new deployment marker. Called by a CI/CD pipeline
// on release, or manually from the Service Page for environments without one.
func (h *DeploymentHandler) RecordDeployment(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		http.Error(w, "database unavailable", http.StatusServiceUnavailable)
		return
	}

	var req deploymentRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if req.Service == "" || req.Version == "" {
		http.Error(w, "service and version are required", http.StatusBadRequest)
		return
	}
	if req.Environment == "" {
		req.Environment = "production"
	}

	var id string
	var deployedAt string
	err := h.db.QueryRow(`
		INSERT INTO deployments (tenant_id, service, version, git_sha, environment, deployed_by, notes)
		VALUES ($1, $2, $3, NULLIF($4, ''), $5, NULLIF($6, ''), NULLIF($7, ''))
		RETURNING id, deployed_at::text
	`, tenantFromRequest(r), req.Service, req.Version, req.GitSHA, req.Environment, req.DeployedBy, req.Notes).Scan(&id, &deployedAt)
	if err != nil {
		log.Printf("[DeploymentHandler] failed to record deployment: %v", err)
		http.Error(w, "failed to record deployment", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"id":          id,
		"service":     req.Service,
		"version":     req.Version,
		"deployed_at": deployedAt,
	})
}

// incidentDeploy is a deploy candidate for change-failure linking.
type incidentDeploy struct {
	ID          string    `json:"id"`
	Service     string    `json:"service"`
	Version     string    `json:"version"`
	Environment string    `json:"environment"`
	DeployedBy  string    `json:"deployed_by"`
	DeployedAt  time.Time `json:"deployed_at"`
}

// nearestPrecedingDeploy picks the most recent deploy at or before `at`. A deploy
// after `at` can't have caused something that started at `at`, so it's ignored.
// Pure and unit-tested — the "which change caused this?" heuristic.
func nearestPrecedingDeploy(at time.Time, deploys []incidentDeploy) *incidentDeploy {
	var best *incidentDeploy
	for i := range deploys {
		d := &deploys[i]
		if d.DeployedAt.After(at) {
			continue
		}
		if best == nil || d.DeployedAt.After(best.DeployedAt) {
			best = d
		}
	}
	return best
}

// GetDeployForIncident returns the deploy that most likely triggered an incident:
// the last deploy of the service at or before the incident's start, within a
// lookback window. Backs the "caused by deploy" link on an incident.
//
//	GET /api/v1/deployments/for-incident?service=&at=&lookback_hours=
func (h *DeploymentHandler) GetDeployForIncident(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if h.db == nil {
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"deploy": nil})
		return
	}

	service := r.URL.Query().Get("service")
	at, ok := parseDeployTime(r.URL.Query().Get("at"))
	if service == "" || !ok {
		http.Error(w, "service and a valid RFC3339 'at' are required", http.StatusBadRequest)
		return
	}

	lookback := 24 * time.Hour
	if h, err := time.ParseDuration(r.URL.Query().Get("lookback_hours") + "h"); err == nil && h > 0 && h <= 168*time.Hour {
		lookback = h
	}

	rows, err := h.db.Query(`
		SELECT id, service, version, environment, COALESCE(deployed_by, ''), deployed_at
		FROM deployments
		WHERE tenant_id = $1 AND service = $2 AND deployed_at <= $3 AND deployed_at >= $4
		ORDER BY deployed_at DESC LIMIT 50`,
		tenantFromRequest(r), service, at, at.Add(-lookback))
	if err != nil {
		log.Printf("[DeploymentHandler] for-incident query failed: %v", err)
		http.Error(w, "failed to query deployments", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	candidates := []incidentDeploy{}
	for rows.Next() {
		var d incidentDeploy
		if err := rows.Scan(&d.ID, &d.Service, &d.Version, &d.Environment, &d.DeployedBy, &d.DeployedAt); err != nil {
			continue
		}
		candidates = append(candidates, d)
	}

	deploy := nearestPrecedingDeploy(at, candidates)
	if deploy == nil {
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"deploy": nil})
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"deploy":        deploy,
		"minutes_before": int(at.Sub(deploy.DeployedAt).Minutes()),
	})
}

// ListDeployments returns the most recent deployments for a service, newest first.
func (h *DeploymentHandler) ListDeployments(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	if h.db == nil {
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"data": []interface{}{}})
		return
	}

	service := r.URL.Query().Get("service")
	if service == "" {
		http.Error(w, "missing service query param", http.StatusBadRequest)
		return
	}

	// Optional [from,to] window so a chart can fetch exactly the deploy markers
	// inside its visible time range. Both are parameterized; malformed values are
	// ignored (treated as "no bound") rather than failing the request.
	args := []interface{}{tenantFromRequest(r), service}
	where := "tenant_id = $1 AND service = $2"
	windowed := false
	if from, ok := parseDeployTime(r.URL.Query().Get("from")); ok {
		args = append(args, from)
		where += fmt.Sprintf(" AND deployed_at >= $%d", len(args))
		windowed = true
	}
	if to, ok := parseDeployTime(r.URL.Query().Get("to")); ok {
		args = append(args, to)
		where += fmt.Sprintf(" AND deployed_at <= $%d", len(args))
		windowed = true
	}
	// A default (unwindowed) call keeps the small "recent deploys" cap; a windowed
	// call wants every marker in range (still bounded to a safe ceiling).
	limit := 20
	if windowed {
		limit = 500
	}

	query := fmt.Sprintf(`
		SELECT id, service, version, COALESCE(git_sha, ''), environment, COALESCE(deployed_by, ''), COALESCE(notes, ''), deployed_at::text
		FROM deployments
		WHERE %s
		ORDER BY deployed_at DESC
		LIMIT %d
	`, where, limit)
	rows, err := h.db.Query(query, args...)
	if err != nil {
		log.Printf("[DeploymentHandler] failed to list deployments: %v", err)
		http.Error(w, "failed to list deployments", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	type deployment struct {
		ID          string `json:"id"`
		Service     string `json:"service"`
		Version     string `json:"version"`
		GitSHA      string `json:"git_sha"`
		Environment string `json:"environment"`
		DeployedBy  string `json:"deployed_by"`
		Notes       string `json:"notes"`
		DeployedAt  string `json:"deployed_at"`
	}

	out := []deployment{}
	for rows.Next() {
		var d deployment
		if err := rows.Scan(&d.ID, &d.Service, &d.Version, &d.GitSHA, &d.Environment, &d.DeployedBy, &d.Notes, &d.DeployedAt); err != nil {
			continue
		}
		out = append(out, d)
	}

	_ = json.NewEncoder(w).Encode(map[string]interface{}{"data": out})
}
