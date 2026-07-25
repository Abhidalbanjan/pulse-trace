package quota

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/pulsetrace/shared/metering"
)

func TestLimitsForPlan(t *testing.T) {
	if limitsForPlan("free").Traces == 0 {
		t.Error("free plan should have a finite trace limit")
	}
	if limitsForPlan("enterprise").forSignal(metering.SignalTraces) != 0 {
		t.Error("enterprise should be unlimited (0) for traces")
	}
	// Unknown plans fall back to the most restrictive (free) limits.
	if limitsForPlan("mystery").Logs != limitsForPlan("free").Logs {
		t.Error("unknown plan should fall back to free limits")
	}
}

func TestIngestSignal(t *testing.T) {
	cases := []struct {
		method, path string
		wantSignal   string
		wantOK       bool
	}{
		{http.MethodPost, "/v1/traces", metering.SignalTraces, true},
		{http.MethodPost, "/v1/metrics", metering.SignalMetrics, true},
		{http.MethodPost, "/v1/logs", metering.SignalLogs, true},
		{http.MethodPost, "/api/v1/logs", metering.SignalLogs, true},
		{http.MethodPost, "/api/v1/rum/ingest", metering.SignalRUM, true},
		{http.MethodGet, "/api/v1/services", "", false},   // not ingestion
		{http.MethodPost, "/api/v1/incidents", "", false}, // not ingestion
	}
	for _, c := range cases {
		r := httptest.NewRequest(c.method, c.path, nil)
		sig, ok := ingestSignal(r)
		if ok != c.wantOK || sig != c.wantSignal {
			t.Errorf("ingestSignal(%s %s) = (%q,%v), want (%q,%v)", c.method, c.path, sig, ok, c.wantSignal, c.wantOK)
		}
	}
}

// With no DB the plan resolves to "free" and a disabled meter reports 0 usage, so
// ingestion is allowed. This exercises the default-allow path end to end.
func TestAllowUnderQuota(t *testing.T) {
	e := New(metering.New("", nil), nil)
	if !e.Allow(context.Background(), "acme", metering.SignalTraces) {
		t.Error("a tenant with zero usage should be under quota")
	}
}

// A non-ingest request always passes the middleware untouched.
func TestMiddlewarePassesNonIngest(t *testing.T) {
	e := New(metering.New("", nil), nil)
	called := false
	h := e.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true }))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/v1/services", nil))
	if !called || rr.Code != http.StatusOK {
		t.Errorf("non-ingest request should pass through, called=%v code=%d", called, rr.Code)
	}
}
