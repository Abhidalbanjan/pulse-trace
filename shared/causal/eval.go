package causal

// Causal-AI evaluation harness (ROAD_TO_100 · F0.5).
//
// "AI RCA" can only be sold as such if its accuracy is measured, not asserted.
// This harness scores any Analyzer (the deterministic rule-based fallback in CI,
// or an LLM provider when a key is present) against a labelled fixture set of
// incidents whose correct root cause is known, and produces a Scorecard with a
// published accuracy number. The deterministic score is gated in CI so a
// regression in root-cause inference fails the build.
//
// Scored dimensions per fixture:
//   • RootService — does the causal chain blame the correct originating service?
//     (the core "did RCA work" signal; empty when the incident is localized.)
//   • Playbook    — does the suggested remediation match the expected one?
//   • Confidence  — is the confidence at/above the expected floor?
//   • Narrative   — does the narrative/hypothesis mention the root-cause keyword?

import (
	"context"
	"sort"
	"strings"

	"github.com/pulsetrace/shared/models"
)

// EvalFixture is one labelled incident scenario with its expected deterministic
// root-cause outcome.
type EvalFixture struct {
	Name string
	// Evidence is the input the analyzer reasons over.
	Evidence *Evidence
	// ExpectRootService is the service the causal chain should identify as the
	// origin. Empty means the incident is localized (no upstream chain expected).
	ExpectRootService string
	// ExpectPlaybook is the SuggestPlaybook name the root cause should map to.
	ExpectPlaybook string
	// MinConfidence is the lowest acceptable confidence for this incident.
	MinConfidence float64
	// RootCauseKeyword must appear in the narrative or refined hypothesis.
	RootCauseKeyword string
}

// FixtureResult is the per-fixture scoring outcome.
type FixtureResult struct {
	Name          string
	RootServiceOK bool
	PlaybookOK    bool
	ConfidenceOK  bool
	NarrativeOK   bool
	GotRootSvc    string
	GotPlaybook   string
	GotConfidence float64
	Err           string
}

// passed reports whether every scored dimension held for this fixture.
func (r FixtureResult) passed() bool {
	return r.Err == "" && r.RootServiceOK && r.PlaybookOK && r.ConfidenceOK && r.NarrativeOK
}

// Scorecard aggregates results across the fixture set for one analyzer.
type Scorecard struct {
	Analyzer            string
	Results             []FixtureResult
	Count               int
	RootServiceAccuracy float64 // fraction with the correct blamed service
	PlaybookAccuracy    float64
	ConfidenceAccuracy  float64
	NarrativeAccuracy   float64
	Overall             float64 // fraction of fixtures where every dimension held
}

// Evaluate runs the analyzer over every fixture and scores it. It never returns
// an error: an analyzer failure on a fixture is recorded as a failed result so a
// flaky provider scores lower rather than aborting the run.
func Evaluate(ctx context.Context, a Analyzer, fixtures []EvalFixture) Scorecard {
	sc := Scorecard{Analyzer: a.Name(), Count: len(fixtures)}
	var rootHits, playbookHits, confHits, narrHits, overallHits int

	for _, f := range fixtures {
		res := FixtureResult{Name: f.Name}
		analysis, err := a.Analyze(ctx, f.Evidence)
		if err != nil || analysis == nil {
			if err != nil {
				res.Err = err.Error()
			} else {
				res.Err = "analyzer returned nil analysis"
			}
			sc.Results = append(sc.Results, res)
			continue
		}

		res.GotRootSvc = rootService(analysis.Chain)
		res.RootServiceOK = res.GotRootSvc == f.ExpectRootService

		if analysis.Playbook != nil {
			res.GotPlaybook = analysis.Playbook.Name
		}
		res.PlaybookOK = res.GotPlaybook == f.ExpectPlaybook

		res.GotConfidence = analysis.Confidence
		res.ConfidenceOK = analysis.Confidence >= f.MinConfidence

		haystack := strings.ToLower(analysis.Narrative + " " + analysis.RootCause)
		res.NarrativeOK = f.RootCauseKeyword == "" || strings.Contains(haystack, strings.ToLower(f.RootCauseKeyword))

		if res.RootServiceOK {
			rootHits++
		}
		if res.PlaybookOK {
			playbookHits++
		}
		if res.ConfidenceOK {
			confHits++
		}
		if res.NarrativeOK {
			narrHits++
		}
		if res.passed() {
			overallHits++
		}
		sc.Results = append(sc.Results, res)
	}

	if n := float64(len(fixtures)); n > 0 {
		sc.RootServiceAccuracy = float64(rootHits) / n
		sc.PlaybookAccuracy = float64(playbookHits) / n
		sc.ConfidenceAccuracy = float64(confHits) / n
		sc.NarrativeAccuracy = float64(narrHits) / n
		sc.Overall = float64(overallHits) / n
	}
	return sc
}

// rootService returns the origin of the causal DAG: the service that appears as a
// cause (FromService) but never as an effect (ToService). That is the true root
// of the blame graph, more robust than taking chain[0]. Empty when there is no
// chain (a localized incident) or no unique source.
func rootService(chain []models.CausalLink) string {
	if len(chain) == 0 {
		return ""
	}
	effects := make(map[string]bool, len(chain))
	for _, l := range chain {
		effects[l.ToService] = true
	}
	var roots []string
	seen := map[string]bool{}
	for _, l := range chain {
		if !effects[l.FromService] && !seen[l.FromService] {
			roots = append(roots, l.FromService)
			seen[l.FromService] = true
		}
	}
	if len(roots) == 1 {
		return roots[0]
	}
	if len(roots) == 0 {
		return chain[0].FromService // cycle: fall back to the first inferred cause
	}
	// Multiple independent sources: pick deterministically so scoring is stable.
	sort.Strings(roots)
	return roots[0]
}
