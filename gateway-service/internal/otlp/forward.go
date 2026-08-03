package otlp

import (
	"context"

	collogspb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	colmetricspb "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	coltracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
)

// ForwardTraces and ForwardLogs let another already-authenticated ingestion
// path (the Datadog/Splunk migration proxy) reuse this receiver's tenant
// stamping, quota, metering, and upstream connection. The caller has resolved
// the tenant from its own protocol-native credential (DD-API-KEY / Splunk
// token) rather than gRPC metadata, so these take the tenant explicitly instead
// of pulling it from the context the way the gRPC Export handlers do.
//
// The stamping is identical to the gRPC path — any client-supplied tenant.id is
// dropped and the resolved tenant is stamped onto every Resource — so telemetry
// arriving via a competitor's agent gets the exact same isolation as native OTLP.

func (r *Receiver) ForwardTraces(ctx context.Context, tenantID, tier string, req *coltracepb.ExportTraceServiceRequest) error {
	if err := r.stamper.checkQuota(ctx, tenantID, "traces"); err != nil {
		return err
	}
	for _, rs := range req.GetResourceSpans() {
		rs.Resource = stampResource(rs.GetResource(), tenantID, tier)
	}
	r.stamper.meter(ctx, tenantID, "traces", countTraceSpans(req))
	_, err := r.traceClient.Export(ctx, req)
	return err
}

func (r *Receiver) ForwardLogs(ctx context.Context, tenantID, tier string, req *collogspb.ExportLogsServiceRequest) error {
	if err := r.stamper.checkQuota(ctx, tenantID, "logs"); err != nil {
		return err
	}
	// emitLogs stamps, meters, and routes to the log sink (Kafka → Quickwit) when
	// one is wired, else forwards to the collector — the same path as the gRPC
	// logsServer, so migration-log fallback and native OTLP logs behave identically.
	return r.stamper.emitLogs(ctx, tenantID, tier, req)
}

func (r *Receiver) ForwardMetrics(ctx context.Context, tenantID, tier string, req *colmetricspb.ExportMetricsServiceRequest) error {
	if err := r.stamper.checkQuota(ctx, tenantID, "metrics"); err != nil {
		return err
	}
	for _, rm := range req.GetResourceMetrics() {
		rm.Resource = stampResource(rm.GetResource(), tenantID, tier)
	}
	r.stamper.meter(ctx, tenantID, "metrics", countMetricPoints(req))
	_, err := r.metricsClient.Export(ctx, req)
	return err
}
