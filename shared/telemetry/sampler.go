package telemetry

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

// ForceKeepAttributeKey is the span attribute the tail_sampling "manual-keep-policy"
// (otel-collector/otel-collector-config.yaml) matches on. Any trace containing a span
// with this attribute is retained regardless of error/latency/random sampling -
// the same "retention filter" mechanism Datadog uses for force-keep.
const ForceKeepAttributeKey = "sampling.priority"

// ForceKeep marks the current span (and therefore its whole trace, once tail_sampling
// evaluates it) to always be retained - for critical business transactions that must
// never be dropped by cost-driven sampling. No-op if ctx has no active span.
func ForceKeep(ctx context.Context) {
	trace.SpanFromContext(ctx).SetAttributes(attribute.String(ForceKeepAttributeKey, "keep"))
}

// defaultForceDropSpanNames are well-known noisy, low-value endpoints (health/readiness
// checks, metrics scrapes) that are dropped at head-sampling time - before the collector
// or any backend ever sees them - so they never cost ingestion or storage. Extend via
// OTEL_FORCE_DROP_SPAN_NAMES (comma-separated span names, e.g. "GET /healthz,GET /ping").
var defaultForceDropSpanNames = []string{
	"GET /healthz",
	"GET /health",
	"GET /metrics",
	"GET /debug/pprof/",
}

func forceDropSpanNames() map[string]struct{} {
	names := make(map[string]struct{}, len(defaultForceDropSpanNames))
	for _, n := range defaultForceDropSpanNames {
		names[n] = struct{}{}
	}
	if extra := os.Getenv("OTEL_FORCE_DROP_SPAN_NAMES"); extra != "" {
		for _, n := range strings.Split(extra, ",") {
			if n = strings.TrimSpace(n); n != "" {
				names[n] = struct{}{}
			}
		}
	}
	return names
}

// namedDropSampler wraps another sampler, force-dropping spans whose name matches a
// configured noise list before ever consulting the wrapped sampler.
type namedDropSampler struct {
	inner     sdktrace.Sampler
	dropNames map[string]struct{}
}

func withForceDrop(inner sdktrace.Sampler) sdktrace.Sampler {
	return &namedDropSampler{inner: inner, dropNames: forceDropSpanNames()}
}

func (s *namedDropSampler) ShouldSample(p sdktrace.SamplingParameters) sdktrace.SamplingResult {
	if _, drop := s.dropNames[p.Name]; drop {
		psc := trace.SpanContextFromContext(p.ParentContext)
		return sdktrace.SamplingResult{
			Decision:   sdktrace.Drop,
			Tracestate: psc.TraceState(),
		}
	}
	return s.inner.ShouldSample(p)
}

func (s *namedDropSampler) Description() string {
	return fmt.Sprintf("ForceDrop(%s)", s.inner.Description())
}

func floatToBits(f float64) uint64 { return math.Float64bits(f) }
func bitsToFloat(b uint64) float64 { return math.Float64frombits(b) }

// staticSamplerFromEnv builds a sampler from the standard OTel env vars
// (OTEL_TRACES_SAMPLER / OTEL_TRACES_SAMPLER_ARG). Returns nil, false if unset,
// so the caller can fall back to a different default (dynamic or AlwaysSample).
func staticSamplerFromEnv() (sdktrace.Sampler, bool) {
	name := os.Getenv("OTEL_TRACES_SAMPLER")
	if name == "" {
		return nil, false
	}
	arg := os.Getenv("OTEL_TRACES_SAMPLER_ARG")
	ratio := 1.0
	if arg != "" {
		if v, err := strconv.ParseFloat(arg, 64); err == nil {
			ratio = v
		} else {
			log.Printf("telemetry: invalid OTEL_TRACES_SAMPLER_ARG %q, defaulting to 1.0: %v", arg, err)
		}
	}

	switch name {
	case "always_on":
		return sdktrace.AlwaysSample(), true
	case "always_off":
		return sdktrace.NeverSample(), true
	case "traceidratio":
		return sdktrace.TraceIDRatioBased(ratio), true
	case "parentbased_always_on":
		return sdktrace.ParentBased(sdktrace.AlwaysSample()), true
	case "parentbased_always_off":
		return sdktrace.ParentBased(sdktrace.NeverSample()), true
	case "parentbased_traceidratio":
		return sdktrace.ParentBased(sdktrace.TraceIDRatioBased(ratio)), true
	default:
		log.Printf("telemetry: unrecognized OTEL_TRACES_SAMPLER %q, ignoring", name)
		return nil, false
	}
}

// DynamicSampler is a trace-id-ratio sampler whose ratio can be updated at
// runtime (e.g. by polling topology-service's per-service agent config).
type DynamicSampler struct {
	ratioBits atomic.Uint64 // float64 bits, read/written via math.Float64bits
}

func NewDynamicSampler(initialRatio float64) *DynamicSampler {
	d := &DynamicSampler{}
	d.Set(initialRatio)
	return d
}

func (d *DynamicSampler) Set(ratio float64) {
	if ratio < 0 {
		ratio = 0
	}
	if ratio > 1 {
		ratio = 1
	}
	d.ratioBits.Store(floatToBits(ratio))
}

func (d *DynamicSampler) Ratio() float64 {
	return bitsToFloat(d.ratioBits.Load())
}

func (d *DynamicSampler) ShouldSample(p sdktrace.SamplingParameters) sdktrace.SamplingResult {
	return sdktrace.ParentBased(sdktrace.TraceIDRatioBased(d.Ratio())).ShouldSample(p)
}

func (d *DynamicSampler) Description() string {
	return fmt.Sprintf("DynamicSampler{ratio=%.4f}", d.Ratio())
}

type agentConfigResponse struct {
	LogLevel      string  `json:"log_level"`
	TraceSampling float64 `json:"trace_sampling"`
}

// pollAgentConfig periodically fetches this service's recommended trace sampling
// rate from topology-service (which raises it to 100% while the service is
// DEGRADED/PREDICTIVE_WARNING) and applies it to sampler. Runs until ctx is cancelled.
func pollAgentConfig(ctx context.Context, serviceName, topologyURL string, sampler *DynamicSampler) {
	client := &http.Client{Timeout: 5 * time.Second}
	url := topologyURL + "/api/v1/topology/agent-config/" + serviceName

	fetch := func() {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return
		}
		resp, err := client.Do(req)
		if err != nil {
			log.Printf("telemetry: agent-config poll failed for %s: %v", serviceName, err)
			return
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return
		}
		var cfg agentConfigResponse
		if err := json.NewDecoder(resp.Body).Decode(&cfg); err != nil {
			log.Printf("telemetry: failed to decode agent-config for %s: %v", serviceName, err)
			return
		}
		if prev := sampler.Ratio(); prev != cfg.TraceSampling {
			log.Printf("telemetry: %s trace sampling %.4f -> %.4f (topology-service agent-config)", serviceName, prev, cfg.TraceSampling)
		}
		sampler.Set(cfg.TraceSampling)
	}

	fetch() // apply immediately on startup rather than waiting a full interval
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			fetch()
		}
	}
}
