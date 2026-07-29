// Package ingestproxy is the "Trojan Horse" zero-code migration front door: it
// accepts telemetry in competitors' native wire formats (Datadog agent traces,
// Splunk HEC events) so a prospect can migrate by changing only the ingestion
// URL in their existing agents — no re-instrumentation.
//
// Crucially, it is an *authenticating, tenant-attributing* front door, not a raw
// pass-through. Each request is authenticated with a per-tenant PulseTrace
// ingestion key (carried in the protocol's own auth header — DD-API-KEY for
// Datadog, "Authorization: Splunk <token>" for Splunk), the payload is
// translated to OTLP, and it is forwarded through the same tenant-stamping path
// as native OTLP (see otlp.Receiver.ForwardTraces/ForwardLogs). This is what
// lets these formats land in the shared stores with correct tenant isolation —
// the reason the collector's own datadog/splunk receivers (which do neither auth
// nor tenant attribution) must not be exposed directly.
package ingestproxy

import (
	"bytes"
	"compress/flate"
	"compress/gzip"
	"compress/zlib"
	"context"
	"io"
	"net/http"
	"strings"

	collogspb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	colmetricspb "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	coltracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"

	"github.com/pulsetrace/gateway-service/internal/auth"
)

// Forwarder is the tenant-stamping OTLP forward path (satisfied by
// *otlp.Receiver). Injected so this package doesn't import otlp and stays
// unit-testable with a fake.
type Forwarder interface {
	ForwardTraces(ctx context.Context, tenantID, tier string, req *coltracepb.ExportTraceServiceRequest) error
	ForwardLogs(ctx context.Context, tenantID, tier string, req *collogspb.ExportLogsServiceRequest) error
	ForwardMetrics(ctx context.Context, tenantID, tier string, req *colmetricspb.ExportMetricsServiceRequest) error
}

// TenantResolver maps an ingestion-key plaintext to its tenant/tier/scope
// (satisfied by *auth.IngestionKeyStore) — the same key store the OTLP and HTTP
// ingestion paths use.
type TenantResolver interface {
	Resolve(ctx context.Context, plaintext string) (tenantID, tier, scope string, ok bool)
}

// Proxy holds the shared auth + forward wiring for both migration formats.
type Proxy struct {
	fwd        Forwarder
	resolver   TenantResolver
	requireKey bool
}

func New(fwd Forwarder, resolver TenantResolver, requireKey bool) *Proxy {
	return &Proxy{fwd: fwd, resolver: resolver, requireKey: requireKey}
}

// RegisterRoutes wires the Datadog trace-agent and Splunk HEC intake endpoints.
func (p *Proxy) RegisterRoutes(mux *http.ServeMux) {
	// Datadog trace-agent intake (what a DD tracing library / the trace-agent
	// posts to). v0.5 uses a string-table format we don't decode; leaving it
	// unregistered makes the agent negotiate down to v0.4.
	mux.HandleFunc("POST /v0.3/traces", p.DatadogTraces)
	mux.HandleFunc("POST /v0.4/traces", p.DatadogTraces)
	mux.HandleFunc("POST /v0.5/traces", p.DatadogTraces) // string-table msgpack

	// Datadog metrics and logs intake.
	mux.HandleFunc("POST /api/v1/series", p.DatadogSeries) // v1 series (JSON)
	mux.HandleFunc("POST /api/v2/series", p.DatadogSeries) // v2 series (JSON)
	mux.HandleFunc("POST /api/v2/logs", p.DatadogLogs)

	// Splunk HTTP Event Collector (routes both log and metric events).
	mux.HandleFunc("POST /services/collector", p.SplunkHEC)
	mux.HandleFunc("POST /services/collector/event", p.SplunkHEC)
	mux.HandleFunc("POST /services/collector/event/1.0", p.SplunkHEC)
	mux.HandleFunc("POST /services/collector/raw", p.SplunkHECRaw)
}

// readBody reads and decompresses a request body, honoring the Content-Encoding
// the Datadog agent uses (gzip for traces, deflate/zlib for metrics). An
// unrecognized/empty encoding is read as-is. Capped to guard against
// decompression bombs.
func readBody(r *http.Request, maxBytes int64) ([]byte, error) {
	raw, err := io.ReadAll(io.LimitReader(r.Body, maxBytes))
	if err != nil {
		return nil, err
	}
	switch strings.ToLower(strings.TrimSpace(r.Header.Get("Content-Encoding"))) {
	case "gzip":
		gz, err := gzip.NewReader(bytes.NewReader(raw))
		if err != nil {
			return nil, err
		}
		defer gz.Close()
		return io.ReadAll(io.LimitReader(gz, maxBytes))
	case "deflate", "zlib":
		// DD sends zlib-wrapped deflate; fall back to raw flate if there's no
		// zlib header.
		if zr, err := zlib.NewReader(bytes.NewReader(raw)); err == nil {
			defer zr.Close()
			return io.ReadAll(io.LimitReader(zr, maxBytes))
		}
		fr := flate.NewReader(bytes.NewReader(raw))
		defer fr.Close()
		return io.ReadAll(io.LimitReader(fr, maxBytes))
	}
	return raw, nil
}

// resolveTenant applies the exact policy the OTLP/HTTP ingestion paths use:
//   - a key that resolves and has the "ingest" scope → its tenant/tier
//   - a key that resolves but is RUM-scoped → 403 (never write server telemetry)
//   - no/invalid key → 401 when REQUIRE_INGESTION_KEY, else the default tenant
//
// On rejection it returns the HTTP status and false; the handler writes it.
func (p *Proxy) resolveTenant(ctx context.Context, key string) (tenantID, tier string, status int, ok bool) {
	if tid, tr, scope, resolved := p.resolver.Resolve(ctx, key); resolved {
		if scope != auth.ScopeIngest {
			return "", "", http.StatusForbidden, false
		}
		return tid, tr, 0, true
	}
	if p.requireKey {
		return "", "", http.StatusUnauthorized, false
	}
	return defaultTenantID, defaultTenantTier, 0, true
}

const (
	defaultTenantID   = "default"
	defaultTenantTier = "standard"
)
