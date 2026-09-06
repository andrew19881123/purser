//! Prometheus observability.
//!
//! A single process-wide Prometheus recorder is installed lazily; the render
//! handle is stored in [`AppState`](crate::state::AppState) and exposed at
//! `GET /metrics`. Per-request metrics are labelled by `model` and `tenant`
//! (never by api-key secret or request content):
//!
//! **Core request metrics:**
//! * `purser_gateway_requests_total{model,tenant,status}` — request counter.
//! * `purser_gateway_request_duration_seconds{model,tenant}` — end-to-end
//!   latency histogram.
//! * `purser_gateway_tokens_input_total{model,tenant}` — prompt tokens.
//! * `purser_gateway_tokens_output_total{model,tenant}` — completion tokens.
//! * `purser_gateway_tokens_per_second{model,tenant}` — throughput histogram.
//!
//! **LLM-specific metrics (Obs-01):**
//! * `purser_gateway_time_to_first_token_seconds{model,tenant}` — TTFT
//!   histogram; p50/p99 are key SLO indicators.
//! * `purser_gateway_active_streams{model,tenant}` — gauge of concurrent SSE
//!   connections; decremented via RAII `StreamGauge` so no leaks on errors.
//! * `purser_gateway_errors_total{model,tenant,error_type}` — typed error
//!   counter; `error_type` ∈ {`timeout_upstream`, `node_unavailable`,
//!   `auth_failure`, `quota_exceeded`, `bad_request`, `rate_limited`}.
//! * `purser_gateway_queue_wait_seconds{model}` — per-model queue-wait
//!   histogram (always ~0 for the current try-acquire path, non-zero once
//!   blocking queues are introduced).
//! * `purser_gateway_model_queue_depth{model}` — gauge of in-flight requests
//!   currently holding a per-model semaphore permit.

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
/// Metric descriptions are registered immediately after the recorder is
/// installed so `# HELP` lines always appear in the exposition output.
pub fn prometheus_handle() -> PrometheusHandle {
    static HANDLE: OnceLock<PrometheusHandle> = OnceLock::new();
    HANDLE
        .get_or_init(|| {
            let handle = PrometheusBuilder::new()
                .install_recorder()
                .expect("failed to install Prometheus recorder");
            describe_gateway_metrics();
            handle
        })
        .clone()
}

/// Register `# HELP` descriptions for all gateway metrics with the global
/// Prometheus recorder. Called once from [`prometheus_handle`].
pub fn describe_gateway_metrics() {
    // Core request metrics.
    metrics::describe_counter!(
        "purser_gateway_requests_total",
        "Total inference requests by model, tenant, and HTTP status."
    );
    metrics::describe_histogram!(
        "purser_gateway_request_duration_seconds",
        "End-to-end request latency in seconds."
    );
    metrics::describe_counter!(
        "purser_gateway_tokens_input_total",
        "Prompt tokens consumed."
    );
    metrics::describe_counter!(
        "purser_gateway_tokens_output_total",
        "Completion tokens generated."
    );
    metrics::describe_histogram!(
        "purser_gateway_tokens_per_second",
        "Token generation throughput histogram (tokens/second)."
    );

    // LLM-specific metrics (Obs-01).
    metrics::describe_histogram!(
        "purser_gateway_time_to_first_token_seconds",
        "Time from request dispatch to first SSE token received (TTFT). \
         Use p50/p99 as primary SLO indicators."
    );
    metrics::describe_gauge!(
        "purser_gateway_active_streams",
        "Number of currently active SSE streaming connections. \
         Decremented via RAII guard — a non-zero value after all requests \
         complete indicates a resource leak."
    );
    metrics::describe_counter!(
        "purser_gateway_errors_total",
        "Total gateway errors broken down by error_type \
         (timeout_upstream, node_unavailable, auth_failure, quota_exceeded, \
         bad_request, rate_limited)."
    );
    metrics::describe_histogram!(
        "purser_gateway_queue_wait_seconds",
        "Time (seconds) a request spent waiting to acquire a per-model \
         semaphore slot before being dispatched upstream."
    );
    metrics::describe_gauge!(
        "purser_gateway_model_queue_depth",
        "Current number of in-flight requests holding a per-model semaphore \
         permit. Equals max_queue_depth minus available permits."
    );
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

// ---------------------------------------------------------------------------
// LLM-specific metric helpers (Obs-01)
// ---------------------------------------------------------------------------

/// Record a Time-to-First-Token sample for a streaming or buffered response.
///
/// For streaming responses `ttft_secs` is the wall-clock time from request
/// start to when the first non-empty SSE chunk was received.  For buffered
/// responses it is the time from request start to when the upstream response
/// headers arrived (≈ time-to-first-byte).
pub fn record_ttft(model: &str, tenant: &str, ttft_secs: f64) {
    metrics::histogram!(
        "purser_gateway_time_to_first_token_seconds",
        "model" => model.to_owned(),
        "tenant" => tenant.to_owned(),
    )
    .record(ttft_secs);
}

/// Increment the typed error counter.
///
/// `error_type` should be one of: `"timeout_upstream"`, `"node_unavailable"`,
/// `"auth_failure"`, `"quota_exceeded"`, `"bad_request"`, `"rate_limited"`.
pub fn record_error(model: &str, tenant: &str, error_type: &str) {
    metrics::counter!(
        "purser_gateway_errors_total",
        "model" => model.to_owned(),
        "tenant" => tenant.to_owned(),
        "error_type" => error_type.to_owned(),
    )
    .increment(1);
}

/// Record the time spent waiting for a per-model semaphore permit.
///
/// With the current non-blocking `try_acquire` this is always near zero;
/// the histogram is reserved for future blocking-queue variants.
pub fn record_queue_wait(model: &str, wait_secs: f64) {
    metrics::histogram!(
        "purser_gateway_queue_wait_seconds",
        "model" => model.to_owned(),
    )
    .record(wait_secs);
}

/// Update the per-model queue-depth gauge to `depth`.
///
/// Called immediately after a successful `try_acquire` so the gauge reflects
/// the number of currently in-flight requests (including the one just admitted).
pub fn record_queue_depth(model: &str, depth: f64) {
    metrics::gauge!(
        "purser_gateway_model_queue_depth",
        "model" => model.to_owned(),
    )
    .set(depth);
}
