package handler

import (
	"strings"
	"testing"

	"github.com/pulsetrace/shared/models"
)

func TestBuildSuggestions_NoIncidentsUsesStarters(t *testing.T) {
	got := buildSuggestions(nil)
	if len(got) != maxSuggestions {
		t.Fatalf("expected %d starter suggestions, got %d: %v", maxSuggestions, len(got), got)
	}
	// All must be the grounded starters (no incident phrasing).
	for _, s := range got {
		if strings.Contains(s, "active incident") {
			t.Errorf("no-incident case should not mention incidents: %q", s)
		}
	}
}

func TestBuildSuggestions_IncidentFirst(t *testing.T) {
	incidents := []*models.Incident{
		{Status: models.IncidentStatusOpen, ServiceNames: []string{"payment-service"}, Title: "Latency spike"},
		{Status: models.IncidentStatusResolved, ServiceNames: []string{"cart-service"}}, // must be ignored
		{Status: models.IncidentStatusOpen, Title: "Checkout errors"},                    // no service → title phrasing
	}
	got := buildSuggestions(incidents)

	if len(got) == 0 || !strings.Contains(got[0], "payment-service") {
		t.Fatalf("first suggestion should reference the open incident's service, got %v", got)
	}
	// Resolved incident's service must not appear.
	for _, s := range got {
		if strings.Contains(s, "cart-service") {
			t.Errorf("resolved incident must be ignored, but got %q", s)
		}
	}
	// The service-less open incident contributes a title-based prompt.
	foundTitle := false
	for _, s := range got {
		if strings.Contains(s, "Checkout errors") {
			foundTitle = true
		}
	}
	if !foundTitle {
		t.Errorf("service-less open incident should yield a title prompt, got %v", got)
	}
	// Always capped, always leaves room for starters.
	if len(got) > maxSuggestions {
		t.Errorf("exceeded cap: %v", got)
	}
	if len(got) < 3 {
		t.Errorf("should still include grounded starters alongside incidents, got %v", got)
	}
}

func TestBuildSuggestions_Dedupe(t *testing.T) {
	// Two open incidents on the same service should not produce duplicate prompts.
	incidents := []*models.Incident{
		{Status: models.IncidentStatusOpen, ServiceNames: []string{"api"}},
		{Status: models.IncidentStatusOpen, ServiceNames: []string{"api"}},
	}
	got := buildSuggestions(incidents)
	seen := map[string]bool{}
	for _, s := range got {
		if seen[s] {
			t.Errorf("duplicate suggestion %q in %v", s, got)
		}
		seen[s] = true
	}
}
