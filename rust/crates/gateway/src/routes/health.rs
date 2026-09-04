//! Liveness and readiness probes for monitoring/orchestration.

use axum::extract::State;
use axum::routing::get;
use axum::{Json, Router};
use serde_json::json;

use crate::state::AppState;

/// `GET /healthz` — liveness. The process is up and serving.
async fn healthz() -> &'static str {
    "ok"
}

/// `GET /readyz` — readiness. Always `200` (the process is up and able to
/// serve the management plane), with a count of active vs. total routes so
/// orchestrators can distinguish "ready but idle" from "actively serving".
async fn readyz(State(state): State<AppState>) -> Json<serde_json::Value> {
    let (active, total) = state.route_counts().await;
    Json(json!({
        "status": "ready",
        "active_routes": active,
        "total_routes": total,
    }))
}

/// Root-level probe routes.
pub fn router() -> Router<AppState> {
    Router::new()
        .route("/healthz", get(healthz))
        .route("/readyz", get(readyz))
}
