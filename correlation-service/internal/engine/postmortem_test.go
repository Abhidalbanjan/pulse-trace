package engine

import (
	"strings"
	"testing"
	"time"

	"github.com/pulsetrace/shared/models"
)

func sampleIncident() (*models.Incident, []models.IncidentAlert, []models.IncidentTimelineEvent) {
	start := time.Date(2026, 8, 12, 10, 0, 0, 0, time.UTC)
	resolved := start.Add(42 * time.Minute)
	inc := &models.Incident{
		ID:           "inc-1",
		Title:        "Checkout latency spike",
		RootCause:    "redis pool exhaustion in payment-service",
		Status:       models.IncidentStatusResolved,
		Severity:     models.LogLevel("CRITICAL"),
		ServiceNames: []string{"payment-service", "cart-service"},
		AlertCount:   2,
		StartedAt:    start,
		ResolvedAt:   &resolved,
		Causal: &models.CausalAnalysis{
			Narrative:  "payment-service exhausted its redis pool, cascading latency to cart-service.",
			RootCause:  "redis pool exhaustion",
			Confidence: 0.82,
			Model:      "claude-test",
			Chain: []models.CausalLink{
				{FromService: "payment-service", ToService: "cart-service", Evidence: "latency cascade"},
			},
		},
	}
	alerts := []models.IncidentAlert{
		{ServiceName: "cart-service", Level: "ERROR", Message: "p99 > 2s", TriggeredAt: start.Add(2 * time.Minute)},
		{ServiceName: "payment-service", Level: "CRITICAL", Message: "redis timeouts", TriggeredAt: start},
	}
	timeline := []models.IncidentTimelineEvent{
		{At: start, EventType: "incident_opened", ServiceName: "payment-service", Description: "opened"},
		{At: resolved, EventType: "incident_resolved", Description: "recovered"},
	}
	return inc, alerts, timeline
}

func TestDeterministicPostmortem_HasAllSections(t *testing.T) {
	inc, alerts, timeline := sampleIncident()
	md := DeterministicPostmortem(inc, alerts, timeline)
	for _, sec := range postmortemSections {
		if !strings.Contains(md, "## "+sec) {
			t.Errorf("postmortem missing section %q", sec)
		}
	}
}

func TestDeterministicPostmortem_GroundedInEvidence(t *testing.T) {
	inc, alerts, timeline := sampleIncident()
	md := DeterministicPostmortem(inc, alerts, timeline)
	for _, want := range []string{
		inc.Title,
		"payment-service", "cart-service", // affected services
		"redis timeouts",                  // an alert message
		inc.Causal.Narrative,              // root-cause narrative
		"42m",                             // humanized duration
	} {
		if !strings.Contains(md, want) {
			t.Errorf("expected postmortem to reference %q", want)
		}
	}
	// Action Items must be a checklist.
	if !strings.Contains(md, "- [ ]") {
		t.Error("Action Items should be a checklist")
	}
}

func TestDeterministicPostmortem_AlertsChronological(t *testing.T) {
	inc, alerts, timeline := sampleIncident()
	md := DeterministicPostmortem(inc, alerts, timeline)
	// payment-service alert fired first (start), cart-service two minutes later;
	// the Contributing Factors list must reflect that order regardless of input order.
	iPay := strings.Index(md, "redis timeouts")
	iCart := strings.Index(md, "p99 > 2s")
	if iPay < 0 || iCart < 0 || iPay > iCart {
		t.Fatalf("expected earliest alert first (payment before cart): pay=%d cart=%d", iPay, iCart)
	}
}

func TestDeterministicPostmortem_HandlesSparseIncident(t *testing.T) {
	inc := &models.Incident{ID: "inc-2", Title: "Minimal", Severity: models.LogLevel("WARNING"), StartedAt: time.Now().UTC()}
	md := DeterministicPostmortem(inc, nil, nil)
	if !strings.Contains(md, "## Root Cause") || !strings.Contains(md, "not yet determined") {
		t.Error("sparse incident should still produce all sections with honest placeholders")
	}
	if !strings.Contains(md, "No alerts recorded") || !strings.Contains(md, "No timeline events") {
		t.Error("sparse incident should note the absence of alerts/timeline")
	}
}

func TestBuildPostmortemPrompt_StructureAndEvidence(t *testing.T) {
	inc, alerts, timeline := sampleIncident()
	system, user := BuildPostmortemPrompt(inc, alerts, timeline)
	for _, sec := range postmortemSections {
		if !strings.Contains(system, sec) {
			t.Errorf("system prompt should require section %q", sec)
		}
	}
	if !strings.Contains(system, "do not invent") {
		t.Error("system prompt must instruct the model to stay grounded")
	}
	for _, want := range []string{inc.Title, "payment-service", "redis timeouts", inc.Causal.Narrative} {
		if !strings.Contains(user, want) {
			t.Errorf("user payload should include evidence %q", want)
		}
	}
}

func TestSentenceHelper(t *testing.T) {
	if got := sentence("redis pool exhausted"); got != "Redis pool exhausted." {
		t.Errorf("sentence() = %q", got)
	}
	if got := sentence(""); !strings.Contains(got, "not yet determined") {
		t.Errorf("empty sentence should be a placeholder, got %q", got)
	}
}
