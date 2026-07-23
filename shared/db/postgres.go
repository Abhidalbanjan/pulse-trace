package db

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
)

// NewPostgresPool creates a connection pool from the DATABASE_URL env variable.
// It retries up to maxRetries times to handle container startup ordering.
func NewPostgresPool(ctx context.Context) (*pgxpool.Pool, error) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		return nil, fmt.Errorf("DATABASE_URL environment variable is not set")
	}

	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("failed to parse database config: %w", err)
	}

	// Pool tuning — sensible defaults for a single service.
	config.MaxConns = 20
	config.MinConns = 2
	config.MaxConnLifetime = 30 * time.Minute
	config.MaxConnIdleTime = 5 * time.Minute
	config.HealthCheckPeriod = 1 * time.Minute

	const maxRetries = 10
	var pool *pgxpool.Pool

	for i := range maxRetries {
		pool, err = pgxpool.NewWithConfig(ctx, config)
		if err == nil {
			if pingErr := pool.Ping(ctx); pingErr == nil {
				log.Printf("connected to PostgreSQL (attempt %d)", i+1)
				return pool, nil
			}
			pool.Close()
		}
		log.Printf("postgres not ready, retrying in 2s (attempt %d/%d): %v", i+1, maxRetries, err)
		time.Sleep(2 * time.Second)
	}

	return nil, fmt.Errorf("could not connect to postgres after %d attempts: %w", maxRetries, err)
}

// OpenSQLForMigrations opens a short-lived *sql.DB (via pgx's database/sql
// stdlib driver) suitable for running schema migrations. It forces the simple
// query protocol so that multi-statement migration files execute as a single
// Exec, matching how lib/pq behaves for the services that use that driver.
//
// The returned handle is intended to be used once at startup for migrations
// and then closed - the service's actual runtime queries go through the
// pgxpool.Pool from NewPostgresPool, not this handle.
func OpenSQLForMigrations(ctx context.Context) (*sql.DB, error) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		return nil, fmt.Errorf("DATABASE_URL environment variable is not set")
	}

	connConfig, err := pgx.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("failed to parse database config: %w", err)
	}
	// Simple protocol => multi-statement Exec works (pgx defaults to the
	// extended protocol, which rejects multiple commands in one Exec).
	connConfig.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol

	sqlDB := stdlib.OpenDB(*connConfig)
	sqlDB.SetMaxOpenConns(1)

	// Ping with a short bounded retry so migrations don't race container startup.
	pingCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	var pingErr error
	for i := 0; i < 10; i++ {
		if pingErr = sqlDB.PingContext(pingCtx); pingErr == nil {
			return sqlDB, nil
		}
		time.Sleep(2 * time.Second)
	}
	sqlDB.Close()
	return nil, fmt.Errorf("could not reach postgres for migrations: %w", pingErr)
}
