package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/pulsetrace/alert-service/internal/consumer"
	"github.com/pulsetrace/alert-service/internal/handler"
	"github.com/pulsetrace/alert-service/internal/repository"
	"github.com/pulsetrace/shared/db"
	"github.com/pulsetrace/shared/kafka"
	"github.com/pulsetrace/shared/middleware"
)

const (
	logsTopic = "logs"
	groupID   = "alert-service"
)

func main() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Connect to PostgreSQL.
	pool, err := db.NewPostgresPool(ctx)
	if err != nil {
		log.Fatalf("database connection failed: %v", err)
	}
	defer pool.Close()

	// Wire up dependencies.
	repo := repository.NewAlertRepository(pool)
	logConsumer := consumer.NewLogConsumer(repo)
	alertHandler := handler.NewAlertHandler(repo)

	// Start Kafka consumer group in the background.
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

	// HTTP server for querying alerts.
	mux := http.NewServeMux()
	alertHandler.RegisterRoutes(mux)

	chain := middleware.CORS(middleware.RequestLogger(mux))

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

	// Graceful shutdown on SIGINT / SIGTERM.
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	log.Println("shutting down alert-service...")
	cancel() // stop kafka consumer

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Fatalf("forced shutdown: %v", err)
	}
	log.Println("alert-service stopped")
}
