//! Anthropic Messages API compatibility layer.
//!
//! `POST /v1/messages` accepts the Anthropic SDK wire format — including the
//! `x-api-key` header used by `@anthropic-ai/sdk` — and translates it to an
//! OpenAI-compatible request before forwarding to the upstream deployment host.
//! Responses are translated back to Anthropic format (SSE or JSON).
//!
//! This lets Claude Code, Cursor, and any tool built on `import Anthropic from
//! "@anthropic-ai/sdk"` point at Purser by changing only the `base_url`.

use std::sync::Arc;
use std::time::Instant;

use axum::body::{Body, Bytes};
use axum::extract::{DefaultBodyLimit, FromRef, FromRequestParts, State};
use axum::http::header::{AUTHORIZATION, CONTENT_TYPE};
use axum::http::request::Parts;
use axum::http::HeaderName;
use axum::response::Response;
use axum::routing::post;
use axum::Router;
use futures_util::StreamExt;
use serde::Deserialize;
use tokio::sync::OwnedSemaphorePermit;
use tracing::Instrument as _;

use crate::auth::ApiKey;
use crate::error::ApiError;
use crate::metrics::record_request;
use crate::openai::gen_id;
use crate::state::AppState;
use crate::upstream::count_sse_tokens;

// ---------------------------------------------------------------------------
// Auth extractor — accepts x-api-key OR Authorization: Bearer
// ---------------------------------------------------------------------------

/// Like [`ApiKey`] but also accepts the `x-api-key` header used by the
/// official Anthropic SDK (which does not send `Authorization: Bearer`).
struct AnthropicApiKey(ApiKey);

static X_API_KEY_HEADER: HeaderName = HeaderName::from_static("x-api-key");

impl<S> FromRequestParts<S> for AnthropicApiKey
where
    AppState: FromRef<S>,
    S: Send + Sync,
{
    type Rejection = Response;

    async fn from_request_parts(parts: &mut Parts, state: &S) -> Result<Self, Self::Rejection> {
        let app = AppState::from_ref(state);

        // 1) Try x-api-key (Anthropic SDK default).
        let token_opt = parts
            .headers
            .get(&X_API_KEY_HEADER)
            .and_then(|v| v.to_str().ok())
            .map(str::to_owned)
            .or_else(|| {
                // 2) Fall back to Authorization: Bearer <key>.
                parts
                    .headers
                    .get(AUTHORIZATION)
                    .and_then(|v| v.to_str().ok())
                    .and_then(|h| h.strip_prefix("Bearer ").map(str::to_owned))
            });

        let token = match token_opt {
            Some(t) => t,
            None => {
                return Err(anthropic_error_response(
                    401,
                    "authentication_error",
                    "Missing API key. Provide x-api-key header or Authorization: Bearer <key>.",
                ))
            }
        };

        app.auth.validate(&token).map(AnthropicApiKey).map_err(|e| {
            let msg = match e {
                ApiError::Unauthorized(m) => m,
                other => format!("{other:?}"),
            };
            anthropic_error_response(401, "authentication_error", &msg)
        })
    }
}

// ---------------------------------------------------------------------------
// Anthropic request types
// ---------------------------------------------------------------------------

/// A content block inside the `content` array of a message.
#[derive(Debug, Deserialize)]
#[serde(tag = "type")]
enum ContentBlock {
    #[serde(rename = "text")]
    Text { text: String },
    #[serde(other)]
    Unknown,
}

/// The `content` field of a message: either a plain string or an array of
/// typed content blocks (Anthropic allows both forms).
#[derive(Debug, Deserialize)]
#[serde(untagged)]
enum MessageContent {
    Text(String),
    Blocks(Vec<ContentBlock>),
}

impl MessageContent {
    /// Flatten to a plain string, concatenating all `text` blocks.
    fn to_text(&self) -> String {
        match self {
            MessageContent::Text(s) => s.clone(),
            MessageContent::Blocks(blocks) => blocks
                .iter()
                .filter_map(|b| match b {
                    ContentBlock::Text { text } => Some(text.as_str()),
                    ContentBlock::Unknown => None,
                })
                .collect::<Vec<_>>()
                .join(""),
        }
    }
}

#[derive(Debug, Deserialize)]
struct AnthropicMessage {
    role: String,
    content: MessageContent,
}

/// `POST /v1/messages` request body (Anthropic Messages API).
#[derive(Debug, Deserialize)]
struct MessagesRequest {
    model: String,
    messages: Vec<AnthropicMessage>,
    #[serde(default)]
    max_tokens: Option<u64>,
    #[serde(default)]
    system: Option<String>,
    #[serde(default)]
    stream: bool,
    #[serde(default)]
    temperature: Option<f64>,
}

// ---------------------------------------------------------------------------
// Router
// ---------------------------------------------------------------------------

/// Routes that expose the Anthropic Messages API surface. Mounted under `/v1`
/// so `POST /v1/messages` is the live URL (matching the SDK default).
pub fn router() -> Router<AppState> {
    Router::new()
        .route("/messages", post(messages_handler))
        .layer(DefaultBodyLimit::max(4 * 1024 * 1024))
}

// ---------------------------------------------------------------------------
// Handler
// ---------------------------------------------------------------------------

async fn messages_handler(
    State(state): State<AppState>,
    AnthropicApiKey(api_key): AnthropicApiKey,
    body: Bytes,
) -> Response {
    let span = tracing::info_span!(
        "purser.gateway.anthropic",
        "http.route" = "/v1/messages",
        "model.id" = tracing::field::Empty,
    );
    match proxy_messages(&state, &api_key, body)
        .instrument(span)
        .await
    {
        Ok(r) => r,
        Err(e) => api_error_to_anthropic(e),
    }
}

async fn proxy_messages(
    state: &AppState,
    api_key: &ApiKey,
    body: Bytes,
) -> Result<Response, ApiError> {
    // Parse the Anthropic request.
    let req: MessagesRequest = serde_json::from_slice(&body).map_err(|e| ApiError::BadRequest {
        message: format!("Invalid JSON request body: {e}"),
        code: Some("invalid_body".to_string()),
    })?;

    if req.model.is_empty() {
        return Err(ApiError::BadRequest {
            message: "model is required".to_string(),
            code: Some("invalid_request_error".to_string()),
        });
    }

    tracing::Span::current().record("model.id", req.model.as_str());

    // Translate to OpenAI format.
    let (openai_body_bytes, input_tokens_for_usage) =
        translate_to_openai(&req).map_err(|e| ApiError::BadRequest {
            message: format!("Request translation failed: {e}"),
            code: Some("invalid_request_error".to_string()),
        })?;

    let model = req.model.clone();
    let want_stream = req.stream;

    // Resolve route (503 if unknown).
    let route = state.resolve_active(&model).await?;

    // Per-model concurrency gate.
    let queue_permit = state.queue.try_acquire(&model).map_err(|pos| {
        tracing::warn!(model = %model, queue_position = pos, "per-model queue full");
        ApiError::RateLimited {
            message: format!("Model '{model}' request queue is full; retry after a short delay."),
            retry_after_secs: 5,
            queue_position: Some(pos),
        }
    })?;

    // Quota / rate-limit admission.
    let prompt_tokens = approx_tokens(body.as_ref());
    let guard = state
        .limiter
        .acquire(&api_key.id, &state.quota, prompt_tokens)?;

    let cp_url = state.control_plane_url.clone();
    let cp_token = state.auth.internal_token.clone();

    let url = format!(
        "{}/v1/chat/completions",
        route.endpoint.trim_end_matches('/')
    );
    let start = Instant::now();

    tracing::info!(
        api_key_id = %api_key.id,
        tenant = %api_key.tenant,
        model = %model,
        deployment_id = %route.deployment_id,
        stream = want_stream,
        "routing Anthropic /v1/messages to deployment host"
    );

    let resp = match state.http.send_json(&url, openai_body_bytes).await {
        Ok(r) => r,
        Err(err) => {
            drop(guard);
            record_request(
                &model,
                &api_key.tenant,
                err.status_code().as_u16(),
                start.elapsed().as_secs_f64(),
                prompt_tokens,
                0,
            );
            return Err(err);
        }
    };

    let upstream_status = resp.status();

    if want_stream {
        Ok(anthropic_stream_response(
            state,
            api_key,
            &model,
            start,
            prompt_tokens,
            input_tokens_for_usage,
            cp_url,
            cp_token,
            upstream_status,
            resp,
            guard,
            queue_permit,
        ))
    } else {
        anthropic_buffered_response(
            state,
            api_key,
            &model,
            start,
            prompt_tokens,
            input_tokens_for_usage,
            cp_url,
            cp_token,
            upstream_status,
            resp,
            guard,
            queue_permit,
        )
        .await
    }
}

// ---------------------------------------------------------------------------
// Request translation: Anthropic → OpenAI
// ---------------------------------------------------------------------------

/// Build an OpenAI `POST /v1/chat/completions` body from an Anthropic
/// `POST /v1/messages` request.  Returns the JSON bytes and an estimated
/// input-token count for usage accounting.
fn translate_to_openai(req: &MessagesRequest) -> Result<(Bytes, u64), String> {
    let mut messages: Vec<serde_json::Value> = Vec::new();

    if let Some(sys) = &req.system {
        if !sys.is_empty() {
            messages.push(serde_json::json!({
                "role": "system",
                "content": sys,
            }));
        }
    }

    let mut input_words: u64 = 0;
    for msg in &req.messages {
        let text = msg.content.to_text();
        input_words += text.split_whitespace().count() as u64;
        messages.push(serde_json::json!({
            "role": msg.role,
            "content": text,
        }));
    }
    if let Some(sys) = &req.system {
        input_words += sys.split_whitespace().count() as u64;
    }
    let input_tokens = input_words / 4;

    let mut body = serde_json::json!({
        "model":    req.model,
        "messages": messages,
        "stream":   req.stream,
    });

    if let Some(max_tokens) = req.max_tokens {
        body["max_tokens"] = serde_json::json!(max_tokens);
    }
    if let Some(temp) = req.temperature {
        body["temperature"] = serde_json::json!(temp);
    }

    let bytes = serde_json::to_vec(&body)
        .map_err(|e| e.to_string())
        .map(Bytes::from)?;

    Ok((bytes, input_tokens))
}

// ---------------------------------------------------------------------------
// Streaming response: OpenAI SSE → Anthropic SSE
// ---------------------------------------------------------------------------

#[allow(clippy::too_many_arguments)]
fn anthropic_stream_response(
    state: &AppState,
    api_key: &ApiKey,
    model: &str,
    start: Instant,
    prompt_tokens: u64,
    input_tokens_for_usage: u64,
    cp_url: Option<Arc<String>>,
    cp_token: Option<String>,
    upstream_status: reqwest::StatusCode,
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
    let msg_id = gen_id("msg");

    let body_stream = async_stream::stream! {
        let _guard = guard;
        let _queue_permit = queue_permit;
        tokio::pin!(upstream);

        let mut out_tokens: u64 = 0;
        let mut sent_preamble = false;
        let mut stream_ended = false;
        // Line accumulation buffer — chunks do not align with SSE frames.
        let mut buf = String::new();

        'outer: loop {
            match tokio::time::timeout(idle, upstream.next()).await {
                Err(_elapsed) => {
                    tracing::warn!(model = %model, "upstream idle timeout mid-stream (Anthropic)");
                    let err_frame = Bytes::from_static(
                        b"event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"api_error\",\"message\":\"upstream timed out mid-stream\"}}\n\n",
                    );
                    yield Ok::<Bytes, std::io::Error>(err_frame);
                    break;
                }
                Ok(None) => break,
                Ok(Some(Ok(chunk))) => {
                    out_tokens += count_sse_tokens(&chunk);
                    buf.push_str(&String::from_utf8_lossy(&chunk));

                    // Process all complete lines.
                    while let Some(nl) = buf.find('\n') {
                        let raw = buf[..nl].trim_end_matches('\r').to_owned();
                        buf = buf[nl + 1..].to_owned();

                        let data = match raw.strip_prefix("data:") {
                            Some(d) => d.trim().to_owned(),
                            None => continue, // event:, id:, comment lines
                        };

                        if data == "[DONE]" {
                            if !sent_preamble {
                                for ev in preamble_events(&msg_id, &model, input_tokens_for_usage) {
                                    yield Ok(ev);
                                }
                            }
                            if !stream_ended {
                                for ev in closing_events(out_tokens) {
                                    yield Ok(ev);
                                }
                                stream_ended = true;
                            }
                            break 'outer;
                        }

                        if let Ok(v) = serde_json::from_str::<serde_json::Value>(&data) {
                            if v.get("error").is_some() {
                                let err_frame = Bytes::from_static(
                                    b"event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"api_error\",\"message\":\"upstream error\"}}\n\n",
                                );
                                yield Ok(err_frame);
                                stream_ended = true;
                                break 'outer;
                            }

                            let content = v["choices"][0]["delta"]["content"]
                                .as_str()
                                .map(str::to_owned);
                            let finish_reason = v["choices"][0]["finish_reason"]
                                .as_str()
                                .map(str::to_owned);

                            if !sent_preamble {
                                for ev in preamble_events(&msg_id, &model, input_tokens_for_usage) {
                                    yield Ok(ev);
                                }
                                sent_preamble = true;
                            }

                            if let Some(text) = content {
                                if !text.is_empty() {
                                    yield Ok(content_delta_event(0, &text));
                                }
                            }

                            if finish_reason.as_deref().map(|r| !r.is_empty()).unwrap_or(false) {
                                let stop_reason = finish_reason
                                    .as_deref()
                                    .map(finish_to_stop_reason)
                                    .unwrap_or("end_turn");
                                if !stream_ended {
                                    for ev in closing_events_with_stop(out_tokens, stop_reason) {
                                        yield Ok(ev);
                                    }
                                    stream_ended = true;
                                }
                            }
                        }
                    }
                }
                Ok(Some(Err(err))) => {
                    tracing::warn!(error = %err, "upstream stream error (Anthropic)");
                    let err_frame = Bytes::from_static(
                        b"event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"api_error\",\"message\":\"upstream stream interrupted\"}}\n\n",
                    );
                    yield Ok::<Bytes, std::io::Error>(err_frame);
                    break;
                }
            }
        }

        // Ensure closing events are emitted if stream ended without explicit [DONE].
        if sent_preamble && !stream_ended {
            for ev in closing_events(out_tokens) {
                yield Ok(ev);
            }
        }

        limiter.charge_tokens(&key_id, out_tokens);
        record_request(
            &model,
            &tenant,
            upstream_status.as_u16(),
            start.elapsed().as_secs_f64(),
            prompt_tokens,
            out_tokens,
        );
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

    let mut response = Response::new(Body::from_stream(body_stream));
    *response.status_mut() = axum::http::StatusCode::OK;
    if let Ok(value) = "text/event-stream".parse() {
        response.headers_mut().insert(CONTENT_TYPE, value);
    }
    response
}

// ---------------------------------------------------------------------------
// Buffered (non-streaming) response: OpenAI JSON → Anthropic JSON
// ---------------------------------------------------------------------------

#[allow(clippy::too_many_arguments)]
async fn anthropic_buffered_response(
    state: &AppState,
    api_key: &ApiKey,
    model: &str,
    start: Instant,
    prompt_tokens: u64,
    input_tokens_for_usage: u64,
    cp_url: Option<Arc<String>>,
    cp_token: Option<String>,
    upstream_status: reqwest::StatusCode,
    resp: reqwest::Response,
    guard: crate::quota::RequestGuard,
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
            tracing::warn!(err = %err, model = %model, "upstream body read failed (Anthropic)");
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
        Ok(Ok(b)) => b,
    };

    let anthropic_json = openai_to_anthropic_json(&bytes, model, input_tokens_for_usage);
    let out_tokens = anthropic_json["usage"]["output_tokens"]
        .as_u64()
        .unwrap_or(0);

    state.limiter.charge_tokens(&api_key.id, out_tokens);
    record_request(
        model,
        &api_key.tenant,
        upstream_status.as_u16(),
        start.elapsed().as_secs_f64(),
        prompt_tokens,
        out_tokens,
    );
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

    let body_bytes = serde_json::to_vec(&anthropic_json).unwrap_or_else(|_| b"{}".to_vec());
    let mut response = Response::new(Body::from(body_bytes));
    *response.status_mut() = to_axum_status(upstream_status);
    if let Ok(value) = "application/json".parse() {
        response.headers_mut().insert(CONTENT_TYPE, value);
    }
    Ok(response)
}

// ---------------------------------------------------------------------------
// JSON translation helpers
// ---------------------------------------------------------------------------

fn openai_to_anthropic_json(bytes: &[u8], model: &str, input_tokens: u64) -> serde_json::Value {
    let msg_id = gen_id("msg");
    let v: serde_json::Value = serde_json::from_slice(bytes).unwrap_or_default();

    let text = v["choices"][0]["message"]["content"]
        .as_str()
        .unwrap_or("")
        .to_owned();

    let finish_reason = v["choices"][0]["finish_reason"].as_str().unwrap_or("stop");
    let stop_reason = finish_to_stop_reason(finish_reason);

    let out_tokens = v["usage"]["completion_tokens"].as_u64().unwrap_or(0);
    let in_tokens = v["usage"]["prompt_tokens"].as_u64().unwrap_or(input_tokens);

    serde_json::json!({
        "id":            format!("msg_{}", msg_id),
        "type":          "message",
        "role":          "assistant",
        "content":       [{"type": "text", "text": text}],
        "model":         model,
        "stop_reason":   stop_reason,
        "stop_sequence": null,
        "usage": {
            "input_tokens":  in_tokens,
            "output_tokens": out_tokens,
        },
    })
}

fn finish_to_stop_reason(r: &str) -> &'static str {
    match r {
        "max_tokens" | "length" => "max_tokens",
        _ => "end_turn",
    }
}

// ---------------------------------------------------------------------------
// SSE event builders
// ---------------------------------------------------------------------------

fn preamble_events(msg_id: &str, model: &str, input_tokens: u64) -> Vec<Bytes> {
    let msg_start = serde_json::json!({
        "type": "message_start",
        "message": {
            "id": format!("msg_{}", msg_id),
            "type": "message",
            "role": "assistant",
            "content": [],
            "model": model,
            "stop_reason": null,
            "stop_sequence": null,
            "usage": {"input_tokens": input_tokens, "output_tokens": 0},
        }
    });
    let cb_start = serde_json::json!({
        "type": "content_block_start",
        "index": 0,
        "content_block": {"type": "text", "text": ""},
    });
    vec![
        Bytes::from(format!("event: message_start\ndata: {}\n\n", msg_start)),
        Bytes::from(format!(
            "event: content_block_start\ndata: {}\n\n",
            cb_start
        )),
        Bytes::from_static(b"event: ping\ndata: {\"type\":\"ping\"}\n\n"),
    ]
}

fn content_delta_event(index: u32, text: &str) -> Bytes {
    let escaped = serde_json::to_string(text).unwrap_or_else(|_| "\"\"".to_string());
    Bytes::from(format!(
        "event: content_block_delta\ndata: {{\"type\":\"content_block_delta\",\"index\":{index},\"delta\":{{\"type\":\"text_delta\",\"text\":{escaped}}}}}\n\n"
    ))
}

fn closing_events(out_tokens: u64) -> Vec<Bytes> {
    closing_events_with_stop(out_tokens, "end_turn")
}

fn closing_events_with_stop(out_tokens: u64, stop_reason: &str) -> Vec<Bytes> {
    let msg_delta = serde_json::json!({
        "type": "message_delta",
        "delta": {"stop_reason": stop_reason, "stop_sequence": null},
        "usage": {"output_tokens": out_tokens},
    });
    vec![
        Bytes::from_static(
            b"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n",
        ),
        Bytes::from(format!("event: message_delta\ndata: {}\n\n", msg_delta)),
        Bytes::from_static(b"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"),
    ]
}

// ---------------------------------------------------------------------------
// Error helpers
// ---------------------------------------------------------------------------

/// Build an Anthropic-format error response.
///
/// The Anthropic error envelope is `{"type":"error","error":{"type":"...","message":"..."}}`,
/// distinct from the OpenAI `{"error":{"type":"...","message":"..."}}` shape.
pub(crate) fn anthropic_error_response(status: u16, error_type: &str, message: &str) -> Response {
    let body = serde_json::json!({
        "type": "error",
        "error": {
            "type": error_type,
            "message": message,
        }
    });
    let body_bytes = serde_json::to_vec(&body).unwrap_or_else(|_| b"{}".to_vec());
    Response::builder()
        .status(status)
        .header(CONTENT_TYPE, "application/json")
        .body(Body::from(body_bytes))
        .unwrap_or_else(|_| {
            let mut r = Response::new(Body::empty());
            *r.status_mut() = axum::http::StatusCode::INTERNAL_SERVER_ERROR;
            r
        })
}

fn api_error_to_anthropic(err: ApiError) -> Response {
    let (status, error_type, message) = match err {
        ApiError::Unauthorized(m) => (401u16, "authentication_error", m),
        ApiError::Forbidden(m) => (403, "permission_error", m),
        ApiError::BadRequest { message, .. } => (400, "invalid_request_error", message),
        ApiError::ModelNotFound { model, available } => {
            let list = if available.is_empty() {
                "none".to_string()
            } else {
                available.join(", ")
            };
            (
                404,
                "invalid_request_error",
                format!("Model '{model}' not found. Available: {list}."),
            )
        }
        ApiError::RateLimited { message, .. } => (429, "rate_limit_error", message),
        ApiError::NodeUnavailable(m) => (529, "overloaded_error", m),
        ApiError::Timeout(m) => (504, "api_error", m),
        ApiError::Internal(m) => (500, "api_error", m),
    };
    anthropic_error_response(status, error_type, &message)
}

// ---------------------------------------------------------------------------
// Misc helpers
// ---------------------------------------------------------------------------

/// Approximate token count from raw bytes (word count / 4).
fn approx_tokens(body: &[u8]) -> u64 {
    std::str::from_utf8(body)
        .map(|s| s.split_whitespace().count() as u64)
        .unwrap_or(0)
        / 4
}

fn to_axum_status(status: reqwest::StatusCode) -> axum::http::StatusCode {
    axum::http::StatusCode::from_u16(status.as_u16()).unwrap_or(axum::http::StatusCode::OK)
}

/// Fire-and-forget usage report to the Control Plane (mirrors inference.rs).
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
            tracing::debug!(error = %e, "Anthropic path: usage report failed (fire-and-forget)");
        }
    });
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

#[cfg(test)]
mod tests {
    use super::*;

    // ---- unit tests (no HTTP) -----------------------------------------------

    #[test]
    fn translate_to_openai_system_becomes_first_message() {
        let req = MessagesRequest {
            model: "test-model".to_string(),
            messages: vec![AnthropicMessage {
                role: "user".to_string(),
                content: MessageContent::Text("hello".to_string()),
            }],
            max_tokens: Some(512),
            system: Some("You are a helpful assistant.".to_string()),
            stream: false,
            temperature: None,
        };
        let (bytes, _) = translate_to_openai(&req).unwrap();
        let v: serde_json::Value = serde_json::from_slice(&bytes).unwrap();
        let messages = v["messages"].as_array().unwrap();
        assert_eq!(messages.len(), 2, "system + user");
        assert_eq!(messages[0]["role"], "system");
        assert_eq!(messages[0]["content"], "You are a helpful assistant.");
        assert_eq!(messages[1]["role"], "user");
        assert_eq!(messages[1]["content"], "hello");
    }

    #[test]
    fn translate_to_openai_content_blocks_are_flattened() {
        let req = MessagesRequest {
            model: "m".to_string(),
            messages: vec![AnthropicMessage {
                role: "user".to_string(),
                content: MessageContent::Blocks(vec![
                    ContentBlock::Text {
                        text: "Hello".to_string(),
                    },
                    ContentBlock::Text {
                        text: " World".to_string(),
                    },
                ]),
            }],
            max_tokens: None,
            system: None,
            stream: false,
            temperature: None,
        };
        let (bytes, _) = translate_to_openai(&req).unwrap();
        let v: serde_json::Value = serde_json::from_slice(&bytes).unwrap();
        assert_eq!(v["messages"][0]["content"], "Hello World");
    }

    #[test]
    fn translate_to_openai_no_system_produces_single_message() {
        let req = MessagesRequest {
            model: "m".to_string(),
            messages: vec![AnthropicMessage {
                role: "user".to_string(),
                content: MessageContent::Text("hi".to_string()),
            }],
            max_tokens: None,
            system: None,
            stream: false,
            temperature: None,
        };
        let (bytes, _) = translate_to_openai(&req).unwrap();
        let v: serde_json::Value = serde_json::from_slice(&bytes).unwrap();
        let messages = v["messages"].as_array().unwrap();
        assert_eq!(messages.len(), 1);
        assert_eq!(messages[0]["role"], "user");
    }

    #[test]
    fn openai_to_anthropic_json_maps_fields_correctly() {
        let openai = serde_json::json!({
            "id": "chatcmpl-abc",
            "choices": [{
                "index": 0,
                "message": {"role": "assistant", "content": "Hello from upstream!"},
                "finish_reason": "stop"
            }],
            "usage": {"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15}
        });
        let bytes = serde_json::to_vec(&openai).unwrap();
        let result = openai_to_anthropic_json(&bytes, "test-model", 10);

        assert_eq!(result["type"], "message");
        assert_eq!(result["role"], "assistant");
        assert_eq!(result["content"][0]["type"], "text");
        assert_eq!(result["content"][0]["text"], "Hello from upstream!");
        assert_eq!(result["stop_reason"], "end_turn");
        assert_eq!(result["usage"]["input_tokens"], 10);
        assert_eq!(result["usage"]["output_tokens"], 5);
        assert_eq!(result["model"], "test-model");
    }

    #[test]
    fn finish_reason_mappings_are_correct() {
        assert_eq!(finish_to_stop_reason("max_tokens"), "max_tokens");
        assert_eq!(finish_to_stop_reason("length"), "max_tokens");
        assert_eq!(finish_to_stop_reason("stop"), "end_turn");
        assert_eq!(finish_to_stop_reason(""), "end_turn");
    }

    // ---- HTTP tests (tower::ServiceExt) -------------------------------------

    use crate::auth::{ApiKeyInfo, AuthConfig};
    use crate::routes::app;
    use crate::state::{AppState, ModelRoute, MOCK_MODEL};
    use axum::body::Body;
    use axum::http::{header, Request, StatusCode};
    use axum::response::Response as AxumResponse;
    use http_body_util::BodyExt;
    use serde_json::{json, Value};
    use std::collections::HashMap;
    use tower::ServiceExt;

    async fn body_bytes(response: AxumResponse) -> Vec<u8> {
        response
            .into_body()
            .collect()
            .await
            .expect("collect body")
            .to_bytes()
            .to_vec()
    }

    async fn body_json(response: AxumResponse) -> Value {
        serde_json::from_slice(&body_bytes(response).await).expect("valid JSON body")
    }

    async fn body_text(response: AxumResponse) -> String {
        String::from_utf8(body_bytes(response).await).expect("utf8 body")
    }

    fn post_anthropic(
        x_api_key: Option<&str>,
        bearer: Option<&str>,
        payload: &Value,
    ) -> Request<Body> {
        let mut builder = Request::builder()
            .method("POST")
            .uri("/v1/messages")
            .header(header::CONTENT_TYPE, "application/json");
        if let Some(k) = x_api_key {
            builder = builder.header("x-api-key", k);
        }
        if let Some(tok) = bearer {
            builder = builder.header(header::AUTHORIZATION, format!("Bearer {tok}"));
        }
        builder.body(Body::from(payload.to_string())).unwrap()
    }

    async fn spawn_mock_openai_host() -> String {
        use axum::routing::post as axum_post;
        let router =
            axum::Router::new().route("/v1/chat/completions", axum_post(mock_openai_inference));
        let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
        let addr = listener.local_addr().unwrap();
        tokio::spawn(async move {
            axum::serve(listener, router).await.unwrap();
        });
        format!("http://{addr}")
    }

    async fn mock_openai_inference(body: Bytes) -> AxumResponse {
        let v: Value = serde_json::from_slice(&body).unwrap_or_else(|_| json!({}));
        if v["stream"].as_bool().unwrap_or(false) {
            let d1 =
                json!({"choices":[{"index":0,"delta":{"content":"pong"},"finish_reason":null}]});
            let d2 = json!({"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]});
            let sse = format!("data: {d1}\n\ndata: {d2}\n\ndata: [DONE]\n\n");
            Response::builder()
                .status(200)
                .header(header::CONTENT_TYPE, "text/event-stream")
                .body(Body::from(sse))
                .unwrap()
        } else {
            let full = json!({
                "id": "chatcmpl-mock",
                "object": "chat.completion",
                "choices": [{
                    "index": 0,
                    "message": {"role": "assistant", "content": "pong"},
                    "finish_reason": "stop"
                }],
                "usage": {"prompt_tokens": 2, "completion_tokens": 1, "total_tokens": 3}
            });
            Response::builder()
                .status(200)
                .header(header::CONTENT_TYPE, "application/json")
                .body(Body::from(full.to_string()))
                .unwrap()
        }
    }

    async fn state_with_anthropic_host() -> AppState {
        let host = spawn_mock_openai_host().await;
        let state = AppState::with_mock();
        state
            .insert_route(MOCK_MODEL, ModelRoute::active(&host, "dep-a", "Q4_K_M"))
            .await;
        state
    }

    // Test 1: x-api-key header accepted (open dev mode).
    #[tokio::test]
    async fn x_api_key_header_is_accepted() {
        let state = state_with_anthropic_host().await;
        let payload = json!({
            "model": MOCK_MODEL,
            "messages": [{"role": "user", "content": "ping"}],
            "max_tokens": 16,
            "stream": false
        });
        let response = app(state)
            .oneshot(post_anthropic(Some("any-key"), None, &payload))
            .await
            .unwrap();
        assert_eq!(
            response.status(),
            StatusCode::OK,
            "x-api-key should be accepted in dev mode"
        );
    }

    // Test 2: missing auth → 401 with Anthropic error format.
    #[tokio::test]
    async fn missing_auth_returns_401_anthropic_format() {
        let payload = json!({
            "model": MOCK_MODEL,
            "messages": [{"role": "user", "content": "hi"}],
            "stream": false
        });
        let response = app(AppState::with_mock())
            .oneshot(post_anthropic(None, None, &payload))
            .await
            .unwrap();
        assert_eq!(response.status(), StatusCode::UNAUTHORIZED);
        let body = body_json(response).await;
        assert_eq!(body["type"], "error", "Anthropic envelope must be present");
        assert_eq!(body["error"]["type"], "authentication_error");
    }

    // Test 3: strict auth rejects unknown x-api-key.
    #[tokio::test]
    async fn strict_auth_rejects_unknown_x_api_key() {
        let mut keys = HashMap::new();
        keys.insert(
            "sk-valid".to_string(),
            ApiKeyInfo {
                id: "id1".to_string(),
                tenant: "t1".to_string(),
            },
        );
        let state = AppState::with_mock().with_auth(AuthConfig::strict(keys, None));
        let payload = json!({"model": MOCK_MODEL, "messages": [{"role":"user","content":"hi"}], "stream": false});
        let response = app(state)
            .oneshot(post_anthropic(Some("sk-bogus"), None, &payload))
            .await
            .unwrap();
        assert_eq!(response.status(), StatusCode::UNAUTHORIZED);
        let body = body_json(response).await;
        assert_eq!(body["type"], "error");
        assert_eq!(body["error"]["type"], "authentication_error");
    }

    // Test 4: non-streaming returns Anthropic JSON.
    #[tokio::test]
    async fn non_streaming_returns_anthropic_json_format() {
        let state = state_with_anthropic_host().await;
        let payload = json!({
            "model": MOCK_MODEL,
            "messages": [{"role": "user", "content": "ping"}],
            "max_tokens": 16,
            "stream": false
        });
        let response = app(state)
            .oneshot(post_anthropic(Some("any-key"), None, &payload))
            .await
            .unwrap();
        assert_eq!(response.status(), StatusCode::OK);
        let body = body_json(response).await;
        assert_eq!(body["type"], "message");
        assert_eq!(body["role"], "assistant");
        assert_eq!(body["content"][0]["type"], "text");
        assert_eq!(body["content"][0]["text"], "pong");
        assert_eq!(body["stop_reason"], "end_turn");
        assert!(body["usage"]["input_tokens"].as_u64().is_some());
        assert!(body["usage"]["output_tokens"].as_u64().is_some());
    }

    // Test 5: system prompt accepted without error.
    #[tokio::test]
    async fn system_prompt_is_accepted() {
        let state = state_with_anthropic_host().await;
        let payload = json!({
            "model": MOCK_MODEL,
            "system": "You are a test assistant.",
            "messages": [{"role": "user", "content": "hello"}],
            "stream": false
        });
        let response = app(state)
            .oneshot(post_anthropic(Some("any-key"), None, &payload))
            .await
            .unwrap();
        assert_eq!(response.status(), StatusCode::OK);
    }

    // Test 6: streaming returns Anthropic SSE.
    #[tokio::test]
    async fn streaming_returns_anthropic_sse_format() {
        let state = state_with_anthropic_host().await;
        let payload = json!({
            "model": MOCK_MODEL,
            "messages": [{"role": "user", "content": "ping"}],
            "stream": true
        });
        let response = app(state)
            .oneshot(post_anthropic(Some("any-key"), None, &payload))
            .await
            .unwrap();
        assert_eq!(response.status(), StatusCode::OK);
        let ct = response
            .headers()
            .get(header::CONTENT_TYPE)
            .and_then(|v| v.to_str().ok())
            .unwrap_or_default()
            .to_string();
        assert!(ct.starts_with("text/event-stream"), "content-type: {ct}");
        let text = body_text(response).await;
        assert!(
            text.contains("message_start"),
            "must contain message_start: {text}"
        );
        assert!(
            text.contains("content_block_start"),
            "must contain content_block_start: {text}"
        );
        assert!(
            text.contains("content_block_delta"),
            "must contain content_block_delta: {text}"
        );
        assert!(
            text.contains("message_stop"),
            "must contain message_stop: {text}"
        );
        assert!(text.contains("pong"), "must contain the token: {text}");
    }
}
