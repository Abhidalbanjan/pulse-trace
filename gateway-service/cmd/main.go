package main

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/pulsetrace/gateway-service/internal/auth"
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

	// ── Auth Handler ──────────────────────────────────────────────────────────
	authHandler, err := auth.NewAuthHandler()
	if err != nil {
		log.Printf("gateway-service: auth database connection failed: %v", err)
	}

	// ── Routes ────────────────────────────────────────────────────────────────
	logServiceURL := getEnv("LOG_SERVICE_URL", "http://localhost:8081")
	alertServiceURL := getEnv("ALERT_SERVICE_URL", "http://localhost:8082")
	correlationServiceURL := getEnv("CORRELATION_SERVICE_URL", "http://localhost:8083")
	otelCollectorHTTPURL := getEnv("OTEL_COLLECTOR_HTTP_URL", "http://localhost:4318")

	routes := []proxy.Route{
		{Prefix: "/api/v1/logs", Upstream: logServiceURL},
		{Prefix: "/api/v1/alerts", Upstream: alertServiceURL},
		{Prefix: "/api/v1/incidents", Upstream: correlationServiceURL},
		{Prefix: "/api/v1/slo", Upstream: correlationServiceURL},
		{Prefix: "/v1/traces", Upstream: otelCollectorHTTPURL},
		{Prefix: "/v1/metrics", Upstream: otelCollectorHTTPURL},
		{Prefix: "/v1/logs", Upstream: otelCollectorHTTPURL},
	}

	router := proxy.NewRouter(routes)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok", "service": "gateway"})
	})

	if authHandler != nil {
		mux.HandleFunc("POST /api/v1/auth/login", authHandler.Login)
		mux.HandleFunc("POST /api/v1/auth/register", authHandler.Register)
		mux.HandleFunc("GET /api/v1/admin/users", authHandler.GetUsers)
		mux.HandleFunc("POST /api/v1/admin/users", authHandler.CreateUser)
		mux.HandleFunc("DELETE /api/v1/admin/users", authHandler.DeleteUser)
		mux.HandleFunc("PUT /api/v1/admin/users/role", authHandler.UpdateUserRole)
	}

	mux.Handle("/", router)

	// Middleware chain: CORS → Tracing → RequestLogger → Auth → RBAC → router
	var chain http.Handler = mux
	chain = auth.RBACMiddleware(chain)
	chain = auth.AuthMiddleware(chain)
	chain = middleware.RequestLogger(chain)
	chain = middleware.Tracing(serviceName)(chain)
	chain = middleware.CORS(chain)

	port := getEnv("PORT", "8080")
	srv := &http.Server{
		Addr:         ":" + port,
		Handler:      chain,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	// ── OTLP gRPC TCP Forwarder ────────────────────────────────────────────────
	otelCollectorGRPCAddr := getEnv("OTEL_COLLECTOR_GRPC_ADDR", "localhost:4317")
	go startGRPCProxy(ctx, ":4317", otelCollectorGRPCAddr)

	go func() {
		log.Printf("gateway-service listening on :%s", port)
		log.Printf("routing /api/v1/logs       → %s", logServiceURL)
		log.Printf("routing /api/v1/alerts     → %s", alertServiceURL)
		log.Printf("routing /api/v1/incidents  → %s", correlationServiceURL)
		log.Printf("routing /v1/traces|metrics|logs  → %s", otelCollectorHTTPURL)
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

// startGRPCProxy runs a high-performance transparent TCP tunnel for OTLP/gRPC.
func startGRPCProxy(ctx context.Context, listenAddr, targetAddr string) {
	listener, err := net.Listen("tcp", listenAddr)
	if err != nil {
		log.Printf("gateway-service: failed to start OTLP gRPC proxy listener: %v", err)
		return
	}
	defer listener.Close()

	go func() {
		<-ctx.Done()
		listener.Close()
	}()

	log.Printf("gateway-service: OTLP gRPC TCP proxy listening on %s -> forwarding to %s", listenAddr, targetAddr)

	for {
		clientConn, err := listener.Accept()
		if err != nil {
			select {
			case <-ctx.Done():
				return
			default:
				log.Printf("gateway-service: gRPC proxy accept error: %v", err)
				continue
			}
		}

		go func(cc net.Conn) {
			defer cc.Close()
			backendConn, err := net.DialTimeout("tcp", targetAddr, 5*time.Second)
			if err != nil {
				log.Printf("gateway-service: failed to connect to backend gRPC %s: %v", targetAddr, err)
				return
			}
			defer backendConn.Close()

			errChan := make(chan error, 2)
			go func() {
				_, err := io.Copy(backendConn, cc)
				errChan <- err
			}()
			go func() {
				_, err := io.Copy(cc, backendConn)
				errChan <- err
			}()
			<-errChan
		}(clientConn)
	}
}
