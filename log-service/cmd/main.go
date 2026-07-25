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


	"github.com/pulsetrace/log-service/internal/handler"
	"github.com/pulsetrace/shared/kafka"
	"github.com/pulsetrace/shared/metering"
	"github.com/pulsetrace/shared/middleware"
	"github.com/pulsetrace/shared/telemetry"
	"github.com/grafana/pyroscope-go"
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
