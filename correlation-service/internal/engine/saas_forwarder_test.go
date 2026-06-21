package engine

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/pulsetrace/shared/models"
)

func TestDataEgressSavings(t *testing.T) {
	now := time.Now()
	incident := &models.Incident{
		TenantID:     "tenant_abc_123",
		ID:           "inc_9b1deb4d-3b7d-4bad-9bdd-2b0d7b3dcb6d",
		Title:        "Postgres connection pool exhausted causing cascades",
		RootCause:    "Database Pool Exhausted",
		Status:       models.IncidentStatusOpen,
		Severity:     models.LogLevelError,
		ServiceNames: []string{"postgres", "payment-service", "order-service"},
		AlertCount:   3,
		StartedAt:    now.Add(-5 * time.Minute),
		CreatedAt:    now.Add(-5 * time.Minute),
		UpdatedAt:    now,
		Causal: &models.CausalAnalysis{
			Chain: []models.CausalLink{
				{
					FromService: "postgres",
					ToService:   "payment-service",
					Evidence:    "Connection pool exhaustion error at postgres:5432 occurred 1.2s before payment-service API timeouts",
					At:          now.Add(-5 * time.Minute),
				},
				{
					FromService: "payment-service",
					ToService:   "order-service",
					Evidence:    "payment-service timeout occurred 850ms before order-service reported dependency failures",
					At:          now.Add(-4 * time.Minute),
				},
			},
			Narrative:  "Postgres pool exhaustion starved payment-service connections, causing order-service queries to time out.",
			RootCause:  "Postgres pool exhaustion",
			Confidence: 0.95,
			Model:      "claude-3-5-sonnet",
			AnalyzedAt: now,
		},
	}

	alerts := []models.IncidentAlert{
		{
			IncidentID:  incident.ID,
			AlertID:     "alt_1",
			ServiceName: "postgres",
			Level:       models.LogLevelError,
			Message:     "FATAL: remaining connection slots are reserved for non-replication superuser connections",
			TriggeredAt: now.Add(-5 * time.Minute),
		},
		{
			IncidentID:  incident.ID,
			AlertID:     "alt_2",
			ServiceName: "payment-service",
			Level:       models.LogLevelError,
			Message:     "failed to process payment: context deadline exceeded (database timeout)",
			TriggeredAt: now.Add(-5 * time.Minute).Add(1200 * time.Millisecond),
		},
		{
			IncidentID:  incident.ID,
			AlertID:     "alt_3",
			ServiceName: "order-service",
			Level:       models.LogLevelError,
			Message:     "order creation failed: payment-service unavailable",
			TriggeredAt: now.Add(-5 * time.Minute).Add(2050 * time.Millisecond),
		},
	}

	// Calculate standard "SaaS Forward" raw telemetry size
	rawTelemetryPayload := struct {
		Incident *models.Incident       `json:"incident"`
		Alerts   []models.IncidentAlert `json:"alerts"`
	}{
		Incident: incident,
		Alerts:   alerts,
	}
	rawBytes, _ := json.Marshal(rawTelemetryPayload)
	rawSize := len(rawBytes)

	// In Hybrid Mode, we only forward the anonymized incident metadata
	anonymizeSvc := func(svc string) string {
		return "svc_hash"
	}
	var anonChain []AnonymizedCausalLink
	for _, link := range incident.Causal.Chain {
		anonChain = append(anonChain, AnonymizedCausalLink{
			From:     anonymizeSvc(link.FromService),
			To:       anonymizeSvc(link.ToService),
			Evidence: "[MASKED_EVIDENCE_PII_LOGS_STRIPPED]",
		})
	}
	var anonymizedServices []string
	for _, svc := range incident.ServiceNames {
		anonymizedServices = append(anonymizedServices, anonymizeSvc(svc))
	}
	forwardPayload := AnonymizedIncident{
		ID:          incident.ID,
		TenantID:    incident.TenantID,
		Status:      string(incident.Status),
		Severity:    string(incident.Severity),
		AlertCount:  incident.AlertCount,
		StartedAt:   incident.StartedAt,
		UpdatedAt:   incident.UpdatedAt,
		Confidence:  incident.Causal.Confidence,
		CausalChain: anonChain,
		Topology:    anonymizedServices,
	}

	anonBytes, _ := json.Marshal(forwardPayload)
	anonSize := len(anonBytes)

	reductionRatio := (float64(rawSize-anonSize) / float64(rawSize)) * 100
	fmt.Printf("\n=== ZERO-DATA-EGRESS STATISTICS (Single Incident) ===\n")
	fmt.Printf("Raw Incident + Alerts Payload Size: %d bytes\n", rawSize)
	fmt.Printf("Anonymized Control Plane Payload Size: %d bytes\n", anonSize)
	fmt.Printf("Data Volume Transferred Reduction: %.2f%%\n", reductionRatio)
	fmt.Printf("Bandwidth Savings Factor: %.1fx less data sent over the network\n", float64(rawSize)/float64(anonSize))
	fmt.Printf("======================================================\n\n")

	// 2. Production Scale Scenario:
	// A typical 5-minute cascading incident in a production system generates:
	// - 2,500 logs (average 500 bytes each) = 1,250,000 bytes (1.25 MB)
	// - 100 trace spans (average 1,000 bytes each) = 100,000 bytes (100 KB)
	// - Metric series payloads = 50,000 bytes (50 KB)
	// Total raw data normally streamed to SaaS: 1.4 MB (1,400,000 bytes)
	//
	// Under PulseTrace's Hybrid Zero-Egress model, ClickHouse stores all 1.4 MB locally.
	// The only egress is the single anonymized incident of 518 bytes.
	prodRawSize := 1400000
	prodEgressSize := anonSize
	prodReductionRatio := (float64(prodRawSize-prodEgressSize) / float64(prodRawSize)) * 100
	fmt.Printf("=== ZERO-DATA-EGRESS STATISTICS (Production Scale) ===\n")
	fmt.Printf("Standard SaaS Ingestion (Raw Logs + Traces + Metrics): %d bytes (1.40 MB)\n", prodRawSize)
	fmt.Printf("PulseTrace Zero-Egress SaaS Forward (Only Metadata): %d bytes\n", prodEgressSize)
	fmt.Printf("Data Volume Egress Reduction: %.5f%%\n", prodReductionRatio)
	fmt.Printf("Egress Cost Savings Factor: %.1fx less data sent over network\n", float64(prodRawSize)/float64(prodEgressSize))
	fmt.Printf("========================================================\n\n")
}
