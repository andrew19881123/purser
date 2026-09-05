//! OpenAI-style error mapping.
//!
//! Every error the gateway returns to a client is shaped like OpenAI's:
//! ```json
//! { "error": { "message": "...", "type": "...", "code": "..." } }
//! ```
//! so existing clients parse failures the same way they already do. [`ApiError`]
//! is a single reusable enum that carries the HTTP status, the OpenAI `type`,
//! an optional machine `code`, and — for 429 — the `Retry-After` header.
//!
//! Errors are meant to be *actionable*: the message tells the client what to
//! do (retry later, pick another model, check the key), not just a bare code.

use axum::http::{header, HeaderName, HeaderValue, StatusCode};
use axum::response::{IntoResponse, Response};
use axum::Json;
use serde::Serialize;

/// The `{ "error": { .. } }` envelope.
#[derive(Debug, Serialize)]
pub struct ErrorEnvelope {
    pub error: ErrorPayload,
}

/// The inner OpenAI error object.
#[derive(Debug, Serialize)]
pub struct ErrorPayload {
    pub message: String,
    #[serde(rename = "type")]
    pub error_type: String,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub param: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub code: Option<String>,
}

/// All error conditions the gateway can surface, mapped 1:1 onto the HTTP
/// statuses called out in the API-gateway design doc.
#[derive(Debug, Clone)]
pub enum ApiError {
    /// 400 — malformed request the client can fix.
    BadRequest {
        message: String,
        code: Option<String>,
    },
    /// 401 — missing/invalid API key.
    Unauthorized(String),
    /// 403 — authenticated but not permitted.
    Forbidden(String),
    /// 404 — requested model is not served; carries the available list so the
    /// client can pick a valid one.
    ModelNotFound {
        model: String,
        available: Vec<String>,
    },
    /// 429 — quota exceeded, cluster saturated (backpressure), or per-model
    /// request queue full. Sets `Retry-After`; optionally sets
    /// `X-Queue-Position` when the rejection is from the per-model semaphore.
    RateLimited {
        message: String,
        retry_after_secs: u64,
        /// Position in the notional per-model queue (set for queue-full 429s).
        queue_position: Option<usize>,
    },
    /// 503 — model is not deployed, deployment failed, or a pipeline node is
    /// down. Sets `Retry-After: 30` so LiteLLM and other retrying callers back
    /// off before a follow-up attempt.
    NodeUnavailable(String),
    /// 504 — generation timed out.
    Timeout(String),
    /// 500 — unexpected internal failure.
    Internal(String),
}

impl ApiError {
    /// Convenience constructor for a 404 with the served-model list.
    pub fn model_not_found(model: impl Into<String>, available: Vec<String>) -> Self {
        ApiError::ModelNotFound {
            model: model.into(),
            available,
        }
    }

    /// The HTTP status this error maps to (also used for metrics labelling).
    pub fn status_code(&self) -> StatusCode {
        match self {
            ApiError::BadRequest { .. } => StatusCode::BAD_REQUEST,
            ApiError::Unauthorized(_) => StatusCode::UNAUTHORIZED,
            ApiError::Forbidden(_) => StatusCode::FORBIDDEN,
            ApiError::ModelNotFound { .. } => StatusCode::NOT_FOUND,
            ApiError::RateLimited { .. } => StatusCode::TOO_MANY_REQUESTS,
            ApiError::NodeUnavailable(_) => StatusCode::SERVICE_UNAVAILABLE,
            ApiError::Timeout(_) => StatusCode::GATEWAY_TIMEOUT,
            ApiError::Internal(_) => StatusCode::INTERNAL_SERVER_ERROR,
        }
    }
}

impl std::fmt::Display for ApiError {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        write!(f, "{:?}", self)
    }
}

impl std::error::Error for ApiError {}

/// The pieces needed to render a response, produced by matching an [`ApiError`].
struct Rendered {
    status: StatusCode,
    error_type: &'static str,
    code: Option<String>,
    message: String,
    retry_after_secs: Option<u64>,
    /// When set, the `X-Queue-Position` header is added to the response.
    queue_position: Option<usize>,
}

impl ApiError {
    fn render(self) -> Rendered {
        match self {
            ApiError::BadRequest { message, code } => Rendered {
                status: StatusCode::BAD_REQUEST,
                error_type: "invalid_request_error",
                code,
                message,
                retry_after_secs: None,
                queue_position: None,
            },
            ApiError::Unauthorized(message) => Rendered {
                status: StatusCode::UNAUTHORIZED,
                error_type: "authentication_error",
                code: Some("invalid_api_key".to_string()),
                message,
                retry_after_secs: None,
                queue_position: None,
            },
            ApiError::Forbidden(message) => Rendered {
                status: StatusCode::FORBIDDEN,
                error_type: "permission_error",
                code: Some("insufficient_permissions".to_string()),
                message,
                retry_after_secs: None,
                queue_position: None,
            },
            ApiError::ModelNotFound { model, available } => {
                let list = if available.is_empty() {
                    "none".to_string()
                } else {
                    available.join(", ")
                };
                Rendered {
                    status: StatusCode::NOT_FOUND,
                    error_type: "invalid_request_error",
                    code: Some("model_not_found".to_string()),
                    message: format!(
                        "The model '{model}' does not exist or is not currently served by \
                         this cluster. Available models: {list}."
                    ),
                    retry_after_secs: None,
                    queue_position: None,
                }
            }
            ApiError::RateLimited {
                message,
                retry_after_secs,
                queue_position,
            } => Rendered {
                status: StatusCode::TOO_MANY_REQUESTS,
                error_type: "rate_limit_error",
                code: Some("rate_limit_exceeded".to_string()),
                message,
                retry_after_secs: Some(retry_after_secs),
                queue_position,
            },
            ApiError::NodeUnavailable(message) => Rendered {
                status: StatusCode::SERVICE_UNAVAILABLE,
                // Use the OpenAI-compatible service_unavailable type so
                // LiteLLM and other callers can classify this correctly.
                error_type: "service_unavailable",
                code: Some("node_unavailable".to_string()),
                message,
                // Signal to retrying callers to back off 30 s before
                // re-attempting — appropriate for both missing routes and
                // temporarily-down deployment hosts.
                retry_after_secs: Some(30),
                queue_position: None,
            },
            ApiError::Timeout(message) => Rendered {
                status: StatusCode::GATEWAY_TIMEOUT,
                error_type: "api_error",
                code: Some("timeout".to_string()),
                message,
                retry_after_secs: None,
                queue_position: None,
            },
            ApiError::Internal(message) => Rendered {
                status: StatusCode::INTERNAL_SERVER_ERROR,
                error_type: "api_error",
                code: None,
                message,
                retry_after_secs: None,
                queue_position: None,
            },
        }
    }
}

impl IntoResponse for ApiError {
    fn into_response(self) -> Response {
        let Rendered {
            status,
            error_type,
            code,
            message,
            retry_after_secs,
            queue_position,
        } = self.render();

        if status.is_server_error() {
            tracing::warn!(status = status.as_u16(), message = %message, "gateway error");
        }

        let body = ErrorEnvelope {
            error: ErrorPayload {
                message,
                error_type: error_type.to_string(),
                param: None,
                code,
            },
        };

        let mut response = (status, Json(body)).into_response();
        if let Some(secs) = retry_after_secs {
            response
                .headers_mut()
                .insert(header::RETRY_AFTER, HeaderValue::from(secs));
        }
        if let Some(pos) = queue_position {
            if let Ok(value) = HeaderValue::from_str(&pos.to_string()) {
                response.headers_mut().insert(
                    HeaderName::from_static("x-queue-position"),
                    value,
                );
            }
        }
        response
    }
}
