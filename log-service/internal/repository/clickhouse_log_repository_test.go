package repository

import (
	"context"
	"testing"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	"github.com/pulsetrace/shared/models"
)

// mockBatch implements driver.Batch for testing.
type mockBatch struct {
	driver.Batch
	appended [][]any
	aborted  bool
	sent     bool
}

func (m *mockBatch) Append(v ...any) error {
	m.appended = append(m.appended, v)
	return nil
}

func (m *mockBatch) Send() error {
	m.sent = true
	return nil
}

func (m *mockBatch) Abort() error {
	m.aborted = true
	return nil
}

// mockConn implements driver.Conn for testing.
type mockConn struct {
	driver.Conn
	batches []*mockBatch
	execs   []string
}

func (m *mockConn) Exec(ctx context.Context, query string, args ...any) error {
	m.execs = append(m.execs, query)
	return nil
}

func (m *mockConn) PrepareBatch(ctx context.Context, query string, opts ...driver.PrepareBatchOption) (driver.Batch, error) {
	b := &mockBatch{}
	m.batches = append(m.batches, b)
	return b, nil
}

func TestClickHouseMultiTenancySharding(t *testing.T) {
	defaultMock := &mockConn{}
	enterpriseMock := &mockConn{}

	repo := NewClickHouseLogRepository(defaultMock, enterpriseMock)

	// Create a mixed list of standard and enterprise logs
	now := time.Now()
	entries := []*models.LogEntry{
		{
			ID:          "log-std-1",
			TenantID:    "tenant-std-a",
			TenantTier:  "standard",
			ServiceName: "payment-service",
			Level:       models.LogLevelInfo,
			Message:     "standard tier request processed",
			Timestamp:   now,
			CreatedAt:   now,
		},
		{
			ID:          "log-ent-1",
			TenantID:    "tenant-ent-b",
			TenantTier:  "enterprise",
			ServiceName: "order-service",
			Level:       models.LogLevelError,
			Message:     "enterprise tier error database timeout",
			Timestamp:   now,
			CreatedAt:   now,
		},
		{
			ID:          "log-std-2",
			TenantID:    "tenant-std-a",
			TenantTier:  "STANDARD", // Case insensitivity check
			ServiceName: "auth-service",
			Level:       models.LogLevelDebug,
			Message:     "standard debug level log",
			Timestamp:   now,
			CreatedAt:   now,
		},
		{
			ID:          "log-ent-2",
			TenantID:    "tenant-ent-c",
			TenantTier:  "Enterprise", // Case insensitivity check
			ServiceName: "billing-service",
			Level:       models.LogLevelInfo,
			Message:     "enterprise tier processing billing",
			Timestamp:   now,
			CreatedAt:   now,
		},
	}

	ctx := context.Background()

	// Perform BulkInsert
	err := repo.BulkInsert(ctx, entries)
	if err != nil {
		t.Fatalf("BulkInsert failed: %v", err)
	}

	// Verify sharding routing
	// 1. Verify standard logs went to the default ClickHouse connection
	if len(defaultMock.batches) != 1 {
		t.Errorf("Expected 1 batch on default connection, got %d", len(defaultMock.batches))
	} else {
		batch := defaultMock.batches[0]
		if len(batch.appended) != 2 {
			t.Errorf("Expected 2 entries in standard batch, got %d", len(batch.appended))
		}
		// First entry should be log-std-1
		if batch.appended[0][1] != "log-std-1" {
			t.Errorf("Expected first standard log ID to be log-std-1, got %v", batch.appended[0][1])
		}
		// Second entry should be log-std-2
		if batch.appended[1][1] != "log-std-2" {
			t.Errorf("Expected second standard log ID to be log-std-2, got %v", batch.appended[1][1])
		}
		if !batch.sent {
			t.Errorf("Standard batch was not sent")
		}
	}

	// 2. Verify enterprise logs went to the enterprise ClickHouse connection
	if len(enterpriseMock.batches) != 1 {
		t.Errorf("Expected 1 batch on enterprise connection, got %d", len(enterpriseMock.batches))
	} else {
		batch := enterpriseMock.batches[0]
		if len(batch.appended) != 2 {
			t.Errorf("Expected 2 entries in enterprise batch, got %d", len(batch.appended))
		}
		// First entry should be log-ent-1
		if batch.appended[0][1] != "log-ent-1" {
			t.Errorf("Expected first enterprise log ID to be log-ent-1, got %v", batch.appended[0][1])
		}
		// Second entry should be log-ent-2
		if batch.appended[1][1] != "log-ent-2" {
			t.Errorf("Expected second enterprise log ID to be log-ent-2, got %v", batch.appended[1][1])
		}
		if !batch.sent {
			t.Errorf("Enterprise batch was not sent")
		}
	}
}
