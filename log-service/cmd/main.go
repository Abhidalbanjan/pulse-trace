package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/pulsetrace/log-service/internal/handler"
	"github.com/pulsetrace/log-service/internal/repository"
	"github.com/pulsetrace/shared/db"
	"github.com/pulsetrace/shared/kafka"
	"github.com/pulsetrace/shared/middleware"
	"github.com/pulsetrace/shared/telemetry"
)

const serviceName = "log-service"

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

	// ── Database ──────────────────────────────────────────────────────────────
	pool, err := db.NewPostgresPool(ctx)
	if err != nil {
		log.Fatalf("database connection failed: %v", err)
	}
	defer pool.Close()

	// ── Kafka producer ────────────────────────────────────────────────────────
	producer, err := kafka.NewProducer()
	if err != nil {
		log.Printf("WARNING: kafka producer unavailable, continuing without event publishing: %v", err)
		producer = nil
	} else {
		defer producer.Close()
	}

	// ── HTTP server ───────────────────────────────────────────────────────────
	repo := repository.NewLogRepository(pool)
	logHandler := handler.NewLogHandler(repo, producer)

	mux := http.NewServeMux()
	logHandler.RegisterRoutes(mux)

	// Middleware chain: CORS → Tracing → RequestLogger → router
	chain := middleware.CORS(middleware.Tracing(serviceName)(middleware.RequestLogger(mux)))

	port := os.Getenv("PORT")
	if port == "" {
		port = "8081"
	}

	srv := &http.Server{
		Addr:         ":" + port,
		Handler:      chain,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	go func() {
		log.Printf("log-service listening on :%s", port)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("server error: %v", err)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	log.Println("shutting down log-service...")
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Fatalf("forced shutdown: %v", err)
	}
	log.Println("log-service stopped")
}
