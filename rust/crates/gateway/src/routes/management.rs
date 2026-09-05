//! Management plane (`/api/v1/…`) — Control-Plane operations.
//!
//! Kept strictly separate from the OpenAI inference plane so the two never mix.
//! The core of this plane is **route-sync**: the Control Plane (a separate Go
//! process) tells the gateway which models are served and where, by upserting
//! and deleting entries in the shared `model -> route` table.
//!
//! ## Route-sync contract (authoritative)
//!
//! All route-sync calls require the shared secret in the
//! `X-Purser-Internal-Token` header (missing → `401`, wrong → `403`), enforced
//! by the [`ControlPlaneAuth`] extractor.
//!
//! * `PUT /api/v1/routes` — body:
//!   ```json
//!   {
//!     "model_id": "llama-3-8b",
//!     "endpoint": "http://host:port",
//!     "deployment_id": "dep-123",
//!     "quantization": "Q4_K_M",
//!     "state": "active" | "draining" | "stopped"
//!   }
//!   ```
//!   `active`/`draining` upsert the route; `stopped` removes it.
//! * `DELETE /api/v1/routes/{model_id}` — removes the route (idempotent).

use axum::extract::{Path, State};
use axum::routing::{delete, get, put};
use axum::{Json, Router};
use serde::Deserialize;
use serde_json::{json, Value};

use crate::auth::ControlPlaneAuth;
use crate::error::ApiError;
use crate::state::{AppState, ModelRoute, RouteState};

/// Routes under `/api/v1`.
pub fn router() -> Router<AppState> {
    Router::new()
        .route("/", get(index))
        .route("/routes", put(put_route))
        .route("/routes/{model_id}", delete(delete_route))
}

/// `GET /api/v1` — plane descriptor.
///
/// Node/deployment listings are **not** served here: that data lives in the
/// Control Plane, which exposes its own `/api/v1/nodes`. The gateway's
/// management plane is limited to route-sync.
async fn index() -> Json<Value> {
    Json(json!({
        "plane": "management",
        "status": "ready",
        "endpoints": ["PUT /api/v1/routes", "DELETE /api/v1/routes/{model_id}"],
    }))
}

/// Body of `PUT /api/v1/routes` — the route-sync contract.
#[derive(Debug, Deserialize)]
struct RouteUpsert {
    model_id: String,
    #[serde(default)]
    endpoint: String,
    #[serde(default)]
    deployment_id: String,
    #[serde(default)]
    quantization: String,
    state: String,
}

/// `PUT /api/v1/routes` — upsert (`active`/`draining`) or remove (`stopped`) a
/// route. Requires the `X-Purser-Internal-Token` header.
async fn put_route(
    State(state): State<AppState>,
    _auth: ControlPlaneAuth,
    Json(req): Json<RouteUpsert>,
) -> Result<Json<Value>, ApiError> {
    if req.model_id.trim().is_empty() {
        return Err(ApiError::BadRequest {
            message: "`model_id` must not be empty.".to_string(),
            code: Some("invalid_route".to_string()),
        });
    }

    match req.state.as_str() {
        "active" | "draining" => {
            if req.endpoint.trim().is_empty() {
                return Err(ApiError::BadRequest {
                    message: "`endpoint` is required for active/draining routes.".to_string(),
                    code: Some("invalid_route".to_string()),
                });
            }
            let route = ModelRoute {
                endpoint: req.endpoint.trim().to_string(),
                deployment_id: req.deployment_id,
                quantization: req.quantization,
                state: if req.state == "active" {
                    RouteState::Active
                } else {
                    RouteState::Draining
                },
            };
            state.insert_route(req.model_id.clone(), route).await;
            tracing::info!(model_id = %req.model_id, state = %req.state, "route upserted by control plane");
            Ok(Json(json!({
                "status": "ok",
                "model_id": req.model_id,
                "state": req.state,
            })))
        }
        "stopped" => {
            let removed = state.remove_route(&req.model_id).await;
            tracing::info!(model_id = %req.model_id, removed, "route stopped by control plane");
            Ok(Json(json!({
                "status": "ok",
                "model_id": req.model_id,
                "state": "stopped",
                "removed": removed,
            })))
        }
        other => Err(ApiError::BadRequest {
            message: format!("Invalid `state` '{other}'; expected active|draining|stopped."),
            code: Some("invalid_state".to_string()),
        }),
    }
}

/// `DELETE /api/v1/routes/{model_id}` — remove a route (idempotent). Requires
/// the `X-Purser-Internal-Token` header.
async fn delete_route(
    State(state): State<AppState>,
    _auth: ControlPlaneAuth,
    Path(model_id): Path<String>,
) -> Json<Value> {
    let removed = state.remove_route(&model_id).await;
    tracing::info!(model_id = %model_id, removed, "route deleted by control plane");
    Json(json!({
        "status": "ok",
        "model_id": model_id,
        "removed": removed,
    }))
}
