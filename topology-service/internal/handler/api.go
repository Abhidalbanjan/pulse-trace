package handler

import (
	"encoding/json"
	"log"
	"net/http"
	"strings"

	"github.com/pulsetrace/topology-service/internal/repository"
)

type API struct {
	repo *repository.Neo4jRepository
}

func NewAPI(repo *repository.Neo4jRepository) *API {
	return &API{repo: repo}
}

// RegisterRoutes sets up the HTTP handlers for the topology service.
func (a *API) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/topology/graph", a.handleGetGraph)
	mux.HandleFunc("GET /api/v1/topology/dependencies/downstream/", a.handleGetDownstream)
	mux.HandleFunc("GET /api/v1/topology/dependencies/upstream/", a.handleGetUpstream)
	mux.HandleFunc("GET /api/v1/topology/agent-config/", a.handleGetAgentConfig)
	mux.HandleFunc("POST /api/v1/topology/state", a.handleUpdateState)
	mux.HandleFunc("POST /api/v1/topology/causal-path", a.handleUpdateCausalPath)
}

func (a *API) handleGetDownstream(w http.ResponseWriter, r *http.Request) {
	serviceName := strings.TrimPrefix(r.URL.Path, "/api/v1/topology/dependencies/downstream/")
	if serviceName == "" {
		http.Error(w, "missing service name", http.StatusBadRequest)
		return
	}

	deps, err := a.repo.GetDownstreamDependencies(r.Context(), serviceName)
	if err != nil {
		log.Printf("failed to get dependencies for %s: %v", serviceName, err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if len(deps) == 0 {
		w.Write([]byte("[]"))
		return
	}
	json.NewEncoder(w).Encode(deps)
}

func (a *API) handleGetUpstream(w http.ResponseWriter, r *http.Request) {
	serviceName := strings.TrimPrefix(r.URL.Path, "/api/v1/topology/dependencies/upstream/")
	if serviceName == "" {
		http.Error(w, "missing service name", http.StatusBadRequest)
		return
	}

	deps, err := a.repo.GetUpstreamDependencies(r.Context(), serviceName)
	if err != nil {
		log.Printf("failed to get upstream dependencies for %s: %v", serviceName, err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if len(deps) == 0 {
		w.Write([]byte("[]"))
		return
	}
	json.NewEncoder(w).Encode(deps)
}

type AgentConfig struct {
	LogLevel      string  `json:"log_level"`
	TraceSampling float64 `json:"trace_sampling"`
}

func (a *API) handleGetAgentConfig(w http.ResponseWriter, r *http.Request) {
	serviceName := strings.TrimPrefix(r.URL.Path, "/api/v1/topology/agent-config/")
	if serviceName == "" {
		http.Error(w, "missing service name", http.StatusBadRequest)
		return
	}

	state, err := a.repo.GetServiceState(r.Context(), serviceName)
	if err != nil {
		log.Printf("failed to get service state for %s: %v", serviceName, err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	config := AgentConfig{
		LogLevel:      "WARN", // default log level
		TraceSampling: 0.01,   // default 1% tracing
	}

	if state == "PREDICTIVE_WARNING" || state == "DEGRADED" {
		config.LogLevel = "DEBUG"
		config.TraceSampling = 1.0 // 100% trace sampling for debugging
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(config)
}

type UpdateStateRequest struct {
	ServiceName string `json:"service_name"`
	State       string `json:"state"`
}

func (a *API) handleUpdateState(w http.ResponseWriter, r *http.Request) {
	var req UpdateStateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	if err := a.repo.UpdateServiceState(r.Context(), req.ServiceName, req.State); err != nil {
		log.Printf("failed to update state for %s: %v", req.ServiceName, err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
}

func (a *API) handleUpdateCausalPath(w http.ResponseWriter, r *http.Request) {
	var req []repository.CausalLink
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	if err := a.repo.UpdateCausalPath(r.Context(), req); err != nil {
		log.Printf("failed to update causal path: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
}

func (a *API) handleGetGraph(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	if r.Method == http.MethodOptions {
		w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		w.WriteHeader(http.StatusOK)
		return
	}

	graph, err := a.repo.GetGraph(r.Context())
	if err != nil {
		log.Printf("failed to get graph: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(graph)
}
