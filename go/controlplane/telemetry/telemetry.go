// Package telemetry initialises OpenTelemetry signal providers for the
// Purser control-plane.
//
// Supported signals: traces and metrics, both exported over OTLP/HTTP.
// When OTEL_EXPORTER_OTLP_ENDPOINT is unset (the common case in development
// and air-gapped deployments) all providers stay as zero-overhead no-ops so
// there is no runtime cost.
package telemetry

import (
	"context"
	"os"
	"strconv"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
)

const defaultServiceName = "purser-control-plane"

// buildSampler selects a sdktrace.Sampler based on the standard
// OTEL_TRACES_SAMPLER and OTEL_TRACES_SAMPLER_ARG environment variables.
//
// Supported sampler names (case-sensitive, matching the OpenTelemetry spec):
//
//   - "always_off"               → NeverSample()
//   - "traceidratio"             → TraceIDRatioBased(ratio)
//   - "parentbased_traceidratio" → ParentBased(TraceIDRatioBased(ratio))
//   - "parentbased_always_off"   → ParentBased(NeverSample())
//   - "always_on" / "" (default) → AlwaysSample()
//
// When the sampler requires a ratio (traceidratio / parentbased_traceidratio)
// and OTEL_TRACES_SAMPLER_ARG cannot be parsed as a float64, the ratio
// defaults to 1.0 (100% sampling).
func buildSampler() sdktrace.Sampler {
	sampler := os.Getenv("OTEL_TRACES_SAMPLER")
	arg := os.Getenv("OTEL_TRACES_SAMPLER_ARG")
	switch sampler {
	case "always_off":
		return sdktrace.NeverSample()
	case "traceidratio":
		ratio, err := strconv.ParseFloat(arg, 64)
		if err != nil {
			ratio = 1.0
		}
		return sdktrace.TraceIDRatioBased(ratio)
	case "parentbased_traceidratio":
		ratio, err := strconv.ParseFloat(arg, 64)
		if err != nil {
			ratio = 1.0
		}
		return sdktrace.ParentBased(sdktrace.TraceIDRatioBased(ratio))
	case "parentbased_always_off":
		return sdktrace.ParentBased(sdktrace.NeverSample())
	default: // "always_on" or empty
		return sdktrace.AlwaysSample()
	}
}

// Init initialises global TracerProvider and MeterProvider from env vars:
//
//	OTEL_SERVICE_NAME            service.name resource attribute
//	                             (default: "purser-control-plane")
//	OTEL_EXPORTER_OTLP_ENDPOINT  OTLP/HTTP base URL, e.g. http://collector:4318
//	OTEL_EXPORTER_OTLP_HEADERS   optional header(s) for auth, e.g.
//	                             "Authorization=Api-Token dt0c01.xxx" (Dynatrace)
//
// If OTEL_EXPORTER_OTLP_ENDPOINT is empty the global providers remain as
// their built-in no-ops and a no-op shutdown function is returned: zero
// overhead, nothing phoned home.
//
// Returns a shutdown function that must be called on server stop to flush and
// close the exporters.
func Init(ctx context.Context) (shutdown func(context.Context) error, err error) {
	endpoint := os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT")
	if endpoint == "" {
		// No collector configured — leave global no-op providers in place.
		return func(context.Context) error { return nil }, nil
	}

	svcName := os.Getenv("OTEL_SERVICE_NAME")
	if svcName == "" {
		svcName = defaultServiceName
	}

	res, err := resource.New(ctx,
		resource.WithSchemaURL(semconv.SchemaURL),
		resource.WithAttributes(semconv.ServiceName(svcName)),
	)
	if err != nil {
		return nil, err
	}

	// --- Traces ---------------------------------------------------------------
	// OTLP/HTTP trace exporter. TLS follows the URL scheme: http:// is plain,
	// https:// uses the system certificate pool (or OTEL_EXPORTER_OTLP_CERTIFICATE).
	traceExp, err := otlptracehttp.New(ctx,
		otlptracehttp.WithEndpointURL(endpoint),
	)
	if err != nil {
		return nil, err
	}
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(traceExp),
		sdktrace.WithResource(res),
		sdktrace.WithSampler(buildSampler()),
	)
	otel.SetTracerProvider(tp)

	// --- Metrics --------------------------------------------------------------
	// OTLP/HTTP metric exporter. Metrics are pushed every 30 s.
	metricExp, err := otlpmetrichttp.New(ctx,
		otlpmetrichttp.WithEndpointURL(endpoint),
	)
	if err != nil {
		_ = tp.Shutdown(ctx)
		return nil, err
	}
	mp := sdkmetric.NewMeterProvider(
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(metricExp,
			sdkmetric.WithInterval(30*time.Second))),
		sdkmetric.WithResource(res),
	)
	otel.SetMeterProvider(mp)

	return func(ctx context.Context) error {
		err1 := tp.Shutdown(ctx)
		err2 := mp.Shutdown(ctx)
		if err1 != nil {
			return err1
		}
		return err2
	}, nil
}
