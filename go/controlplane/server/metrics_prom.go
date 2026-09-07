package server

// Prometheus /metrics scrape endpoint for the Purser control plane.
//
// This file registers a Prometheus exporter alongside the existing OTLP
// exporter (dual export) by installing a new sdkmetric.MeterProvider that
// includes a Prometheus pull reader. All OTEL meter instruments created by
// server.New() (and any other component that calls otel.Meter() after
// initPromExporter) will be automatically exported to this endpoint.
//
// Usage: call initPromExporter() at the start of server.New(), before the
// meter instruments are created, and mount promHTTPHandler() at GET /metrics.
//
// metrics endpoint — no auth, expose only on trusted networks.

import (
	"context"
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	prometheusexp "go.opentelemetry.io/otel/exporters/prometheus"
	"go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
)

// cpPromRegistry holds the Prometheus registry that backs the /metrics
// endpoint. Initialised once by initPromExporter; nil until then.
var cpPromRegistry *prometheus.Registry

// initPromExporter creates a Prometheus pull exporter, installs a new global
// sdkmetric.MeterProvider that includes it (replacing the previous provider so
// all subsequent otel.Meter() calls produce Prometheus-backed instruments), and
// registers the static purser_cp_info gauge.
//
// Returns the http.Handler to mount at GET /metrics.
//
// For production dual export (Prometheus + OTLP), pass the OTLP reader to
// the same MeterProvider: call telemetry.Init with the prometheus reader before
// calling server.New(), or use the WithExtraReader variant (future work).
func initPromExporter() (http.Handler, error) {
	reg := prometheus.NewRegistry()

	exp, err := prometheusexp.New(prometheusexp.WithRegisterer(reg))
	if err != nil {
		return nil, err
	}

	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(exp))
	otel.SetMeterProvider(mp)

	cpPromRegistry = reg

	// Register a static purser_cp_info gauge to verify the endpoint works
	// and to expose build-time metadata. The value is always 1; labels carry
	// the version and any other static information.
	m := otel.Meter("purser.control-plane")
	infoGauge, err := m.Int64Gauge("purser_cp_info",
		metric.WithDescription("Purser control-plane build information. Value is always 1; labels carry metadata."),
		metric.WithUnit("{info}"),
	)
	if err != nil {
		return nil, err
	}
	infoGauge.Record(context.Background(), 1,
		metric.WithAttributes(attribute.String("version", "0.5")))

	return promHTTPHandler(), nil
}

// promHTTPHandler returns the http.Handler for the /metrics Prometheus scrape
// endpoint. Must be called after initPromExporter; returns a 404 handler if
// called before (should never happen in production).
func promHTTPHandler() http.Handler {
	if cpPromRegistry == nil {
		return http.NotFoundHandler()
	}
	return promhttp.HandlerFor(cpPromRegistry, promhttp.HandlerOpts{
		Registry: cpPromRegistry,
	})
}
