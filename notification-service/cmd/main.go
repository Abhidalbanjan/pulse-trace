package main

import (
	"context"
	"log"
	"net/http"
	_ "net/http/pprof"
	"os"
	"os/signal"
	"syscall"

	"github.com/pulsetrace/notification-service/internal/worker"
	"github.com/pulsetrace/shared/rabbitmq"
	"github.com/pulsetrace/shared/telemetry"
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
