package otlp

import (
	"log"
	"net"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	collogspb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	colmetricspb "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	coltracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
)

// Receiver is an in-process OTLP/gRPC server that authenticates and
// tenant-stamps telemetry, then forwards it to the upstream OTel Collector. It
// replaces the gateway's previous raw TCP tunnel to :4317.
type Receiver struct {
	stamper  *tenantStamper
	upstream *grpc.ClientConn

	traceClient   coltracepb.TraceServiceClient
	metricsClient colmetricspb.MetricsServiceClient
	logsClient    collogspb.LogsServiceClient

	grpcServer *grpc.Server
}

// NewReceiver dials the upstream collector (lazily; grpc.NewClient does not
// connect until the first RPC) and prepares the tenant-stamping servers.
// resolver is the ingestion-key store; requireKey mirrors REQUIRE_INGESTION_KEY;
// record meters ingested volume (nil disables metering); allow enforces per-plan
// quota on the gRPC path (nil allows all).
func NewReceiver(resolver TenantResolver, requireKey bool, upstreamAddr string, record RecordFunc, allow AllowFunc) (*Receiver, error) {
	conn, err := grpc.NewClient(upstreamAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, err
	}
	return &Receiver{
		stamper:       &tenantStamper{resolver: resolver, requireKey: requireKey, record: record, allow: allow},
		upstream:      conn,
		traceClient:   coltracepb.NewTraceServiceClient(conn),
		metricsClient: colmetricspb.NewMetricsServiceClient(conn),
		logsClient:    collogspb.NewLogsServiceClient(conn),
	}, nil
}

// Start binds listenAddr and serves the OTLP trace/metrics/logs services in a
// background goroutine. Returns once the listener is bound (or an error if the
// bind fails), so startup ordering is deterministic.
func (r *Receiver) Start(listenAddr string) error {
	lis, err := net.Listen("tcp", listenAddr)
	if err != nil {
		return err
	}
	r.grpcServer = grpc.NewServer()
	coltracepb.RegisterTraceServiceServer(r.grpcServer, &traceServer{stamper: r.stamper, up: r.traceClient})
	colmetricspb.RegisterMetricsServiceServer(r.grpcServer, &metricsServer{stamper: r.stamper, up: r.metricsClient})
	collogspb.RegisterLogsServiceServer(r.grpcServer, &logsServer{stamper: r.stamper, up: r.logsClient})

	go func() {
		log.Printf("otlp: in-process OTLP/gRPC receiver listening on %s → forwarding tenant-stamped telemetry to collector", listenAddr)
		if err := r.grpcServer.Serve(lis); err != nil && err != grpc.ErrServerStopped {
			log.Printf("otlp: gRPC serve error: %v", err)
		}
	}()
	return nil
}

// Stop gracefully drains in-flight exports and closes the upstream connection.
func (r *Receiver) Stop() {
	if r.grpcServer != nil {
		r.grpcServer.GracefulStop()
	}
	if r.upstream != nil {
		_ = r.upstream.Close()
	}
}
