package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
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
	"github.com/pulsetrace/gateway-service/internal/ingestproxy"
	"github.com/pulsetrace/gateway-service/internal/logbridge"
	"github.com/pulsetrace/gateway-service/internal/otlp"
	"github.com/pulsetrace/gateway-service/internal/pii"
	"github.com/pulsetrace/gateway-service/internal/proxy"
	"github.com/pulsetrace/gateway-service/internal/quota"
	"github.com/pulsetrace/gateway-service/internal/sqlq"
	"github.com/pulsetrace/gateway-service/internal/tenantdata"
	gatewaymigrations "github.com/pulsetrace/gateway-service/migrations"
	"github.com/pulsetrace/shared/bus"
	"github.com/pulsetrace/shared/metering"
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
		// Tamper-evidence back-fill (F20): hash-chain any audit rows written
		// before the chain existed, so the whole trail verifies. Idempotent and
		// self-skipping once every row already carries a hash.
		if err := auth.BackfillAuditChain(ctx, authHandler.GetDB()); err != nil {
			log.Printf("gateway-service: WARNING - audit chain back-fill failed: %v", err)
		}
	} else {
		log.Println("gateway-service: WARNING — no database connection, skipping migrations")
	}
	// Per-tenant ingestion keys: the source of truth for which tenant an
	// ingestion request belongs to. AuthMiddleware resolves the presented key
	// against this store instead of trusting a client-supplied X-Tenant-ID header.
	ingestionKeys := auth.NewIngestionKeyStore(authHandler.GetDB())
	// Revocable-session registry (F18): AuthMiddleware consults its in-memory
	// revoked-jti cache to reject tokens that were signed out.
	sessionStore := auth.NewSessionStore(authHandler.GetDB())
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
	profilerHandler := handler.NewProfilerHandler(pyroscopeURL)
	syntheticsHandler := handler.NewSyntheticsHandler(clickhouseURL, authHandler.GetDB())
	// StartWorker is deferred until after the Kafka log producer is available, so
	// the worker can be wired with the failure→alert publisher (see below).

	// ── Routes ────────────────────────────────────────────────────────────────
	logServiceURL := getEnv("LOG_SERVICE_URL", "http://localhost:8081")
	alertServiceURL := getEnv("ALERT_SERVICE_URL", "http://localhost:8082")
	correlationServiceURL := getEnv("CORRELATION_SERVICE_URL", "http://localhost:8083")
	topologyServiceURL := getEnv("TOPOLOGY_SERVICE_URL", "http://localhost:8084")
	notificationServiceURL := getEnv("NOTIFICATION_SERVICE_URL", "http://localhost:8086")
	actionServiceURL := getEnv("ACTION_SERVICE_URL", "http://localhost:8085")
	otelCollectorHTTPURL := getEnv("OTEL_COLLECTOR_HTTP_URL", "http://localhost:4318")

	quickwitURL := getEnv("QUICKWIT_URL", "http://pulsetrace-quickwit:7280")
	jaegerURL := getEnv("JAEGER_URL", "http://pulsetrace-jaeger:16686")

	routes := []proxy.Route{
		{Prefix: "/api/v1/logs", Upstream: logServiceURL},
		{Prefix: "/api/v1/alerts", Upstream: alertServiceURL},
		{Prefix: "/api/v1/incidents", Upstream: correlationServiceURL},
		{Prefix: "/api/v1/slo", Upstream: correlationServiceURL},
		// Per-tenant anomaly-detection tuning (F14): read/update thresholds +
		// on/off on correlation-service; admin-gated by RBAC, tenant-scoped.
		{Prefix: "/api/v1/anomaly", Upstream: correlationServiceURL},
		// Per-tenant alert delivery channels (F3): CRUD + test-send live on
		// notification-service; admin-gated by RBACEngine.Middleware like other
		// /api/v1 admin surfaces, and tenant-scoped from the JWT server-side.
		{Prefix: "/api/v1/notification-channels", Upstream: notificationServiceURL},
		// Remediation policy (GET /api/v1/remediation/policy) lives on
		// correlation-service's playbook handler and gates the Incidents
		// remediation UI's approve path — it needs its own prefix (it is not
		// under /api/v1/incidents).
		{Prefix: "/api/v1/remediation", Upstream: correlationServiceURL},
		// Previously missing entirely - the homepage's "AI SRE" chat page
		// (frontend/src/app/page.tsx) has always POSTed to /api/v1/chat, but
		// with no route for it here, every request 404'd at the gateway and
		// never reached correlation-service's ChatHandler.
		{Prefix: "/api/v1/chat", Upstream: correlationServiceURL},
		// Causal-AI provider health (F15): GET /api/v1/causal/providers reports
		// whether the LLM analyzer chain is up and on which provider, backing
		// the Incidents page's provider-health badge. Read-only, not tenant-
		// scoped (deployment-wide config), served by correlation-service.
		{Prefix: "/api/v1/causal", Upstream: correlationServiceURL},
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
		mux.HandleFunc("GET /api/v1/usage/series", usageHandler.GetUsageSeries)

		// Billing: provider-agnostic (Stripe for SaaS, manual for on-prem).
		billingHandler := billing.NewHandler(billing.FromEnv(), tenantStore)
		// In-app plan comparison (F17): the catalog with per-plan upgrade/downgrade
		// CTAs, quota limits straight from the enforcer.
		mux.HandleFunc("GET /api/v1/billing/plans", billingHandler.Plans)
		// Invoice history (F17): the tenant's past invoices from the provider.
		mux.HandleFunc("GET /api/v1/billing/invoices", billingHandler.Invoices)
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

		// Multi-factor auth (F18): TOTP enrolment + two-step login. /mfa/login is
		// public (it redeems the post-password challenge, see AuthMiddleware
		// allowlist); the rest require a session and act on the caller's own user.
		mfaHandler := auth.NewMFAHandler(authHandler.GetDB())
		mux.HandleFunc("POST /api/v1/auth/mfa/login", mfaHandler.Login)
		mux.HandleFunc("GET /api/v1/auth/mfa/status", mfaHandler.Status)
		mux.HandleFunc("POST /api/v1/auth/mfa/enroll", mfaHandler.Enroll)
		mux.HandleFunc("POST /api/v1/auth/mfa/verify", mfaHandler.Verify)
		mux.HandleFunc("POST /api/v1/auth/mfa/disable", mfaHandler.Disable)

		// Session revocation & device management (F18): list the caller's active
		// sessions and revoke one or all-others. Enforcement lives in
		// AuthMiddleware, which rejects a token whose jti has been revoked.
		sessionHandler := auth.NewSessionHandler(sessionStore, authHandler.GetDB())
		mux.HandleFunc("GET /api/v1/auth/sessions", sessionHandler.List)
		mux.HandleFunc("POST /api/v1/auth/sessions/revoke-others", sessionHandler.RevokeOthers)
		mux.HandleFunc("POST /api/v1/auth/sessions/{id}/revoke", sessionHandler.Revoke)

		// Password management (F18): authenticated change (revokes other sessions)
		// + the anti-enumeration forgot/reset flow. forgot/reset are public (see
		// the AuthMiddleware allowlist); change requires a session.
		passwordHandler := auth.NewPasswordHandler(authHandler.GetDB(), sessionStore, auth.MailerFromEnv())
		mux.HandleFunc("POST /api/v1/auth/password/change", passwordHandler.Change)
		mux.HandleFunc("POST /api/v1/auth/password/forgot", passwordHandler.Forgot)
		mux.HandleFunc("POST /api/v1/auth/password/reset", passwordHandler.Reset)

		// SCIM 2.0 provisioning (F18): enterprise IdPs push user lifecycle here.
		// Machine-to-machine — authenticated by SCIM_TOKEN inside the handler (see
		// the AuthMiddleware allowlist for /scim/), disabled until a token is set.
		scimHandler := auth.NewSCIMHandler(authHandler.GetDB(), sessionStore)
		if scimHandler.Enabled() {
			log.Println("gateway-service: SCIM 2.0 provisioning enabled at /scim/v2/Users")
		}
		mux.HandleFunc("/scim/v2/Users", scimHandler.ServeUsers)
		mux.HandleFunc("/scim/v2/Users/{id}", scimHandler.ServeUser)

		// SAML 2.0 SSO (F18): the enterprise alternative to OIDC. Metadata/login/
		// ACS are public (the ACS verifies the IdP's signed assertion itself, via
		// crewjam/saml); disabled until SAML_IDP_METADATA_* is configured.
		samlHandler := auth.NewSAMLHandler(authHandler.GetDB(), sessionStore)
		if samlHandler.Configured() {
			log.Println("gateway-service: SAML SSO enabled at /api/v1/auth/saml/login")
		}
		mux.HandleFunc("GET /api/v1/auth/saml/metadata", samlHandler.Metadata)
		mux.HandleFunc("GET /api/v1/auth/saml/login", samlHandler.Login)
		mux.HandleFunc("POST /api/v1/auth/saml/acs", samlHandler.ACS)
		mux.HandleFunc("GET /api/v1/admin/users", authHandler.GetUsers)
		mux.HandleFunc("POST /api/v1/admin/users", authHandler.CreateUser)
		mux.HandleFunc("DELETE /api/v1/admin/users", authHandler.DeleteUser)
		mux.HandleFunc("PUT /api/v1/admin/users/role", authHandler.UpdateUserRole)

		// Tenant data deletion (GDPR / offboarding): purge telemetry, or fully
		// close the account. Confirmation-guarded; admin-gated by RBAC.
		purger := tenantdata.New(authHandler.GetDB(), clickhouseURL, topologyServiceURL, quickwitURL)
		mux.HandleFunc("POST /api/v1/admin/tenant/purge-data", purger.PurgeDataHandler)
		mux.HandleFunc("POST /api/v1/admin/tenant/close", purger.CloseAccountHandler)

		// Shift-left deploy gates (F5): the GitHub webhook runs each PR through the
		// SLO-risk evaluator and records the verdict; the Deploy Gates screen reads
		// the recorded feed. The webhook is public (GitHub can't present a JWT) and
		// HMAC-verified when GITHUB_WEBHOOK_SECRET is set — see the AuthMiddleware
		// allowlist. The read endpoint is tenant-scoped and JWT-gated.
		githubHandler := handler.NewGithubWebhookHandler(authHandler.GetDB(), correlationServiceURL)
		mux.HandleFunc("POST /api/v1/webhooks/github", githubHandler.Handle)
		mux.HandleFunc("GET /api/v1/deployments/gates", githubHandler.ListGates)

		// Per-tenant ingestion keys: mint/list/revoke the credentials telemetry
		// agents present so ingestion is attributed to a tenant server-side rather
		// than from a spoofable header. Admin-gated by RBACEngine.Middleware like
		// every other /api/v1/admin route.
		mux.HandleFunc("GET /api/v1/admin/ingestion-keys", ingestionKeys.ListIngestionKeys)
		mux.HandleFunc("POST /api/v1/admin/ingestion-keys", ingestionKeys.CreateIngestionKey)
		mux.HandleFunc("DELETE /api/v1/admin/ingestion-keys/{id}", ingestionKeys.RevokeIngestionKey)
		mux.HandleFunc("POST /api/v1/admin/ingestion-keys/{id}/rotate", ingestionKeys.RotateIngestionKey)

		// Dynamic RBAC: role CRUD (permissions e.g. "read"/"write"/"admin"/"*")
		mux.HandleFunc("GET /api/v1/admin/roles", rbacEngine.ListRoles)
		mux.HandleFunc("POST /api/v1/admin/roles", rbacEngine.CreateRole)
		mux.HandleFunc("PUT /api/v1/admin/roles/{name}", rbacEngine.UpdateRole)
		mux.HandleFunc("DELETE /api/v1/admin/roles/{name}", rbacEngine.DeleteRole)

		// ABAC: attribute-based policy CRUD (expr-lang conditions over subject/resource/action)
		mux.HandleFunc("GET /api/v1/admin/policies", rbacEngine.ListPolicies)
		mux.HandleFunc("POST /api/v1/admin/policies", rbacEngine.CreatePolicy)
		// Live condition validation for the guided policy builder (F18): compiles
		// the expr-lang condition and returns the exact error, no persistence.
		mux.HandleFunc("POST /api/v1/admin/policies/validate", rbacEngine.ValidatePolicy)
		mux.HandleFunc("PUT /api/v1/admin/policies/{id}", rbacEngine.UpdatePolicy)
		mux.HandleFunc("DELETE /api/v1/admin/policies/{id}", rbacEngine.DeletePolicy)

		// Audit trail for role/policy/user mutations — tamper-evident (F20): the
		// log is hash-chained, exportable for compliance, and verifiable on demand.
		mux.HandleFunc("GET /api/v1/admin/audit-log", auditLogHandler.ListAuditLog)
		mux.HandleFunc("GET /api/v1/admin/audit-log/verify", auditLogHandler.VerifyAuditLog)
		mux.HandleFunc("GET /api/v1/admin/audit-log/export", auditLogHandler.ExportAuditLog)

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

		// Saved searches — per-user named log/trace queries. Not under /admin:
		// any user may manage their own (owner-scoped in the handler), and the
		// default viewer role's read / editor role's write permissions apply.
		savedSearchHandler := handler.NewSavedSearchHandler(authHandler.GetDB())
		mux.HandleFunc("GET /api/v1/saved-searches", savedSearchHandler.List)
		mux.HandleFunc("POST /api/v1/saved-searches", savedSearchHandler.Create)
		mux.HandleFunc("PUT /api/v1/saved-searches/{id}", savedSearchHandler.Update)
		mux.HandleFunc("DELETE /api/v1/saved-searches/{id}", savedSearchHandler.Delete)

		// User-authored SQL (P3.1/P3.2).
		//
		// The engine is built here rather than lazily so a misconfiguration —
		// a relation with no scanner — fails at startup instead of on someone's
		// first query. If it cannot be built the endpoint is simply not
		// registered: a query surface that 500s is worse than one that is
		// visibly absent, and the log says which relations were unserved.
		if engine, err := buildQueryEngine(authHandler.GetDB(), clickhouseURL, quickwitURL); err != nil {
			log.Printf("WARNING: SQL query endpoint unavailable: %v", err)
		} else {
			sqlQueryHandler := handler.NewSQLQueryHandler(authHandler.GetDB(), engine)
			mux.HandleFunc("POST /api/v1/query/sql", sqlQueryHandler.Execute)
			mux.HandleFunc("GET /api/v1/query/schema", sqlQueryHandler.Schema)
			log.Printf("SQL query endpoint ready at POST /api/v1/query/sql")
		}
	}

	// Analytics APIs (powered by ClickHouse)
	mux.HandleFunc("GET /api/v1/analytics/traces", analyticsHandler.GetTraceAnalytics)
	mux.HandleFunc("GET /api/v1/analytics/traces/facets", analyticsHandler.GetTraceFacets)

	// Service Page APIs (powered by ClickHouse) — per-service and per-resource RED metrics
	mux.HandleFunc("GET /api/v1/services", serviceHandler.ListServices)
	mux.HandleFunc("GET /api/v1/services/{name}", serviceHandler.GetServiceDetail)

	// First-class trace search + retrieval over ClickHouse otel_traces (Traces · E1):
	// APM-style search (service/operation/duration/status/tag) → per-trace summaries,
	// and full-span fetch for the waterfall.
	tracesHandler := handler.NewTracesHandler(clickhouseURL)
	mux.HandleFunc("GET /api/v1/traces", tracesHandler.Search)
	mux.HandleFunc("GET /api/v1/traces/latency", tracesHandler.GetLatency)
	mux.HandleFunc("GET /api/v1/traces/{id}", tracesHandler.GetTrace)

	// Native Metrics APIs (powered by ClickHouse otel_metrics_* tables, populated
	// by the collector's clickhouse/metrics exporter — see
	// otel-collector/otel-collector-config.yaml)
	mux.HandleFunc("GET /api/v1/metrics", metricsHandler.ListMetricNames)
	// Metric explorer (Metrics · E1): label keys/values for a metric so the UI can
	// show what a series can be sliced by.
	mux.HandleFunc("GET /api/v1/metrics/catalog", metricsHandler.MetricCatalog)
	mux.HandleFunc("GET /api/v1/metrics/query", metricsHandler.QueryMetric)
	mux.HandleFunc("GET /api/v1/metrics/formula", metricsHandler.QueryFormula)

	// Error Tracking APIs (ClickHouse grouping + Postgres triage workflow)
	mux.HandleFunc("GET /api/v1/errors/groups", errorTrackingHandler.ListErrorGroups)
	mux.HandleFunc("GET /api/v1/errors/groups/{fingerprint}/timeline", errorTrackingHandler.GetErrorGroupTimeline)
	mux.HandleFunc("GET /api/v1/errors/groups/{fingerprint}/similar", errorTrackingHandler.GetSimilarErrorGroups)
	mux.HandleFunc("POST /api/v1/errors/groups/{fingerprint}/resolve", errorTrackingHandler.ResolveErrorGroup)
	mux.HandleFunc("POST /api/v1/errors/groups/{fingerprint}/mute", errorTrackingHandler.MuteErrorGroup)
	mux.HandleFunc("POST /api/v1/errors/groups/{fingerprint}/reopen", errorTrackingHandler.ReopenErrorGroup)
	mux.HandleFunc("PATCH /api/v1/errors/groups/{fingerprint}", errorTrackingHandler.UpdateErrorGroup)

	// Deployment Tracking APIs (Postgres)
	mux.HandleFunc("POST /api/v1/deployments", deploymentHandler.RecordDeployment)
	mux.HandleFunc("GET /api/v1/deployments", deploymentHandler.ListDeployments)
	mux.HandleFunc("GET /api/v1/deployments/dora", deploymentHandler.GetDORA)
	mux.HandleFunc("GET /api/v1/deployments/for-incident", deploymentHandler.GetDeployForIncident)

	// RUM APIs (powered by ClickHouse)
	mux.HandleFunc("POST /api/v1/rum/ingest", rumHandler.Ingest)
	mux.HandleFunc("GET /api/v1/rum/analytics", rumHandler.GetAnalytics)
	mux.HandleFunc("GET /api/v1/rum/errors", rumHandler.GetErrors)
	// Native profiler surface: these specific paths win over the /api/v1/profiler/
	// catch-all proxy (mux most-specific-wins), so PulseTrace computes the flat
	// profile + diff itself instead of embedding Pyroscope's UI.
	mux.HandleFunc("GET /api/v1/profiler/functions", profilerHandler.GetFunctions)
	mux.HandleFunc("GET /api/v1/profiler/diff", profilerHandler.GetDiff)
	mux.HandleFunc("GET /api/v1/rum/trends", rumHandler.GetTrends)
	mux.HandleFunc("GET /api/v1/rum/sessions", rumHandler.GetSessions)
	mux.HandleFunc("GET /api/v1/rum/sessions/{id}", rumHandler.GetSession)
	mux.HandleFunc("GET /api/v1/rum/devices", rumHandler.GetDevices)
	// Core Web Vitals by page/device with good/needs-improvement/poor ratings (E4).
	mux.HandleFunc("GET /api/v1/rum/web-vitals", rumHandler.GetWebVitals)

	// Synthetics API
	mux.HandleFunc("GET /api/v1/synthetics/results", syntheticsHandler.GetResults)
	mux.HandleFunc("GET /api/v1/synthetics/uptime", syntheticsHandler.GetUptime)
	mux.HandleFunc("GET /api/v1/synthetics/tests", syntheticsHandler.ListTargets)
	mux.HandleFunc("POST /api/v1/synthetics/tests", syntheticsHandler.CreateTarget)
	mux.HandleFunc("DELETE /api/v1/synthetics/tests", syntheticsHandler.DeleteTarget)

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

	// Shift-left deploy gate (GitHub webhook) + its read feed are registered in the
	// authHandler block above, with DB persistence and tenant-scoped listing.

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
	chain = auth.AuthMiddleware(ingestionKeys, sessionStore)(chain)
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

	// "Trojan Horse" zero-code migration: accept Datadog agent traces and Splunk
	// HEC events in their native wire formats, authenticated with a PulseTrace
	// ingestion key (DD-API-KEY / Splunk token) and tenant-stamped, forwarded
	// through the same OTLP path as native telemetry. Registered on the mux
	// before the server starts serving (below), so it's race-free even though the
	// mux is already wrapped by the middleware chain.
	migrationProxy := ingestproxy.New(otlpReceiver, ingestionKeys, auth.RequireIngestionKey())
	// Route migration logs (Datadog /api/v2/logs, Splunk HEC) through the native
	// log path — published to Kafka as LogEntry records so Quickwit indexes them
	// into the pulsetrace-logs index the log explorer reads, instead of only
	// reaching ClickHouse otel_logs (which the explorer never queries). If Kafka
	// isn't reachable at startup, we log and leave the proxy on its OTLP fallback
	// rather than blocking gateway boot on the optional migration feature.
	if logBus, err := bus.NewKafkaBus(); err != nil {
		log.Printf("WARNING: log publishing to Quickwit disabled (bus unavailable); migration + OTLP logs fall back to ClickHouse otel_logs: %v", err)
		// No bus: the synthetics + error-regression workers still run, they just
		// can't page.
		syntheticsHandler.StartWorker()
		errorTrackingHandler.StartRegressionWorker()
	} else {
		defer logBus.Close()
		migrationProxy.SetLogSink(logBus, usageMeter.Record, quotaEnforcer.Allow)
		// Route OTLP-native logs (gRPC + HTTP) to the same Kafka → Quickwit path so
		// they surface in the log explorer alongside migration and app logs, instead
		// of only reaching the unqueried ClickHouse otel_logs table. Metering/quota
		// are applied by the OTLP receiver before the bridge publishes.
		logBridge := logbridge.New(logBus)
		otlpReceiver.SetLogSink(logBridge.Publish)
		otlpHTTP.SetLogSink(logBridge.Publish)
		log.Printf("logs (migration + OTLP-native) → Kafka topic 'logs' → Quickwit (log explorer)")
		// Wire the synthetics failure→alert and error-regression→alert paths onto
		// the same logs topic, then start their workers (wiring before start avoids
		// racing the first poll).
		syntheticsHandler.WithAlertPublisher(logBus).StartWorker()
		errorTrackingHandler.WithAlertPublisher(logBus).StartRegressionWorker()
	}
	migrationProxy.RegisterRoutes(mux)
	// Optional TLS/mTLS for the OTLP/gRPC listener, so the per-tenant ingestion
	// key isn't carried in cleartext. Disabled by default (plaintext) for local
	// dev and for deployments that terminate TLS at an upstream LB/ingress.
	otlpTLS, err := otlp.BuildServerTLS(
		getEnv("OTLP_TLS_CERT_FILE", ""),
		getEnv("OTLP_TLS_KEY_FILE", ""),
		getEnv("OTLP_TLS_CLIENT_CA_FILE", ""),
	)
	if err != nil {
		log.Fatalf("gateway-service: invalid OTLP TLS configuration: %v", err)
	}
	if err := otlpReceiver.Start(":4317", otlpTLS); err != nil {
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

// buildQueryEngine wires each catalog relation to the scanner that serves it.
//
// Relations are taken from the catalog rather than listed here, so adding one
// without a scanner is caught by NewEngine at startup instead of surfacing as a
// runtime failure on a query that passed validation.
func buildQueryEngine(db *sql.DB, clickhouseURL, quickwitURL string) (*sqlq.Engine, error) {
	catalog := sqlq.DefaultCatalog()
	httpClient := &http.Client{Timeout: 30 * time.Second}

	var scanners []sqlq.Scanner
	for _, name := range catalog.Names() {
		rel, _ := catalog.Lookup("", name)
		switch rel.Store {
		case sqlq.StoreLogs:
			scanners = append(scanners, &sqlq.QuickwitScanner{
				Rel: rel, URL: quickwitURL, Index: "pulsetrace-logs", Client: httpClient,
			})
		case sqlq.StoreAnalytics:
			scanners = append(scanners, &sqlq.ClickHouseScanner{
				Rel:    rel,
				URL:    clickhouseURL,
				User:   getEnv("CLICKHOUSE_USER", "pulsetrace"),
				Pass:   getEnv("CLICKHOUSE_PASSWORD", "pulsetrace_secret"),
				Client: httpClient,
			})
		case sqlq.StoreMeta:
			scanners = append(scanners, &sqlq.PostgresScanner{Rel: rel, DB: db})
		default:
			return nil, fmt.Errorf("relation %q has no store binding", rel.Name)
		}
	}
	return sqlq.NewEngine(catalog, sqlq.DefaultPolicy(), sqlq.DefaultBudget(), scanners...)
}
