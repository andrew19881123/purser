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

use std::sync::{Arc, LazyLock};
use std::time::Instant;

use axum::body::{Body, Bytes};
use axum::extract::{DefaultBodyLimit, State};
use axum::http::header::CONTENT_TYPE;
use axum::response::{Json, Response};
use axum::routing::{get, post};
use axum::Router;
use futures_util::StreamExt;
use serde::Deserialize;
use tokio::sync::{OwnedSemaphorePermit, Semaphore};
use tracing::Instrument as _;

use crate::auth::ApiKey;
use crate::error::ApiError;
use crate::metrics::record_request;
use crate::openai::{gen_id, unix_now, ModelList, ModelObject};
use crate::state::{AppState, OWNED_BY};
use crate::upstream::{count_sse_tokens, json_completion_tokens};

// ---------------------------------------------------------------------------
// Global bounded semaphore for fire-and-forget usage reports (Fix M3).
// Caps the number of concurrent reporting tasks so a slow Control Plane cannot
// grow an unbounded task list; exceeding the limit silently drops the report
// (usage accounting is best-effort, not transactional).
// pub(crate) so the Anthropic path (anthropic.rs) shares the same semaphore
// rather than having a separate, uncoordinated limit.
pub(crate) static USAGE_SEMAPHORE: LazyLock<Arc<Semaphore>> =
    LazyLock::new(|| Arc::new(Semaphore::new(256)));

// Global bounded semaphore for inference audit emits — same rationale as the
// usage semaphore: a slow Control Plane must not grow an unbounded task list.
static AUDIT_SEMAPHORE: LazyLock<Arc<Semaphore>> = LazyLock::new(|| Arc::new(Semaphore::new(256)));

// ---------------------------------------------------------------------------
// Inference audit helpers
// ---------------------------------------------------------------------------

/// Payload for `POST /api/v1/inference-events` on the Control Plane.
/// Serialised as JSON; field names mirror the Go `InferenceEvent` struct.
#[derive(serde::Serialize)]
struct InferenceEventPayload {
    request_id: String,
    api_key_hash: String,
    model_id: String,
    tenant_id: String,
    /// RFC 3339 UTC timestamp.
    timestamp: String,
    prompt_tokens: i64,
    completion_tokens: i64,
    /// Inference protocol: `"openai"` | `"anthropic"` | `"embeddings"`.
    endpoint: String,
    /// CIDR `/24` prefix of the caller's IP — never the full address.
    client_ip_prefix: String,
    latency_ms: f32,
    /// `"stop"`, `"length"`, or `"error"`.
    finish_reason: String,
}

/// Format the current UTC instant as an RFC 3339 string (`YYYY-MM-DDTHH:MM:SSZ`).
///
/// Uses Howard Hinnant's civil-calendar algorithm to avoid a `chrono` dependency.
fn rfc3339_now() -> String {
    use std::time::{SystemTime, UNIX_EPOCH};
    let secs = SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .unwrap_or_default()
        .as_secs();
    // Civil calendar from Unix days (Howard Hinnant's algorithm).
    let z = (secs / 86_400) as i64 + 719_468;
    let era = if z >= 0 { z } else { z - 146_096 } / 146_097;
    let doe = z - era * 146_097;
    let yoe = (doe - doe / 1_460 + doe / 36_524 - doe / 146_096) / 365;
    let y = yoe + era * 400;
    let doy = doe - (365 * yoe + yoe / 4 - yoe / 100);
    let mp = (5 * doy + 2) / 153;
    let d = doy - (153 * mp + 2) / 5 + 1;
    let m = if mp < 10 { mp + 3 } else { mp - 9 };
    let year = y + if m <= 2 { 1 } else { 0 };
    let tod = secs % 86_400;
    let h = tod / 3_600;
    let mn = (tod % 3_600) / 60;
    let s = tod % 60;
    format!("{year:04}-{m:02}-{d:02}T{h:02}:{mn:02}:{s:02}Z")
}

/// Fire-and-forget: POST an inference audit event to the Control Plane.
///
/// Bounded by [`AUDIT_SEMAPHORE`]: at most 256 tasks in-flight simultaneously.
/// Failures are logged at debug level and never propagated — the inference
/// response is already delivered to the client.
fn emit_inference_event(
    client: reqwest::Client,
    cp_url: Arc<String>,
    internal_token: Option<String>,
    event: InferenceEventPayload,
) {
    match AUDIT_SEMAPHORE.clone().try_acquire_owned() {
        Ok(permit) => {
            tokio::spawn(async move {
                let _permit = permit;
                let url = format!("{}/api/v1/inference-events", cp_url.trim_end_matches('/'));
                let mut builder = client.post(&url).json(&event);
                if let Some(tok) = internal_token.as_deref() {
                    builder = builder.header("X-Purser-Internal-Token", tok);
                }
                if let Err(e) = builder.send().await {
                    tracing::debug!(
                        error = %e,
                        "inference audit emit failed (non-fatal)"
                    );
                }
            });
        }
        Err(_) => {
            tracing::debug!(
                model = event.model_id,
                "audit semaphore full; dropping inference audit event"
            );
        }
    }
}

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
///
/// Bounded by [`USAGE_SEMAPHORE`]: at most 256 reporting tasks can be
/// in-flight simultaneously. When the semaphore is exhausted the report is
/// dropped with a debug log rather than spawning another task (usage
/// accounting is best-effort, not transactional).
fn spawn_usage_report(
    client: reqwest::Client,
    cp_url: Arc<String>,
    internal_token: Option<String>,
    api_key_id: String,
    model_id: String,
    input_tokens: u64,
    output_tokens: u64,
) {
    match USAGE_SEMAPHORE.clone().try_acquire_owned() {
        Ok(permit) => {
            tokio::spawn(async move {
                let _permit = permit; // released when the task completes
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
        Err(_) => {
            tracing::debug!("usage semaphore full; dropping usage report for model {model_id}");
        }
    }
}

/// Routes under `/v1`.
///
/// A 4 MB body-size cap is applied to the POST inference endpoints. `GET
/// /v1/models` has no body so the cap does not restrict it.
pub fn router() -> Router<AppState> {
    Router::new()
        .route("/chat/completions", post(chat_completions))
        .route("/completions", post(completions))
        .route("/embeddings", post(embeddings))
        .layer(DefaultBodyLimit::max(4 * 1024 * 1024)) // 4 MB cap (Fix H1)
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
    // The `model.id` field is filled in inside proxy_inference once the body
    // is parsed. When tracing-opentelemetry is active this span is exported to
    // the configured OTEL collector as a root span for the inference call.
    let span = tracing::info_span!(
        "purser.gateway.inference",
        "http.route" = "/v1/chat/completions",
        "model.id" = tracing::field::Empty,
    );
    proxy_inference(&state, &api_key, body, "/v1/chat/completions")
        .instrument(span)
        .await
}

async fn completions(
    State(state): State<AppState>,
    api_key: ApiKey,
    body: Bytes,
) -> Result<Response, ApiError> {
    let span = tracing::info_span!(
        "purser.gateway.inference",
        "http.route" = "/v1/completions",
        "model.id" = tracing::field::Empty,
    );
    proxy_inference(&state, &api_key, body, "/v1/completions")
        .instrument(span)
        .await
}

/// `POST /v1/embeddings` — OpenAI-compatible embedding inference.
///
/// Authenticated and quota-checked identically to the chat/completions path.
/// Embeddings are always buffered (no SSE): the upstream's JSON response is
/// read to completion and relayed to the client.
///
/// Expected request shape:
/// ```json
/// {"model": "my-embed-model", "input": "text" | ["text", ...], "encoding_format": "float"}
/// ```
///
/// Expected response shape (mirroring the upstream):
/// ```json
/// {"object":"list","data":[{"object":"embedding","embedding":[...],"index":0}],
///  "model":"my-embed-model","usage":{"prompt_tokens":5,"total_tokens":5}}
/// ```
async fn embeddings(
    State(state): State<AppState>,
    api_key: ApiKey,
    body: Bytes,
) -> Result<Response, ApiError> {
    proxy_inference(&state, &api_key, body, "/v1/embeddings").await
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

    // Record the model id on the enclosing span so it is visible in OTEL
    // traces. When no OTEL provider is configured this is a no-op.
    tracing::Span::current().record("model.id", model.as_str());

    // Resolve the host (404 if unknown, 503 if draining).
    let route = state.resolve_active(&model).await?;

    // Per-model concurrency gate: reject immediately if the model's in-flight
    // slot limit is reached (429 Too Many Requests + Retry-After: 5).
    let queue_permit = state.queue.try_acquire(&model).map_err(|pos| {
        tracing::warn!(
            model = %model,
            queue_position = pos,
            "per-model queue full; shedding request with 429"
        );
        ApiError::RateLimited {
            message: format!("Model '{model}' request queue is full; retry after a short delay."),
            retry_after_secs: 5,
            queue_position: Some(pos),
        }
    })?;

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
            upstream_path,
            start,
            prompt_tokens,
            input_tokens_for_usage,
            cp_url,
            cp_token,
            status,
            upstream_ct,
            resp,
            guard,
            queue_permit,
        ))
    } else {
        buffered_response(
            state,
            api_key,
            &model,
            session_id,
            upstream_path,
            start,
            prompt_tokens,
            input_tokens_for_usage,
            cp_url,
            cp_token,
            status,
            upstream_ct,
            resp,
            guard,
            queue_permit,
        )
        .await
    }
}

/// Pipe the host's SSE stream to the client with minimal buffering. The
/// admission `guard` and the per-model `queue_permit` are moved into the
/// stream so both concurrency slots are held until the last token is delivered.
#[allow(clippy::too_many_arguments)]
fn stream_response(
    state: &AppState,
    api_key: &ApiKey,
    model: &str,
    session_id: String,
    endpoint: &'static str,
    start: Instant,
    prompt_tokens: u64,
    input_tokens_for_usage: u64,
    cp_url: Option<Arc<String>>,
    cp_token: Option<String>,
    status: reqwest::StatusCode,
    upstream_ct: Option<String>,
    resp: reqwest::Response,
    guard: crate::quota::RequestGuard,
    queue_permit: OwnedSemaphorePermit,
) -> Response {
    let idle = state.http.idle;
    let limiter = Arc::clone(&state.limiter);
    let http_client = state.http.client.clone();
    let key_id = api_key.id.clone();
    let tenant = api_key.tenant.clone();
    let model = model.to_owned();
    let upstream = resp.bytes_stream();

    let body_stream = async_stream::stream! {
        // Both held for the whole stream; dropped at the end → releases slots.
        let _guard = guard;
        let _queue_permit = queue_permit;
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
                    tracing::warn!(session_id = %session_id, error = %err, "upstream stream failed mid-flight");
                    // Notify the client with a structured SSE error frame so it
                    // knows the stream ended abnormally, not cleanly (Fix M2).
                    yield Ok::<Bytes, std::io::Error>(Bytes::from_static(
                        b"data: {\"error\":{\"message\":\"upstream stream interrupted\",\"type\":\"api_error\",\"code\":\"upstream_error\"}}\n\n",
                    ));
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
        // Fire-and-forget usage report + inference audit to the Control Plane.
        if let Some(url) = cp_url {
            let audit_url = Arc::clone(&url);
            let audit_token = cp_token.clone();
            spawn_usage_report(
                http_client.clone(),
                url,
                cp_token,
                key_id.clone(),
                model.clone(),
                input_tokens_for_usage,
                out_tokens,
            );
            let finish_reason = if status.is_success() { "stop" } else { "error" };
            emit_inference_event(
                http_client,
                audit_url,
                audit_token,
                InferenceEventPayload {
                    request_id: session_id.clone(),
                    api_key_hash: key_id.clone(),
                    model_id: model.clone(),
                    tenant_id: tenant.clone(),
                    timestamp: rfc3339_now(),
                    prompt_tokens: input_tokens_for_usage as i64,
                    completion_tokens: out_tokens as i64,
                    endpoint: endpoint.to_string(),
                    client_ip_prefix: String::new(),
                    latency_ms: start.elapsed().as_secs_f32() * 1000.0,
                    finish_reason: finish_reason.to_string(),
                },
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
    request_id: String,
    endpoint: &'static str,
    start: Instant,
    prompt_tokens: u64,
    input_tokens_for_usage: u64,
    cp_url: Option<Arc<String>>,
    cp_token: Option<String>,
    status: reqwest::StatusCode,
    upstream_ct: Option<String>,
    resp: reqwest::Response,
    guard: crate::quota::RequestGuard,
    // Held for the duration of the buffered response; released on return.
    _queue_permit: OwnedSemaphorePermit,
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
            tracing::warn!(err = %err, model = %model, "upstream body read failed");
            drop(guard);
            record_request(
                model,
                &api_key.tenant,
                503,
                start.elapsed().as_secs_f64(),
                prompt_tokens,
                0,
            );
            return Err(ApiError::NodeUnavailable(
                "The inference backend failed while sending the response; retry shortly."
                    .to_string(),
            ));
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
    // Fire-and-forget usage report + inference audit to the Control Plane.
    if let Some(url) = cp_url {
        let audit_url = Arc::clone(&url);
        let audit_token = cp_token.clone();
        spawn_usage_report(
            state.http.client.clone(),
            url,
            cp_token,
            api_key.id.clone(),
            model.to_owned(),
            input_tokens_for_usage,
            out_tokens,
        );
        let finish_reason = if status.is_success() { "stop" } else { "error" };
        emit_inference_event(
            state.http.client.clone(),
            audit_url,
            audit_token,
            InferenceEventPayload {
                request_id,
                api_key_hash: api_key.id.clone(),
                model_id: model.to_owned(),
                tenant_id: api_key.tenant.clone(),
                timestamp: rfc3339_now(),
                prompt_tokens: input_tokens_for_usage as i64,
                completion_tokens: out_tokens as i64,
                endpoint: endpoint.to_string(),
                client_ip_prefix: String::new(),
                latency_ms: start.elapsed().as_secs_f32() * 1000.0,
                finish_reason: finish_reason.to_string(),
            },
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
