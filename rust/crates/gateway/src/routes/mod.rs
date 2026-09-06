//! HTTP surface of the gateway.
//!
//! Three planes are kept deliberately separate so they never bleed into each
//! other:
//!
//! * **Inference plane** under `/v1/…` — OpenAI-compatible, what clients hit.
//! * **Anthropic plane** under `/v1/messages` — Anthropic Messages API
//!   compatibility; accepts `x-api-key` auth and returns Anthropic-format JSON
//!   or SSE. Both planes share the same model routing table.
//! * **Management plane** under `/api/v1/…` — route-sync driven by the Control
//!   Plane (`PUT`/`DELETE /api/v1/routes`). Node/deployment listings live in
//!   the Control Plane, not here.
//!
//! Liveness/readiness probes live at the root (`/healthz`, `/readyz`).

pub mod anthropic;
pub mod health;
pub mod inference;
pub mod management;

use axum::routing::get;
use axum::Router;

use crate::metrics::metrics_endpoint;
use crate::state::AppState;

/// Build the full application router with shared [`AppState`] applied.
pub fn app(state: AppState) -> Router {
    Router::new()
        .merge(health::router())
        .route("/metrics", get(metrics_endpoint))
        .nest("/v1", inference::router())
        .nest("/v1", anthropic::router())
        .nest("/api/v1", management::router())
        .with_state(state)
}
