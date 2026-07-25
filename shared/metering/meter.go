// Package metering records per-tenant ingestion volume. Counters live in Redis
// on the hot ingest path (a single INCRBY, no DB write per request) and are
// periodically mirrored into Postgres usage_daily by a background flusher, which
// is what billing and quota enforcement read.
package metering

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

// Signals metered. Kept as constants so callers and the quota checker agree.
const (
	SignalTraces  = "traces"
	SignalMetrics = "metrics"
	SignalLogs    = "logs"
	SignalRUM     = "rum"
)

// counterTTL keeps a day's Redis counter around well beyond the day itself, so
// the flusher always has time to capture the final total before it expires
// (Postgres already holds it long before then).
const counterTTL = 45 * 24 * time.Hour

// Meter accumulates usage counters in Redis and flushes them to Postgres.
type Meter struct {
	rdb *redis.Client
	db  *sql.DB
}

// New returns a Meter, or a no-op Meter (Record/flush do nothing) when redisAddr
// is empty or unreachable — metering must never take the ingest path down.
func New(redisAddr string, db *sql.DB) *Meter {
	if redisAddr == "" {
		return &Meter{db: db}
	}
	rdb := redis.NewClient(&redis.Options{Addr: redisAddr})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := rdb.Ping(ctx).Err(); err != nil {
		log.Printf("metering: redis unreachable at %s (%v); usage metering disabled", redisAddr, err)
		return &Meter{db: db}
	}
	return &Meter{rdb: rdb, db: db}
}

func counterKey(tenantID, day, signal string) string {
	return fmt.Sprintf("usage:%s:%s:%s", tenantID, day, signal)
}

func today() string { return time.Now().UTC().Format("2006-01-02") }

// Record adds count events to the current day's (tenant, signal) counter. It is
// best-effort and non-blocking-ish: a Redis hiccup logs and returns rather than
// erroring the ingest request.
func (m *Meter) Record(ctx context.Context, tenantID, signal string, count int64) {
	if m == nil || m.rdb == nil || count <= 0 || tenantID == "" {
		return
	}
	key := counterKey(tenantID, today(), signal)
	pipe := m.rdb.Pipeline()
	pipe.IncrBy(ctx, key, count)
	pipe.Expire(ctx, key, counterTTL)
	if _, err := pipe.Exec(ctx); err != nil {
		log.Printf("metering: failed to record %d %s for %s: %v", count, signal, tenantID, err)
	}
}

// CurrentUsage returns the tenant's counter total for a signal on the given day
// (UTC), read straight from Redis — used by the quota checker for a fresh value.
// Returns 0 when metering is disabled or the key is absent.
func (m *Meter) CurrentUsage(ctx context.Context, tenantID, signal, day string) int64 {
	if m == nil || m.rdb == nil {
		return 0
	}
	v, err := m.rdb.Get(ctx, counterKey(tenantID, day, signal)).Int64()
	if err != nil {
		return 0
	}
	return v
}

// Today returns the current UTC day string, so callers key CurrentUsage the same way.
func (m *Meter) Today() string { return today() }

// StartFlusher periodically mirrors Redis counters into usage_daily until ctx is
// cancelled. Idempotent: each flush SETs the Postgres row to the current Redis
// total, so Postgres mirrors Redis rather than accumulating on top of it.
func (m *Meter) StartFlusher(ctx context.Context, interval time.Duration) {
	if m == nil || m.rdb == nil || m.db == nil {
		return
	}
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				m.flush(context.Background()) // final flush on shutdown
				return
			case <-ticker.C:
				m.flush(ctx)
			}
		}
	}()
}

func (m *Meter) flush(ctx context.Context) {
	var cursor uint64
	for {
		keys, next, err := m.rdb.Scan(ctx, cursor, "usage:*", 200).Result()
		if err != nil {
			log.Printf("metering: flush scan failed: %v", err)
			return
		}
		for _, key := range keys {
			tenantID, day, signal, ok := parseCounterKey(key)
			if !ok {
				continue
			}
			val, err := m.rdb.Get(ctx, key).Int64()
			if err != nil {
				continue
			}
			if _, err := m.db.ExecContext(ctx, `
				INSERT INTO usage_daily (tenant_id, day, signal, count, updated_at)
				VALUES ($1, $2, $3, $4, now())
				ON CONFLICT (tenant_id, day, signal) DO UPDATE SET count = EXCLUDED.count, updated_at = now()`,
				tenantID, day, signal, val,
			); err != nil {
				log.Printf("metering: flush upsert failed for %s: %v", key, err)
			}
		}
		if next == 0 {
			return
		}
		cursor = next
	}
}

// parseCounterKey splits "usage:<tenant>:<day>:<signal>". The tenant id itself
// can contain hyphens but never a colon, so splitting on ':' is unambiguous.
func parseCounterKey(key string) (tenantID, day, signal string, ok bool) {
	parts := strings.Split(key, ":")
	if len(parts) != 4 || parts[0] != "usage" {
		return "", "", "", false
	}
	if _, err := time.Parse("2006-01-02", parts[2]); err != nil {
		return "", "", "", false
	}
	return parts[1], parts[2], parts[3], true
}
