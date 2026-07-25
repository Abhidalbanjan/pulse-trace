// Package otlp terminates OTLP/gRPC ingestion in-process at the gateway so that
// telemetry can be authenticated and attributed to a tenant before it reaches
// the OTel Collector.
//
// Previously the gateway ran a raw TCP tunnel from :4317 straight to the
// collector. That tunnel was invisible to the HTTP middleware, so OTLP/gRPC
// traffic could be neither authenticated (no ingestion-key check) nor
// tenant-attributed (nothing stamped a tenant onto the spans/metrics/logs). As a
// result the ClickHouse tables the collector writes (otel_traces, otel_metrics_*)
// had no tenant dimension at all, making every trace/metric query cross-tenant
// readable. This receiver closes that gap: it verifies a per-tenant ingestion key
// on each export, stamps the resolved tenant onto every Resource as the
// `tenant.id` / `tenant.tier` resource attribute, and forwards to the collector —
// which persists those attributes so queries can filter ResourceAttributes['tenant.id'].
package otlp

import (
	"context"
	"strings"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	collogspb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	colmetricspb "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	coltracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
)

const (
	// TenantIDAttr / TenantTierAttr are the resource-attribute keys the tenant is
	// stamped under. The collector persists resource attributes into the
	// ClickHouse ResourceAttributes Map column, so gateway queries filter on
	// ResourceAttributes['tenant.id']. Keep these in sync with those queries.
	TenantIDAttr   = "tenant.id"
	TenantTierAttr = "tenant.tier"

	defaultTenantID   = "default"
	defaultTenantTier = "standard"

	// scopeIngest is the key scope permitted to write server telemetry. A public
	// RUM-scoped key must never be accepted on this (gRPC) path. Kept as a local
	// constant so this package doesn't import auth.
	scopeIngest = "ingest"
)

// TenantResolver maps an ingestion-key plaintext to the tenant/tier/scope it
// belongs to. Satisfied by auth.IngestionKeyStore, so the gRPC path reuses the
// exact same key store (and cache) as the HTTP ingestion path.
type TenantResolver interface {
	Resolve(ctx context.Context, plaintext string) (tenantID, tier, scope string, ok bool)
}

// tenantStamper holds the shared auth+stamp logic used by all three OTLP service
// servers (trace/metrics/logs each need their own type because the OTLP proto
// gives all three an identically-named Export method).
type tenantStamper struct {
	resolver   TenantResolver
	requireKey bool
}

// authTenant resolves the tenant for an incoming export from its ingestion key
// (Authorization: Bearer <key> in the gRPC metadata). When no valid key is
// present it either rejects (requireKey) or falls back to the default tenant —
// the same policy the HTTP AuthMiddleware applies.
func (s *tenantStamper) authTenant(ctx context.Context) (tenantID, tier string, err error) {
	token := bearerFromMetadata(ctx)
	if tid, tr, scope, ok := s.resolver.Resolve(ctx, token); ok {
		// The gRPC receiver only carries server telemetry (traces/metrics/logs);
		// a public RUM-scoped key must never be usable here.
		if scope != scopeIngest {
			return "", "", status.Error(codes.PermissionDenied, "this key is not permitted for server telemetry ingestion")
		}
		return tid, tr, nil
	}
	if s.requireKey {
		return "", "", status.Error(codes.Unauthenticated, "valid ingestion key required")
	}
	return defaultTenantID, defaultTenantTier, nil
}

// forwardContext strips the caller's ingestion key before forwarding upstream —
// the collector must never receive the tenant's secret key.
func forwardContext(ctx context.Context) context.Context {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return ctx
	}
	out := md.Copy()
	out.Delete("authorization")
	return metadata.NewOutgoingContext(ctx, out)
}

// bearerFromMetadata pulls the token out of an "authorization: Bearer <token>"
// gRPC metadata entry, or "" if absent/malformed. Metadata keys are lowercased
// by gRPC, so we look up "authorization".
func bearerFromMetadata(ctx context.Context) string {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return ""
	}
	vals := md.Get("authorization")
	if len(vals) == 0 || !strings.HasPrefix(vals[0], "Bearer ") {
		return ""
	}
	return strings.TrimPrefix(vals[0], "Bearer ")
}

// stampResource returns res with the tenant stamped onto its attributes. Any
// client-supplied tenant.id/tenant.tier is dropped first, so a client can't
// spoof its tenant by pre-setting those resource attributes. A nil resource
// (valid per OTLP) is materialized so the tenant is always present.
func stampResource(res *resourcepb.Resource, tenantID, tier string) *resourcepb.Resource {
	if res == nil {
		res = &resourcepb.Resource{}
	}
	res.Attributes = append(withoutTenant(res.Attributes),
		&commonpb.KeyValue{Key: TenantIDAttr, Value: stringValue(tenantID)},
		&commonpb.KeyValue{Key: TenantTierAttr, Value: stringValue(tier)},
	)
	return res
}

// withoutTenant returns attrs with any existing tenant.id/tenant.tier removed.
func withoutTenant(attrs []*commonpb.KeyValue) []*commonpb.KeyValue {
	out := attrs[:0]
	for _, kv := range attrs {
		if kv == nil || kv.Key == TenantIDAttr || kv.Key == TenantTierAttr {
			continue
		}
		out = append(out, kv)
	}
	return out
}

func stringValue(s string) *commonpb.AnyValue {
	return &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: s}}
}

// ── The three OTLP service servers ────────────────────────────────────────────
// Each authenticates, stamps the tenant onto every resource, then forwards to
// the upstream collector client.

type traceServer struct {
	coltracepb.UnimplementedTraceServiceServer
	stamper *tenantStamper
	up      coltracepb.TraceServiceClient
}

func (t *traceServer) Export(ctx context.Context, req *coltracepb.ExportTraceServiceRequest) (*coltracepb.ExportTraceServiceResponse, error) {
	tenantID, tier, err := t.stamper.authTenant(ctx)
	if err != nil {
		return nil, err
	}
	for _, rs := range req.GetResourceSpans() {
		rs.Resource = stampResource(rs.GetResource(), tenantID, tier)
	}
	return t.up.Export(forwardContext(ctx), req)
}

type metricsServer struct {
	colmetricspb.UnimplementedMetricsServiceServer
	stamper *tenantStamper
	up      colmetricspb.MetricsServiceClient
}

func (m *metricsServer) Export(ctx context.Context, req *colmetricspb.ExportMetricsServiceRequest) (*colmetricspb.ExportMetricsServiceResponse, error) {
	tenantID, tier, err := m.stamper.authTenant(ctx)
	if err != nil {
		return nil, err
	}
	for _, rm := range req.GetResourceMetrics() {
		rm.Resource = stampResource(rm.GetResource(), tenantID, tier)
	}
	return m.up.Export(forwardContext(ctx), req)
}

type logsServer struct {
	collogspb.UnimplementedLogsServiceServer
	stamper *tenantStamper
	up      collogspb.LogsServiceClient
}

func (l *logsServer) Export(ctx context.Context, req *collogspb.ExportLogsServiceRequest) (*collogspb.ExportLogsServiceResponse, error) {
	tenantID, tier, err := l.stamper.authTenant(ctx)
	if err != nil {
		return nil, err
	}
	for _, rl := range req.GetResourceLogs() {
		rl.Resource = stampResource(rl.GetResource(), tenantID, tier)
	}
	return l.up.Export(forwardContext(ctx), req)
}
