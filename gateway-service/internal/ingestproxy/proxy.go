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
	"errors"
	"io"
	"net/http"
	"strings"

	collogspb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	colmetricspb "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	coltracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"

	"github.com/pulsetrace/gateway-service/internal/auth"
	"github.com/pulsetrace/shared/models"
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

	// Optional log sink. When logPub is set, migration logs are published as
	// native LogEntry records to Kafka (→ Quickwit → log explorer) instead of
	// being forwarded as OTLP to ClickHouse otel_logs, and are metered/quota-
	// checked here (meter/allow) since they no longer traverse the OTLP forward.
	logPub LogPublisher
	meter  MeterFunc
	allow  QuotaFunc
}

func New(fwd Forwarder, resolver TenantResolver, requireKey bool) *Proxy {
	return &Proxy{fwd: fwd, resolver: resolver, requireKey: requireKey}
}

// SetLogSink wires the native log path for migration logs. Once set, DatadogLogs
// and the Splunk HEC log events publish LogEntry records to Kafka (so they land
// in the same Quickwit index the log explorer reads) rather than forwarding OTLP
// logs to ClickHouse. meter/allow keep per-tenant usage accounting and quotas
// intact on that path. When it isn't set (e.g. unit tests, or Kafka unavailable
// at startup), the handlers fall back to the OTLP forward.
func (p *Proxy) SetLogSink(pub LogPublisher, meter MeterFunc, allow QuotaFunc) {
	p.logPub, p.meter, p.allow = pub, meter, allow
}

// publishLogs meters, quota-checks, and publishes native LogEntry records for a
// migration request. Returns (handled, err): handled=false means no log sink is
// wired and the caller should fall back to the OTLP forward. A quota rejection is
// surfaced as an error the caller maps to 429.
func (p *Proxy) publishLogs(ctx context.Context, tenantID string, entries []*models.LogEntry) (handled bool, err error) {
	if p.logPub == nil {
		return false, nil
	}
	if len(entries) == 0 {
		return true, nil
	}
	if p.allow != nil && !p.allow(ctx, tenantID, "logs") {
		return true, errQuotaExceeded
	}
	if err := p.logPub.PublishBatch(ctx, logsTopic, entries); err != nil {
		return true, err
	}
	if p.meter != nil {
		p.meter(ctx, tenantID, "logs", int64(len(entries)))
	}
	return true, nil
}

// errQuotaExceeded is mapped to HTTP 429 by writeLogSinkError.
var errQuotaExceeded = errors.New("ingestion quota exceeded")

// writeLogSinkError translates a publishLogs error into an HTTP response.
func writeLogSinkError(w http.ResponseWriter, err error) {
	if errors.Is(err, errQuotaExceeded) {
		http.Error(w, err.Error(), http.StatusTooManyRequests)
		return
	}
	http.Error(w, "failed to publish logs", http.StatusBadGateway)
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
