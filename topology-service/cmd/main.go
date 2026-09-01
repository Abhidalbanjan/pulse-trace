package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	_ "net/http/pprof"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/grafana/pyroscope-go"
	"github.com/pulsetrace/shared/bus"
	sharddb "github.com/pulsetrace/shared/db"
	// The SQL topology store runs on Postgres in a cluster and SQLite in a
	// single binary; the binary linking this one chooses which drivers it
	// carries, exactly as shared/db's doc describes.
	_ "github.com/pulsetrace/shared/db/driver/postgres"
	_ "github.com/pulsetrace/shared/db/driver/sqlite"
	"github.com/pulsetrace/shared/graph/sqlstore"
	"github.com/pulsetrace/shared/migrate"
	"github.com/pulsetrace/shared/telemetry"
	"github.com/pulsetrace/topology-service/internal/consumer"
	"github.com/pulsetrace/topology-service/internal/handler"
	"github.com/pulsetrace/topology-service/internal/repository"
	"github.com/pulsetrace/topology-service/migrations"
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

	redisAddr := os.Getenv("REDIS_ADDR")
	repo, err := openRepository(ctx, redisAddr)
	if err != nil {
		log.Fatalf("topology store: %v", err)
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

func seedEdges(ctx context.Context, repo *repository.Repository) {
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

// openRepository picks the topology backend.
//
// TOPOLOGY_STORE selects it explicitly: "neo4j" (the default, and what the
// cluster runs) or "sql" (what a single binary runs, on the database it already
// has). Contradictory configuration is a startup error rather than a silent
// preference — naming both backends means one of them is not going to be used
// and the operator should be told which.
//
// This switch is deliberately small and local. P1.6 replaces it with
// shared/runtime.ResolveMode, which makes the lite/cluster decision once for
// the whole binary instead of once per service.
func openRepository(ctx context.Context, redisAddr string) (*repository.Repository, error) {
	kind := strings.ToLower(strings.TrimSpace(os.Getenv("TOPOLOGY_STORE")))
	neo4jURI := os.Getenv("NEO4J_URI")

	if kind == "" {
		// Infer, but only from an unambiguous configuration.
		if neo4jURI == "" && os.Getenv("DATABASE_URL") != "" {
			kind = "sql"
		} else {
			kind = "neo4j"
		}
	}

	switch kind {
	case "neo4j":
		uri := neo4jURI
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
		repo, err := repository.NewNeo4j(uri, user, pass, redisAddr)
		if err != nil {
			return nil, fmt.Errorf("connect to neo4j at %s: %w", uri, err)
		}
		log.Printf("topology store: neo4j at %s", uri)
		return repo, nil

	case "sql":
		if neo4jURI != "" {
			return nil, fmt.Errorf("TOPOLOGY_STORE=sql but NEO4J_URI is also set (%s): "+
				"name one backend, not two", neo4jURI)
		}
		dsn := os.Getenv("DATABASE_URL")
		if dsn == "" {
			return nil, fmt.Errorf("TOPOLOGY_STORE=sql requires DATABASE_URL")
		}
		conn, dialect, err := sharddb.Open(ctx, dsn)
		if err != nil {
			return nil, fmt.Errorf("open topology database: %w", err)
		}
		// The graph tables are this service's own migrations, applied here for
		// the same reason every other service applies its own: the schema and
		// the code that depends on it ship together.
		if err := migrate.Run(ctx, conn, "topology-service", migrations.FS); err != nil {
			conn.Close()
			return nil, fmt.Errorf("apply topology migrations: %w", err)
		}
		log.Printf("topology store: sql (%s)", dialect.Kind())
		return repository.New(sqlstore.New(conn, dialect), redisAddr), nil

	default:
		return nil, fmt.Errorf("TOPOLOGY_STORE=%q is not a known backend (want \"neo4j\" or \"sql\")", kind)
	}
}
