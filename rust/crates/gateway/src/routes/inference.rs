//! OpenAI-compatible inference plane (`/v1/…`).
//!
//! `POST /v1/chat/completions` and `/v1/completions` are authenticated, quota-
//! checked, then **reverse-proxied** to the deployment host resolved from the
//! routing table. The client's raw request body is forwarded unchanged (so all
//! OpenAI parameters — temperature, max_tokens, tools, … — pass through), and
//! the host's response is relayed:
//!
//! * `stream: true` → the host's SSE token stream is piped to the client with
//!   minimal buffering (chunk-by-chunk), bounded by an idle timeout.
//! * `stream: false` → the full JSON body is buffered and returned.
//!
//! Failure mapping: unknown model → `404` (with the served list); host down →
//! `503`; time-to-first-byte timeout → `504`; mid-stream idle timeout → the
//! stream is closed after a trailing error frame (partial output preserved).
//!
//! `GET /v1/models` reflects the currently **active** routes.

use std::sync::Arc;
use std::time::Instant;

use axum::body::{Body, Bytes};
use axum::extract::State;
use axum::http::header::CONTENT_TYPE;
use axum::response::{Json, Response};
use axum::routing::{get, post};
use axum::Router;
use futures_util::StreamExt;
use serde::Deserialize;

use crate::auth::ApiKey;
use crate::error::ApiError;
use crate::metrics::record_request;
use crate::openai::{gen_id, unix_now, ModelList, ModelObject};
use crate::state::{AppState, OWNED_BY};
use crate::upstream::{count_sse_tokens, json_completion_tokens};

// ---------------------------------------------------------------------------
// Usage reporting helpers
// ---------------------------------------------------------------------------

/// Estimate input tokens from the `messages` array in the request body by
/// counting whitespace-split words in each message's `content` field and
/// dividing by 4 (≈ 4 chars/token). Falls back to a whole-body estimate on
/// parse failure.
fn estimate_input_tokens(body: &[u8]) -> u64 {
    let fallback = approx_prompt_tokens(body) / 4;
    let json: serde_json::Value = match serde_json::from_slice(body) {
        Ok(v) => v,
        Err(_) => return fallback,
    };
    let messages = match json.get("messages").and_then(|m| m.as_array()) {
        Some(arr) => arr,
        None => return fallback,
    };
    let mut word_count: u64 = 0;
    for msg in messages {
        if let Some(content) = msg.get("content").and_then(|c| c.as_str()) {
            word_count += content.split_whitespace().count() as u64;
        }
    }
    word_count / 4
}

/// Fire-and-forget: POST token usage to the Control Plane.
/// Errors are logged at debug level and otherwise ignored so they never
/// affect the inference response path.
fn spawn_usage_report(
    client: reqwest::Client,
    cp_url: Arc<String>,
    internal_token: Option<String>,
    api_key_id: String,
    model_id: String,
    input_tokens: u64,
    output_tokens: u64,
) {
    tokio::spawn(async move {
        let url = format!("{}/api/v1/usage", cp_url.trim_end_matches('/'));
        let body = serde_json::json!({
            "api_key_id": api_key_id,
            "model_id":   model_id,
            "input_tokens":  input_tokens,
            "output_tokens": output_tokens,
        });
        let mut builder = client.post(&url).json(&body);
        if let Some(tok) = internal_token.as_deref() {
            builder = builder.header("X-Purser-Internal-Token", tok);
        }
        if let Err(e) = builder.send().await {
            tracing::debug!(error = %e, "usage report to control plane failed (fire-and-forget)");
        }
    });
}

/// Routes under `/v1`.
pub fn router() -> Router<AppState> {
    Router::new()
        .route("/chat/completions", post(chat_completions))
        .route("/completions", post(completions))
        .route("/models", get(models))
}

/// Minimal projection of an inference request: enough to route and decide
/// streaming. All other fields are forwarded verbatim to the host.
#[derive(Debug, Deserialize)]
struct RoutedRequest {
    model: String,
    #[serde(default)]
    stream: bool,
}

// ---------------------------------------------------------------------------
// Handlers
// ---------------------------------------------------------------------------

async fn chat_completions(
    State(state): State<AppState>,
    api_key: ApiKey,
    body: Bytes,
) -> Result<Response, ApiError> {
    proxy_inference(&state, &api_key, body, "/v1/chat/completions").await
}

async fn completions(
    State(state): State<AppState>,
    api_key: ApiKey,
    body: Bytes,
) -> Result<Response, ApiError> {
    proxy_inference(&state, &api_key, body, "/v1/completions").await
}

async fn models(State(state): State<AppState>) -> Json<ModelList> {
    let created = unix_now();
    let ids = state.active_model_ids().await;
    let data = ids
        .into_iter()
        .map(|id| ModelObject {
            id,
            object: "model",
            created,
            owned_by: OWNED_BY.to_string(),
        })
        .collect();
    Json(ModelList {
        object: "list",
        data,
    })
}

// ---------------------------------------------------------------------------
// Proxy core
// ---------------------------------------------------------------------------

async fn proxy_inference(
    state: &AppState,
    api_key: &ApiKey,
    body: Bytes,
    upstream_path: &'static str,
) -> Result<Response, ApiError> {
    let session_id = gen_id("sess");

    let routed: RoutedRequest =
        serde_json::from_slice(&body).map_err(|e| ApiError::BadRequest {
            message: format!("Invalid JSON request body: {e}"),
            code: Some("invalid_body".to_string()),
        })?;
    let model = routed.model;
    let want_stream = routed.stream;

    // Resolve the host (404 if unknown, 503 if draining).
    let route = state.resolve_active(&model).await?;

    // Admission: quota / rate-limit / backpressure (429 on rejection).
    let prompt_tokens = approx_prompt_tokens(&body);
    let guard = state
        .limiter
        .acquire(&api_key.id, &state.quota, prompt_tokens)?;

    // Estimate input tokens for usage accounting (messages content ÷ 4).
    let input_tokens_for_usage = estimate_input_tokens(&body);

    // Capture usage-reporting context before moving `body` into the proxy.
    let cp_url = state.control_plane_url.clone();
    let cp_token = state.auth.internal_token.clone();

    let url = format!("{}{}", route.endpoint.trim_end_matches('/'), upstream_path);
    let start = Instant::now();

    tracing::info!(
        session_id = %session_id,
        api_key_id = %api_key.id,
        tenant = %api_key.tenant,
        model = %model,
        deployment_id = %route.deployment_id,
        stream = want_stream,
        "routing inference request to deployment host"
    );

    let resp = match state.http.send_json(&url, body).await {
        Ok(resp) => resp,
        Err(err) => {
            // guard drops here, releasing the admission slot.
            record_request(
                &model,
                &api_key.tenant,
                err.status_code().as_u16(),
                start.elapsed().as_secs_f64(),
                prompt_tokens,
                0,
            );
            tracing::warn!(session_id = %session_id, model = %model, error = %err, "upstream request failed");
            return Err(err);
        }
    };

    let status = resp.status();
    let upstream_ct = resp
        .headers()
        .get(CONTENT_TYPE)
        .and_then(|v| v.to_str().ok())
        .map(str::to_owned);

    if want_stream {
        Ok(stream_response(
            state,
            api_key,
            &model,
            session_id,
            start,
            prompt_tokens,
            input_tokens_for_usage,
            cp_url,
            cp_token,
            status,
            upstream_ct,
            resp,
            guard,
        ))
    } else {
        buffered_response(
            state,
            api_key,
            &model,
            start,
            prompt_tokens,
            input_tokens_for_usage,
            cp_url,
            cp_token,
            status,
            upstream_ct,
            resp,
            guard,
        )
        .await
    }
}

/// Pipe the host's SSE stream to the client with minimal buffering. The
/// admission `guard` is moved into the stream so the concurrency slot is held
/// until the last token is delivered.
#[allow(clippy::too_many_arguments)]
fn stream_response(
    state: &AppState,
    api_key: &ApiKey,
    model: &str,
    session_id: String,
    start: Instant,
    prompt_tokens: u64,
    input_tokens_for_usage: u64,
    cp_url: Option<Arc<String>>,
    cp_token: Option<String>,
    status: reqwest::StatusCode,
    upstream_ct: Option<String>,
    resp: reqwest::Response,
    guard: crate::quota::RequestGuard,
) -> Response {
    let idle = state.http.idle;
    let limiter = Arc::clone(&state.limiter);
    let http_client = state.http.client.clone();
    let key_id = api_key.id.clone();
    let tenant = api_key.tenant.clone();
    let model = model.to_owned();
    let upstream = resp.bytes_stream();

    let body_stream = async_stream::stream! {
        // Held for the whole stream; dropped at the end → releases the slot.
        let _guard = guard;
        tokio::pin!(upstream);
        let mut out_tokens: u64 = 0;

        loop {
            match tokio::time::timeout(idle, upstream.next()).await {
                Err(_elapsed) => {
                    tracing::warn!(
                        session_id = %session_id, model = %model,
                        "upstream idle timeout mid-stream; closing with partial output"
                    );
                    yield Ok::<Bytes, std::io::Error>(Bytes::from_static(
                        b"data: {\"error\":{\"message\":\"upstream timed out mid-stream\",\"type\":\"api_error\",\"code\":\"timeout\"}}\n\n",
                    ));
                    break;
                }
                Ok(None) => break,
                Ok(Some(Ok(chunk))) => {
                    out_tokens += count_sse_tokens(&chunk);
                    yield Ok(chunk);
                }
                Ok(Some(Err(err))) => {
                    tracing::warn!(session_id = %session_id, error = %err, "upstream stream error");
                    break;
                }
            }
        }

        limiter.charge_tokens(&key_id, out_tokens);
        record_request(
            &model,
            &tenant,
            status.as_u16(),
            start.elapsed().as_secs_f64(),
            prompt_tokens,
            out_tokens,
        );
        // Fire-and-forget usage report to the Control Plane.
        if let Some(url) = cp_url {
            spawn_usage_report(
                http_client,
                url,
                cp_token,
                key_id.clone(),
                model.clone(),
                input_tokens_for_usage,
                out_tokens,
            );
        }
    };

    let content_type = upstream_ct.unwrap_or_else(|| "text/event-stream".to_string());
    let mut response = Response::new(Body::from_stream(body_stream));
    *response.status_mut() = to_axum_status(status);
    if let Ok(value) = content_type.parse() {
        response.headers_mut().insert(CONTENT_TYPE, value);
    }
    response
}

/// Buffer and relay a non-streaming JSON response.
#[allow(clippy::too_many_arguments)]
async fn buffered_response(
    state: &AppState,
    api_key: &ApiKey,
    model: &str,
    start: Instant,
    prompt_tokens: u64,
    input_tokens_for_usage: u64,
    cp_url: Option<Arc<String>>,
    cp_token: Option<String>,
    status: reqwest::StatusCode,
    upstream_ct: Option<String>,
    resp: reqwest::Response,
    guard: crate::quota::RequestGuard,
) -> Result<Response, ApiError> {
    let bytes = match tokio::time::timeout(state.http.idle, resp.bytes()).await {
        Err(_elapsed) => {
            drop(guard);
            record_request(
                model,
                &api_key.tenant,
                504,
                start.elapsed().as_secs_f64(),
                prompt_tokens,
                0,
            );
            return Err(ApiError::Timeout(
                "The deployment host timed out while sending the response.".to_string(),
            ));
        }
        Ok(Err(err)) => {
            drop(guard);
            record_request(
                model,
                &api_key.tenant,
                503,
                start.elapsed().as_secs_f64(),
                prompt_tokens,
                0,
            );
            return Err(ApiError::NodeUnavailable(format!(
                "The deployment host failed while sending the response: {err}"
            )));
        }
        Ok(Ok(bytes)) => bytes,
    };

    let out_tokens = json_completion_tokens(&bytes);
    state.limiter.charge_tokens(&api_key.id, out_tokens);
    record_request(
        model,
        &api_key.tenant,
        status.as_u16(),
        start.elapsed().as_secs_f64(),
        prompt_tokens,
        out_tokens,
    );
    // Fire-and-forget usage report to the Control Plane.
    if let Some(url) = cp_url {
        spawn_usage_report(
            state.http.client.clone(),
            url,
            cp_token,
            api_key.id.clone(),
            model.to_owned(),
            input_tokens_for_usage,
            out_tokens,
        );
    }
    drop(guard);

    let content_type = upstream_ct.unwrap_or_else(|| "application/json".to_string());
    let mut response = Response::new(Body::from(bytes));
    *response.status_mut() = to_axum_status(status);
    if let Ok(value) = content_type.parse() {
        response.headers_mut().insert(CONTENT_TYPE, value);
    }
    Ok(response)
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

/// Approximate prompt tokens from the raw request body (whitespace words). A
/// rough proxy used for quota charging and metrics, not exact tokenization.
fn approx_prompt_tokens(body: &[u8]) -> u64 {
    std::str::from_utf8(body)
        .map(|s| s.split_whitespace().count() as u64)
        .unwrap_or(0)
}

/// Bridge a `reqwest`/`http` 1.x status into axum's `http` status. Both are the
/// same `http` crate version in this workspace, but convert defensively.
fn to_axum_status(status: reqwest::StatusCode) -> axum::http::StatusCode {
    axum::http::StatusCode::from_u16(status.as_u16()).unwrap_or(axum::http::StatusCode::OK)
}
