package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pulsetrace/shared/models"
)

// LogRepository handles all PostgreSQL operations for log entries.
type LogRepository struct {
	db *pgxpool.Pool
}

func NewLogRepository(db *pgxpool.Pool) *LogRepository {
	return &LogRepository{db: db}
}

// Insert persists a new log entry and returns it with generated fields populated.
func (r *LogRepository) Insert(ctx context.Context, req *models.CreateLogRequest) (*models.LogEntry, error) {
	entry := &models.LogEntry{
		ID:          uuid.New().String(),
		ServiceName: req.ServiceName,
		Level:       req.Level,
		Message:     req.Message,
		TraceID:     req.TraceID,
		SpanID:      req.SpanID,
		CreatedAt:   time.Now().UTC(),
	}

	// Use provided timestamp or default to now.
	if req.Timestamp != nil {
		entry.Timestamp = req.Timestamp.UTC()
	} else {
		entry.Timestamp = entry.CreatedAt
	}

	// Serialize optional metadata map to JSON string.
	if len(req.Metadata) > 0 {
		b, err := json.Marshal(req.Metadata)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal metadata: %w", err)
		}
		entry.Metadata = string(b)
	}

	const q = `
		INSERT INTO log_entries (id, service_name, level, message, trace_id, span_id, metadata, timestamp, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
	`
	_, err := r.db.Exec(ctx, q,
		entry.ID, entry.ServiceName, entry.Level, entry.Message,
		entry.TraceID, entry.SpanID, entry.Metadata, entry.Timestamp, entry.CreatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to insert log entry: %w", err)
	}

	return entry, nil
}

// QueryResult holds a page of log entries plus total count.
type QueryResult struct {
	Entries  []*models.LogEntry
	Total    int64
	Page     int
	PageSize int
}

// Query fetches log entries with optional filters and pagination.
func (r *LogRepository) Query(ctx context.Context, params *models.LogQueryParams) (*QueryResult, error) {
	// Defaults
	page := params.Page
	if page < 1 {
		page = 1
	}
	pageSize := params.PageSize
	if pageSize < 1 || pageSize > 200 {
		pageSize = 50
	}
	offset := (page - 1) * pageSize

	// Build dynamic WHERE clause.
	args := []interface{}{}
	where := "WHERE 1=1"
	argIdx := 1

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
	if params.TraceID != "" {
		where += fmt.Sprintf(" AND trace_id = $%d", argIdx)
		args = append(args, params.TraceID)
		argIdx++
	}
	if params.From != "" {
		t, err := time.Parse(time.RFC3339, params.From)
		if err == nil {
			where += fmt.Sprintf(" AND timestamp >= $%d", argIdx)
			args = append(args, t)
			argIdx++
		}
	}
	if params.To != "" {
		t, err := time.Parse(time.RFC3339, params.To)
		if err == nil {
			where += fmt.Sprintf(" AND timestamp <= $%d", argIdx)
			args = append(args, t)
			argIdx++
		}
	}

	// Count total matching rows.
	var total int64
	countQ := fmt.Sprintf("SELECT COUNT(*) FROM log_entries %s", where)
	if err := r.db.QueryRow(ctx, countQ, args...).Scan(&total); err != nil {
		return nil, fmt.Errorf("failed to count log entries: %w", err)
	}

	// Fetch the page.
	dataQ := fmt.Sprintf(`
		SELECT id, service_name, level, message, trace_id, span_id, metadata, timestamp, created_at
		FROM log_entries %s
		ORDER BY timestamp DESC
		LIMIT $%d OFFSET $%d
	`, where, argIdx, argIdx+1)
	args = append(args, pageSize, offset)

	rows, err := r.db.Query(ctx, dataQ, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to query log entries: %w", err)
	}
	defer rows.Close()

	var entries []*models.LogEntry
	for rows.Next() {
		e := &models.LogEntry{}
		if err := rows.Scan(
			&e.ID, &e.ServiceName, &e.Level, &e.Message,
			&e.TraceID, &e.SpanID, &e.Metadata, &e.Timestamp, &e.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("failed to scan log entry: %w", err)
		}
		entries = append(entries, e)
	}

	return &QueryResult{
		Entries:  entries,
		Total:    total,
		Page:     page,
		PageSize: pageSize,
	}, nil
}

// GetByID fetches a single log entry by its UUID.
func (r *LogRepository) GetByID(ctx context.Context, id string) (*models.LogEntry, error) {
	const q = `
		SELECT id, service_name, level, message, trace_id, span_id, metadata, timestamp, created_at
		FROM log_entries WHERE id = $1
	`
	e := &models.LogEntry{}
	err := r.db.QueryRow(ctx, q, id).Scan(
		&e.ID, &e.ServiceName, &e.Level, &e.Message,
		&e.TraceID, &e.SpanID, &e.Metadata, &e.Timestamp, &e.CreatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("log entry not found: %w", err)
	}
	return e, nil
}

// TotalPages is a helper to compute page count.
func TotalPages(total int64, pageSize int) int {
	return int(math.Ceil(float64(total) / float64(pageSize)))
}
