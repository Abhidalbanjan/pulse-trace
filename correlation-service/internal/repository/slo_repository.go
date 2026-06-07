package repository

import (
	"context"
	"fmt"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pulsetrace/shared/models"
)

// SLORepository handles all PostgreSQL operations for SLO definitions,
// snapshots, and budget alerts, and queries ClickHouse for SLIs.
type SLORepository struct {
	db *pgxpool.Pool
	ch driver.Conn
}

func NewSLORepository(db *pgxpool.Pool, ch driver.Conn) *SLORepository {
	return &SLORepository{db: db, ch: ch}
}

// ── SLO Definitions ─────────────────────────────────────────────────────────

// UpsertDefinition creates or updates an SLO target for a service.
func (r *SLORepository) UpsertDefinition(ctx context.Context, def *models.SLODefinition) (*models.SLODefinition, error) {
	const q = `
		INSERT INTO slo_definitions (id, service_name, slo_target, sli_type, window_days, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, NOW(), NOW())
		ON CONFLICT (service_name) DO UPDATE
		  SET slo_target  = EXCLUDED.slo_target,
		      sli_type    = EXCLUDED.sli_type,
		      window_days = EXCLUDED.window_days,
		      updated_at  = NOW()
		RETURNING id, service_name, slo_target, sli_type, window_days, created_at, updated_at
	`
	result := &models.SLODefinition{}
	err := r.db.QueryRow(ctx, q,
		def.ID, def.ServiceName, def.SLOTarget, def.SLIType, def.WindowDays,
	).Scan(
		&result.ID, &result.ServiceName, &result.SLOTarget,
		&result.SLIType, &result.WindowDays,
		&result.CreatedAt, &result.UpdatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("upsert slo definition: %w", err)
	}
	return result, nil
}

// ListDefinitions returns all configured SLO definitions.
func (r *SLORepository) ListDefinitions(ctx context.Context) ([]*models.SLODefinition, error) {
	const q = `
		SELECT id, service_name, slo_target, sli_type, window_days, created_at, updated_at
		FROM slo_definitions
		ORDER BY service_name ASC
	`
	rows, err := r.db.Query(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("list slo definitions: %w", err)
	}
	defer rows.Close()

	var defs []*models.SLODefinition
	for rows.Next() {
		d := &models.SLODefinition{}
		if err := rows.Scan(
			&d.ID, &d.ServiceName, &d.SLOTarget,
			&d.SLIType, &d.WindowDays,
			&d.CreatedAt, &d.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan slo definition: %w", err)
		}
		defs = append(defs, d)
	}
	return defs, nil
}

// DeleteDefinition removes an SLO definition by ID.
func (r *SLORepository) DeleteDefinition(ctx context.Context, id string) error {
	_, err := r.db.Exec(ctx, "DELETE FROM slo_definitions WHERE id = $1", id)
	if err != nil {
		return fmt.Errorf("delete slo definition: %w", err)
	}
	return nil
}

// ── SLI Computation ─────────────────────────────────────────────────────────

// ComputeSLI calculates the availability SLI from ClickHouse logs table for a service
// within the given time range. Returns total events, error events, and SLI %.
func (r *SLORepository) ComputeSLI(ctx context.Context, serviceName string, from, to time.Time) (total int64, errors int64, sli float64, err error) {
	if r.ch == nil {
		const q = `
			SELECT
			    COUNT(*)                                                    AS total_events,
			    COUNT(*) FILTER (WHERE level IN ('ERROR', 'FATAL'))         AS error_events
			FROM log_entries
			WHERE service_name = $1
			  AND timestamp >= $2
			  AND timestamp <= $3
		`
		if err = r.db.QueryRow(ctx, q, serviceName, from, to).Scan(&total, &errors); err != nil {
			return 0, 0, 0, fmt.Errorf("compute sli: %w", err)
		}
		if total == 0 {
			return 0, 0, 100.0, nil // no events → 100% availability
		}
		sli = (1.0 - float64(errors)/float64(total)) * 100.0
		return total, errors, sli, nil
	}

	const q = `
		SELECT
		    count() AS total_events,
		    countIf(level IN ('ERROR', 'FATAL')) AS error_events
		FROM logs
		WHERE service_name = ?
		  AND timestamp >= ?
		  AND timestamp <= ?
	`
	var totalVal, errorsVal uint64
	if err = r.ch.QueryRow(ctx, q, serviceName, from, to).Scan(&totalVal, &errorsVal); err != nil {
		return 0, 0, 0, fmt.Errorf("compute sli from clickhouse: %w", err)
	}
	total = int64(totalVal)
	errors = int64(errorsVal)

	if total == 0 {
		return 0, 0, 100.0, nil
	}
	sli = (1.0 - float64(errors)/float64(total)) * 100.0
	return total, errors, sli, nil
}

// ── SLO Snapshots ───────────────────────────────────────────────────────────

// InsertSnapshot persists a periodic SLI measurement.
func (r *SLORepository) InsertSnapshot(ctx context.Context, snap *models.SLOSnapshot) error {
	const q = `
		INSERT INTO slo_snapshots (service_name, sli_value, total_events, error_events, window_start, window_end, snapshot_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
	`
	_, err := r.db.Exec(ctx, q,
		snap.ServiceName, snap.SLIValue, snap.TotalEvents, snap.ErrorEvents,
		snap.WindowStart, snap.WindowEnd, snap.SnapshotAt,
	)
	if err != nil {
		return fmt.Errorf("insert slo snapshot: %w", err)
	}
	return nil
}

// GetCurrentSLI returns the aggregate SLI from snapshots over the configured window.
func (r *SLORepository) GetCurrentSLI(ctx context.Context, serviceName string, windowDays int) (float64, int64, int64, error) {
	windowStart := time.Now().UTC().AddDate(0, 0, -windowDays)

	const q = `
		SELECT
		    COALESCE(SUM(total_events), 0) AS total,
		    COALESCE(SUM(error_events), 0) AS errors
		FROM slo_snapshots
		WHERE service_name = $1
		  AND snapshot_at >= $2
	`
	var total, errors int64
	if err := r.db.QueryRow(ctx, q, serviceName, windowStart).Scan(&total, &errors); err != nil {
		return 0, 0, 0, fmt.Errorf("get current sli: %w", err)
	}
	if total == 0 {
		return 100.0, 0, 0, nil
	}
	sli := (1.0 - float64(errors)/float64(total)) * 100.0
	return sli, total, errors, nil
}

// GetTrend returns time-series SLI data points for the sparkline charts.
// Groups snapshots into hourly buckets over the last N days.
func (r *SLORepository) GetTrend(ctx context.Context, serviceName string, days int) ([]models.SLOTrendPoint, error) {
	windowStart := time.Now().UTC().AddDate(0, 0, -days)

	const q = `
		SELECT
		    date_trunc('hour', snapshot_at) AS bucket,
		    CASE WHEN SUM(total_events) = 0 THEN 100.0
		         ELSE (1.0 - SUM(error_events)::numeric / SUM(total_events)::numeric) * 100.0
		    END AS sli
		FROM slo_snapshots
		WHERE service_name = $1
		  AND snapshot_at >= $2
		GROUP BY bucket
		ORDER BY bucket ASC
	`
	rows, err := r.db.Query(ctx, q, serviceName, windowStart)
	if err != nil {
		return nil, fmt.Errorf("get slo trend: %w", err)
	}
	defer rows.Close()

	var points []models.SLOTrendPoint
	for rows.Next() {
		var p models.SLOTrendPoint
		if err := rows.Scan(&p.At, &p.SLIValue); err != nil {
			return nil, fmt.Errorf("scan slo trend point: %w", err)
		}
		points = append(points, p)
	}
	return points, nil
}

// ── Budget Alerts ───────────────────────────────────────────────────────────

// InsertBudgetAlert persists a burn rate breach event.
func (r *SLORepository) InsertBudgetAlert(ctx context.Context, alert *models.SLOBudgetAlert) error {
	const q = `
		INSERT INTO slo_budget_alerts (id, service_name, burn_rate, budget_remaining_pct, severity, message, triggered_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
	`
	_, err := r.db.Exec(ctx, q,
		alert.ID, alert.ServiceName, alert.BurnRate,
		alert.BudgetRemainingPct, alert.Severity, alert.Message, alert.TriggeredAt,
	)
	if err != nil {
		return fmt.Errorf("insert budget alert: %w", err)
	}
	return nil
}

// ListBudgetAlerts returns recent budget alert events, optionally filtered by service.
func (r *SLORepository) ListBudgetAlerts(ctx context.Context, serviceName string, limit int) ([]*models.SLOBudgetAlert, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}

	var q string
	var args []interface{}

	if serviceName != "" {
		q = `
			SELECT id, service_name, burn_rate, budget_remaining_pct, severity, message, triggered_at
			FROM slo_budget_alerts
			WHERE service_name = $1
			ORDER BY triggered_at DESC
			LIMIT $2
		`
		args = []interface{}{serviceName, limit}
	} else {
		q = `
			SELECT id, service_name, burn_rate, budget_remaining_pct, severity, message, triggered_at
			FROM slo_budget_alerts
			ORDER BY triggered_at DESC
			LIMIT $1
		`
		args = []interface{}{limit}
	}

	rows, err := r.db.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("list budget alerts: %w", err)
	}
	defer rows.Close()

	var alerts []*models.SLOBudgetAlert
	for rows.Next() {
		a := &models.SLOBudgetAlert{}
		if err := rows.Scan(
			&a.ID, &a.ServiceName, &a.BurnRate,
			&a.BudgetRemainingPct, &a.Severity, &a.Message, &a.TriggeredAt,
		); err != nil {
			return nil, fmt.Errorf("scan budget alert: %w", err)
		}
		alerts = append(alerts, a)
	}
	return alerts, nil
}

// CleanupOldSnapshots removes snapshots older than the given retention period.
// Called periodically to prevent unbounded table growth.
func (r *SLORepository) CleanupOldSnapshots(ctx context.Context, retentionDays int) (int64, error) {
	cutoff := time.Now().UTC().AddDate(0, 0, -retentionDays)
	tag, err := r.db.Exec(ctx, "DELETE FROM slo_snapshots WHERE snapshot_at < $1", cutoff)
	if err != nil {
		return 0, fmt.Errorf("cleanup old snapshots: %w", err)
	}
	return tag.RowsAffected(), nil
}
