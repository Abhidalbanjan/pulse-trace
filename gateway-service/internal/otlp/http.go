package otlp

import (
	"bytes"
	"compress/gzip"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	collogspb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	colmetricspb "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	coltracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
)

// Signal identifies which OTLP signal an HTTP request carries.
type Signal int

const (
	SignalTraces Signal = iota
	SignalMetrics
	SignalLogs
)

func (s Signal) path() string {
	switch s {
	case SignalMetrics:
		return "/v1/metrics"
	case SignalLogs:
		return "/v1/logs"
	default:
		return "/v1/traces"
	}
}

// HTTPHandler terminates OTLP/HTTP in-process the same way the gRPC Receiver
// does: it stamps the caller's tenant (already resolved onto X-Tenant-ID by the
// gateway AuthMiddleware) onto every resource, then forwards the re-encoded
// payload to the upstream collector. Without this, HTTP-exported traces/metrics
// would reach ClickHouse with no tenant dimension, unlike the gRPC path.
type HTTPHandler struct {
	upstreamBase string // e.g. http://otel-collector:4318
	client       *http.Client
}

func NewHTTPHandler(upstreamBase string) *HTTPHandler {
	return &HTTPHandler{
		upstreamBase: strings.TrimRight(upstreamBase, "/"),
		client:       &http.Client{Timeout: 30 * time.Second},
	}
}

// Handler returns an http.HandlerFunc for the given signal.
func (h *HTTPHandler) Handler(signal Signal) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tenantID := r.Header.Get("X-Tenant-ID")
		if tenantID == "" {
			tenantID = defaultTenantID
		}
		tier := r.Header.Get("X-Tenant-Tier")
		if tier == "" {
			tier = defaultTenantTier
		}

		raw, err := readBody(r)
		if err != nil {
			http.Error(w, "failed to read body: "+err.Error(), http.StatusBadRequest)
			return
		}

		isJSON := strings.Contains(r.Header.Get("Content-Type"), "json")
		stamped, err := stampPayload(signal, raw, isJSON, tenantID, tier)
		if err != nil {
			// A malformed OTLP body is the client's error, not ours; don't forward.
			http.Error(w, "invalid OTLP payload: "+err.Error(), http.StatusBadRequest)
			return
		}

		contentType := "application/x-protobuf"
		if isJSON {
			contentType = "application/json"
		}
		h.forward(w, r, signal, stamped, contentType)
	}
}

// forward posts the re-encoded payload to the collector and copies its response
// back to the client (OTLP clients rely on the collector's status/body).
func (h *HTTPHandler) forward(w http.ResponseWriter, r *http.Request, signal Signal, body []byte, contentType string) {
	url := h.upstreamBase + signal.path()
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		http.Error(w, "failed to build upstream request", http.StatusInternalServerError)
		return
	}
	req.Header.Set("Content-Type", contentType)

	resp, err := h.client.Do(req)
	if err != nil {
		log.Printf("otlp/http: failed to forward %s to collector: %v", signal.path(), err)
		http.Error(w, "telemetry backend unavailable", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	if ct := resp.Header.Get("Content-Type"); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

// readBody reads and (if gzip-encoded) decompresses an OTLP/HTTP request body.
func readBody(r *http.Request) ([]byte, error) {
	reader := io.Reader(r.Body)
	if strings.EqualFold(r.Header.Get("Content-Encoding"), "gzip") {
		gz, err := gzip.NewReader(r.Body)
		if err != nil {
			return nil, err
		}
		defer gz.Close()
		reader = gz
	}
	return io.ReadAll(reader)
}

// stampPayload decodes an OTLP export request, stamps the tenant onto every
// resource, and re-encodes it in the same wire format (protobuf or JSON).
func stampPayload(signal Signal, data []byte, isJSON bool, tenantID, tier string) ([]byte, error) {
	switch signal {
	case SignalMetrics:
		req := &colmetricspb.ExportMetricsServiceRequest{}
		if err := decode(data, isJSON, req); err != nil {
			return nil, err
		}
		for _, rm := range req.GetResourceMetrics() {
			rm.Resource = stampResource(rm.GetResource(), tenantID, tier)
		}
		return encode(req, isJSON)
	case SignalLogs:
		req := &collogspb.ExportLogsServiceRequest{}
		if err := decode(data, isJSON, req); err != nil {
			return nil, err
		}
		for _, rl := range req.GetResourceLogs() {
			rl.Resource = stampResource(rl.GetResource(), tenantID, tier)
		}
		return encode(req, isJSON)
	default:
		req := &coltracepb.ExportTraceServiceRequest{}
		if err := decode(data, isJSON, req); err != nil {
			return nil, err
		}
		for _, rs := range req.GetResourceSpans() {
			rs.Resource = stampResource(rs.GetResource(), tenantID, tier)
		}
		return encode(req, isJSON)
	}
}

func decode(data []byte, isJSON bool, m protoreflect.ProtoMessage) error {
	if isJSON {
		return protojson.Unmarshal(data, m)
	}
	return proto.Unmarshal(data, m)
}

func encode(m protoreflect.ProtoMessage, isJSON bool) ([]byte, error) {
	if isJSON {
		return protojson.Marshal(m)
	}
	return proto.Marshal(m)
}
