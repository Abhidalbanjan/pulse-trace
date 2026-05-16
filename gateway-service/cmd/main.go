package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/pulsetrace/gateway-service/internal/proxy"
	"github.com/pulsetrace/shared/middleware"
	"github.com/pulsetrace/shared/telemetry"
)

const serviceName = "gateway-service"

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

	// ── Routes ────────────────────────────────────────────────────────────────
	logServiceURL := getEnv("LOG_SERVICE_URL", "http://localhost:8081")
	alertServiceURL := getEnv("ALERT_SERVICE_URL", "http://localhost:8082")
	correlationServiceURL := getEnv("CORRELATION_SERVICE_URL", "http://localhost:8083")

	routes := []proxy.Route{
		{Prefix: "/api/v1/logs", Upstream: logServiceURL},
		{Prefix: "/api/v1/alerts", Upstream: alertServiceURL},
		{Prefix: "/api/v1/incidents", Upstream: correlationServiceURL},
	}

	router := proxy.NewRouter(routes)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok", "service": "gateway"})
	})
	mux.Handle("/", router)

	// Middleware chain: CORS → Tracing → RequestLogger → router
	chain := middleware.CORS(middleware.Tracing(serviceName)(middleware.RequestLogger(mux)))

	port := getEnv("PORT", "8080")
	srv := &http.Server{
		Addr:         ":" + port,
		Handler:      chain,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	go func() {
		log.Printf("gateway-service listening on :%s", port)
		log.Printf("routing /api/v1/logs       → %s", logServiceURL)
		log.Printf("routing /api/v1/alerts     → %s", alertServiceURL)
		log.Printf("routing /api/v1/incidents  → %s", correlationServiceURL)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("server error: %v", err)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	log.Println("shutting down gateway...")
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Fatalf("forced shutdown: %v", err)
	}
	log.Println("gateway stopped")
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
