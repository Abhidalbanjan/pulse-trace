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

// RecordFunc meters `count` events of a signal ("traces"/"metrics"/"logs") for a
// tenant. Injected so this package doesn't import the metering package. nil = off.
type RecordFunc func(ctx context.Context, tenantID, signal string, count int64)

// AllowFunc reports whether a tenant may still ingest `signal` under its plan
// quota. Injected so this package doesn't import the quota package. nil = allow all.
type AllowFunc func(ctx context.Context, tenantID, signal string) bool

// LogSinkFunc receives a tenant-stamped OTLP log export and persists it somewhere
// other than the collector — the gateway wires this to publish logs to Kafka
// (→ Quickwit → the log explorer) so OTLP-native logs land in the same store as
// every other log source. nil keeps the default behavior (forward to collector).
type LogSinkFunc func(ctx context.Context, tenantID, tier string, req *collogspb.ExportLogsServiceRequest) error

// tenantStamper holds the shared auth+stamp logic used by all three OTLP service
// servers (trace/metrics/logs each need their own type because the OTLP proto
// gives all three an identically-named Export method).
type tenantStamper struct {
	resolver   TenantResolver
	requireKey bool
	record     RecordFunc
	allow      AllowFunc

	// logSink, when set, receives stamped log exports instead of the collector;
	// logsUp is the upstream collector client used when no sink is wired.
	logSink LogSinkFunc
	logsUp  collogspb.LogsServiceClient
}

// emitLogs stamps the tenant onto every resource, meters the records, then routes
// the export to the log sink (Kafka → Quickwit) if one is wired, else forwards it
// to the upstream collector. Auth and quota are the caller's responsibility.
func (s *tenantStamper) emitLogs(ctx context.Context, tenantID, tier string, req *collogspb.ExportLogsServiceRequest) error {
	for _, rl := range req.GetResourceLogs() {
		rl.Resource = stampResource(rl.GetResource(), tenantID, tier)
	}
	s.meter(ctx, tenantID, "logs", countLogRecords(req))
	if s.logSink != nil {
		return s.logSink(ctx, tenantID, tier, req)
	}
	_, err := s.logsUp.Export(forwardContext(ctx), req)
	return err
}

// meter records count events for the tenant if metering is wired.
func (s *tenantStamper) meter(ctx context.Context, tenantID, signal string, count int64) {
	if s.record != nil && count > 0 {
		s.record(ctx, tenantID, signal, count)
	}
}

// checkQuota returns a ResourceExhausted error if the tenant is over its plan
// quota for the signal, else nil. No-op when no AllowFunc is wired.
func (s *tenantStamper) checkQuota(ctx context.Context, tenantID, signal string) error {
	if s.allow != nil && !s.allow(ctx, tenantID, signal) {
		return status.Error(codes.ResourceExhausted, "monthly "+signal+" ingestion quota exceeded for this plan")
	}
	return nil
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
	if err := t.stamper.checkQuota(ctx, tenantID, "traces"); err != nil {
		return nil, err
	}
	for _, rs := range req.GetResourceSpans() {
		rs.Resource = stampResource(rs.GetResource(), tenantID, tier)
	}
	t.stamper.meter(ctx, tenantID, "traces", countTraceSpans(req))
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
	if err := m.stamper.checkQuota(ctx, tenantID, "metrics"); err != nil {
		return nil, err
	}
	for _, rm := range req.GetResourceMetrics() {
		rm.Resource = stampResource(rm.GetResource(), tenantID, tier)
	}
	m.stamper.meter(ctx, tenantID, "metrics", countMetricPoints(req))
	return m.up.Export(forwardContext(ctx), req)
}

type logsServer struct {
	collogspb.UnimplementedLogsServiceServer
	stamper *tenantStamper
}

func (l *logsServer) Export(ctx context.Context, req *collogspb.ExportLogsServiceRequest) (*collogspb.ExportLogsServiceResponse, error) {
	tenantID, tier, err := l.stamper.authTenant(ctx)
	if err != nil {
		return nil, err
	}
	if err := l.stamper.checkQuota(ctx, tenantID, "logs"); err != nil {
		return nil, err
	}
	if err := l.stamper.emitLogs(ctx, tenantID, tier, req); err != nil {
		return nil, err
	}
	return &collogspb.ExportLogsServiceResponse{}, nil
}

// ── Volume counting for metering ──────────────────────────────────────────────

func countTraceSpans(req *coltracepb.ExportTraceServiceRequest) int64 {
	var n int64
	for _, rs := range req.GetResourceSpans() {
		for _, ss := range rs.GetScopeSpans() {
			n += int64(len(ss.GetSpans()))
		}
	}
	return n
}

func countLogRecords(req *collogspb.ExportLogsServiceRequest) int64 {
	var n int64
	for _, rl := range req.GetResourceLogs() {
		for _, sl := range rl.GetScopeLogs() {
			n += int64(len(sl.GetLogRecords()))
		}
	}
	return n
}

// countMetricPoints counts individual datapoints across all metric instrument
// types — the billable unit for metrics, not the number of metric names.
func countMetricPoints(req *colmetricspb.ExportMetricsServiceRequest) int64 {
	var n int64
	for _, rm := range req.GetResourceMetrics() {
		for _, sm := range rm.GetScopeMetrics() {
			for _, metric := range sm.GetMetrics() {
				n += int64(len(metric.GetGauge().GetDataPoints()))
				n += int64(len(metric.GetSum().GetDataPoints()))
				n += int64(len(metric.GetHistogram().GetDataPoints()))
				n += int64(len(metric.GetExponentialHistogram().GetDataPoints()))
				n += int64(len(metric.GetSummary().GetDataPoints()))
			}
		}
	}
	return n
}
