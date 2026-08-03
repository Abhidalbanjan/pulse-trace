package repository

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pulsetrace/shared/models"
)

// AlertRepository handles all PostgreSQL operations for alerts.
type AlertRepository struct {
	db *pgxpool.Pool
}

func NewAlertRepository(db *pgxpool.Pool) *AlertRepository {
	return &AlertRepository{db: db}
}

// Insert persists a new alert derived from a log entry.
func (r *AlertRepository) Insert(ctx context.Context, entry *models.LogEntry) (*models.Alert, error) {
	alert := &models.Alert{
		TenantID:    entry.TenantID,
		ID:          uuid.New().String(),
		LogEntryID:  entry.ID,
		ServiceName: entry.ServiceName,
		Level:       entry.Level,
		Message:     entry.Message,
		TraceID:     entry.TraceID,
		TriggeredAt: entry.Timestamp,
		CreatedAt:   time.Now().UTC(),
	}

	const q = `
		INSERT INTO alerts (tenant_id, id, log_entry_id, service_name, level, message, trace_id, triggered_at, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
	`
	_, err := r.db.Exec(ctx, q,
		alert.TenantID, alert.ID, alert.LogEntryID, alert.ServiceName, alert.Level,
		alert.Message, alert.TraceID, alert.TriggeredAt, alert.CreatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to insert alert: %w", err)
	}
	return alert, nil
}

// QueryResult holds a page of alerts plus total count.
type QueryResult struct {
	Alerts   []*models.Alert
	Total    int64
	Page     int
	PageSize int
}

// Query fetches alerts with optional filters and pagination.
func (r *AlertRepository) Query(ctx context.Context, params *models.AlertQueryParams) (*QueryResult, error) {
	page := params.Page
	if page < 1 {
		page = 1
	}
	pageSize := params.PageSize
	if pageSize < 1 || pageSize > 200 {
		pageSize = 50
	}
	offset := (page - 1) * pageSize

	args := []interface{}{}
	where := "WHERE 1=1"
	argIdx := 1

	if params.TenantID != "" {
		where += fmt.Sprintf(" AND tenant_id = $%d", argIdx)
		args = append(args, params.TenantID)
		argIdx++
	}
	if params.ServiceName != "" {
		where += fmt.Sprintf(" AND service_name = $%d", argIdx)
		args = append(args, params.ServiceName)
		argIdx++
	}
	if params.Level != "" {
		where += fmt.Sprintf(" AND level = $%d", argIdx)
		args = append(args, string(params.Level))
		argIdx++
	}
	if params.From != "" {
		t, err := time.Parse(time.RFC3339, params.From)
		if err == nil {
			where += fmt.Sprintf(" AND triggered_at >= $%d", argIdx)
			args = append(args, t)
			argIdx++
		}
	}
	if params.To != "" {
		t, err := time.Parse(time.RFC3339, params.To)
		if err == nil {
			where += fmt.Sprintf(" AND triggered_at <= $%d", argIdx)
			args = append(args, t)
			argIdx++
		}
	}

	var total int64
	countQ := fmt.Sprintf("SELECT COUNT(*) FROM alerts %s", where)
	if err := r.db.QueryRow(ctx, countQ, args...).Scan(&total); err != nil {
		return nil, fmt.Errorf("failed to count alerts: %w", err)
	}

	dataQ := fmt.Sprintf(`
		SELECT tenant_id, id, log_entry_id, service_name, level, message, trace_id, triggered_at, created_at
		FROM alerts %s
		ORDER BY triggered_at DESC
		LIMIT $%d OFFSET $%d
	`, where, argIdx, argIdx+1)
	args = append(args, pageSize, offset)

	rows, err := r.db.Query(ctx, dataQ, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to query alerts: %w", err)
	}
	defer rows.Close()

	var alerts []*models.Alert
	for rows.Next() {
		a := &models.Alert{}
		if err := rows.Scan(
			&a.TenantID, &a.ID, &a.LogEntryID, &a.ServiceName, &a.Level,
			&a.Message, &a.TraceID, &a.TriggeredAt, &a.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("failed to scan alert: %w", err)
		}
		alerts = append(alerts, a)
	}

	return &QueryResult{
		Alerts:   alerts,
		Total:    total,
		Page:     page,
		PageSize: pageSize,
	}, nil
}

// GetByID fetches a single alert by its UUID.
// GetByID fetches a single alert by ID without a tenant filter. It exists only
// for internal callers that have already resolved the tenant; any handler acting
// on a request must use GetByIDForTenant, or an alert ID from one tenant becomes
// readable by another.
func (r *AlertRepository) GetByID(ctx context.Context, id string) (*models.Alert, error) {
	const q = `
		SELECT tenant_id, id, log_entry_id, service_name, level, message, trace_id, triggered_at, created_at
		FROM alerts WHERE id = $1
	`
	a := &models.Alert{}
	err := r.db.QueryRow(ctx, q, id).Scan(
		&a.TenantID, &a.ID, &a.LogEntryID, &a.ServiceName, &a.Level,
		&a.Message, &a.TraceID, &a.TriggeredAt, &a.CreatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("alert not found: %w", err)
	}
	return a, nil
}

// GetByIDForTenant fetches a single alert, refusing to return one that belongs to
// another tenant (returns not-found on a tenant mismatch, so the caller can't even
// confirm the ID exists elsewhere).
func (r *AlertRepository) GetByIDForTenant(ctx context.Context, tenantID, id string) (*models.Alert, error) {
	const q = `
		SELECT tenant_id, id, log_entry_id, service_name, level, message, trace_id, triggered_at, created_at
		FROM alerts WHERE id = $1 AND tenant_id = $2
	`
	a := &models.Alert{}
	err := r.db.QueryRow(ctx, q, id, tenantID).Scan(
		&a.TenantID, &a.ID, &a.LogEntryID, &a.ServiceName, &a.Level,
		&a.Message, &a.TraceID, &a.TriggeredAt, &a.CreatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("alert not found: %w", err)
	}
	return a, nil
}

// TotalPages computes the number of pages for a given total and page size.
func TotalPages(total int64, pageSize int) int {
	return int(math.Ceil(float64(total) / float64(pageSize)))
}
