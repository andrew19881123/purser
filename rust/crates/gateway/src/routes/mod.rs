//! HTTP surface of the gateway.
//!
//! Two planes are kept deliberately separate so they never bleed into each
//! other:
//!
//! * **Inference plane** under `/v1/…` — OpenAI-compatible, what clients hit.
//! * **Management plane** under `/api/v1/…` — Control-Plane operations (nodes,
//!   deployments, metrics). Only a placeholder in this phase.
//!
//! Liveness/readiness probes live at the root (`/healthz`, `/readyz`).

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
        .nest("/api/v1", management::router())
        .with_state(state)
}
