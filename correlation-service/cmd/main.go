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
	"github.com/pulsetrace/correlation-service/internal/repository"
	"github.com/pulsetrace/shared/causal"
	"github.com/pulsetrace/shared/client"
	"github.com/pulsetrace/shared/db"
	"github.com/pulsetrace/shared/kafka"
	"github.com/pulsetrace/shared/middleware"
	"github.com/pulsetrace/shared/rabbitmq"
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
	playbookSecret := os.Getenv("PLAYBOOK_HMAC_SECRET")
	agentURL := topoURL + "/api/v1/agent/playbook/execute"
	autoRouter := engine.NewAutomationRouter(repository.NewIncidentRepository(pool), agentURL, playbookSecret)

	// ── Wire up dependencies ──────────────────────────────────────────────────
	repo := repository.NewIncidentRepository(pool)
	correlator := engine.NewCorrelator(repo, publisher, analyzer, topoClient, forwarder, autoRouter)
	incidentHandler := handler.NewIncidentHandler(repo)

	// ── SLO subsystem ────────────────────────────────────────────────────────
	// ── Quickwit (SLI queries) ────────────────────────────────────────────────
	quickwitURL := os.Getenv("QUICKWIT_URL")
	if quickwitURL == "" {
		log.Println("WARNING: QUICKWIT_URL not set. SLI computation will fallback to Postgres.")
	} else {
		log.Printf("correlation-service: using Quickwit at %s for SLI queries", quickwitURL)
	}
	// ── LLM Chat Handler ──────────────────────────────────────────────────────
	ollamaProvider := llm.NewOllamaProvider("", "") // Defaults to host.docker.internal

	sloRepo := repository.NewSLORepository(pool, quickwitURL)
	sloHandler := handler.NewSLOHandler(sloRepo, ollamaProvider)
	sloWorker := engine.NewSLOWorker(sloRepo, publisher)
	go sloWorker.Start(ctx)

	// ── Anomaly Detector ──────────────────────────────────────────────────────
	anomalyDetector := engine.NewAnomalyDetector(topoClient)
	go anomalyDetector.Start(ctx)

	// ── User-Defined Alert Rules ──────────────────────────────────────────────
	alertRuleRepo := repository.NewAlertRuleRepository(pool)
	alertRuleEvaluator := engine.NewAlertRuleEvaluator(alertRuleRepo, publisher)
	go alertRuleEvaluator.Start(ctx)

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
	chatHandler := handler.NewChatHandler(ollamaProvider)

	// ── HTTP server ───────────────────────────────────────────────────────────
	mux := http.NewServeMux()
	incidentHandler.RegisterRoutes(mux)
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

// selectCausalAnalyzer picks the causal-AI implementation based on env:
//   - CAUSAL_DISABLED=true       → NoopAnalyzer (no inference, rule-based only)
//   - LLM_PROVIDER is configured → LangChainAnalyzer
//   - ANTHROPIC_API_KEY set      → LangChainAnalyzer (via backward-compatibility check)
//   - otherwise                  → NoopAnalyzer
func selectCausalAnalyzer() causal.Analyzer {
	if os.Getenv("CAUSAL_DISABLED") == "true" {
		return &causal.NoopAnalyzer{}
	}

	provider := os.Getenv("LLM_PROVIDER")
	// Legacy backward-compatibility check
	if provider == "" && os.Getenv("ANTHROPIC_API_KEY") != "" {
		provider = "anthropic"
	}

	if provider == "" {
		return &causal.NoopAnalyzer{}
	}

	modelName := os.Getenv("LLM_MODEL")
	if modelName == "" && provider == "anthropic" {
		// Use legacy env CAUSAL_MODEL if set
		modelName = os.Getenv("CAUSAL_MODEL")
	}

	analyzer, err := causal.NewLangChainAnalyzer(provider, modelName)
	if err != nil {
		log.Printf("WARNING: failed to initialize LangChain analyzer: %v. Falling back to NoopAnalyzer.", err)
		return &causal.NoopAnalyzer{}
	}
	return analyzer
}
