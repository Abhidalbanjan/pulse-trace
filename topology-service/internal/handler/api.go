package handler

import (
	"bytes"
	"context"
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
	"github.com/pulsetrace/shared/remediation"
	"github.com/pulsetrace/topology-service/internal/repository"
	coltracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/protobuf/encoding/protojson"
)

// commandRunner executes an external remediation command and returns its
// combined output. Injected so tests can exercise the playbook handler
// deterministically without a real kubectl/docker on the box.
type commandRunner func(ctx context.Context, name string, args ...string) (string, error)

// execCommandRunner is the production runner: it actually shells out.
func execCommandRunner(ctx context.Context, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := cmd.Run()
	return strings.TrimSpace(out.String()), err
}

// Playbook execution statuses reported back to the control plane. They mirror
// the models.Playbook* constants; duplicated as local names to keep the
// agent's wire contract explicit at the point it's produced.
const (
	PlaybookStatusExecuted = "EXECUTED"
	PlaybookStatusFailed   = "FAILED"
	PlaybookStatusDryRun   = "DRY_RUN"
)

type API struct {
	repo         *repository.Neo4jRepository
	sharedSecret []byte
	runCmd       commandRunner

	// policy is this agent's own remediation posture, independent of the
	// control plane's. See handleExecutePlaybook for why it's enforced twice.
	policy remediation.Policy
}

func NewAPI(repo *repository.Neo4jRepository, secret string) *API {
	policy, err := remediation.PolicyFromEnv()
	if err != nil {
		// PolicyFromEnv returns the restrictive default alongside its error,
		// so a typo'd REMEDIATION_MODE degrades to "won't touch anything"
		// rather than to unrestricted execution.
		log.Printf("agent-handler: WARNING: %v — falling back to remediation policy %q", err, policy.Mode)
	}
	return NewAPIWithPolicy(repo, secret, policy)
}

func NewAPIWithPolicy(repo *repository.Neo4jRepository, secret string, policy remediation.Policy) *API {
	if secret == "" {
		secret = "pulsetrace_secure_playbook_hmac_secret"
	}
	return &API{
		repo:         repo,
		sharedSecret: []byte(secret),
		runCmd:       execCommandRunner,
		policy:       policy,
	}
}

// tenantOf returns the caller's tenant from the gateway-verified X-Tenant-ID
// header (proxied through from the gateway AuthMiddleware), or "default".
func tenantOf(r *http.Request) string {
	if t := r.Header.Get("X-Tenant-ID"); t != "" {
		return t
	}
	return "default"
}

// orDefaultTenant normalizes an empty tenant (e.g. from an older caller that
// doesn't set one yet) to "default".
func orDefaultTenant(t string) string {
	if t == "" {
		return "default"
	}
	return t
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
	mux.HandleFunc("DELETE /api/v1/topology/tenant", a.handleDeleteTenant)
	mux.HandleFunc("POST /v1/traces", a.handleReceiveTraces)
	mux.HandleFunc("POST /api/v1/agent/playbook/execute", a.handleExecutePlaybook)
}

func (a *API) handleGetDownstream(w http.ResponseWriter, r *http.Request) {
	serviceName := strings.TrimPrefix(r.URL.Path, "/api/v1/topology/dependencies/downstream/")
	if serviceName == "" {
		http.Error(w, "missing service name", http.StatusBadRequest)
		return
	}

	deps, err := a.repo.GetDownstreamDependencies(r.Context(), tenantOf(r), serviceName)
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

	deps, err := a.repo.GetUpstreamDependencies(r.Context(), tenantOf(r), serviceName)
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

	state, err := a.repo.GetServiceState(r.Context(), tenantOf(r), serviceName)
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
	TenantID    string `json:"tenant_id"`
	ServiceName string `json:"service_name"`
	State       string `json:"state"`
}

func (a *API) handleUpdateState(w http.ResponseWriter, r *http.Request) {
	var req UpdateStateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	if err := a.repo.UpdateServiceState(r.Context(), orDefaultTenant(req.TenantID), req.ServiceName, req.State); err != nil {
		log.Printf("failed to update state for %s: %v", req.ServiceName, err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
}

type UpdateCatalogRequest struct {
	TenantID    string `json:"tenant_id"`
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

	if err := a.repo.UpsertServiceCatalog(r.Context(), orDefaultTenant(req.TenantID), req.ServiceName, req.Team, req.Repo, req.Slack); err != nil {
		log.Printf("failed to update catalog for %s: %v", req.ServiceName, err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
}

type UpdateCausalPathRequest struct {
	TenantID   string                  `json:"tenant_id"`
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

	if err := a.repo.UpdateCausalPath(r.Context(), orDefaultTenant(req.TenantID), req.IncidentID, req.Links); err != nil {
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

	graph, err := a.repo.GetGraph(r.Context(), tenantOf(r))
	if err != nil {
		log.Printf("failed to get graph: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusOK, graph)
}

// handleDeleteTenant removes all of a tenant's topology (Neo4j nodes/edges). The
// gateway calls this as part of tenant data deletion; the tenant comes from the
// gateway-verified X-Tenant-ID header.
func (a *API) handleDeleteTenant(w http.ResponseWriter, r *http.Request) {
	if err := a.repo.DeleteTenant(r.Context(), tenantOf(r)); err != nil {
		log.Printf("failed to delete tenant topology for %s: %v", tenantOf(r), err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
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
		tenant := "default"
		if resourceSpans.Resource != nil {
			for _, attr := range resourceSpans.Resource.Attributes {
				switch attr.Key {
				case "service.name":
					serviceName = attr.Value.GetStringValue()
				case "tenant.id":
					// Stamped by the gateway OTLP receiver; scopes this service's
					// node/edges so same-named services in different tenants stay separate.
					if v := attr.Value.GetStringValue(); v != "" {
						tenant = v
					}
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
							log.Printf("topology: span relation discovered edge: %s -> %s (tenant %s)", parentService, serviceName, tenant)
							if err := a.repo.UpsertDependencyEdge(ctx, tenant, parentService, serviceName); err != nil {
								log.Printf("topology: failed to upsert edge %s -> %s: %v", parentService, serviceName, err)
							}
							if err := a.repo.RecordEdgeMetric(ctx, tenant, parentService, serviceName, durationMs, isError); err != nil {
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
						log.Printf("topology: pending span relation resolved edge: %s -> %s (tenant %s)", serviceName, entry.Service, tenant)
						if err := a.repo.UpsertDependencyEdge(ctx, tenant, serviceName, entry.Service); err != nil {
							log.Printf("topology: failed to upsert edge %s -> %s: %v", serviceName, entry.Service, err)
						}
						if err := a.repo.RecordEdgeMetric(ctx, tenant, serviceName, entry.Service, entry.DurationMs, entry.IsError); err != nil {
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
	// DryRun asks the agent to report what it would do without doing it. It
	// is covered by the HMAC (see the signed payload below) because the
	// dangerous direction is an attacker flipping a captured dry-run request
	// into a live production change.
	DryRun    bool   `json:"dry_run"`
	Signature string `json:"signature"`
}

// playbookPlan describes what a playbook would do, for dry-run responses.
// Each case in the executor produces one before deciding whether to act, so
// the plan and the real thing cannot drift apart.
type playbookPlan struct {
	summary string
	steps   []string
}

func (p playbookPlan) render(serviceName string) string {
	out := fmt.Sprintf("DRY RUN — nothing was changed on %s.\n%s", serviceName, p.summary)
	for _, s := range p.steps {
		out += "\n  would run: " + s
	}
	return out
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

	// 2. Verify HMAC signature. dry_run is part of the signed payload so a
	// captured dry-run request cannot be replayed as a live change.
	expectedPayload := fmt.Sprintf("%s:%s:%s:%d:dry_run=%t",
		req.IncidentID, req.PlaybookName, req.ServiceName, ts.Unix(), req.DryRun)
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

	// 3. Enforce the agent's own remediation policy, independently of what the
	// control plane asked for.
	//
	// This is not redundant with the check in correlation-service's
	// AutomationRouter. This service is where commands actually run, it is the
	// component an enterprise customer deploys inside their own cluster, and
	// the caller is a different process across a network boundary. An operator
	// who pins this agent to dry-run means it — a compromised or misconfigured
	// control plane must not be able to talk it into mutating their
	// infrastructure.
	dryRun := req.DryRun
	if !dryRun && !a.policy.AllowsExecution() {
		log.Printf("agent-handler: downgrading a live request to dry-run — this agent's REMEDIATION_MODE is %q",
			a.policy.Mode)
		dryRun = true
	}

	verb := "Executing"
	if dryRun {
		verb = "Planning (dry-run)"
	}
	log.Printf("agent-handler: SUCCESSFUL signature verified. %s playbook %q on service %q for incident %q",
		verb, req.PlaybookName, req.ServiceName, req.IncidentID)

	status, output := a.runPlaybook(r.Context(), req, dryRun)

	writeJSON(w, http.StatusOK, map[string]any{
		"status":  status,
		"output":  output,
		"dry_run": dryRun,
	})
}

// runPlaybook computes what a playbook would do and — unless this is a dry run
// — does it.
//
// Every case builds its plan first and returns early when dryRun is set, so
// the planned commands and the executed ones are literally the same strings.
// A dry-run mode that describes something other than what would really happen
// is worse than no dry-run mode, because it manufactures false confidence.
func (a *API) runPlaybook(ctx context.Context, req SignedPlaybookRequest, dryRun bool) (status, output string) {
	switch req.PlaybookName {
	case "recycle_db_pool":
		const recycleQuery = `
			SELECT pg_terminate_backend(pid)
			FROM pg_stat_activity
			WHERE state = 'idle in transaction'
			  AND state_change < NOW() - INTERVAL '1 minute'
		`
		plan := playbookPlan{
			summary: "Would terminate Postgres backends idle in transaction for over a minute.",
			steps:   []string{"SQL: " + strings.Join(strings.Fields(recycleQuery), " ")},
		}
		if dryRun {
			return PlaybookStatusDryRun, plan.render(req.ServiceName)
		}

		pgPool, err := db.NewPostgresPool(ctx)
		if err != nil {
			return PlaybookStatusFailed, fmt.Sprintf("Failed to connect to database for recycling connection pool: %v", err)
		}
		defer pgPool.Close()

		tag, err := pgPool.Exec(ctx, recycleQuery)
		if err != nil {
			return PlaybookStatusFailed, fmt.Sprintf("Recycle DB pool failed: %v", err)
		}
		return PlaybookStatusExecuted, fmt.Sprintf("Successfully recycled database connection pool. Terminated connections: %d.", tag.RowsAffected())

	case "restart_service":
		plan := playbookPlan{
			summary: fmt.Sprintf("Would perform a rolling restart of %s, falling back to Docker if Kubernetes is unavailable.", req.ServiceName),
			steps: []string{
				"kubectl rollout restart deployment/" + req.ServiceName,
				"docker restart pulsetrace-" + req.ServiceName + "   (only if the kubectl step fails)",
			},
		}
		if dryRun {
			return PlaybookStatusDryRun, plan.render(req.ServiceName)
		}

		kOut, kErr := a.runCmd(ctx, "kubectl", "rollout", "restart", "deployment/"+req.ServiceName)
		if kErr == nil {
			return PlaybookStatusExecuted, fmt.Sprintf("Successfully executed rolling restart on Kubernetes for deployment/%s. Output: %s", req.ServiceName, kOut)
		}

		log.Printf("agent-handler: kubectl restart failed, trying docker restart fallback: %v", kErr)
		dOut, dErr := a.runCmd(ctx, "docker", "restart", "pulsetrace-"+req.ServiceName)
		if dErr != nil {
			log.Printf("agent-handler: docker restart fallback failed: %v", dErr)
			return PlaybookStatusFailed, fmt.Sprintf("Failed to restart service %q. Kubectl error: %v (output: %q). Docker error: %v (output: %q).",
				req.ServiceName, kErr, kOut, dErr, dOut)
		}
		return PlaybookStatusExecuted, fmt.Sprintf("Successfully restarted container pulsetrace-%s using Docker. Output: %s", req.ServiceName, dOut)

	case "scale_replicas":
		plan := playbookPlan{
			summary: fmt.Sprintf("Would scale %s to 4 replicas.", req.ServiceName),
			steps:   []string{"kubectl scale deployment/" + req.ServiceName + " --replicas=4"},
		}
		if dryRun {
			return PlaybookStatusDryRun, plan.render(req.ServiceName)
		}

		kOut, kErr := a.runCmd(ctx, "kubectl", "scale", "deployment/"+req.ServiceName, "--replicas=4")
		if kErr != nil {
			log.Printf("agent-handler: kubectl scale failed: %v", kErr)
			return PlaybookStatusFailed, fmt.Sprintf("Failed to scale deployment/%s. Kubectl error: %v (output: %q).",
				req.ServiceName, kErr, kOut)
		}
		return PlaybookStatusExecuted, fmt.Sprintf("Successfully scaled deployment/%s to 4 replicas. Output: %s", req.ServiceName, kOut)

	default:
		// An unknown playbook is a failure in both modes: a dry run that
		// reported "nothing to do" would hide the misconfiguration.
		return PlaybookStatusFailed, fmt.Sprintf("Unknown playbook %q — no handler registered for service %q.", req.PlaybookName, req.ServiceName)
	}
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
