package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	_ "net/http/pprof"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/grafana/pyroscope-go"
	"github.com/pulsetrace/gateway-service/internal/auth"
	"github.com/pulsetrace/gateway-service/internal/billing"
	"github.com/pulsetrace/gateway-service/internal/handler"
	"github.com/pulsetrace/gateway-service/internal/otlp"
	"github.com/pulsetrace/gateway-service/internal/pii"
	"github.com/pulsetrace/gateway-service/internal/proxy"
	"github.com/pulsetrace/gateway-service/internal/quota"
	"github.com/pulsetrace/gateway-service/internal/tenantdata"
	"github.com/pulsetrace/shared/metering"
	gatewaymigrations "github.com/pulsetrace/gateway-service/migrations"
	"github.com/pulsetrace/shared/middleware"
	"github.com/pulsetrace/shared/migrate"
	"github.com/pulsetrace/shared/telemetry"
)

const serviceName = "gateway-service"

func main() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// ── Telemetry ─────────────────────────────────────────────────────────────
	_, shutdownTracer, err := telemetry.InitTracer(ctx, serviceName)
	if err != nil {
		log.Printf("WARNING: tracing unavailable: %v", err)
	} else {
		defer func() {
			if err := shutdownTracer(context.Background()); err != nil {
				log.Printf("tracer shutdown error: %v", err)
			}
		}()
	}

	// ── Continuous Profiling (Pyroscope) ──────────────────────────────────────
	pyroscopeURL := getEnv("PYROSCOPE_URL", "http://pyroscope:4040")
	pyroscope.Start(pyroscope.Config{
		ApplicationName: serviceName,
		ServerAddress:   pyroscopeURL,
		Logger:          pyroscope.StandardLogger,
		ProfileTypes: []pyroscope.ProfileType{
			pyroscope.ProfileCPU,
			pyroscope.ProfileAllocObjects,
			pyroscope.ProfileAllocSpace,
			pyroscope.ProfileInuseObjects,
			pyroscope.ProfileInuseSpace,
		},
	})

	// ── Auth Handler ──────────────────────────────────────────────────────────
	clickhouseURL := getEnv("CLICKHOUSE_URL", "http://clickhouse:8123")

	// Initialize handlers
	authHandler, err := auth.NewAuthHandler()
	if err != nil {
		log.Printf("gateway-service: auth database connection failed: %v", err)
	}

	// Apply this service's schema migrations before serving. gateway-service
	// owns the users/RBAC/deployments/error-tracking/rate-limit/audit tables;
	// previously these were only created by hand, so a fresh database came up
	// missing every one of them. Reuse the auth handler's existing lib/pq
	// connection (multi-statement Exec works natively there).
	if authHandler != nil && authHandler.GetDB() != nil {
		if err := migrate.Run(ctx, authHandler.GetDB(), "gateway", gatewaymigrations.FS); err != nil {
			log.Fatalf("gateway-service: schema migration failed: %v", err)
		}
	} else {
		log.Println("gateway-service: WARNING — no database connection, skipping migrations")
	}
	// Per-tenant ingestion keys: the source of truth for which tenant an
	// ingestion request belongs to. AuthMiddleware resolves the presented key
	// against this store instead of trusting a client-supplied X-Tenant-ID header.
	ingestionKeys := auth.NewIngestionKeyStore(authHandler.GetDB())
	// Tenants as first-class entities + the self-serve signup funnel.
	tenantStore := auth.NewTenantStore(authHandler.GetDB())
	// Usage metering: Redis counters on the hot path, flushed to usage_daily.
	usageMeter := metering.New(getEnv("REDIS_ADDR", "redis:6379"), authHandler.GetDB())
	usageMeter.StartFlusher(ctx, 30*time.Second)
	// Per-plan monthly quota enforcement, backed by the meter + tenants.plan.
	quotaEnforcer := quota.New(usageMeter, authHandler.GetDB())
	analyticsHandler := handler.NewAnalyticsHandler(clickhouseURL)
	serviceHandler := handler.NewServiceHandler(clickhouseURL)
	metricsHandler := handler.NewMetricsHandler(clickhouseURL)
	errorTrackingHandler := handler.NewErrorTrackingHandler(clickhouseURL, authHandler.GetDB())
	deploymentHandler := handler.NewDeploymentHandler(authHandler.GetDB())
	rbacEngine := auth.NewRBACEngine(authHandler.GetDB())
	auditLogHandler := auth.NewAuditLogHandler(authHandler.GetDB())
	// Distributed rate limiting: counters live in Redis, so the budget holds across
	// every gateway-service replica, not just per-instance. The initial rules here are
	// just a pre-Postgres-load seed - rateLimitRuleHandler immediately overwrites them
	// with whatever's in rate_limit_rules and keeps polling every 5s after that, so
	// admins can add/edit/disable a rule from Settings with no redeploy.
	rateLimiter := middleware.NewRateLimiter(getEnv("REDIS_ADDR", "redis:6379"), []middleware.RateLimitRule{
		{Name: "auth-login", PathPrefixes: []string{"/api/v1/auth/login", "/api/v1/auth/register"}, Limit: 10, Window: time.Minute},
		{Name: "telemetry-ingest", PathPrefixes: []string{"/v1/traces", "/v1/metrics", "/v1/logs", "/api/v1/logs"}, Limit: 6000, Window: time.Minute},
		{Name: "default", PathPrefixes: []string{"/"}, Limit: 600, Window: time.Minute},
	})
	rateLimitRuleHandler := handler.NewRateLimitRuleHandler(authHandler.GetDB(), rateLimiter)
	alertRuleHandler := handler.NewAlertRuleHandler(authHandler.GetDB())
	rateLimitRuleHandler.StartPolling(ctx, 5*time.Second)
	rumHandler := handler.NewRUMHandler(clickhouseURL, usageMeter)
	syntheticsHandler := handler.NewSyntheticsHandler(clickhouseURL, authHandler.GetDB())
	syntheticsHandler.StartWorker()

	// ── Routes ────────────────────────────────────────────────────────────────
	logServiceURL := getEnv("LOG_SERVICE_URL", "http://localhost:8081")
	alertServiceURL := getEnv("ALERT_SERVICE_URL", "http://localhost:8082")
	correlationServiceURL := getEnv("CORRELATION_SERVICE_URL", "http://localhost:8083")
	topologyServiceURL := getEnv("TOPOLOGY_SERVICE_URL", "http://localhost:8084")
	actionServiceURL := getEnv("ACTION_SERVICE_URL", "http://localhost:8085")
	otelCollectorHTTPURL := getEnv("OTEL_COLLECTOR_HTTP_URL", "http://localhost:4318")

	quickwitURL := getEnv("QUICKWIT_URL", "http://pulsetrace-quickwit:7280")
	jaegerURL := getEnv("JAEGER_URL", "http://pulsetrace-jaeger:16686")

	routes := []proxy.Route{
		{Prefix: "/api/v1/logs", Upstream: logServiceURL},
		{Prefix: "/api/v1/alerts", Upstream: alertServiceURL},
		{Prefix: "/api/v1/incidents", Upstream: correlationServiceURL},
		{Prefix: "/api/v1/slo", Upstream: correlationServiceURL},
		// Previously missing entirely - the homepage's "AI SRE" chat page
		// (frontend/src/app/page.tsx) has always POSTed to /api/v1/chat, but
		// with no route for it here, every request 404'd at the gateway and
		// never reached correlation-service's ChatHandler.
		{Prefix: "/api/v1/chat", Upstream: correlationServiceURL},
		{Prefix: "/api/v1/topology/", Upstream: topologyServiceURL},
		{Prefix: "/api/v1/actions", Upstream: actionServiceURL},
		{Prefix: "/api/v1/profiler/", Upstream: pyroscopeURL},
		// Quickwit's real search API is /api/v1/{index}/search, not
		// /api/v1/search/{index}/search - the frontend's gateway-facing path has
		// an extra "search" segment used purely to route here, so it must be
		// stripped (and /api/v1 re-added) before forwarding upstream.
		{Prefix: "/api/v1/search/", Upstream: quickwitURL, Rewrite: func(p string) string {
			return "/api/v1" + strings.TrimPrefix(p, "/api/v1/search")
		}},
		{Prefix: "/api/traces", Upstream: jaegerURL},
		{Prefix: "/api/services", Upstream: jaegerURL},
		// NOTE: /v1/traces|metrics|logs are NOT proxied raw anymore — they're
		// terminated in-process below (otlpHTTP) so telemetry is tenant-stamped
		// before reaching the collector, mirroring the gRPC receiver.
	}

	router := proxy.NewRouter(routes)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok", "service": "gateway"})
	})
	mux.Handle("/debug/pprof/", http.DefaultServeMux)

	if authHandler != nil {
		mux.HandleFunc("POST /api/v1/auth/login", authHandler.Login)
		mux.HandleFunc("POST /api/v1/auth/register", authHandler.Register)
		// Self-serve signup: creates a new tenant and its first admin.
		mux.HandleFunc("POST /api/v1/auth/signup", tenantStore.Signup)
		// The caller's own tenant (plan/status) — authenticated.
		mux.HandleFunc("GET /api/v1/tenant", tenantStore.GetCurrentTenant)
	// Metered usage for the current billing period.
		usageHandler := handler.NewUsageHandler(authHandler.GetDB())
		mux.HandleFunc("GET /api/v1/usage", usageHandler.GetUsage)

		// Billing: provider-agnostic (Stripe for SaaS, manual for on-prem).
		billingHandler := billing.NewHandler(billing.FromEnv(), tenantStore)
		mux.HandleFunc("POST /api/v1/billing/checkout", billingHandler.Checkout)
		mux.HandleFunc("POST /api/v1/billing/portal", billingHandler.Portal)
		if billingHandler.IsStripe() {
			// Public, signature-verified — see the allowlist in AuthMiddleware.
			mux.HandleFunc("POST /api/v1/webhooks/stripe", billingHandler.Webhook)
		} else {
			// Manual/on-prem: operators set plans directly (admin-gated). Never
			// exposed under Stripe, so a SaaS tenant can't self-upgrade for free.
			mux.HandleFunc("POST /api/v1/admin/tenant/plan", billingHandler.SetPlan)
		}
		mux.HandleFunc("GET /api/v1/auth/sso/login", authHandler.SSOLogin)
		mux.HandleFunc("GET /api/v1/auth/sso/config", authHandler.GetSSOConfig)
		mux.HandleFunc("GET /api/v1/auth/sso/callback", authHandler.SSOCallback)
		mux.HandleFunc("GET /api/v1/admin/users", authHandler.GetUsers)
		mux.HandleFunc("POST /api/v1/admin/users", authHandler.CreateUser)
		mux.HandleFunc("DELETE /api/v1/admin/users", authHandler.DeleteUser)
		mux.HandleFunc("PUT /api/v1/admin/users/role", authHandler.UpdateUserRole)

		// Tenant data deletion (GDPR / offboarding): purge telemetry, or fully
		// close the account. Confirmation-guarded; admin-gated by RBAC.
		purger := tenantdata.New(authHandler.GetDB(), clickhouseURL, topologyServiceURL, quickwitURL)
		mux.HandleFunc("POST /api/v1/admin/tenant/purge-data", purger.PurgeDataHandler)
		mux.HandleFunc("POST /api/v1/admin/tenant/close", purger.CloseAccountHandler)

		// Per-tenant ingestion keys: mint/list/revoke the credentials telemetry
		// agents present so ingestion is attributed to a tenant server-side rather
		// than from a spoofable header. Admin-gated by RBACEngine.Middleware like
		// every other /api/v1/admin route.
		mux.HandleFunc("GET /api/v1/admin/ingestion-keys", ingestionKeys.ListIngestionKeys)
		mux.HandleFunc("POST /api/v1/admin/ingestion-keys", ingestionKeys.CreateIngestionKey)
		mux.HandleFunc("DELETE /api/v1/admin/ingestion-keys/{id}", ingestionKeys.RevokeIngestionKey)

		// Dynamic RBAC: role CRUD (permissions e.g. "read"/"write"/"admin"/"*")
		mux.HandleFunc("GET /api/v1/admin/roles", rbacEngine.ListRoles)
		mux.HandleFunc("POST /api/v1/admin/roles", rbacEngine.CreateRole)
		mux.HandleFunc("PUT /api/v1/admin/roles/{name}", rbacEngine.UpdateRole)
		mux.HandleFunc("DELETE /api/v1/admin/roles/{name}", rbacEngine.DeleteRole)

		// ABAC: attribute-based policy CRUD (expr-lang conditions over subject/resource/action)
		mux.HandleFunc("GET /api/v1/admin/policies", rbacEngine.ListPolicies)
		mux.HandleFunc("POST /api/v1/admin/policies", rbacEngine.CreatePolicy)
		mux.HandleFunc("PUT /api/v1/admin/policies/{id}", rbacEngine.UpdatePolicy)
		mux.HandleFunc("DELETE /api/v1/admin/policies/{id}", rbacEngine.DeletePolicy)

		// Audit trail for role/policy/user mutations
		mux.HandleFunc("GET /api/v1/admin/audit-log", auditLogHandler.ListAuditLog)

		// Dynamic rate limit rules: DB-backed, no redeploy needed to change a limit
		mux.HandleFunc("GET /api/v1/admin/rate-limits", rateLimitRuleHandler.ListRateLimitRules)
		mux.HandleFunc("POST /api/v1/admin/rate-limits", rateLimitRuleHandler.CreateRateLimitRule)
		mux.HandleFunc("PUT /api/v1/admin/rate-limits/{id}", rateLimitRuleHandler.UpdateRateLimitRule)
		mux.HandleFunc("DELETE /api/v1/admin/rate-limits/{id}", rateLimitRuleHandler.DeleteRateLimitRule)

		// User-defined alert rules: DB-backed, evaluated by correlation-service's
		// AlertRuleEvaluator (polls this same table directly), no redeploy needed.
		mux.HandleFunc("GET /api/v1/admin/alert-rules", alertRuleHandler.ListAlertRules)
		mux.HandleFunc("POST /api/v1/admin/alert-rules", alertRuleHandler.CreateAlertRule)
		mux.HandleFunc("PUT /api/v1/admin/alert-rules/{id}", alertRuleHandler.UpdateAlertRule)
		mux.HandleFunc("DELETE /api/v1/admin/alert-rules/{id}", alertRuleHandler.DeleteAlertRule)
	}

	// Analytics APIs (powered by ClickHouse)
	mux.HandleFunc("GET /api/v1/analytics/traces", analyticsHandler.GetTraceAnalytics)
	mux.HandleFunc("GET /api/v1/analytics/traces/facets", analyticsHandler.GetTraceFacets)

	// Service Page APIs (powered by ClickHouse) — per-service and per-resource RED metrics
	mux.HandleFunc("GET /api/v1/services", serviceHandler.ListServices)
	mux.HandleFunc("GET /api/v1/services/{name}", serviceHandler.GetServiceDetail)

	// Native Metrics APIs (powered by ClickHouse otel_metrics_* tables, populated
	// by the collector's clickhouse/metrics exporter — see
	// otel-collector/otel-collector-config.yaml)
	mux.HandleFunc("GET /api/v1/metrics", metricsHandler.ListMetricNames)
	mux.HandleFunc("GET /api/v1/metrics/query", metricsHandler.QueryMetric)

	// Error Tracking APIs (ClickHouse grouping + Postgres triage workflow)
	mux.HandleFunc("GET /api/v1/errors/groups", errorTrackingHandler.ListErrorGroups)
	mux.HandleFunc("POST /api/v1/errors/groups/{fingerprint}/resolve", errorTrackingHandler.ResolveErrorGroup)
	mux.HandleFunc("POST /api/v1/errors/groups/{fingerprint}/mute", errorTrackingHandler.MuteErrorGroup)
	mux.HandleFunc("POST /api/v1/errors/groups/{fingerprint}/reopen", errorTrackingHandler.ReopenErrorGroup)

	// Deployment Tracking APIs (Postgres)
	mux.HandleFunc("POST /api/v1/deployments", deploymentHandler.RecordDeployment)
	mux.HandleFunc("GET /api/v1/deployments", deploymentHandler.ListDeployments)

	// RUM APIs (powered by ClickHouse)
	mux.HandleFunc("POST /api/v1/rum/ingest", rumHandler.Ingest)
	mux.HandleFunc("GET /api/v1/rum/analytics", rumHandler.GetAnalytics)
	mux.HandleFunc("GET /api/v1/rum/errors", rumHandler.GetErrors)

	// Synthetics API
	mux.HandleFunc("GET /api/v1/synthetics/results", syntheticsHandler.GetResults)
	mux.HandleFunc("POST /api/v1/synthetics/tests", syntheticsHandler.CreateTarget)

	// Mock SaaS Control Plane endpoint for Zero-Data-Egress metadata
	mux.HandleFunc("POST /api/v1/control-plane/incidents", func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			http.Error(w, "invalid JSON", http.StatusBadRequest)
			return
		}
		log.Printf("[SAAS CONTROL PLANE] Received zero-egress anonymized incident: %+v", payload)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "received", "action": "stored_in_saas_db"})
	})

	// USP 3: Preventative Shift-Left Gates (GitHub Webhook)
	githubWebhookHandler := handler.NewGithubWebhookHandler()
	mux.HandleFunc("POST /api/v1/webhooks/github", githubWebhookHandler.Handle)

	// OTLP/HTTP ingestion, terminated in-process so each payload is tenant-stamped
	// (tenant resolved onto X-Tenant-ID by AuthMiddleware) before forwarding to the
	// collector — the HTTP counterpart of the in-process gRPC receiver below.
	otlpHTTP := otlp.NewHTTPHandler(otelCollectorHTTPURL, usageMeter.Record)
	mux.HandleFunc("POST /v1/traces", otlpHTTP.Handler(otlp.SignalTraces))
	mux.HandleFunc("POST /v1/metrics", otlpHTTP.Handler(otlp.SignalMetrics))
	mux.HandleFunc("POST /v1/logs", otlpHTTP.Handler(otlp.SignalLogs))

	mux.Handle("/", router)

	// Middleware chain: CORS → Tracing → RequestLogger → Auth → RateLimit → PII Sanitizer → RBAC/ABAC → router
	var chain http.Handler = mux
	chain = rbacEngine.Middleware(chain)
	chain = pii.PIISanitizerMiddleware(chain)
	chain = rateLimiter.RateLimit(chain)
	chain = quotaEnforcer.Middleware(chain)
	chain = auth.AuthMiddleware(ingestionKeys)(chain)
	chain = middleware.RequestLogger(chain)
	chain = middleware.Tracing(serviceName)(chain)
	chain = middleware.CORS(chain)

	port := getEnv("PORT", "8080")
	srv := &http.Server{
		Addr:         ":" + port,
		Handler:      chain,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	// ── In-process OTLP/gRPC receiver ──────────────────────────────────────────
	// Replaces the old raw TCP tunnel to :4317. This terminates OTLP/gRPC here so
	// each export is authenticated with its ingestion key and stamped with the
	// resolved tenant (tenant.id resource attribute) before being forwarded to the
	// collector — which is what gives otel_traces/otel_metrics a tenant dimension.
	otelCollectorGRPCAddr := getEnv("OTEL_COLLECTOR_GRPC_ADDR", "localhost:4317")
	otlpReceiver, err := otlp.NewReceiver(ingestionKeys, auth.RequireIngestionKey(), otelCollectorGRPCAddr, usageMeter.Record, quotaEnforcer.Allow)
	if err != nil {
		log.Fatalf("gateway-service: failed to create OTLP receiver: %v", err)
	}
	if err := otlpReceiver.Start(":4317"); err != nil {
		log.Fatalf("gateway-service: failed to start OTLP receiver: %v", err)
	}

	go func() {
		log.Printf("gateway-service listening on :%s", port)
		log.Printf("routing /api/v1/logs       → %s", logServiceURL)
		log.Printf("routing /api/v1/alerts     → %s", alertServiceURL)
		log.Printf("routing /api/v1/incidents  → %s", correlationServiceURL)
		log.Printf("routing /v1/traces|metrics|logs  → %s", otelCollectorHTTPURL)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("server error: %v", err)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	log.Println("shutting down gateway...")
	otlpReceiver.Stop()
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Fatalf("forced shutdown: %v", err)
	}
	log.Println("gateway stopped")
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
