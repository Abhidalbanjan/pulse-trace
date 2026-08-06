package repository

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// createAnomalyTableDDL mirrors migration 005 so the DB test is self-contained.
const createAnomalyTableDDL = `CREATE TABLE IF NOT EXISTS anomaly_config (
    tenant_id VARCHAR(50) PRIMARY KEY,
    enabled BOOLEAN NOT NULL DEFAULT true,
    p99_multiplier DOUBLE PRECISION NOT NULL DEFAULT 1.6,
    error_rate_jump_points DOUBLE PRECISION NOT NULL DEFAULT 5.0,
    min_error_rate DOUBLE PRECISION NOT NULL DEFAULT 5.0,
    throughput_drop_ratio DOUBLE PRECISION NOT NULL DEFAULT 0.4,
    updated_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT now()
)`

func setupAnomalyDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set; skipping DB-backed anomaly config test")
	}
	schema := fmt.Sprintf("anomaly_%d", time.Now().UnixNano())
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse dsn: %v", err)
	}
	// Pin every connection to an isolated schema so the test is self-contained.
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	cfg.MaxConns = 1
	ctx := context.Background()
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	t.Cleanup(pool.Close)
	if _, err := pool.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	t.Cleanup(func() { pool.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE") })
	if _, err := pool.Exec(ctx, createAnomalyTableDDL); err != nil {
		t.Fatalf("create table: %v", err)
	}
	return pool
}

func TestAnomalyConfigGetUpsert(t *testing.T) {
	pool := setupAnomalyDB(t)
	repo := NewAnomalyConfigRepository(pool)
	ctx := context.Background()

	// No row yet → defaults.
	got, err := repo.Get(ctx, "acme")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got != DefaultAnomalyConfig() {
		t.Errorf("expected defaults for an unset tenant, got %+v", got)
	}

	// Upsert then read back.
	want := AnomalyConfig{Enabled: false, P99Multiplier: 2.0, ErrorRateJumpPoints: 3, MinErrorRate: 2, ThroughputDropRatio: 0.5}
	saved, err := repo.Upsert(ctx, "acme", want)
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if saved != want {
		t.Errorf("upsert returned %+v, want %+v", saved, want)
	}
	got, _ = repo.Get(ctx, "acme")
	if got != want {
		t.Errorf("get after upsert = %+v, want %+v", got, want)
	}

	// Sanitize clamps a bad multiplier / ratio back to defaults on write.
	bad := AnomalyConfig{Enabled: true, P99Multiplier: 0.1, ThroughputDropRatio: 5, ErrorRateJumpPoints: -1, MinErrorRate: -1}
	clamped, _ := repo.Upsert(ctx, "acme", bad)
	d := DefaultAnomalyConfig()
	if clamped.P99Multiplier != d.P99Multiplier || clamped.ThroughputDropRatio != d.ThroughputDropRatio {
		t.Errorf("bad config should clamp to defaults, got %+v", clamped)
	}

	// Tenant isolation: another tenant still sees defaults.
	if other, _ := repo.Get(ctx, "other"); other != DefaultAnomalyConfig() {
		t.Errorf("config must be tenant-scoped, got %+v", other)
	}
}
