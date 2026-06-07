package repository

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	"github.com/pulsetrace/shared/models"
)

// ClickHouseLogRepository handles log operations against ClickHouse.
type ClickHouseLogRepository struct {
	conn driver.Conn
}

// NewClickHouseLogRepository creates a new ClickHouseLogRepository instance.
func NewClickHouseLogRepository(conn driver.Conn) *ClickHouseLogRepository {
	return &ClickHouseLogRepository{conn: conn}
}

// InitializeSchema sets up the logs table in ClickHouse.
func (r *ClickHouseLogRepository) InitializeSchema(ctx context.Context) error {
	const query = `
	CREATE TABLE IF NOT EXISTS logs (
		id String,
		service_name LowCardinality(String),
		level LowCardinality(String),
		message String,
		trace_id String,
		span_id String,
		metadata String,
		timestamp DateTime64(6, 'UTC'),
		created_at DateTime64(6, 'UTC')
	) ENGINE = MergeTree()
	PARTITION BY toYYYYMM(timestamp)
	ORDER BY (service_name, level, timestamp, id)
	SETTINGS storage_policy = 'tiered';
	`
	if err := r.conn.Exec(ctx, query); err != nil {
		return fmt.Errorf("failed to create logs table: %w", err)
	}
	return nil
}

// BulkInsert inserts a slice of LogEntry in a single native ClickHouse batch.
func (r *ClickHouseLogRepository) BulkInsert(ctx context.Context, entries []*models.LogEntry) error {
	if len(entries) == 0 {
		return nil
	}

	batch, err := r.conn.PrepareBatch(ctx, "INSERT INTO logs (id, service_name, level, message, trace_id, span_id, metadata, timestamp, created_at)")
	if err != nil {
		return fmt.Errorf("failed to prepare clickhouse batch: %w", err)
	}

	for _, entry := range entries {
		err = batch.Append(
			entry.ID,
			entry.ServiceName,
			string(entry.Level),
			entry.Message,
			entry.TraceID,
			entry.SpanID,
			entry.Metadata,
			entry.Timestamp,
			entry.CreatedAt,
		)
		if err != nil {
			_ = batch.Abort()
			return fmt.Errorf("failed to append to clickhouse batch: %w", err)
		}
	}

	if err := batch.Send(); err != nil {
		return fmt.Errorf("failed to send clickhouse batch: %w", err)
	}

	return nil
}

// QueryResult wraps a page of logs and total count.
type QueryResult struct {
	Entries  []*models.LogEntry
	Total    int64
	Page     int
	PageSize int
}

// Query fetches matching logs using paging, filtering, and timestamp range constraints.
func (r *ClickHouseLogRepository) Query(ctx context.Context, params *models.LogQueryParams) (*QueryResult, error) {
	page := params.Page
	if page < 1 {
		page = 1
	}
	pageSize := params.PageSize
	if pageSize < 1 || pageSize > 200 {
		pageSize = 50
	}
	offset := (page - 1) * pageSize

	var args []any
	where := "WHERE 1=1"

	if params.ServiceName != "" {
		where += " AND service_name = ?"
		args = append(args, params.ServiceName)
	}
	if params.Level != "" {
		where += " AND level = ?"
		args = append(args, string(params.Level))
	}
	if params.TraceID != "" {
		where += " AND trace_id = ?"
		args = append(args, params.TraceID)
	}
	if params.From != "" {
		t, err := time.Parse(time.RFC3339, params.From)
		if err == nil {
			where += " AND timestamp >= ?"
			args = append(args, t)
		}
	}
	if params.To != "" {
		t, err := time.Parse(time.RFC3339, params.To)
		if err == nil {
			where += " AND timestamp <= ?"
			args = append(args, t)
		}
	}

	var total uint64
	countQ := fmt.Sprintf("SELECT count() FROM logs %s", where)
	if err := r.conn.QueryRow(ctx, countQ, args...).Scan(&total); err != nil {
		return nil, fmt.Errorf("failed to count clickhouse logs: %w", err)
	}

	dataQ := fmt.Sprintf(`
		SELECT id, service_name, level, message, trace_id, span_id, metadata, timestamp, created_at
		FROM logs %s
		ORDER BY timestamp DESC
		LIMIT ? OFFSET ?
	`, where)

	pageArgs := append(args, uint64(pageSize), uint64(offset))
	rows, err := r.conn.Query(ctx, dataQ, pageArgs...)
	if err != nil {
		return nil, fmt.Errorf("failed to query clickhouse logs: %w", err)
	}
	defer rows.Close()

	var entries []*models.LogEntry
	for rows.Next() {
		e := &models.LogEntry{}
		var timestamp, createdAt time.Time
		var levelStr string
		if err := rows.Scan(
			&e.ID, &e.ServiceName, &levelStr, &e.Message,
			&e.TraceID, &e.SpanID, &e.Metadata, &timestamp, &createdAt,
		); err != nil {
			return nil, fmt.Errorf("failed to scan clickhouse log: %w", err)
		}
		e.Level = models.LogLevel(levelStr)
		e.Timestamp = timestamp
		e.CreatedAt = createdAt
		entries = append(entries, e)
	}

	return &QueryResult{
		Entries:  entries,
		Total:    int64(total),
		Page:     page,
		PageSize: pageSize,
	}, nil
}

// GetByID queries a single log by its UUID.
func (r *ClickHouseLogRepository) GetByID(ctx context.Context, id string) (*models.LogEntry, error) {
	const query = `
		SELECT id, service_name, level, message, trace_id, span_id, metadata, timestamp, created_at
		FROM logs WHERE id = ?
	`
	e := &models.LogEntry{}
	var timestamp, createdAt time.Time
	var levelStr string
	err := r.conn.QueryRow(ctx, query, id).Scan(
		&e.ID, &e.ServiceName, &levelStr, &e.Message,
		&e.TraceID, &e.SpanID, &e.Metadata, &timestamp, &createdAt,
	)
	if err != nil {
		return nil, fmt.Errorf("clickhouse log entry not found: %w", err)
	}
	e.Level = models.LogLevel(levelStr)
	e.Timestamp = timestamp
	e.CreatedAt = createdAt
	return e, nil
}

// TotalPages is a helper to compute page count.
func TotalPages(total int64, pageSize int) int {
	return int(math.Ceil(float64(total) / float64(pageSize)))
}
