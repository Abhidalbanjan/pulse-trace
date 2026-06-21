package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	"github.com/pulsetrace/log-service/internal/consumer"
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

	// ── ClickHouse ────────────────────────────────────────────────────────────
	chConn, err := db.NewClickHouseConnection()
	if err != nil {
		log.Fatalf("ClickHouse connection failed: %v", err)
	}
	defer chConn.Close()

	var chEnterpriseConn driver.Conn
	enterpriseAddr := os.Getenv("CLICKHOUSE_ADDR_ENTERPRISE")
	if enterpriseAddr != "" {
		enterpriseUser := os.Getenv("CLICKHOUSE_USER_ENTERPRISE")
		enterprisePassword := os.Getenv("CLICKHOUSE_PASSWORD_ENTERPRISE")
		chEnterpriseConn, err = db.NewClickHouseConnectionWithAddr(enterpriseAddr, enterpriseUser, enterprisePassword, "default")
		if err != nil {
			log.Printf("WARNING: Enterprise ClickHouse connection failed (addr=%s): %v. Proceeding with default cluster only.", enterpriseAddr, err)
		} else {
			defer chEnterpriseConn.Close()
			log.Printf("Connected to Enterprise ClickHouse shard at %s", enterpriseAddr)
		}
	}

	chRepo := repository.NewClickHouseLogRepository(chConn, chEnterpriseConn)
	if err := chRepo.InitializeSchema(ctx); err != nil {
		log.Fatalf("ClickHouse schema initialization failed: %v", err)
	}

	// ── Kafka producer ────────────────────────────────────────────────────────
	producer, err := kafka.NewProducer()
	if err != nil {
		log.Printf("WARNING: kafka producer unavailable, continuing without event publishing: %v", err)
		producer = nil
	} else {
		defer producer.Close()
	}

	// ── Kafka consumer (logs topic for ClickHouse batching) ───────────────────
	chConsumer := consumer.NewClickHouseConsumer(chRepo)
	defer chConsumer.Close()

	cg, err := kafka.NewConsumerGroup("log-service-clickhouse", []string{"logs"}, chConsumer.Handle)
	if err != nil {
		log.Fatalf("failed to create ClickHouse Kafka consumer group: %v", err)
	}
	defer cg.Close()

	go func() {
		log.Println("log-service: starting ClickHouse consumer group on topic \"logs\"")
		if err := cg.Start(ctx); err != nil {
			log.Printf("ClickHouse consumer group stopped: %v", err)
		}
	}()

	// ── HTTP server ───────────────────────────────────────────────────────────
	logHandler := handler.NewLogHandler(chRepo, producer)

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
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer shutdownCancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Printf("forced HTTP server shutdown: %v", err)
	} else {
		log.Println("HTTP server stopped accepting connections")
	}

	// Cleanly drain in-memory shock absorber queue and flush remaining logs
	logHandler.Close()
	log.Println("log-service stopped")
}
