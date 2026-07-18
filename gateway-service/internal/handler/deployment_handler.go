package handler

import (
	"database/sql"
	"encoding/json"
	"log"
	"net/http"
)

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
		INSERT INTO deployments (service, version, git_sha, environment, deployed_by, notes)
		VALUES ($1, $2, NULLIF($3, ''), $4, NULLIF($5, ''), NULLIF($6, ''))
		RETURNING id, deployed_at::text
	`, req.Service, req.Version, req.GitSHA, req.Environment, req.DeployedBy, req.Notes).Scan(&id, &deployedAt)
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

	rows, err := h.db.Query(`
		SELECT id, service, version, COALESCE(git_sha, ''), environment, COALESCE(deployed_by, ''), COALESCE(notes, ''), deployed_at::text
		FROM deployments
		WHERE service = $1
		ORDER BY deployed_at DESC
		LIMIT 20
	`, service)
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
