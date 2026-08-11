package causal

import (
	"sort"
	"strings"

	"github.com/pulsetrace/shared/models"
)

// groundedConfidenceCeiling caps the confidence of any analysis that had a
// hallucinated reference removed. The reasoning: if the model named a service
// the incident never involved, its own certainty score is not trustworthy at
// face value, however eloquent the narrative reads. We keep the analysis (a
// pruned chain is still useful) but refuse to present it as high-confidence.
const groundedConfidenceCeiling = 0.4

// GroundAnalysis validates an analyzer's output against the incident's actual
// evidence and returns a copy with hallucinated references removed and a
// GroundingReport attached. This is the hallucination guardrail.
//
// The check is deterministic and pure — it makes no external calls and does
// not mutate its inputs — so it runs on every analysis regardless of which
// provider (or the rule-based fallback) produced it, and its behaviour is
// fully unit-testable.
//
// What it enforces:
//
//   - Every causal link must reference services that appear in the incident's
//     evidence (the incident's own services, the alerting services, and the
//     dependency graph). A link touching an unknown service is dropped and the
//     unknown name is recorded.
//   - Confidence is clamped to [0,1], and further capped to
//     groundedConfidenceCeiling when any hallucinated reference was found —
//     a fabricated claim should not ship as high-confidence.
//
// When the evidence carries no known services at all (a degenerate incident
// with nothing to validate against) the analysis is returned unchanged with a
// vacuously-grounded report: there is nothing to contradict, so nothing is
// dropped.
func GroundAnalysis(result *models.CausalAnalysis, e *Evidence) *models.CausalAnalysis {
	if result == nil {
		return nil
	}

	// Copy so the guardrail never mutates the analyzer's output in place —
	// callers may still hold a reference to the original.
	grounded := *result
	grounded.Confidence = clamp01(grounded.Confidence)

	known := knownServices(e)

	// With no ground truth to check against, we cannot assess grounding.
	// Report it as grounded (nothing was dropped) but keep the chain intact.
	if len(known) == 0 {
		grounded.Grounding = &models.GroundingReport{Grounded: true}
		return &grounded
	}

	keptChain := make([]models.CausalLink, 0, len(result.Chain))
	unknownSet := make(map[string]struct{})
	dropped := 0
	for _, link := range result.Chain {
		fromOK := isKnown(link.FromService, known)
		toOK := isKnown(link.ToService, known)
		if fromOK && toOK {
			keptChain = append(keptChain, link)
			continue
		}
		dropped++
		if !fromOK && strings.TrimSpace(link.FromService) != "" {
			unknownSet[link.FromService] = struct{}{}
		}
		if !toOK && strings.TrimSpace(link.ToService) != "" {
			unknownSet[link.ToService] = struct{}{}
		}
	}

	report := &models.GroundingReport{
		Grounded:        dropped == 0,
		DroppedLinks:    dropped,
		UnknownServices: sortedKeys(unknownSet),
	}

	if dropped > 0 {
		grounded.Chain = keptChain
		if grounded.Confidence > groundedConfidenceCeiling {
			report.ConfidencePenalty = grounded.Confidence - groundedConfidenceCeiling
			grounded.Confidence = groundedConfidenceCeiling
		}
	}

	grounded.Grounding = report
	return &grounded
}

// knownServices is the ground-truth set of service names an analysis may
// legitimately reference: the incident's own services, every service that
// raised an alert, and both ends of every declared dependency edge. Names are
// normalized (trimmed, lower-cased) so casing differences between the LLM's
// output and the topology don't read as hallucinations.
func knownServices(e *Evidence) map[string]struct{} {
	known := make(map[string]struct{})
	if e == nil {
		return known
	}
	add := func(name string) {
		if n := normalizeService(name); n != "" {
			known[n] = struct{}{}
		}
	}
	if e.Incident != nil {
		for _, svc := range e.Incident.ServiceNames {
			add(svc)
		}
	}
	for _, a := range e.Alerts {
		add(a.ServiceName)
	}
	for svc, upstreams := range e.Dependencies {
		add(svc)
		for _, up := range upstreams {
			add(up)
		}
	}
	return known
}

func isKnown(name string, known map[string]struct{}) bool {
	n := normalizeService(name)
	if n == "" {
		// An empty endpoint is not a hallucinated *service* — a rule-based
		// chain can legitimately leave one side blank. Treat it as known so it
		// isn't counted as an unknown service, but the link is still dropped by
		// the caller only when the *other* side is unknown.
		return true
	}
	_, ok := known[n]
	return ok
}

func normalizeService(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}

func sortedKeys(set map[string]struct{}) []string {
	if len(set) == 0 {
		return nil
	}
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
