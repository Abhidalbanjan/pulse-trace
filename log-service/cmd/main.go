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

	"github.com/grafana/pyroscope-go"
	"github.com/pulsetrace/log-service/internal/handler"
	"github.com/pulsetrace/shared/kafka"
	"github.com/pulsetrace/shared/metering"
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

	// ── Kafka producer ────────────────────────────────────────────────────────
	producer, err := kafka.NewProducer()
	if err != nil {
		log.Printf("WARNING: kafka producer unavailable, continuing without event publishing: %v", err)
		producer = nil
	} else {
		defer producer.Close()
	}

	// ── Kafka retention watchdog ──────────────────────────────────────────────
	//
	// Kafka retention is 24h (it was 168h, which cost 4.32 GiB against a 2 GiB
	// ingest — the largest single line in our storage footprint). Quickwit is
	// the system of record for logs; Kafka only has to hold a record long
	// enough for every consumer to read it.
	//
	// The shorter window is safe only if a stalled consumer is noticed before
	// the broker deletes what it has not read. This watches each group's
	// committed offset against the oldest offset still retained and says so,
	// loudly, when records have been dropped unread. Failure to start is not
	// fatal — log-service's job is ingestion, and refusing to serve because the
	// watchdog could not connect would trade a monitoring gap for an outage.
	if watcher, err := kafka.NewRetentionWatcher([]string{"logs"}); err != nil {
		log.Printf("WARNING: kafka retention watchdog unavailable, retention is unmonitored: %v", err)
	} else {
		defer watcher.Close()
		go watcher.Run(ctx, time.Minute)
		log.Printf("kafka retention watchdog running on topic %q", "logs")
	}

	// ── HTTP server ───────────────────────────────────────────────────────────
	quickwitURL := os.Getenv("QUICKWIT_URL")
	if quickwitURL == "" {
		quickwitURL = "http://quickwit:7280"
	}
	// Usage metering: increment the same shared-Redis counters the gateway flushes
	// to usage_daily. No DB here — log-service only records, the gateway flushes.
	redisAddr := os.Getenv("REDIS_ADDR")
	if redisAddr == "" {
		redisAddr = "redis:6379"
	}
	usageMeter := metering.New(redisAddr, nil)
	logHandler := handler.NewLogHandler(producer, quickwitURL, usageMeter)

	mux := http.NewServeMux()
	logHandler.RegisterRoutes(mux)
	mux.Handle("/debug/pprof/", http.DefaultServeMux)

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
