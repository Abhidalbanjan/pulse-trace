package repository

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	"github.com/pulsetrace/shared/models"
)

// ClickHouseLogRepository handles log operations against ClickHouse.
type ClickHouseLogRepository struct {
	defaultConn    driver.Conn
	enterpriseConn driver.Conn
}

// NewClickHouseLogRepository creates a new ClickHouseLogRepository instance.
func NewClickHouseLogRepository(defaultConn driver.Conn, enterpriseConn driver.Conn) *ClickHouseLogRepository {
	return &ClickHouseLogRepository{
		defaultConn:    defaultConn,
		enterpriseConn: enterpriseConn,
	}
}

func (r *ClickHouseLogRepository) getConn(tier string) driver.Conn {
	if strings.ToLower(tier) == "enterprise" && r.enterpriseConn != nil {
		return r.enterpriseConn
	}
	return r.defaultConn
}

// InitializeSchema sets up the logs table in ClickHouse.
func (r *ClickHouseLogRepository) InitializeSchema(ctx context.Context) error {
	const query = `
	CREATE TABLE IF NOT EXISTS logs (
		tenant_id LowCardinality(String),
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
	ORDER BY (tenant_id, service_name, level, timestamp, id)
	TTL toDateTime(timestamp) + INTERVAL 1 HOUR TO VOLUME 'cold'
	SETTINGS storage_policy = 'tiered';
	`
	if err := r.defaultConn.Exec(ctx, query); err != nil {
		return fmt.Errorf("failed to create logs table on default connection: %w", err)
	}
	if r.enterpriseConn != nil {
		if err := r.enterpriseConn.Exec(ctx, query); err != nil {
			return fmt.Errorf("failed to create logs table on enterprise connection: %w", err)
		}
	}
	return nil
}

// BulkInsert inserts a slice of LogEntry in native ClickHouse batch(es), routed by tenant tier.
func (r *ClickHouseLogRepository) BulkInsert(ctx context.Context, entries []*models.LogEntry) error {
	if len(entries) == 0 {
		return nil
	}

	defaultBatch := make([]*models.LogEntry, 0)
	enterpriseBatch := make([]*models.LogEntry, 0)

	for _, entry := range entries {
		if strings.ToLower(entry.TenantTier) == "enterprise" && r.enterpriseConn != nil {
			enterpriseBatch = append(enterpriseBatch, entry)
		} else {
			defaultBatch = append(defaultBatch, entry)
		}
	}

	if len(defaultBatch) > 0 {
		if err := r.bulkInsertConn(ctx, r.defaultConn, defaultBatch); err != nil {
			return fmt.Errorf("default shard bulk insert failed: %w", err)
		}
	}
	if len(enterpriseBatch) > 0 {
		if err := r.bulkInsertConn(ctx, r.enterpriseConn, enterpriseBatch); err != nil {
			return fmt.Errorf("enterprise shard bulk insert failed: %w", err)
		}
	}

	return nil
}

func (r *ClickHouseLogRepository) bulkInsertConn(ctx context.Context, conn driver.Conn, entries []*models.LogEntry) error {
	batch, err := conn.PrepareBatch(ctx, "INSERT INTO logs (tenant_id, id, service_name, level, message, trace_id, span_id, metadata, timestamp, created_at)")
	if err != nil {
		return fmt.Errorf("failed to prepare clickhouse batch: %w", err)
	}

	for _, entry := range entries {
		tenantID := entry.TenantID
		if tenantID == "" {
			tenantID = "default"
		}
		err = batch.Append(
			tenantID,
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
	conn := r.getConn(params.TenantTier)

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

	tenantID := params.TenantID
	if tenantID == "" {
		tenantID = "default"
	}
	where += " AND tenant_id = ?"
	args = append(args, tenantID)

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
	if err := conn.QueryRow(ctx, countQ, args...).Scan(&total); err != nil {
		return nil, fmt.Errorf("failed to count clickhouse logs: %w", err)
	}

	dataQ := fmt.Sprintf(`
		SELECT tenant_id, id, service_name, level, message, trace_id, span_id, metadata, timestamp, created_at
		FROM logs %s
		ORDER BY timestamp DESC
		LIMIT ? OFFSET ?
	`, where)

	pageArgs := append(args, uint64(pageSize), uint64(offset))
	rows, err := conn.Query(ctx, dataQ, pageArgs...)
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
			&e.TenantID, &e.ID, &e.ServiceName, &levelStr, &e.Message,
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
		SELECT tenant_id, id, service_name, level, message, trace_id, span_id, metadata, timestamp, created_at
		FROM logs WHERE id = ?
	`
	e := &models.LogEntry{}
	var timestamp, createdAt time.Time
	var levelStr string
	err := r.defaultConn.QueryRow(ctx, query, id).Scan(
		&e.TenantID, &e.ID, &e.ServiceName, &levelStr, &e.Message,
		&e.TraceID, &e.SpanID, &e.Metadata, &timestamp, &createdAt,
	)
	if err != nil && r.enterpriseConn != nil {
		err = r.enterpriseConn.QueryRow(ctx, query, id).Scan(
			&e.TenantID, &e.ID, &e.ServiceName, &levelStr, &e.Message,
			&e.TraceID, &e.SpanID, &e.Metadata, &timestamp, &createdAt,
		)
	}
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
