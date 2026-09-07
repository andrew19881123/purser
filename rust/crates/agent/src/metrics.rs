//! Prometheus observability for the Purser agent.
//!
//! Exposes [`EngineMetrics`] (from the supervised engine) as Prometheus gauges
//! at `GET /metrics` on a dedicated port (default 9091, overridable via
//! `PURSER_AGENT_METRICS_PORT`). The endpoint is unauthenticated — expose it
//! only on a trusted network or scrape-path in production.
//!
//! Metrics exposed:
//! * `purser_node_decode_tokens_per_second{node_id}` — decode throughput gauge.
//! * `purser_node_prefill_tokens_per_second{node_id}` — prefill throughput gauge.
//! * `purser_node_vram_used_gb{node_id}` — VRAM consumption in GiB.
//! * `purser_node_queue_depth{node_id}` — current inference queue depth.
//! * `purser_node_inference_port_alive{node_id}` — 1.0 = engine serving, 0.0 = down.
//! * `purser_node_kv_cache_usage_ratio{node_id}` — KV-cache hit ratio (stub: 0.0).
//! * `purser_node_gpu_utilization{node_id}` — GPU utilization 0..1 (stub: 0.0).

use std::sync::OnceLock;

use axum::http::header::CONTENT_TYPE;
use axum::response::{IntoResponse, Response};
use metrics_exporter_prometheus::{PrometheusBuilder, PrometheusHandle};
use purser_proto::v1::EngineMetrics;

/// Install (once) the global Prometheus recorder and return a render handle.
///
/// Safe to call from multiple places: the recorder is installed exactly once
/// behind a [`OnceLock`]; later calls simply clone the stored handle.
/// Metric descriptions are registered immediately after the recorder is
/// installed so `# HELP` lines always appear in the exposition output.
pub fn prometheus_handle() -> PrometheusHandle {
    static HANDLE: OnceLock<PrometheusHandle> = OnceLock::new();
    HANDLE
        .get_or_init(|| {
            let handle = PrometheusBuilder::new()
                .install_recorder()
                .expect("failed to install Prometheus recorder");
            describe_agent_metrics();
            handle
        })
        .clone()
}

/// Register `# HELP` descriptions for all agent node metrics.
/// Called once from [`prometheus_handle`].
pub fn describe_agent_metrics() {
    metrics::describe_gauge!(
        "purser_node_decode_tokens_per_second",
        "Decode (auto-regressive generation) throughput in tokens per second for this node."
    );
    metrics::describe_gauge!(
        "purser_node_prefill_tokens_per_second",
        "Prefill (prompt-processing) throughput in tokens per second for this node."
    );
    metrics::describe_gauge!(
        "purser_node_vram_used_gb",
        "VRAM / GPU memory currently consumed by the engine, in GiB."
    );
    metrics::describe_gauge!(
        "purser_node_queue_depth",
        "Current number of inference requests in the engine's request queue."
    );
    metrics::describe_gauge!(
        "purser_node_inference_port_alive",
        "1.0 when the engine is in the RUNNING phase (serving requests), 0.0 otherwise."
    );
    metrics::describe_gauge!(
        "purser_node_kv_cache_usage_ratio",
        "KV-cache hit ratio (0.0–1.0). \
         Stub: always 0.0 — hardware KV-cache sampling not yet implemented."
    );
    metrics::describe_gauge!(
        "purser_node_gpu_utilization",
        "GPU SM utilization ratio (0.0–1.0). \
         Stub: always 0.0 — GPU utilization requires NVML on real hardware; \
         stub until hardware available."
    );
}

/// Push the latest [`EngineMetrics`] snapshot into the Prometheus gauges.
///
/// `node_id` becomes the `node_id` label on every gauge.  Pass `None` for
/// `engine_metrics` when no metrics are available yet (all numeric gauges will
/// be set to 0.0).  `alive` should be `true` when `supervisor.engine_node_state()
/// == NodeState::Running`.
pub fn update_engine_metrics(node_id: &str, engine_metrics: Option<&EngineMetrics>, alive: bool) {
    let m = engine_metrics.cloned().unwrap_or_default();

    metrics::gauge!(
        "purser_node_decode_tokens_per_second",
        "node_id" => node_id.to_owned(),
    )
    .set(m.decode_tok_s);

    metrics::gauge!(
        "purser_node_prefill_tokens_per_second",
        "node_id" => node_id.to_owned(),
    )
    .set(m.prefill_tok_s);

    metrics::gauge!(
        "purser_node_vram_used_gb",
        "node_id" => node_id.to_owned(),
    )
    .set(m.vram_used_gb);

    metrics::gauge!(
        "purser_node_queue_depth",
        "node_id" => node_id.to_owned(),
    )
    .set(m.queue_depth as f64);

    metrics::gauge!(
        "purser_node_inference_port_alive",
        "node_id" => node_id.to_owned(),
    )
    .set(if alive { 1.0_f64 } else { 0.0_f64 });

    // GPU utilization requires NVML on real hardware; stub until hardware available.
    metrics::gauge!(
        "purser_node_gpu_utilization",
        "node_id" => node_id.to_owned(),
    )
    .set(0.0_f64);

    // KV-cache hit ratio: hardware sampling not yet implemented; stub until available.
    metrics::gauge!(
        "purser_node_kv_cache_usage_ratio",
        "node_id" => node_id.to_owned(),
    )
    .set(0.0_f64);
}

/// `GET /metrics` — Prometheus text-exposition format.
///
/// Unauthenticated, as Prometheus scrapers expect. Expose only on a trusted
/// network or a dedicated scrape-only port in production.
pub async fn metrics_handler() -> Response {
    let body = prometheus_handle().render();
    (
        [(CONTENT_TYPE, "text/plain; version=0.0.4; charset=utf-8")],
        body,
    )
        .into_response()
}

#[cfg(test)]
mod tests {
    use super::*;
    use axum::body::Body;
    use axum::http::{Request, StatusCode};
    use axum::routing::get;
    use axum::Router;
    use http_body_util::BodyExt;
    use tower::ServiceExt;

    /// Smoke-test: the `/metrics` endpoint returns HTTP 200, a `text/plain`
    /// content-type, and a body that contains the agent gauge metric names.
    #[tokio::test]
    async fn test_metrics_endpoint_returns_prometheus_text() {
        // Install the recorder (idempotent) and push a synthetic metrics sample.
        let _handle = prometheus_handle();
        let engine_metrics = EngineMetrics {
            decode_tok_s: 55.0,
            prefill_tok_s: 200.0,
            vram_used_gb: 10.0,
            queue_depth: 2,
            ..Default::default()
        };
        update_engine_metrics("obs2-test-node", Some(&engine_metrics), true);

        let app: Router = Router::new().route("/metrics", get(metrics_handler));

        let response = app
            .oneshot(
                Request::builder()
                    .uri("/metrics")
                    .body(Body::empty())
                    .unwrap(),
            )
            .await
            .unwrap();

        assert_eq!(
            response.status(),
            StatusCode::OK,
            "GET /metrics must return 200"
        );

        let content_type = response
            .headers()
            .get("content-type")
            .and_then(|v| v.to_str().ok())
            .unwrap_or("");
        assert!(
            content_type.starts_with("text/plain"),
            "content-type must be text/plain, got {content_type:?}"
        );

        let body_bytes = response.into_body().collect().await.unwrap().to_bytes();
        let body = std::str::from_utf8(&body_bytes).unwrap();

        assert!(
            body.contains("purser_node_decode_tokens_per_second"),
            "body must contain purser_node_decode_tokens_per_second; got:\n{body}"
        );
        assert!(
            body.contains("purser_node_vram_used_gb"),
            "body must contain purser_node_vram_used_gb; got:\n{body}"
        );
        assert!(
            body.contains("purser_node_inference_port_alive"),
            "body must contain purser_node_inference_port_alive; got:\n{body}"
        );
    }
}
