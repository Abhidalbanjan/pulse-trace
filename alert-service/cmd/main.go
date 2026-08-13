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

	"github.com/pulsetrace/alert-service/internal/consumer"
	"github.com/pulsetrace/alert-service/internal/handler"
	"github.com/pulsetrace/alert-service/internal/repository"
	alertmigrations "github.com/pulsetrace/alert-service/migrations"
	"github.com/pulsetrace/shared/db"
	"github.com/pulsetrace/shared/kafka"
	"github.com/pulsetrace/shared/middleware"
	"github.com/pulsetrace/shared/migrate"
	"github.com/pulsetrace/shared/telemetry"
	"github.com/grafana/pyroscope-go"
)

const (
	logsTopic   = "logs"
	groupID     = "alert-service"
	serviceName = "alert-service"
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

	// Apply this service's schema migrations (the alerts table) before serving.
	if migDB, err := db.OpenSQLForMigrations(ctx); err != nil {
		log.Fatalf("alert-service: could not open db for migrations: %v", err)
	} else {
		if err := migrate.Run(ctx, migDB, "alert", alertmigrations.FS); err != nil {
			migDB.Close()
			log.Fatalf("alert-service: schema migration failed: %v", err)
		}
		migDB.Close()
	}

	// ── Kafka producer (for publishing alerts to correlation engine) ───────────
	producer, err := kafka.NewProducer()
	if err != nil {
		log.Printf("WARNING: kafka producer unavailable: %v", err)
		producer = nil
	} else {
		defer producer.Close()
	}

	// ── Wire up dependencies ──────────────────────────────────────────────────
	repo := repository.NewAlertRepository(pool)
	silenceRepo := repository.NewSilenceRepository(pool)
	logConsumer := consumer.NewLogConsumer(repo, producer)
	alertHandler := handler.NewAlertHandler(repo).WithSilences(silenceRepo)
	silenceHandler := handler.NewSilenceHandler(silenceRepo)

	// ── Kafka consumer (logs topic) ───────────────────────────────────────────
	cg, err := kafka.NewConsumerGroup(groupID, []string{logsTopic}, logConsumer.Handle)
	if err != nil {
		log.Fatalf("kafka consumer group failed: %v", err)
	}
	defer cg.Close()

	go func() {
		log.Printf("alert-service: starting kafka consumer group %q on topic %q", groupID, logsTopic)
		if err := cg.Start(ctx); err != nil {
			log.Printf("kafka consumer stopped: %v", err)
		}
	}()

	// ── HTTP server ───────────────────────────────────────────────────────────
	mux := http.NewServeMux()
	alertHandler.RegisterRoutes(mux)
	silenceHandler.RegisterRoutes(mux)
	mux.Handle("/debug/pprof/", http.DefaultServeMux)

	chain := middleware.CORS(middleware.Tracing(serviceName)(middleware.RequestLogger(mux)))

	port := os.Getenv("PORT")
	if port == "" {
		port = "8082"
	}

	srv := &http.Server{
		Addr:         ":" + port,
		Handler:      chain,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	go func() {
		log.Printf("alert-service HTTP listening on :%s", port)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("server error: %v", err)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	log.Println("shutting down alert-service...")
	cancel()

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Fatalf("forced shutdown: %v", err)
	}
	log.Println("alert-service stopped")
}
