package telemetry

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.21.0"
	"go.opentelemetry.io/otel/trace"
)

// InitTracer sets up the global OTel TracerProvider and TextMapPropagator.
// It exports spans to the OTel Collector via gRPC (OTLP).
// Call the returned shutdown function in main() via defer.
func InitTracer(ctx context.Context, serviceName string) (trace.Tracer, func(context.Context) error, error) {
	endpoint := os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT")
	if endpoint == "" {
		endpoint = "otel-collector:4317"
	}

	exporter, err := otlptracegrpc.New(ctx,
		otlptracegrpc.WithEndpoint(endpoint),
		otlptracegrpc.WithInsecure(),
	)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create OTLP exporter: %w", err)
	}

	version := os.Getenv("SERVICE_VERSION")
	if version == "" {
		version = "dev"
	}

	res, err := resource.New(ctx,
		resource.WithAttributes(
			semconv.ServiceName(serviceName),
			semconv.ServiceVersion(version),
		),
	)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create OTel resource: %w", err)
	}

	sampler, pollCancel := resolveSampler(ctx, serviceName)

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(res),
		sdktrace.WithSampler(sampler),
	)

	// Register as the global provider so otel.Tracer() works anywhere.
	otel.SetTracerProvider(tp)

	// W3C Trace Context + Baggage propagation.
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	log.Printf("telemetry: OTel tracer initialised for service=%q endpoint=%s sampler=%s", serviceName, endpoint, sampler.Description())

	shutdown := func(ctx context.Context) error {
		if pollCancel != nil {
			pollCancel()
		}
		shutdownCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		return tp.Shutdown(shutdownCtx)
	}

	return tp.Tracer(serviceName), shutdown, nil
}

// resolveSampler picks the trace sampler for this service:
//  1. An explicit OTEL_TRACES_SAMPLER always wins (operator override).
//  2. Otherwise, if TOPOLOGY_SERVICE_URL is set, use a DynamicSampler that polls
//     topology-service's per-service agent-config and adjusts live (100% while
//     the service is DEGRADED/PREDICTIVE_WARNING, 1% baseline otherwise).
//  3. Otherwise, sample everything - the original dev-friendly default.
//
// In every case, the result is wrapped with withForceDrop so known-noisy endpoints
// (health checks, metrics scrapes) never get sampled at all, no matter which mode
// is active - a manual force-drop, independent of the tail_sampling retention
// filters that decide keep/drop for everything else.
func resolveSampler(ctx context.Context, serviceName string) (sdktrace.Sampler, context.CancelFunc) {
	if s, ok := staticSamplerFromEnv(); ok {
		return withForceDrop(s), nil
	}

	if topologyURL := os.Getenv("TOPOLOGY_SERVICE_URL"); topologyURL != "" {
		dynamic := NewDynamicSampler(1.0)
		pollCtx, cancel := context.WithCancel(context.Background())
		go pollAgentConfig(pollCtx, serviceName, topologyURL, dynamic)
		return withForceDrop(dynamic), cancel
	}

	return withForceDrop(sdktrace.AlwaysSample()), nil
}
