//! Prometheus observability.
//!
//! A single process-wide Prometheus recorder is installed lazily; the render
//! handle is stored in [`AppState`](crate::state::AppState) and exposed at
//! `GET /metrics`. Per-request metrics are labelled by `model` and `tenant`
//! (never by api-key secret or request content):
//!
//! * `purser_gateway_requests_total{model,tenant,status}` — request counter.
//! * `purser_gateway_request_duration_seconds{model,tenant}` — end-to-end
//!   latency histogram.
//! * `purser_gateway_tokens_input_total{model,tenant}` — prompt tokens.
//! * `purser_gateway_tokens_output_total{model,tenant}` — completion tokens.
//! * `purser_gateway_tokens_per_second{model,tenant}` — throughput histogram.

use std::sync::OnceLock;

use axum::extract::State;
use axum::http::header::CONTENT_TYPE;
use axum::response::{IntoResponse, Response};
use metrics_exporter_prometheus::{PrometheusBuilder, PrometheusHandle};

use crate::state::AppState;

/// Install (once) the global Prometheus recorder and return a render handle.
///
/// Safe to call repeatedly and from multiple test binaries: the recorder is
/// installed exactly once behind a `OnceLock`; later calls clone the handle.
pub fn prometheus_handle() -> PrometheusHandle {
    static HANDLE: OnceLock<PrometheusHandle> = OnceLock::new();
    HANDLE
        .get_or_init(|| {
            PrometheusBuilder::new()
                .install_recorder()
                .expect("failed to install Prometheus recorder")
        })
        .clone()
}

/// `GET /metrics` — Prometheus text exposition format. Unauthenticated, as
/// scrapers expect; expose it only on a trusted network/port in production.
pub async fn metrics_endpoint(State(state): State<AppState>) -> Response {
    let body = state.metrics.render();
    (
        [(CONTENT_TYPE, "text/plain; version=0.0.4; charset=utf-8")],
        body,
    )
        .into_response()
}

/// Record the per-request metrics after a request completes (success or error).
pub fn record_request(
    model: &str,
    tenant: &str,
    status: u16,
    latency_secs: f64,
    tokens_in: u64,
    tokens_out: u64,
) {
    metrics::counter!(
        "purser_gateway_requests_total",
        "model" => model.to_owned(),
        "tenant" => tenant.to_owned(),
        "status" => status.to_string(),
    )
    .increment(1);

    metrics::histogram!(
        "purser_gateway_request_duration_seconds",
        "model" => model.to_owned(),
        "tenant" => tenant.to_owned(),
    )
    .record(latency_secs);

    if tokens_in > 0 {
        metrics::counter!(
            "purser_gateway_tokens_input_total",
            "model" => model.to_owned(),
            "tenant" => tenant.to_owned(),
        )
        .increment(tokens_in);
    }

    if tokens_out > 0 {
        metrics::counter!(
            "purser_gateway_tokens_output_total",
            "model" => model.to_owned(),
            "tenant" => tenant.to_owned(),
        )
        .increment(tokens_out);

        if latency_secs > 0.0 {
            metrics::histogram!(
                "purser_gateway_tokens_per_second",
                "model" => model.to_owned(),
                "tenant" => tenant.to_owned(),
            )
            .record(tokens_out as f64 / latency_secs);
        }
    }
}
