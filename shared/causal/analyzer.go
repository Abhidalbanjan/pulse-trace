// Package causal performs causal root-cause inference on incidents.
//
// Pulse-trace's correlation engine groups related alerts into incidents and
// labels them with a regex-based root-cause hint. The causal package goes one
// step further: it builds a temporal causal chain from the incident's alerts
// + the declared service dependency graph, then optionally enriches the chain
// with an LLM-generated narrative and refined hypothesis.
//
// Two analyzers are provided:
//
//   - NoopAnalyzer:    deterministic, no external calls. Builds a causal chain
//                      by walking the dependency graph in temporal order.
//                      Used as the default fallback when no API key is set.
//
//   - ClaudeAnalyzer:  calls the Anthropic Messages API to narrate the chain,
//                      refine the root-cause hypothesis, and emit a confidence
//                      score. Uses prompt caching for the static system prompt.
package causal

import (
	"context"
	"time"

	"github.com/pulsetrace/shared/models"
)

// Evidence is the input bundle passed to an Analyzer. It contains everything
// the analyzer needs to reason about the incident without re-querying the DB.
type Evidence struct {
	Incident     *models.Incident
	Alerts       []models.IncidentAlert
	Dependencies map[string][]string // service → list of upstream services
	Window       time.Duration       // correlation window used to group alerts
}

// Analyzer infers a causal chain and narrative for an incident.
//
// Implementations should be safe for concurrent use across goroutines.
type Analyzer interface {
	Analyze(ctx context.Context, e *Evidence) (*models.CausalAnalysis, error)
	Name() string
}
