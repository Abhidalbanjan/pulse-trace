package causal

import (
	"reflect"
	"testing"

	"github.com/pulsetrace/shared/models"
)

func evidenceWith(services []string, deps map[string][]string) *Evidence {
	return &Evidence{
		Incident:     &models.Incident{ServiceNames: services},
		Dependencies: deps,
	}
}

func TestGroundAnalysis_AllReferencesKnown_StaysGrounded(t *testing.T) {
	e := evidenceWith(
		[]string{"payment-service", "cart-service"},
		map[string][]string{"cart-service": {"payment-service"}},
	)
	in := &models.CausalAnalysis{
		Chain: []models.CausalLink{
			{FromService: "payment-service", ToService: "cart-service", Evidence: "latency cascade"},
		},
		Confidence: 0.82,
	}

	got := GroundAnalysis(in, e)

	if got.Grounding == nil || !got.Grounding.Grounded {
		t.Fatalf("expected grounded report, got %+v", got.Grounding)
	}
	if len(got.Chain) != 1 {
		t.Fatalf("expected chain preserved, got %d links", len(got.Chain))
	}
	if got.Confidence != 0.82 {
		t.Fatalf("expected confidence unchanged, got %v", got.Confidence)
	}
	if got.Grounding.DroppedLinks != 0 {
		t.Fatalf("expected no dropped links, got %d", got.Grounding.DroppedLinks)
	}
}

func TestGroundAnalysis_HallucinatedService_Dropped_And_ConfidenceCapped(t *testing.T) {
	e := evidenceWith([]string{"payment-service", "cart-service"}, nil)
	in := &models.CausalAnalysis{
		Chain: []models.CausalLink{
			{FromService: "payment-service", ToService: "cart-service"},
			{FromService: "ghost-service", ToService: "cart-service"}, // ghost-service is invented
		},
		Confidence: 0.95,
	}

	got := GroundAnalysis(in, e)

	if got.Grounding.Grounded {
		t.Fatal("expected grounded=false when a hallucinated link is present")
	}
	if got.Grounding.DroppedLinks != 1 {
		t.Fatalf("expected 1 dropped link, got %d", got.Grounding.DroppedLinks)
	}
	if len(got.Chain) != 1 || got.Chain[0].FromService != "payment-service" {
		t.Fatalf("expected only the real link to survive, got %+v", got.Chain)
	}
	want := []string{"ghost-service"}
	if !reflect.DeepEqual(got.Grounding.UnknownServices, want) {
		t.Fatalf("expected unknown=%v, got %v", want, got.Grounding.UnknownServices)
	}
	if got.Confidence != groundedConfidenceCeiling {
		t.Fatalf("expected confidence capped to %v, got %v", groundedConfidenceCeiling, got.Confidence)
	}
	if got.Grounding.ConfidencePenalty <= 0 {
		t.Fatalf("expected a recorded confidence penalty, got %v", got.Grounding.ConfidencePenalty)
	}
}

func TestGroundAnalysis_LowConfidenceHallucination_NotArtificiallyRaised(t *testing.T) {
	e := evidenceWith([]string{"payment-service"}, nil)
	in := &models.CausalAnalysis{
		Chain:      []models.CausalLink{{FromService: "ghost", ToService: "payment-service"}},
		Confidence: 0.2, // already below the ceiling
	}

	got := GroundAnalysis(in, e)

	if got.Confidence != 0.2 {
		t.Fatalf("capping must never raise confidence: got %v", got.Confidence)
	}
	if got.Grounding.ConfidencePenalty != 0 {
		t.Fatalf("no penalty when confidence already below ceiling, got %v", got.Grounding.ConfidencePenalty)
	}
}

func TestGroundAnalysis_DependencyGraphServicesCountAsKnown(t *testing.T) {
	// A service that only appears in the dependency graph (not in ServiceNames)
	// is still real evidence and must not be flagged as hallucinated.
	e := evidenceWith(
		[]string{"cart-service"},
		map[string][]string{"cart-service": {"redis-cache"}},
	)
	in := &models.CausalAnalysis{
		Chain:      []models.CausalLink{{FromService: "redis-cache", ToService: "cart-service"}},
		Confidence: 0.7,
	}

	got := GroundAnalysis(in, e)

	if !got.Grounding.Grounded {
		t.Fatalf("dependency-graph service should be known, got %+v", got.Grounding)
	}
}

func TestGroundAnalysis_CaseInsensitive(t *testing.T) {
	e := evidenceWith([]string{"Payment-Service"}, nil)
	in := &models.CausalAnalysis{
		Chain:      []models.CausalLink{{FromService: "payment-service", ToService: "payment-service"}},
		Confidence: 0.5,
	}
	got := GroundAnalysis(in, e)
	if !got.Grounding.Grounded {
		t.Fatal("service matching should be case-insensitive")
	}
}

func TestGroundAnalysis_ClampsOutOfRangeConfidence(t *testing.T) {
	e := evidenceWith([]string{"a"}, nil)
	over := GroundAnalysis(&models.CausalAnalysis{Confidence: 1.4}, e)
	if over.Confidence != 1.0 {
		t.Fatalf("expected clamp to 1.0, got %v", over.Confidence)
	}
	under := GroundAnalysis(&models.CausalAnalysis{Confidence: -0.3}, e)
	if under.Confidence != 0.0 {
		t.Fatalf("expected clamp to 0.0, got %v", under.Confidence)
	}
}

func TestGroundAnalysis_NoKnownServices_VacuouslyGrounded(t *testing.T) {
	// Degenerate incident with no services anywhere: nothing to validate
	// against, so the chain is preserved and marked grounded.
	e := &Evidence{Incident: &models.Incident{}}
	in := &models.CausalAnalysis{
		Chain:      []models.CausalLink{{FromService: "anything", ToService: "whatever"}},
		Confidence: 0.9,
	}
	got := GroundAnalysis(in, e)
	if got.Grounding == nil || !got.Grounding.Grounded {
		t.Fatalf("expected vacuously grounded, got %+v", got.Grounding)
	}
	if len(got.Chain) != 1 {
		t.Fatalf("expected chain preserved when nothing to check, got %d", len(got.Chain))
	}
}

func TestGroundAnalysis_DoesNotMutateInput(t *testing.T) {
	e := evidenceWith([]string{"real"}, nil)
	in := &models.CausalAnalysis{
		Chain:      []models.CausalLink{{FromService: "ghost", ToService: "real"}},
		Confidence: 0.99,
	}
	GroundAnalysis(in, e)
	if in.Confidence != 0.99 || len(in.Chain) != 1 {
		t.Fatalf("guardrail must not mutate its input: %+v", in)
	}
	if in.Grounding != nil {
		t.Fatal("guardrail must not attach a report to the original")
	}
}

func TestGroundAnalysis_NilResult(t *testing.T) {
	if GroundAnalysis(nil, evidenceWith([]string{"a"}, nil)) != nil {
		t.Fatal("expected nil passthrough for nil result")
	}
}
