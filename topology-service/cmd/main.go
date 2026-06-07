package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/pulsetrace/shared/kafka"
	"github.com/pulsetrace/shared/telemetry"
	"github.com/pulsetrace/topology-service/internal/consumer"
	"github.com/pulsetrace/topology-service/internal/handler"
	"github.com/pulsetrace/topology-service/internal/repository"
)

func main() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Initialize OpenTelemetry
	_, shutdown, err := telemetry.InitTracer(ctx, "topology-service")
	if err != nil {
		log.Fatalf("failed to init telemetry: %v", err)
	}
	defer func() {
		if err := shutdown(context.Background()); err != nil {
			log.Printf("failed to shutdown telemetry: %v", err)
		}
	}()

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

	repo, err := repository.NewNeo4jRepository(uri, user, pass)
	if err != nil {
		log.Fatalf("failed to connect to neo4j: %v", err)
	}
	defer repo.Close(ctx)

	// Seed basic edges for the demo since full trace-based inference is complex
	seedEdges(ctx, repo)

	// API Setup
	api := handler.NewAPI(repo)
	mux := http.NewServeMux()
	api.RegisterRoutes(mux)

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
	cg, err := kafka.NewConsumerGroup(groupID, []string{"logs"}, graphBuilder.Handle)
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
		if err := cg.Start(ctx); err != nil {
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
		if err := repo.UpsertDependencyEdge(ctx, edge[0], edge[1]); err != nil {
			log.Printf("failed to seed edge %s->%s: %v", edge[0], edge[1], err)
		}
	}
}
