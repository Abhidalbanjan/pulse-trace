package repository

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// AnomalyConfig is the per-tenant tuning for the EWMA anomaly detector. The zero
// value is not valid config — use DefaultAnomalyConfig() for tenants with no row.
type AnomalyConfig struct {
	Enabled             bool    `json:"enabled"`
	P99Multiplier       float64 `json:"p99_multiplier"`
	ErrorRateJumpPoints float64 `json:"error_rate_jump_points"`
	MinErrorRate        float64 `json:"min_error_rate"`
	ThroughputDropRatio float64 `json:"throughput_drop_ratio"`
}

// DefaultAnomalyConfig matches the detector's original hardcoded constants, so a
// tenant that never touches the tuning UI behaves exactly as before.
func DefaultAnomalyConfig() AnomalyConfig {
	return AnomalyConfig{
		Enabled:             true,
		P99Multiplier:       1.6,
		ErrorRateJumpPoints: 5.0,
		MinErrorRate:        5.0,
		ThroughputDropRatio: 0.4,
	}
}

// Sanitize clamps values to sane ranges so a bad config can't disable detection
// by accident (e.g. a 0 multiplier that would flag every service).
func (c AnomalyConfig) Sanitize() AnomalyConfig {
	d := DefaultAnomalyConfig()
	if c.P99Multiplier < 1.0 {
		c.P99Multiplier = d.P99Multiplier
	}
	if c.ErrorRateJumpPoints < 0 {
		c.ErrorRateJumpPoints = d.ErrorRateJumpPoints
	}
	if c.MinErrorRate < 0 {
		c.MinErrorRate = d.MinErrorRate
	}
	if c.ThroughputDropRatio <= 0 || c.ThroughputDropRatio > 1 {
		c.ThroughputDropRatio = d.ThroughputDropRatio
	}
	return c
}

type AnomalyConfigRepository struct {
	db *pgxpool.Pool
}

func NewAnomalyConfigRepository(db *pgxpool.Pool) *AnomalyConfigRepository {
	return &AnomalyConfigRepository{db: db}
}

// Get returns the tenant's config, or the defaults when no row exists (or the DB
// is unavailable) — the detector must never stop working because config is missing.
func (r *AnomalyConfigRepository) Get(ctx context.Context, tenantID string) (AnomalyConfig, error) {
	if r == nil || r.db == nil {
		return DefaultAnomalyConfig(), nil
	}
	c := DefaultAnomalyConfig()
	err := r.db.QueryRow(ctx,
		`SELECT enabled, p99_multiplier, error_rate_jump_points, min_error_rate, throughput_drop_ratio
		 FROM anomaly_config WHERE tenant_id = $1`, tenantID,
	).Scan(&c.Enabled, &c.P99Multiplier, &c.ErrorRateJumpPoints, &c.MinErrorRate, &c.ThroughputDropRatio)
	if err == pgx.ErrNoRows {
		return DefaultAnomalyConfig(), nil
	}
	if err != nil {
		return DefaultAnomalyConfig(), err
	}
	return c, nil
}

// Upsert writes the tenant's config (clamped to sane ranges).
func (r *AnomalyConfigRepository) Upsert(ctx context.Context, tenantID string, c AnomalyConfig) (AnomalyConfig, error) {
	c = c.Sanitize()
	_, err := r.db.Exec(ctx,
		`INSERT INTO anomaly_config (tenant_id, enabled, p99_multiplier, error_rate_jump_points, min_error_rate, throughput_drop_ratio, updated_at)
		 VALUES ($1,$2,$3,$4,$5,$6, now())
		 ON CONFLICT (tenant_id) DO UPDATE SET
		   enabled=EXCLUDED.enabled, p99_multiplier=EXCLUDED.p99_multiplier,
		   error_rate_jump_points=EXCLUDED.error_rate_jump_points, min_error_rate=EXCLUDED.min_error_rate,
		   throughput_drop_ratio=EXCLUDED.throughput_drop_ratio, updated_at=now()`,
		tenantID, c.Enabled, c.P99Multiplier, c.ErrorRateJumpPoints, c.MinErrorRate, c.ThroughputDropRatio)
	if err != nil {
		return AnomalyConfig{}, err
	}
	return c, nil
}
