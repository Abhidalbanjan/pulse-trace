package handler

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os/exec"
	"strings"
	"time"

	"github.com/pulsetrace/shared/db"
	"github.com/pulsetrace/shared/jsonpool"
	"github.com/pulsetrace/topology-service/internal/repository"
	coltracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/protobuf/encoding/protojson"
)

type API struct {
	repo         *repository.Neo4jRepository
	sharedSecret []byte
}

func NewAPI(repo *repository.Neo4jRepository, secret string) *API {
	if secret == "" {
		secret = "pulsetrace_secure_playbook_hmac_secret"
	}
	return &API{
		repo:         repo,
		sharedSecret: []byte(secret),
	}
}

// RegisterRoutes sets up the HTTP handlers for the topology service.
func (a *API) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/topology/graph", a.handleGetGraph)
	mux.HandleFunc("GET /api/v1/topology/dependencies/downstream/", a.handleGetDownstream)
	mux.HandleFunc("GET /api/v1/topology/dependencies/upstream/", a.handleGetUpstream)
	mux.HandleFunc("GET /api/v1/topology/agent-config/", a.handleGetAgentConfig)
	mux.HandleFunc("POST /api/v1/topology/state", a.handleUpdateState)
	mux.HandleFunc("POST /api/v1/topology/catalog", a.handleUpdateCatalog)
	mux.HandleFunc("POST /api/v1/topology/causal-path", a.handleUpdateCausalPath)
	mux.HandleFunc("POST /v1/traces", a.handleReceiveTraces)
	mux.HandleFunc("POST /api/v1/agent/playbook/execute", a.handleExecutePlaybook)
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

	if len(deps) == 0 {
		writeJSON(w, http.StatusOK, []string{})
		return
	}
	writeJSON(w, http.StatusOK, deps)
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

	if len(deps) == 0 {
		writeJSON(w, http.StatusOK, []string{})
		return
	}
	writeJSON(w, http.StatusOK, deps)
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

	writeJSON(w, http.StatusOK, config)
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

type UpdateCatalogRequest struct {
	ServiceName string `json:"service_name"`
	Team        string `json:"team"`
	Repo        string `json:"repo"`
	Slack       string `json:"slack"`
}

func (a *API) handleUpdateCatalog(w http.ResponseWriter, r *http.Request) {
	// CORS support
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusOK)
		return
	}

	var req UpdateCatalogRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	if err := a.repo.UpsertServiceCatalog(r.Context(), req.ServiceName, req.Team, req.Repo, req.Slack); err != nil {
		log.Printf("failed to update catalog for %s: %v", req.ServiceName, err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
}

type UpdateCausalPathRequest struct {
	IncidentID string                  `json:"incident_id"`
	Links      []repository.CausalLink `json:"links"`
}

func (a *API) handleUpdateCausalPath(w http.ResponseWriter, r *http.Request) {
	var req UpdateCausalPathRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if req.IncidentID == "" {
		http.Error(w, "incident_id is required", http.StatusBadRequest)
		return
	}

	if err := a.repo.UpdateCausalPath(r.Context(), req.IncidentID, req.Links); err != nil {
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

	writeJSON(w, http.StatusOK, graph)
}

func (a *API) handleReceiveTraces(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		log.Printf("topology: failed to read traces body: %v", err)
		http.Error(w, "failed to read body", http.StatusBadRequest)
		return
	}

	var req coltracepb.ExportTraceServiceRequest
	if err := protojson.Unmarshal(body, &req); err != nil {
		log.Printf("topology: failed to unmarshal OTLP JSON: %v", err)
		http.Error(w, "invalid OTLP JSON", http.StatusBadRequest)
		return
	}

	ctx := r.Context()
	for _, resourceSpans := range req.ResourceSpans {
		serviceName := ""
		if resourceSpans.Resource != nil {
			for _, attr := range resourceSpans.Resource.Attributes {
				if attr.Key == "service.name" {
					serviceName = attr.Value.GetStringValue()
				}
			}
		}
		if serviceName == "" {
			continue
		}

		for _, scopeSpans := range resourceSpans.ScopeSpans {
			for _, span := range scopeSpans.Spans {
				spanID := hex.EncodeToString(span.SpanId)
				parentSpanID := hex.EncodeToString(span.ParentSpanId)
				durationMs := float64(span.EndTimeUnixNano-span.StartTimeUnixNano) / 1e6
				isError := span.Status != nil && span.Status.Code == tracepb.Status_STATUS_CODE_ERROR

				// 1. Store spanID -> serviceName mapping in Redis (TTL 5m)
				spanKey := "span:" + spanID
				if err := a.repo.SetSpanService(ctx, spanKey, serviceName, 5*time.Minute); err != nil {
					log.Printf("topology: failed to store span mapping: %v", err)
				}

				// 2. Check if this span has a parent
				if len(span.ParentSpanId) > 0 && !isAllZeros(span.ParentSpanId) {
					parentKey := "span:" + parentSpanID
					parentService, err := a.repo.GetSpanService(ctx, parentKey)
					if err == nil && parentService != "" {
						// Parent found! Create the dependency edge. This span IS the
						// downstream call itself, so its own duration/status are exactly
						// what should be attributed to the parentService -> serviceName edge.
						if parentService != serviceName {
							log.Printf("topology: span relation discovered edge: %s -> %s", parentService, serviceName)
							if err := a.repo.UpsertDependencyEdge(ctx, parentService, serviceName); err != nil {
								log.Printf("topology: failed to upsert edge %s -> %s: %v", parentService, serviceName, err)
							}
							if err := a.repo.RecordEdgeMetric(ctx, parentService, serviceName, durationMs, isError); err != nil {
								log.Printf("topology: failed to record edge metric %s -> %s: %v", parentService, serviceName, err)
							}
						}
					} else {
						// Parent not found yet. Store this span's own duration/error
						// alongside its service name, so the metric can still be attributed
						// once the parent arrives and resolves it (see step 3 below).
						pendingKey := "pending:" + parentSpanID
						if err := a.repo.AddPendingChild(ctx, pendingKey, serviceName, durationMs, isError, spanID, 5*time.Minute); err != nil {
							log.Printf("topology: failed to add pending child: %v", err)
						}
					}
				}

				// 3. Process any child services waiting for this span ID
				pendingKey := "pending:" + spanID
				children, err := a.repo.GetPendingChildren(ctx, pendingKey)
				if err == nil && len(children) > 0 {
					for _, raw := range children {
						entry, ok := repository.ParsePendingChildEntry(raw)
						if !ok || serviceName == entry.Service {
							continue
						}
						log.Printf("topology: pending span relation resolved edge: %s -> %s", serviceName, entry.Service)
						if err := a.repo.UpsertDependencyEdge(ctx, serviceName, entry.Service); err != nil {
							log.Printf("topology: failed to upsert edge %s -> %s: %v", serviceName, entry.Service, err)
						}
						if err := a.repo.RecordEdgeMetric(ctx, serviceName, entry.Service, entry.DurationMs, entry.IsError); err != nil {
							log.Printf("topology: failed to record edge metric %s -> %s: %v", serviceName, entry.Service, err)
						}
					}
					_ = a.repo.DeleteKey(ctx, pendingKey)
				}
			}
		}
	}

	w.WriteHeader(http.StatusOK)
}

func isAllZeros(b []byte) bool {
	for _, x := range b {
		if x != 0 {
			return false
		}
	}
	return true
}

type SignedPlaybookRequest struct {
	IncidentID   string `json:"incident_id"`
	PlaybookName string `json:"playbook_name"`
	ServiceName  string `json:"service_name"`
	Timestamp    string `json:"timestamp"` // RFC3339
	Signature    string `json:"signature"`
}

func (a *API) handleExecutePlaybook(w http.ResponseWriter, r *http.Request) {
	var req SignedPlaybookRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		log.Printf("agent-handler: invalid request body: %v", err)
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	// 1. Verify timestamp is within 5 minutes (prevent replay attacks)
	ts, err := time.Parse(time.RFC3339, req.Timestamp)
	if err != nil {
		log.Printf("agent-handler: invalid timestamp: %v", err)
		http.Error(w, "invalid timestamp format", http.StatusBadRequest)
		return
	}
	if time.Since(ts).Abs() > 5*time.Minute {
		log.Printf("agent-handler: request expired: ts=%s, current=%s", req.Timestamp, time.Now().Format(time.RFC3339))
		http.Error(w, "request timestamp expired", http.StatusUnauthorized)
		return
	}

	// 2. Verify HMAC signature
	expectedPayload := fmt.Sprintf("%s:%s:%s:%d", req.IncidentID, req.PlaybookName, req.ServiceName, ts.Unix())
	mac := hmac.New(sha256.New, a.sharedSecret)
	mac.Write([]byte(expectedPayload))
	expectedSig := hex.EncodeToString(mac.Sum(nil))

	// Constant-time comparison to prevent timing attacks
	sigBytes, _ := hex.DecodeString(req.Signature)
	expectedBytes, _ := hex.DecodeString(expectedSig)
	if !hmac.Equal(sigBytes, expectedBytes) {
		log.Printf("agent-handler: signature verification failed for incident=%s, service=%s, playbook=%s",
			req.IncidentID, req.ServiceName, req.PlaybookName)
		http.Error(w, "signature verification failed", http.StatusUnauthorized)
		return
	}

	// 3. Execute the playbook (real command execution)
	log.Printf("agent-handler: SUCCESSFUL signature verified. Executing playbook %q on service %q for incident %q",
		req.PlaybookName, req.ServiceName, req.IncidentID)

	var output string
	var errExec error
	var status = "EXECUTED"

	switch req.PlaybookName {
	case "recycle_db_pool":
		// Recycle database connection pools (terminate idle-in-transaction connections)
		pgPool, err := db.NewPostgresPool(r.Context())
		if err != nil {
			errExec = err
			status = "FAILED"
			output = fmt.Sprintf("Failed to connect to database for recycling connection pool: %v", err)
		} else {
			defer pgPool.Close()
			query := `
				SELECT pg_terminate_backend(pid) 
				FROM pg_stat_activity 
				WHERE state = 'idle in transaction' 
				  AND state_change < NOW() - INTERVAL '1 minute'
			`
			tag, err := pgPool.Exec(r.Context(), query)
			if err != nil {
				errExec = err
				status = "FAILED"
				output = fmt.Sprintf("Recycle DB pool failed: %v", err)
			} else {
				output = fmt.Sprintf("Successfully recycled database connection pool. Terminated connections: %d.", tag.RowsAffected())
			}
		}

	case "restart_service":
		// Try Kubectl restart
		cmd := exec.Command("kubectl", "rollout", "restart", "deployment/"+req.ServiceName)
		var out bytes.Buffer
		cmd.Stdout = &out
		cmd.Stderr = &out
		errExec = cmd.Run()

		if errExec != nil {
			// Fallback to Docker Compose restart
			log.Printf("agent-handler: kubectl restart failed, trying docker restart fallback: %v", errExec)
			dockerCmd := exec.Command("docker", "restart", "pulsetrace-"+req.ServiceName)
			var dockerOut bytes.Buffer
			dockerCmd.Stdout = &dockerOut
			dockerCmd.Stderr = &dockerOut
			if errDocker := dockerCmd.Run(); errDocker != nil {
				log.Printf("agent-handler: docker restart fallback failed: %v", errDocker)
				status = "FAILED"
				output = fmt.Sprintf("Failed to restart service %q. Kubectl error: %v (output: %q). Docker error: %v (output: %q).",
					req.ServiceName, errExec, out.String(), errDocker, dockerOut.String())
			} else {
				output = fmt.Sprintf("Successfully restarted container pulsetrace-%s using Docker. Output: %s", req.ServiceName, strings.TrimSpace(dockerOut.String()))
			}
		} else {
			output = fmt.Sprintf("Successfully executed rolling restart on Kubernetes for deployment/%s. Output: %s", req.ServiceName, strings.TrimSpace(out.String()))
		}

	case "scale_replicas":
		// Try Kubectl scale
		cmd := exec.Command("kubectl", "scale", "deployment/"+req.ServiceName, "--replicas=4")
		var out bytes.Buffer
		cmd.Stdout = &out
		cmd.Stderr = &out
		errExec = cmd.Run()

		if errExec != nil {
			log.Printf("agent-handler: kubectl scale failed: %v", errExec)
			status = "FAILED"
			output = fmt.Sprintf("Failed to scale deployment/%s. Kubectl error: %v (output: %q).",
				req.ServiceName, errExec, out.String())
		} else {
			output = fmt.Sprintf("Successfully scaled deployment/%s to 4 replicas. Output: %s", req.ServiceName, strings.TrimSpace(out.String()))
		}

	default:
		status = "FAILED"
		output = fmt.Sprintf("Unknown playbook %q — no handler registered for service %q.", req.PlaybookName, req.ServiceName)
	}

	resp := map[string]string{
		"status": status,
		"output": output,
	}
	writeJSON(w, http.StatusOK, resp)
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	buf := jsonpool.GetBuffer()
	defer jsonpool.PutBuffer(buf)
	if err := json.NewEncoder(buf).Encode(v); err == nil {
		w.Write(buf.Bytes())
	}
}
