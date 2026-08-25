package main

import (
	"context"
	"log"
	"net/http"
	_ "net/http/pprof"
	"os"
	"os/signal"
	"syscall"

	"github.com/grafana/pyroscope-go"
	"github.com/pulsetrace/shared/bus"
	"github.com/pulsetrace/shared/telemetry"
	"github.com/pulsetrace/topology-service/internal/consumer"
	"github.com/pulsetrace/topology-service/internal/handler"
	"github.com/pulsetrace/topology-service/internal/repository"
)

const serviceName = "topology-service"

func main() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Initialize OpenTelemetry
	_, shutdown, err := telemetry.InitTracer(ctx, serviceName)
	if err != nil {
		log.Fatalf("failed to init telemetry: %v", err)
	}
	defer func() {
		if err := shutdown(context.Background()); err != nil {
			log.Printf("failed to shutdown telemetry: %v", err)
		}
	}()

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

	// Neo4j Setup
	uri := os.Getenv("NEO4J_URI")
	if uri == "" {
		uri = "bolt://localhost:7687"
	}
	user := os.Getenv("NEO4J_USERNAME")
	if user == "" {
		user = "neo4j"
	}
	pass := os.Getenv("NEO4J_PASSWORD")
	if pass == "" {
		pass = "pulsetrace_secret"
	}

	redisAddr := os.Getenv("REDIS_ADDR")
	repo, err := repository.NewNeo4jRepository(uri, user, pass, redisAddr)
	if err != nil {
		log.Fatalf("failed to connect to neo4j: %v", err)
	}
	defer repo.Close(ctx)

	// Seed basic edges for the demo since full trace-based inference is complex
	seedEdges(ctx, repo)

	// API Setup
	secret := os.Getenv("PLAYBOOK_HMAC_SECRET")
	api := handler.NewAPI(repo, secret)
	mux := http.NewServeMux()
	api.RegisterRoutes(mux)
	mux.Handle("/debug/pprof/", http.DefaultServeMux)

	port := os.Getenv("PORT")
	if port == "" {
		port = "8084"
	}

	server := &http.Server{
		Addr:    ":" + port,
		Handler: mux,
	}

	// Kafka Setup
	brokers := []string{os.Getenv("KAFKA_BROKERS")}
	if brokers[0] == "" {
		brokers = []string{"localhost:9092"}
	}
	groupID := os.Getenv("KAFKA_GROUP_ID")
	if groupID == "" {
		groupID = "topology-service"
	}

	graphBuilder := consumer.NewGraphBuilder(repo)

	// A consumer-only service needs no producer, so the bus is constructed
	// without one: Subscribe does its own connection. Publishing through this
	// value would return ErrBusUnavailable rather than panic.
	msgbus := bus.NewKafkaBusWith(nil)
	cg, err := msgbus.Subscribe(groupID, []string{"logs"}, graphBuilder.Handle)
	if err != nil {
		log.Fatalf("failed to create consumer group: %v", err)
	}

	// Start servers
	go func() {
		log.Printf("topology-service: starting http server on :%s", port)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("http server error: %v", err)
		}
	}()

	go func() {
		log.Printf("topology-service: starting kafka consumer group %s", groupID)
		if err := cg.Run(ctx); err != nil {
			log.Printf("error from consumer: %v", err)
		}
	}()

	// Graceful shutdown
	sigterm := make(chan os.Signal, 1)
	signal.Notify(sigterm, syscall.SIGINT, syscall.SIGTERM)
	<-sigterm

	log.Println("shutting down topology-service...")
	server.Shutdown(ctx)
	cg.Close()
}

func seedEdges(ctx context.Context, repo *repository.Neo4jRepository) {
	log.Println("topology-service: seeding demo dependency edges...")
	edges := [][2]string{
		{"payment-service", "postgres"},
		{"payment-service", "kafka"},
		{"payment-service", "auth-service"},
		{"auth-service", "postgres"},
		{"order-service", "payment-service"},
		{"order-service", "postgres"},
		{"gateway-service", "log-service"},
		{"log-service", "postgres"},
	}

	for _, edge := range edges {
		if err := repo.UpsertDependencyEdge(ctx, "default", edge[0], edge[1]); err != nil {
			log.Printf("failed to seed edge %s->%s: %v", edge[0], edge[1], err)
		}
	}
}
