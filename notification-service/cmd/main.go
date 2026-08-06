package main

import (
	"context"
	"database/sql"
	"log"
	"net/http"
	_ "net/http/pprof"
	"os"
	"os/signal"
	"syscall"

	_ "github.com/lib/pq"
	"github.com/pulsetrace/notification-service/internal/channels"
	"github.com/pulsetrace/notification-service/internal/worker"
	"github.com/pulsetrace/shared/rabbitmq"
	"github.com/pulsetrace/shared/telemetry"
	"github.com/grafana/pyroscope-go"
)

const serviceName = "notification-service"

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

	go func() {
		log.Println("notification-service pprof server listening on :8085")
		if err := http.ListenAndServe(":8085", nil); err != nil {
			log.Printf("pprof server error: %v", err)
		}
	}()

	// ── RabbitMQ consumer ─────────────────────────────────────────────────────
	consumer, err := rabbitmq.NewConsumer(rabbitmq.QueueNotifications)
	if err != nil {
		log.Fatalf("rabbitmq consumer failed: %v", err)
	}
	defer consumer.Close()

	notifWorker := worker.NewNotificationWorker()

	// ── Per-tenant channel store + management API (F3) ────────────────────────
	// Optional: without DATABASE_URL the service still runs on env-configured
	// channels; with it, tenants can manage channels from the UI and the worker
	// also delivers to those DB-configured channels.
	if dsn := os.Getenv("DATABASE_URL"); dsn != "" {
		db, err := sql.Open("postgres", dsn)
		if err != nil {
			log.Printf("notification-service: channel store disabled (db open failed: %v)", err)
		} else {
			repo := channels.NewRepository(db)
			notifWorker = notifWorker.WithChannels(repo)

			apiMux := http.NewServeMux()
			channels.NewHandler(repo).RegisterRoutes(apiMux)
			apiPort := os.Getenv("CHANNELS_API_PORT")
			if apiPort == "" {
				apiPort = "8086"
			}
			go func() {
				log.Printf("notification-service: channels API listening on :%s", apiPort)
				if err := http.ListenAndServe(":"+apiPort, apiMux); err != nil {
					log.Printf("notification-service: channels API server error: %v", err)
				}
			}()
			if !channels.EncryptionConfigured() {
				log.Printf("notification-service: WARNING — CHANNEL_ENCRYPTION_KEY not set; channel creation with secrets will be rejected until configured")
			}
		}
	}

	log.Printf("notification-service: listening on queue %q", rabbitmq.QueueNotifications)

	go func() {
		if err := consumer.Start(ctx, notifWorker.Handle); err != nil {
			if ctx.Err() == nil {
				log.Printf("rabbitmq consumer error: %v", err)
			}
		}
	}()

	// ── Graceful shutdown ─────────────────────────────────────────────────────
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	log.Println("shutting down notification-service...")
	cancel()
	log.Println("notification-service stopped")
}
