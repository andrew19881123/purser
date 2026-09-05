//! OpenTelemetry initialisation for the Purser API Gateway.
//!
//! When `OTEL_EXPORTER_OTLP_ENDPOINT` is set, a gRPC OTLP trace exporter is
//! installed as a `tracing-opentelemetry` layer on top of the existing JSON
//! log subscriber.  All `tracing::info_span!` / `tracing::instrument` spans in
//! the gateway are automatically exported to the configured OTEL collector
//! (Dynatrace, Grafana Tempo, Datadog, …) **and** still written to JSON stdout.
//!
//! When the env var is absent the function is a fast no-op: zero extra
//! dependencies are activated and there is no overhead.

use std::env;

use opentelemetry::trace::TracerProvider as _;
use opentelemetry_otlp::WithExportConfig as _;
use opentelemetry_sdk::{runtime::Tokio, trace as sdktrace};
use tracing_subscriber::{layer::SubscriberExt as _, util::SubscriberInitExt as _, EnvFilter};

/// A guard that shuts down the OTEL global trace provider on drop.
/// Keep it alive for the full lifetime of the process.
pub struct Guard(bool);

impl Drop for Guard {
    fn drop(&mut self) {
        if self.0 {
            opentelemetry::global::shutdown_tracer_provider();
        }
    }
}

/// Initialise the global tracing subscriber and, when
/// `OTEL_EXPORTER_OTLP_ENDPOINT` is set, install an OTLP/gRPC span exporter.
///
/// Returns a [`Guard`] that must be kept alive until process exit so pending
/// spans are flushed by the `Drop` impl.
///
/// # Panics
///
/// Panics if a global subscriber has already been installed (i.e. called twice).
pub fn init() -> Guard {
    let filter = EnvFilter::try_from_default_env().unwrap_or_else(|_| EnvFilter::new("info"));

    let fmt_layer = tracing_subscriber::fmt::layer()
        .json()
        .with_current_span(true)
        .with_span_list(false);

    let endpoint = env::var("OTEL_EXPORTER_OTLP_ENDPOINT")
        .ok()
        .filter(|s| !s.is_empty());

    if let Some(ref ep) = endpoint {
        match build_provider(ep) {
            Ok(provider) => {
                let tracer = provider.tracer("purser.gateway");
                let otel_layer = tracing_opentelemetry::layer().with_tracer(tracer);

                // Install the global provider so spans can be flushed on drop.
                opentelemetry::global::set_tracer_provider(provider);

                tracing_subscriber::registry()
                    .with(filter)
                    .with(fmt_layer)
                    .with(otel_layer)
                    .init();

                return Guard(true);
            }
            Err(e) => {
                // Fall through to plain JSON subscriber; log the error after
                // the subscriber is up so it appears in the structured log.
                eprintln!(
                    "purser-gateway: OTEL init failed (OTLP endpoint={ep}): {e}; \
                     continuing without OTEL"
                );
            }
        }
    }

    // No OTEL endpoint (or init failed) — plain JSON subscriber only.
    tracing_subscriber::registry()
        .with(filter)
        .with(fmt_layer)
        .init();

    Guard(false)
}

fn build_provider(
    endpoint: &str,
) -> Result<sdktrace::TracerProvider, opentelemetry::trace::TraceError> {
    let svc_name = env::var("OTEL_SERVICE_NAME")
        .unwrap_or_else(|_| "purser-gateway".to_string());

    let resource = opentelemetry_sdk::Resource::new(vec![
        opentelemetry::KeyValue::new("service.name", svc_name),
    ]);

    let exporter = opentelemetry_otlp::new_exporter()
        .tonic()
        .with_endpoint(endpoint)
        .build_span_exporter()?;

    Ok(sdktrace::TracerProvider::builder()
        .with_batch_exporter(exporter, Tokio)
        .with_config(sdktrace::config().with_resource(resource))
        .build())
}
