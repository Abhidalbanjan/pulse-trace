package main

import (
	"context"
	"log"
	"net/http"
	_ "net/http/pprof"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/pulsetrace/correlation-service/internal/engine"
	"github.com/pulsetrace/correlation-service/internal/handler"
	"github.com/pulsetrace/correlation-service/internal/llm"
	"github.com/pulsetrace/correlation-service/internal/query"
	"github.com/pulsetrace/correlation-service/internal/repository"
	correlationmigrations "github.com/pulsetrace/correlation-service/migrations"
	"github.com/pulsetrace/shared/causal"
	"github.com/pulsetrace/shared/client"
	"github.com/pulsetrace/shared/db"
	"github.com/pulsetrace/shared/kafka"
	"github.com/pulsetrace/shared/middleware"
	"github.com/pulsetrace/shared/migrate"
	"github.com/pulsetrace/shared/rabbitmq"
	"github.com/pulsetrace/shared/remediation"
	"github.com/pulsetrace/shared/telemetry"
	"github.com/grafana/pyroscope-go"
)

const (
	alertsTopic = "alerts"
	groupID     = "correlation-service"
	serviceName = "correlation-service"
)

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
	pyroscopeURL := os.Getenv("PYROSCOPE_URL")
	if pyroscopeURL == "" {
		pyroscopeURL = "http://pyroscope:4040"
	}
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

	// ── Database ──────────────────────────────────────────────────────────────
	pool, err := db.NewPostgresPool(ctx)
	if err != nil {
		log.Fatalf("database connection failed: %v", err)
	}
	defer pool.Close()

	// Apply this service's schema migrations (incidents, causal fields, SLO
	// tables, tenant fields) before serving. Uses a short-lived database/sql
	// handle since the runtime pool is pgx-native.
	if migDB, err := db.OpenSQLForMigrations(ctx); err != nil {
		log.Fatalf("correlation-service: could not open db for migrations: %v", err)
	} else {
		if err := migrate.Run(ctx, migDB, "correlation", correlationmigrations.FS); err != nil {
			migDB.Close()
			log.Fatalf("correlation-service: schema migration failed: %v", err)
		}
		migDB.Close()
	}

	// ── RabbitMQ publisher ────────────────────────────────────────────────────
	publisher, err := rabbitmq.NewPublisher()
	if err != nil {
		log.Printf("WARNING: rabbitmq unavailable, notifications disabled: %v", err)
		publisher = nil
	} else {
		defer publisher.Close()
	}

	// ── Causal AI analyzer ────────────────────────────────────────────────────
	// If ANTHROPIC_API_KEY is set (and CAUSAL_DISABLED is not "true"), use
	// the LLM-backed analyzer. Otherwise fall back to the deterministic
	// rule-based analyzer — the service runs identically without an API key.
	analyzer := selectCausalAnalyzer()
	log.Printf("causal analyzer: %s", analyzer.Name())

	// ── Topology Client ───────────────────────────────────────────────────────
	topoURL := os.Getenv("TOPOLOGY_SERVICE_URL")
	if topoURL == "" {
		topoURL = "http://localhost:8084"
	}
	topoClient := client.NewTopologyClient(topoURL)

	// ── SaaS Forwarder ────────────────────────────────────────────────────────
	forwarder := engine.NewSaaSForwarder()

	// ── Automation Router ─────────────────────────────────────────────────────
	// The remediation policy decides whether a high-confidence causal analysis
	// may change the customer's infrastructure by itself. It defaults to
	// requiring human approval; set REMEDIATION_MODE=auto to restore the old
	// unconditional auto-execution. See shared/remediation.
	remediationPolicy, err := remediation.PolicyFromEnv()
	if err != nil {
		// PolicyFromEnv returns the restrictive default alongside its error, so
		// a misconfigured value degrades to "ask a human" rather than to
		// unrestricted execution.
		log.Printf("WARNING: %v — using remediation policy %q", err, remediationPolicy.Mode)
	}
	log.Printf("correlation-service: remediation mode = %q (confidence threshold %.2f)",
		remediationPolicy.Mode, remediationPolicy.ConfidenceThreshold)

	playbookSecret := os.Getenv("PLAYBOOK_HMAC_SECRET")
	agentURL := topoURL + "/api/v1/agent/playbook/execute"
	autoRouter := engine.NewAutomationRouterWithPolicy(
		repository.NewIncidentRepository(pool), agentURL, playbookSecret, remediationPolicy)

	// ── Wire up dependencies ──────────────────────────────────────────────────
	repo := repository.NewIncidentRepository(pool)
	correlator := engine.NewCorrelator(repo, publisher, analyzer, topoClient, forwarder, autoRouter)
	incidentHandler := handler.NewIncidentHandler(repo)
	playbookHandler := handler.NewPlaybookHandler(repo, autoRouter)

	// ── SLO subsystem ────────────────────────────────────────────────────────
	// ── Quickwit (SLI queries) ────────────────────────────────────────────────
	quickwitURL := os.Getenv("QUICKWIT_URL")
	if quickwitURL == "" {
		log.Println("WARNING: QUICKWIT_URL not set. SLI computation will fallback to Postgres.")
	} else {
		log.Printf("correlation-service: using Quickwit at %s for SLI queries", quickwitURL)
	}
	// ── LLM provider (shared by the SLO and chat handlers) ────────────────────
	chatProvider := selectChatProvider()
	log.Printf("correlation-service: chat/SLO LLM provider = %s", chatProvider.Name())

	sloRepo := repository.NewSLORepository(pool, quickwitURL)
	sloHandler := handler.NewSLOHandler(sloRepo, chatProvider)
	sloWorker := engine.NewSLOWorker(sloRepo, publisher)
	go sloWorker.Start(ctx)

	// ── Anomaly Detector ──────────────────────────────────────────────────────
	anomalyDetector := engine.NewAnomalyDetector(topoClient)
	go anomalyDetector.Start(ctx)

	// ── User-Defined Alert Rules ──────────────────────────────────────────────
	alertRuleRepo := repository.NewAlertRuleRepository(pool)
	alertRuleEvaluator := engine.NewAlertRuleEvaluator(alertRuleRepo, publisher)
	go alertRuleEvaluator.Start(ctx)

	// ── Incident auto-resolve ─────────────────────────────────────────────────
	// An OPEN incident with no new alert for AUTO_RESOLVE_QUIET means the service
	// recovered; resolving it publishes a resolved notification, which auto-closes
	// the PagerDuty/Opsgenie alert. Disable by setting AUTO_RESOLVE_QUIET=0.
	autoResolveQuiet := getDurationEnv("AUTO_RESOLVE_QUIET", engine.DefaultAutoResolveQuiet)
	autoResolveInterval := getDurationEnv("AUTO_RESOLVE_INTERVAL", engine.DefaultAutoResolveInterval)
	if autoResolveQuiet > 0 {
		correlator.StartAutoResolveSweeper(ctx, autoResolveInterval, autoResolveQuiet)
	} else {
		log.Printf("correlator: auto-resolve disabled (AUTO_RESOLVE_QUIET=0)")
	}

	// ── Kafka consumer (alerts topic) ─────────────────────────────────────────
	cg, err := kafka.NewConsumerGroup(groupID, []string{alertsTopic}, correlator.Handle)
	if err != nil {
		log.Fatalf("kafka consumer group failed: %v", err)
	}
	defer cg.Close()

	go func() {
		log.Printf("correlation-service: consuming %q as group %q", alertsTopic, groupID)
		if err := cg.Start(ctx); err != nil {
			log.Printf("kafka consumer stopped: %v", err)
		}
	}()

	// ── LLM Chat Handler ──────────────────────────────────────────────────────
	// gatewayURL lets the chat handler's natural-language query experience
	// run real queries (search_logs/search_traces/query_metric) against
	// gateway-service's existing endpoints instead of the LLM ever
	// fabricating telemetry numbers — see internal/query/executor.go.
	gatewayURL := os.Getenv("GATEWAY_URL")
	if gatewayURL == "" {
		gatewayURL = "http://gateway:8080"
	}
	queryExecutor := query.NewExecutor(gatewayURL)
	chatHandler := handler.NewChatHandler(chatProvider, queryExecutor)

	// ── HTTP server ───────────────────────────────────────────────────────────
	mux := http.NewServeMux()
	incidentHandler.RegisterRoutes(mux)
	playbookHandler.RegisterRoutes(mux)
	sloHandler.RegisterRoutes(mux)
	chatHandler.RegisterRoutes(mux)
	mux.Handle("/debug/pprof/", http.DefaultServeMux)

	chain := middleware.CORS(middleware.Tracing(serviceName)(middleware.RequestLogger(mux)))

	port := os.Getenv("PORT")
	if port == "" {
		port = "8083"
	}

	srv := &http.Server{
		Addr:         ":" + port,
		Handler:      chain,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	go func() {
		log.Printf("correlation-service HTTP listening on :%s", port)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("server error: %v", err)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	log.Println("shutting down correlation-service...")
	cancel()

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Fatalf("forced shutdown: %v", err)
	}
	log.Println("correlation-service stopped")
}

// llmProviderSpecs resolves the configured LLM failover chain, newest config
// format first:
//
//   - LLM_PROVIDERS="anthropic:claude-sonnet-4-5,openai:gpt-4o-mini,ollama:llama3"
//     → an ordered failover chain (preferred).
//   - LLM_PROVIDER / LLM_MODEL → a single provider (legacy, still supported).
//   - ANTHROPIC_API_KEY set with neither of the above → anthropic, using the
//     legacy CAUSAL_MODEL if present.
//
// Returns nil when nothing is configured, so callers can pick their own
// no-LLM behaviour rather than being handed an empty chain.
// getDurationEnv parses a Go duration (e.g. "10m", "30s") from an env var, or
// returns def when unset/invalid. "0" disables (returns 0).
func getDurationEnv(key string, def time.Duration) time.Duration {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		log.Printf("invalid %s=%q, using default %s: %v", key, v, def, err)
		return def
	}
	return d
}

func llmProviderSpecs() []causal.ProviderSpec {
	if chain := os.Getenv("LLM_PROVIDERS"); chain != "" {
		return causal.ParseProviderChain(chain)
	}

	provider := os.Getenv("LLM_PROVIDER")
	modelName := os.Getenv("LLM_MODEL")

	if provider == "" && os.Getenv("ANTHROPIC_API_KEY") != "" {
		provider = "anthropic"
		if modelName == "" {
			modelName = os.Getenv("CAUSAL_MODEL")
		}
	}
	if provider == "" {
		return nil
	}
	return []causal.ProviderSpec{{Provider: provider, Model: modelName}}
}

// selectCausalAnalyzer picks the causal-AI implementation based on env:
//   - CAUSAL_DISABLED=true → NoopAnalyzer (no inference, rule-based only)
//   - providers configured → a FallbackAnalyzer chaining them in order, so a
//     rate limit or outage at the primary model degrades RCA quality instead
//     of dropping the analysis entirely
//   - otherwise            → NoopAnalyzer
//
// NoopAnalyzer remains the last resort in every failure path: incidents still
// get a deterministic dependency-graph causal chain, just without a narrative.
func selectCausalAnalyzer() causal.Analyzer {
	if os.Getenv("CAUSAL_DISABLED") == "true" {
		return &causal.NoopAnalyzer{}
	}

	specs := llmProviderSpecs()
	if len(specs) == 0 {
		return &causal.NoopAnalyzer{}
	}

	analyzer, err := causal.NewAnalyzerChain(specs)
	if err != nil {
		log.Printf("WARNING: no causal LLM provider could be initialized: %v. Falling back to NoopAnalyzer.", err)
		return &causal.NoopAnalyzer{}
	}
	log.Printf("correlation-service: causal analyzer = %s", analyzer.Name())
	return analyzer
}

// selectChatProvider picks the LLM backing the chat and SLO handlers, using
// the same LLM_PROVIDERS chain as the causal analyzer.
//
// The last resort here is a direct Ollama client rather than an error: the
// chat surface previously hardcoded Ollama, and a deployment that relied on
// that must keep working with no config change.
func selectChatProvider() llm.Provider {
	specs := llmProviderSpecs()

	chatSpecs := make([]llm.ProviderSpec, 0, len(specs))
	for _, s := range specs {
		chatSpecs = append(chatSpecs, llm.ProviderSpec{Provider: s.Provider, Model: s.Model})
	}

	if len(chatSpecs) > 0 {
		provider, err := llm.NewProviderChain(chatSpecs)
		if err == nil {
			return provider
		}
		log.Printf("WARNING: no chat LLM provider could be initialized: %v. Falling back to local Ollama.", err)
	}

	return llm.NewOllamaProvider("", "") // Defaults to host.docker.internal
}
