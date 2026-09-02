package middleware

import (
	"testing"
	"time"
)

// productionRules mirrors what gateway-service seeds and what gateway migration
// 007 (as amended by 028) stores. Kept here in full rather than imported,
// because the point of the test below is to notice when the two drift.
func productionRules() []RateLimitRule {
	return []RateLimitRule{
		{Name: "auth-login", PathPrefixes: []string{"/api/v1/auth/login", "/api/v1/auth/register"}, Limit: 10, Window: time.Minute},
		{Name: "telemetry-ingest", PathPrefixes: []string{
			"/v1/traces", "/v1/metrics", "/v1/logs", "/api/v1/logs",
			"/api/v2/logs", "/services/collector",
		}, Limit: 6000, Window: time.Minute},
		{Name: "default", PathPrefixes: []string{"/"}, Limit: 600, Window: time.Minute},
	}
}

// Every ingestion path must be on the ingestion limiter.
//
// # What this catches, and what it cost to find out
//
// `telemetry-ingest` listed the native and OTLP paths only, so the Datadog
// (/api/v2/logs) and Splunk (/services/collector) compatibility endpoints fell
// through to `default` — 600/60s, a tenth of the 6000/60s the identical traffic
// gets on /api/v1/logs. The two endpoints exist so a customer can repoint an
// existing vendor agent at us without changing it, and such an agent sends
// whatever it was already sending; 10 req/s made the migration path unusable
// for the migration it exists to enable.
//
// Nothing failed loudly. The weekly scale-baseline job had been failing on this
// since it was introduced, and being scheduled rather than PR-triggered, it had
// no reader. This test is the reader: adding an ingestion endpoint without
// putting it on the limiter now fails a build instead of a cron job nobody
// opens.
func TestEveryIngestionPathIsOnTheIngestLimiter(t *testing.T) {
	rl := &RateLimiter{rules: productionRules()}

	// Every path the gateway accepts telemetry on. A new ingestion endpoint
	// belongs in this list *and* in the rule; the test exists to make leaving
	// it out of the second impossible while it is in the first.
	ingestionPaths := []string{
		"/api/v1/logs",        // native JSON batch
		"/v1/logs",            // OTLP logs
		"/v1/traces",          // OTLP traces
		"/v1/metrics",         // OTLP metrics
		"/api/v2/logs",        // Datadog compatibility
		"/services/collector", // Splunk HEC compatibility
	}

	for _, path := range ingestionPaths {
		got := rl.ruleFor(path)
		if got.Name != "telemetry-ingest" {
			t.Errorf("%s is limited by %q (%d/%v), not telemetry-ingest — "+
				"an ingestion path on the general API budget is throttled roughly "+
				"ten times harder than the identical traffic on /api/v1/logs",
				path, got.Name, got.Limit, got.Window)
		}
	}
}

// The catch-all must stay last, or it swallows every rule after it.
//
// Ordering is load-bearing: ruleFor returns the *first* prefix match, and
// `default` matches "/", which is a prefix of everything. A rule appended after
// it is dead configuration that looks live in the admin UI.
func TestCatchAllRuleIsLast(t *testing.T) {
	rules := productionRules()
	for i, r := range rules {
		for _, p := range r.PathPrefixes {
			if p == "/" && i != len(rules)-1 {
				t.Fatalf("rule %q matches everything but sits at index %d of %d; "+
					"every rule after it is unreachable", r.Name, i, len(rules))
			}
		}
	}
}

// Login stays stricter than ingestion, whatever else moves.
//
// The two limits exist for opposite reasons — one throttles credential
// stuffing, the other sizes a firehose — and a change that accidentally
// harmonised them would quietly reopen brute-force.
func TestLoginIsLimitedFarBelowIngestion(t *testing.T) {
	rl := &RateLimiter{rules: productionRules()}
	login := rl.ruleFor("/api/v1/auth/login")
	ingest := rl.ruleFor("/api/v1/logs")

	if login.Name != "auth-login" {
		t.Fatalf("login is limited by %q, not auth-login", login.Name)
	}
	if login.Limit >= ingest.Limit {
		t.Errorf("login budget (%d) is not below ingestion (%d)", login.Limit, ingest.Limit)
	}
}
