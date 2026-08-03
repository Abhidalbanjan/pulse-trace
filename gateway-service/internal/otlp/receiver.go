package otlp

import (
	"crypto/tls"
	"log"
	"net"
	"strings"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
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
	conn, err := grpc.NewClient(normalizeGRPCTarget(upstreamAddr), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, err
	}
	logsClient := collogspb.NewLogsServiceClient(conn)
	return &Receiver{
		stamper:       &tenantStamper{resolver: resolver, requireKey: requireKey, record: record, allow: allow, logsUp: logsClient},
		upstream:      conn,
		traceClient:   coltracepb.NewTraceServiceClient(conn),
		metricsClient: colmetricspb.NewMetricsServiceClient(conn),
		logsClient:    logsClient,
	}, nil
}

// SetLogSink routes log exports (gRPC, and the migration ForwardLogs fallback) to
// fn instead of the upstream collector — the gateway wires this to publish logs to
// Kafka so OTLP-native logs land in the log explorer's Quickwit index. Must be
// called before Start. nil (the default) keeps forwarding logs to the collector.
func (r *Receiver) SetLogSink(fn LogSinkFunc) { r.stamper.logSink = fn }

// normalizeGRPCTarget makes a bare "host:port" safe for grpc.NewClient. Without
// a scheme, grpc parses "otel-collector:4317" as URI scheme "otel-collector"
// with opaque "4317", so its dns resolver resolves the wrong string and returns
// "produced zero addresses". Prefixing "dns:///" forces the dns resolver on the
// real host:port. Targets that already carry a scheme (dns:///, passthrough:///,
// unix://, an IP:port with a recognizable form) are left untouched.
func normalizeGRPCTarget(addr string) string {
	if addr == "" || strings.Contains(addr, "://") {
		return addr
	}
	return "dns:///" + addr
}

// Start binds listenAddr and serves the OTLP trace/metrics/logs services in a
// background goroutine. Returns once the listener is bound (or an error if the
// bind fails), so startup ordering is deterministic.
//
// tlsConfig (from BuildServerTLS) serves the receiver over TLS/mTLS so the
// ingestion key isn't sent in cleartext; nil keeps a plaintext listener and logs
// a warning, since that's only safe behind a TLS-terminating LB/ingress.
func (r *Receiver) Start(listenAddr string, tlsConfig *tls.Config) error {
	lis, err := net.Listen("tcp", listenAddr)
	if err != nil {
		return err
	}

	var opts []grpc.ServerOption
	if tlsConfig != nil {
		opts = append(opts, grpc.Creds(credentials.NewTLS(tlsConfig)))
		mtls := tlsConfig.ClientAuth == tls.RequireAndVerifyClientCert
		log.Printf("otlp: gRPC receiver TLS enabled (mTLS client-cert required: %v)", mtls)
	} else {
		log.Printf("otlp: WARNING — gRPC receiver on %s is plaintext; the ingestion key travels in the clear unless a TLS-terminating LB/ingress fronts it. Set OTLP_TLS_CERT_FILE/OTLP_TLS_KEY_FILE to serve TLS directly.", listenAddr)
	}
	r.grpcServer = grpc.NewServer(opts...)
	coltracepb.RegisterTraceServiceServer(r.grpcServer, &traceServer{stamper: r.stamper, up: r.traceClient})
	colmetricspb.RegisterMetricsServiceServer(r.grpcServer, &metricsServer{stamper: r.stamper, up: r.metricsClient})
	collogspb.RegisterLogsServiceServer(r.grpcServer, &logsServer{stamper: r.stamper})

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
