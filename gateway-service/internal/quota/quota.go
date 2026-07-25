// Package quota enforces per-plan monthly ingestion limits. It reads a tenant's
// plan from the tenants table (cached) and its running monthly usage from the
// metering counters, and rejects ingestion once a signal is over its plan limit.
package quota

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/pulsetrace/shared/metering"
)

// Limits is a plan's monthly ceiling per signal. 0 means unlimited.
type Limits struct {
	Traces  int64
	Metrics int64
	Logs    int64
	RUM     int64
}

func (l Limits) forSignal(signal string) int64 {
	switch signal {
	case metering.SignalTraces:
		return l.Traces
	case metering.SignalMetrics:
		return l.Metrics
	case metering.SignalLogs:
		return l.Logs
	case metering.SignalRUM:
		return l.RUM
	default:
		return 0
	}
}

// planLimits are the default monthly limits per plan. Enterprise is unlimited (0).
// Unknown plans fall back to the free tier. These are deliberately generous
// starting points; a real deployment would tune them (or load them from config).
var planLimits = map[string]Limits{
	"free":       {Traces: 1_000_000, Metrics: 1_000_000, Logs: 1_000_000, RUM: 200_000},
	"standard":   {Traces: 20_000_000, Metrics: 20_000_000, Logs: 20_000_000, RUM: 5_000_000},
	"premium":    {Traces: 200_000_000, Metrics: 200_000_000, Logs: 200_000_000, RUM: 50_000_000},
	"enterprise": {}, // all zero → unlimited
}

func limitsForPlan(plan string) Limits {
	if l, ok := planLimits[plan]; ok {
		return l
	}
	return planLimits["free"]
}

const planCacheTTL = 60 * time.Second

type planEntry struct {
	plan string
	at   time.Time
}

// Enforcer answers "may this tenant ingest more of this signal this month?".
type Enforcer struct {
	meter *metering.Meter
	db    *sql.DB

	mu    sync.RWMutex
	cache map[string]planEntry // tenant → plan
}

func New(meter *metering.Meter, db *sql.DB) *Enforcer {
	return &Enforcer{meter: meter, db: db, cache: make(map[string]planEntry)}
}

// planFor returns a tenant's plan, cached for planCacheTTL. Falls back to "free"
// on any miss/error so an unknown tenant gets the most restrictive limits.
func (e *Enforcer) planFor(ctx context.Context, tenantID string) string {
	e.mu.RLock()
	entry, ok := e.cache[tenantID]
	e.mu.RUnlock()
	if ok && time.Since(entry.at) < planCacheTTL {
		return entry.plan
	}

	plan := "free"
	if e.db != nil {
		var p string
		if err := e.db.QueryRowContext(ctx, "SELECT plan FROM tenants WHERE id = $1", tenantID).Scan(&p); err == nil && p != "" {
			plan = p
		}
	}
	e.mu.Lock()
	e.cache[tenantID] = planEntry{plan: plan, at: time.Now()}
	e.mu.Unlock()
	return plan
}

// Allow reports whether the tenant is still under its monthly limit for the
// signal. Unlimited (limit 0) always allows.
func (e *Enforcer) Allow(ctx context.Context, tenantID, signal string) bool {
	if e == nil {
		return true
	}
	limit := limitsForPlan(e.planFor(ctx, tenantID)).forSignal(signal)
	if limit == 0 {
		return true
	}
	return e.meter.MonthlyUsage(ctx, tenantID, signal) < limit
}

// ingestSignal classifies an HTTP request as an ingest path and returns which
// signal it carries, or ("", false) if it isn't ingestion.
func ingestSignal(r *http.Request) (string, bool) {
	if r.Method != http.MethodPost {
		return "", false
	}
	switch {
	case strings.HasPrefix(r.URL.Path, "/v1/traces"):
		return metering.SignalTraces, true
	case strings.HasPrefix(r.URL.Path, "/v1/metrics"):
		return metering.SignalMetrics, true
	case strings.HasPrefix(r.URL.Path, "/v1/logs"), r.URL.Path == "/api/v1/logs":
		return metering.SignalLogs, true
	case r.URL.Path == "/api/v1/rum/ingest":
		return metering.SignalRUM, true
	default:
		return "", false
	}
}

// Middleware rejects (429) HTTP ingestion once the tenant is over its monthly
// quota for that signal. It must run after AuthMiddleware, which sets X-Tenant-ID.
// The OTLP/gRPC path bypasses HTTP middleware, so the receiver enforces via Allow.
func (e *Enforcer) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if signal, ok := ingestSignal(r); ok {
			tenantID := r.Header.Get("X-Tenant-ID")
			if tenantID == "" {
				tenantID = "default"
			}
			if !e.Allow(r.Context(), tenantID, signal) {
				w.Header().Set("Retry-After", "3600")
				http.Error(w, fmt.Sprintf("monthly %s ingestion quota exceeded for this plan; upgrade to increase it", signal), http.StatusTooManyRequests)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}
