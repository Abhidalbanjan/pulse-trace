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
	"github.com/pulsetrace/alert-service/internal/consumer"
	"github.com/pulsetrace/alert-service/internal/handler"
	"github.com/pulsetrace/alert-service/internal/repository"
	alertmigrations "github.com/pulsetrace/alert-service/migrations"
	"github.com/pulsetrace/shared/bus"
	"github.com/pulsetrace/shared/db"
	"github.com/pulsetrace/shared/middleware"
	"github.com/pulsetrace/shared/migrate"
	"github.com/pulsetrace/shared/telemetry"
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

	// ── Bus producer (for publishing alerts to the correlation engine) ────────
	//
	// Declared as the interface and left unassigned on failure, rather than
	// assigning a nil *KafkaBus into it. A typed nil in an interface is not ==
	// nil, so the consumer's `if c.bus != nil` guard would pass and it would
	// call through a nil pointer on every alert. Same degradation as before:
	// alerting continues, forwarding is skipped.
	var msgbus bus.Bus
	if kb, err := bus.NewKafkaBus(); err != nil {
		log.Printf("WARNING: bus producer unavailable: %v", err)
	} else {
		msgbus = kb
		defer msgbus.Close()
	}

	// ── Wire up dependencies ──────────────────────────────────────────────────
	repo := repository.NewAlertRepository(pool)
	silenceRepo := repository.NewSilenceRepository(pool)
	logConsumer := consumer.NewLogConsumer(repo, msgbus)
	alertHandler := handler.NewAlertHandler(repo).WithSilences(silenceRepo)
	silenceHandler := handler.NewSilenceHandler(silenceRepo)

	// ── Bus consumer (logs topic) ─────────────────────────────────────────────
	//
	// Subscribing needs a bus value even when the producer failed, so fall back
	// to a producer-less one: consuming does not depend on publishing.
	subscriber := msgbus
	if subscriber == nil {
		subscriber = bus.NewKafkaBusWith(nil)
	}
	cg, err := subscriber.Subscribe(groupID, []string{logsTopic}, logConsumer.Handle)
	if err != nil {
		log.Fatalf("bus subscribe failed: %v", err)
	}
	defer cg.Close()

	go func() {
		log.Printf("alert-service: starting kafka consumer group %q on topic %q", groupID, logsTopic)
		if err := cg.Run(ctx); err != nil {
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
