package causal

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/pulsetrace/shared/models"
)

// BuildChain constructs a deterministic causal chain from a set of incident
// alerts and a service dependency graph.
//
// The algorithm:
//  1. Sort alerts by triggered_at ascending.
//  2. For each alert A, look up A.service's declared upstream dependencies.
//  3. For each upstream U, find the earliest preceding alert from U. If one
//     exists, emit a link U → A.service with the timing as evidence.
//
// This produces a temporally-ordered DAG fragment that mirrors how an SRE
// would mentally trace blame upstream during triage.
func BuildChain(alerts []models.IncidentAlert, deps map[string][]string) []models.CausalLink {
	if len(alerts) == 0 {
		return nil
	}

	sorted := make([]models.IncidentAlert, len(alerts))
	copy(sorted, alerts)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].TriggeredAt.Before(sorted[j].TriggeredAt)
	})

	// Deduplicate links — multiple alerts from the same downstream service
	// pointing at the same upstream cause should collapse into a single edge.
	seen := make(map[string]bool)
	var links []models.CausalLink

	for i := 1; i < len(sorted); i++ {
		target := sorted[i]
		upstream := deps[target.ServiceName]
		if len(upstream) == 0 {
			continue
		}

		for _, u := range upstream {
			for j := 0; j < i; j++ {
				cand := sorted[j]
				if cand.ServiceName != u {
					continue
				}

				key := cand.ServiceName + "→" + target.ServiceName
				if seen[key] {
					break
				}
				seen[key] = true

				links = append(links, models.CausalLink{
					FromService: cand.ServiceName,
					ToService:   target.ServiceName,
					Evidence: fmt.Sprintf(
						"%s alert at %s preceded %s alert at %s; declared dependency",
						cand.ServiceName, cand.TriggeredAt.Format(time.RFC3339),
						target.ServiceName, target.TriggeredAt.Format(time.RFC3339),
					),
					At: cand.TriggeredAt,
				})
				break
			}
		}
	}
	return links
}

// NoopAnalyzer is the zero-dependency baseline. It runs BuildChain and emits
// a templated narrative — no LLM call, no external API key needed. This is
// the fallback when ANTHROPIC_API_KEY is unset.
type NoopAnalyzer struct{}

func (n *NoopAnalyzer) Name() string { return "rule-based" }

func (n *NoopAnalyzer) Analyze(_ context.Context, e *Evidence) (*models.CausalAnalysis, error) {
	chain := BuildChain(e.Alerts, e.Dependencies)
	return &models.CausalAnalysis{
		Chain:      chain,
		Narrative:  buildNoopNarrative(chain, e.Incident),
		RootCause:  e.Incident.RootCause,
		Confidence: noopConfidence(chain, e.Incident),
		Model:      n.Name(),
		AnalyzedAt: time.Now().UTC(),
	}, nil
}

// noopConfidence assigns a heuristic confidence:
//   - 0.4 baseline (regex-based root cause only)
//   - +0.2 if at least one causal link was inferred
//   - +0.1 per additional unique upstream service in the chain (capped at 0.85)
func noopConfidence(chain []models.CausalLink, inc *models.Incident) float64 {
	conf := 0.4
	if len(chain) > 0 {
		conf += 0.2
	}
	upstream := map[string]bool{}
	for _, l := range chain {
		upstream[l.FromService] = true
	}
	conf += 0.1 * float64(len(upstream))
	if conf > 0.85 {
		conf = 0.85
	}
	_ = inc
	return conf
}

func buildNoopNarrative(chain []models.CausalLink, inc *models.Incident) string {
	if len(chain) == 0 {
		return fmt.Sprintf(
			"No upstream service correlations detected. Root cause appears localized to %s. Probable cause: %s.",
			strings.Join(inc.ServiceNames, ", "), inc.RootCause,
		)
	}

	var b strings.Builder
	b.WriteString("Inferred causal chain (deterministic, dependency-graph walk):\n")
	for i, l := range chain {
		fmt.Fprintf(&b, "  %d. %s → %s  (%s)\n", i+1, l.FromService, l.ToService, l.At.Format(time.RFC3339))
	}
	fmt.Fprintf(&b, "\nProbable upstream root cause: %s. Confirm by inspecting %s logs around %s.",
		inc.RootCause,
		chain[0].FromService,
		chain[0].At.Format(time.RFC3339),
	)
	return b.String()
}
