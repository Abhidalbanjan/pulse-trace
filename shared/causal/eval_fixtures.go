package causal

// Labelled incident fixtures for the causal-AI evaluation harness (F0.5).
//
// Each fixture is a realistic incident whose correct root cause is known, so an
// analyzer's inference can be scored objectively. The set is deliberately mixed:
// most are solvable by the deterministic dependency-graph walk, and a couple are
// hard cases (a localized incident with no chain; a root cause hidden behind an
// UNdeclared dependency) that expose where the rule-based engine is limited and
// an LLM narrative has headroom. This keeps the published accuracy number honest.

import (
	"time"

	"github.com/pulsetrace/shared/models"
)

// base is a fixed reference time so fixtures (and their scores) are deterministic.
var evalBase = time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC)

func at(secondsFromBase int) time.Time {
	return evalBase.Add(time.Duration(secondsFromBase) * time.Second)
}

// mkIncident builds the incident + alerts + deps triple an analyzer consumes.
func mkEvidence(rootCause string, services []string, alerts []models.IncidentAlert, deps map[string][]string) *Evidence {
	return &Evidence{
		Incident: &models.Incident{
			ID:           "eval-" + rootCause,
			RootCause:    rootCause,
			ServiceNames: services,
			Status:       "OPEN",
		},
		Alerts:       alerts,
		Dependencies: deps,
		Window:       5 * time.Minute,
	}
}

func alert(service, level, message string, sec int) models.IncidentAlert {
	return models.IncidentAlert{
		ServiceName: service,
		Level:       models.LogLevel(level),
		Message:     message,
		TriggeredAt: at(sec),
	}
}

// EvalFixtures returns the labelled evaluation set.
func EvalFixtures() []EvalFixture {
	return []EvalFixture{
		{
			Name: "db-connection-pool-cascade",
			Evidence: mkEvidence(
				"Database connection pool exhausted",
				[]string{"postgres", "payments-api"},
				[]models.IncidentAlert{
					alert("postgres", "ERROR", "connection pool exhausted, cannot acquire connection", 0),
					alert("payments-api", "ERROR", "failed to reach database: connection refused", 8),
				},
				map[string][]string{"payments-api": {"postgres"}},
			),
			ExpectRootService: "postgres",
			ExpectPlaybook:    "recycle_db_pool",
			MinConfidence:     0.65,
			RootCauseKeyword:  "database",
		},
		{
			Name: "memory-oom-localized",
			Evidence: mkEvidence(
				"Memory pressure — possible OOM condition",
				[]string{"cart-service"},
				[]models.IncidentAlert{
					alert("cart-service", "ERROR", "OOM killed: memory limit exceeded", 0),
				},
				map[string][]string{},
			),
			ExpectRootService: "", // localized: no upstream chain
			ExpectPlaybook:    "restart_service",
			MinConfidence:     0.35,
			RootCauseKeyword:  "memory",
		},
		{
			Name: "latency-timeout-downstream",
			Evidence: mkEvidence(
				"Downstream service latency or resource exhaustion",
				[]string{"payments-api", "checkout"},
				[]models.IncidentAlert{
					alert("payments-api", "WARNING", "upstream timeout, p99 latency elevated", 0),
					alert("checkout", "ERROR", "request timeout waiting on payments-api", 10),
				},
				map[string][]string{"checkout": {"payments-api"}},
			),
			ExpectRootService: "payments-api",
			ExpectPlaybook:    "scale_replicas",
			MinConfidence:     0.65,
			RootCauseKeyword:  "latency",
		},
		{
			Name: "kafka-lag-cascade",
			Evidence: mkEvidence(
				"Kafka broker unavailability or consumer lag",
				[]string{"kafka", "order-consumer"},
				[]models.IncidentAlert{
					alert("kafka", "ERROR", "broker unavailable, consumer lag growing", 0),
					alert("order-consumer", "ERROR", "cannot consume: kafka broker down", 6),
				},
				map[string][]string{"order-consumer": {"kafka"}},
			),
			ExpectRootService: "kafka",
			ExpectPlaybook:    "restart_service", // 'kafka' maps to no specialized playbook
			MinConfidence:     0.65,
			RootCauseKeyword:  "kafka",
		},
		{
			Name: "multi-hop-db-orders-checkout",
			Evidence: mkEvidence(
				"Database or network connectivity issue",
				[]string{"db", "orders", "checkout"},
				[]models.IncidentAlert{
					alert("db", "ERROR", "connection refused on primary", 0),
					alert("orders", "ERROR", "db query failed: connection error", 5),
					alert("checkout", "ERROR", "orders service unavailable", 11),
				},
				map[string][]string{"orders": {"db"}, "checkout": {"orders"}},
			),
			ExpectRootService: "db",
			ExpectPlaybook:    "recycle_db_pool",
			MinConfidence:     0.75, // two-hop chain → higher confidence
			RootCauseKeyword:  "connectivity",
		},
		{
			Name: "auth-degradation-cascade",
			Evidence: mkEvidence(
				"Authentication service degradation",
				[]string{"auth-service", "api-gateway"},
				[]models.IncidentAlert{
					alert("auth-service", "ERROR", "token validation failing, JWKS unreachable", 0),
					alert("api-gateway", "ERROR", "auth check failed for all requests", 7),
				},
				map[string][]string{"api-gateway": {"auth-service"}},
			),
			ExpectRootService: "auth-service",
			ExpectPlaybook:    "restart_service",
			MinConfidence:     0.65,
			RootCauseKeyword:  "authentication",
		},
		{
			Name: "localized-application-crash",
			Evidence: mkEvidence(
				"Application panic or unhandled exception",
				[]string{"worker-service"},
				[]models.IncidentAlert{
					alert("worker-service", "ERROR", "panic: nil pointer dereference; application crash", 0),
				},
				map[string][]string{},
			),
			ExpectRootService: "",
			ExpectPlaybook:    "restart_service",
			MinConfidence:     0.35,
			RootCauseKeyword:  "panic",
		},
		{
			Name: "independent-alerts-no-dependency",
			Evidence: mkEvidence(
				"Elevated ERROR events in search-service",
				[]string{"search-service", "email-service"},
				[]models.IncidentAlert{
					alert("search-service", "ERROR", "index query failed", 0),
					alert("email-service", "ERROR", "smtp send failed", 3),
				},
				map[string][]string{}, // no declared edges: must NOT invent a chain
			),
			ExpectRootService: "",
			ExpectPlaybook:    "restart_service",
			MinConfidence:     0.35,
			RootCauseKeyword:  "search-service",
		},
		{
			Name: "redis-resource-exhaustion",
			Evidence: mkEvidence(
				"Downstream service latency or resource exhaustion",
				[]string{"redis", "inventory"},
				[]models.IncidentAlert{
					alert("redis", "ERROR", "connection timeout, max clients reached: resource exhaustion", 0),
					alert("inventory", "ERROR", "cache unavailable, request timeout", 9),
				},
				map[string][]string{"inventory": {"redis"}},
			),
			ExpectRootService: "redis",
			ExpectPlaybook:    "scale_replicas",
			MinConfidence:     0.65,
			RootCauseKeyword:  "exhaustion",
		},
		{
			Name: "diamond-converging-root",
			Evidence: mkEvidence(
				"Database or network connectivity issue",
				[]string{"db", "payments", "inventory", "checkout"},
				[]models.IncidentAlert{
					alert("db", "ERROR", "connection pool saturated", 0),
					alert("payments", "ERROR", "db connection error", 4),
					alert("inventory", "ERROR", "db connection error", 5),
					alert("checkout", "ERROR", "payments and inventory both failing", 12),
				},
				map[string][]string{
					"payments":  {"db"},
					"inventory": {"db"},
					"checkout":  {"payments", "inventory"},
				},
			),
			ExpectRootService: "db", // both paths converge on db as the single source
			ExpectPlaybook:    "recycle_db_pool",
			MinConfidence:     0.80,
			RootCauseKeyword:  "connectivity",
		},
		{
			// HARD CASE — the deterministic walk is expected to get the root service
			// WRONG here: the true root (config-service) is not a declared upstream of
			// api, so the graph walk blames the nearest declared cause (api) instead.
			// This is the headroom an LLM narrative that reads the messages could
			// close; keeping it in the set keeps the accuracy number honest.
			Name: "hidden-root-undeclared-dependency",
			Evidence: mkEvidence(
				"Bad configuration rollout in config-service",
				[]string{"config-service", "api", "web"},
				[]models.IncidentAlert{
					alert("config-service", "ERROR", "invalid config pushed to all consumers", 0),
					alert("api", "ERROR", "loaded invalid configuration, requests failing", 4),
					alert("web", "ERROR", "api returning 500s", 9),
				},
				// Note: api's dependency on config-service is NOT declared.
				map[string][]string{"web": {"api"}},
			),
			ExpectRootService: "config-service",
			ExpectPlaybook:    "restart_service",
			MinConfidence:     0.65,
			RootCauseKeyword:  "config",
		},
	}
}
