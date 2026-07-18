package middleware

import (
	"context"
	"fmt"
	"net/http"

	"github.com/grafana/pyroscope-go"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	semconv "go.opentelemetry.io/otel/semconv/v1.21.0"
	"go.opentelemetry.io/otel/trace"
)

// Tracing is an HTTP middleware that:
//  1. Extracts an incoming W3C traceparent header (if present) so the span
//     becomes a child of the upstream caller's trace.
//  2. Starts a new server span for the request.
//  3. Injects the span context back into the response as a Traceparent header
//     so clients can correlate their own traces.
//
// serviceName is used as the OTel instrumentation scope name.
func Tracing(serviceName string) func(http.Handler) http.Handler {
	tracer := otel.Tracer(serviceName)

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Extract upstream trace context from incoming headers.
			ctx := otel.GetTextMapPropagator().Extract(r.Context(), propagation.HeaderCarrier(r.Header))

			// Start a server span.
			spanName := r.Method + " " + r.URL.Path
			ctx, span := tracer.Start(ctx, spanName,
				trace.WithSpanKind(trace.SpanKindServer),
				trace.WithAttributes(
					semconv.HTTPMethod(r.Method),
					semconv.HTTPURL(r.URL.String()),
					attribute.String("http.route", r.URL.Path),
					attribute.String("net.peer.addr", r.RemoteAddr),
				),
			)
			defer span.End()

			// Propagate the span context downstream via response header.
			otel.GetTextMapPropagator().Inject(ctx, propagation.HeaderCarrier(w.Header()))

			// Wrap the ResponseWriter to capture the status code.
			rw := &tracingResponseWriter{ResponseWriter: w, status: http.StatusOK}

			// Tag CPU/memory profile samples taken during this request with the
			// span's trace_id/span_id (pprof labels - a no-op if Pyroscope isn't
			// running). This is what makes a slow span clickable through to its
			// own flame graph in the Continuous Profiler.
			sc := span.SpanContext()
			pyroscope.TagWrapper(ctx, pyroscope.Labels(
				"trace_id", sc.TraceID().String(),
				"span_id", sc.SpanID().String(),
				"http_route", r.Method+" "+r.URL.Path,
			), func(ctx context.Context) {
				next.ServeHTTP(rw, r.WithContext(ctx))
			})

			span.SetAttributes(semconv.HTTPStatusCode(rw.status))
			// Mark the root span as an error on server failures so error-rate metrics,
			// Error Tracking, and RED dashboards derived from ClickHouse/StatusCode see it.
			if rw.status >= 500 {
				span.SetStatus(codes.Error, fmt.Sprintf("HTTP %d", rw.status))
			}
		})
	}
}

type tracingResponseWriter struct {
	http.ResponseWriter
	status int
}

func (rw *tracingResponseWriter) WriteHeader(code int) {
	rw.status = code
	rw.ResponseWriter.WriteHeader(code)
}
