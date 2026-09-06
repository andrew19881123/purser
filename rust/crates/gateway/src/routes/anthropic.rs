//! Anthropic Messages API compatibility layer.
//!
//! `POST /v1/messages` accepts the Anthropic SDK wire format — including the
//! `x-api-key` header used by `@anthropic-ai/sdk` — and translates it to an
//! OpenAI-compatible request before forwarding to the upstream deployment host.
//! Responses are translated back to Anthropic format (SSE or JSON).
//!
//! This lets Claude Code, Cursor, and any tool built on `import Anthropic from
//! "@anthropic-ai/sdk"` point at Purser by changing only the `base_url`.

use std::collections::HashMap;
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
use serde_json::json;
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

/// Source descriptor for an image content block (accepted but not forwarded).
#[allow(dead_code)]
#[derive(Debug, Deserialize, Clone)]
struct ImageSource {
    #[serde(rename = "type")]
    source_type: String,
    media_type: Option<String>,
    data: Option<String>,
    url: Option<String>,
}

/// A content block inside the `content` array of a message.
#[allow(dead_code)]
#[derive(Debug, Deserialize, Clone)]
#[serde(tag = "type", rename_all = "snake_case")]
enum ContentBlock {
    Text {
        text: String,
    },
    /// Accepted for protocol compatibility; image pixels are not forwarded.
    Image {
        source: ImageSource,
    },
    #[serde(rename = "tool_use")]
    ToolUse {
        id: String,
        name: String,
        input: serde_json::Value,
    },
    #[serde(rename = "tool_result")]
    ToolResult {
        tool_use_id: String,
        content: Vec<ContentBlock>,
        #[serde(default)]
        is_error: bool,
    },
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

#[derive(Debug, Deserialize)]
struct AnthropicMessage {
    role: String,
    content: MessageContent,
}

/// A tool definition sent by the client.
#[derive(Debug, Deserialize)]
struct AnthropicTool {
    name: String,
    description: Option<String>,
    input_schema: serde_json::Value,
}

/// Controls which tool (if any) the model must call.
#[derive(Debug, Deserialize)]
#[serde(tag = "type", rename_all = "snake_case")]
enum ToolChoice {
    Auto,
    Any,
    Tool { name: String },
}

/// `POST /v1/messages` request body (Anthropic Messages API).
#[derive(Debug, Deserialize, Default)]
struct MessagesRequest {
    #[serde(default)]
    model: String,
    #[serde(default)]
    messages: Vec<AnthropicMessage>,
    #[serde(default)]
    max_tokens: Option<u64>,
    #[serde(default)]
    system: Option<String>,
    #[serde(default)]
    stream: bool,
    #[serde(default)]
    temperature: Option<f64>,
    #[serde(default)]
    tools: Option<Vec<AnthropicTool>>,
    #[serde(default)]
    tool_choice: Option<ToolChoice>,
    #[serde(default)]
    stop_sequences: Option<Vec<String>>,
    #[serde(default)]
    top_k: Option<u32>,
    #[serde(default)]
    top_p: Option<f64>,
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
            messages.push(json!({
                "role": "system",
                "content": sys,
            }));
        }
    }

    let mut input_words: u64 = 0;

    for msg in &req.messages {
        match &msg.content {
            MessageContent::Text(text) => {
                input_words += text.split_whitespace().count() as u64;
                messages.push(json!({
                    "role": msg.role,
                    "content": text,
                }));
            }
            MessageContent::Blocks(blocks) => {
                let has_tool_use = blocks
                    .iter()
                    .any(|b| matches!(b, ContentBlock::ToolUse { .. }));
                let has_tool_result = blocks
                    .iter()
                    .any(|b| matches!(b, ContentBlock::ToolResult { .. }));

                if has_tool_result {
                    // Each ToolResult block → a separate OpenAI role=tool message.
                    for block in blocks {
                        match block {
                            ContentBlock::ToolResult {
                                tool_use_id,
                                content,
                                ..
                            } => {
                                let text = content
                                    .iter()
                                    .filter_map(|b| match b {
                                        ContentBlock::Text { text } => Some(text.as_str()),
                                        _ => None,
                                    })
                                    .collect::<Vec<_>>()
                                    .join("\n");
                                input_words += text.split_whitespace().count() as u64;
                                messages.push(json!({
                                    "role": "tool",
                                    "tool_call_id": tool_use_id,
                                    "content": text,
                                }));
                            }
                            ContentBlock::Text { text } => {
                                // Rare: plain text mixed with tool_result — count words but skip.
                                input_words += text.split_whitespace().count() as u64;
                            }
                            _ => {}
                        }
                    }
                } else if has_tool_use {
                    // Assistant message that called tools → tool_calls array.
                    let text_content: String = blocks
                        .iter()
                        .filter_map(|b| match b {
                            ContentBlock::Text { text } => Some(text.as_str()),
                            _ => None,
                        })
                        .collect::<Vec<_>>()
                        .join("");
                    input_words += text_content.split_whitespace().count() as u64;

                    let tool_calls: Vec<serde_json::Value> = blocks
                        .iter()
                        .filter_map(|b| match b {
                            ContentBlock::ToolUse { id, name, input } => Some(json!({
                                "id": id,
                                "type": "function",
                                "function": {
                                    "name": name,
                                    "arguments": serde_json::to_string(input)
                                        .unwrap_or_default(),
                                }
                            })),
                            _ => None,
                        })
                        .collect();

                    let content_val = if text_content.is_empty() {
                        serde_json::Value::Null
                    } else {
                        json!(text_content)
                    };
                    messages.push(json!({
                        "role": "assistant",
                        "content": content_val,
                        "tool_calls": tool_calls,
                    }));
                } else {
                    // Plain text blocks (and silently-skipped image/unknown blocks).
                    let text = blocks
                        .iter()
                        .filter_map(|b| match b {
                            ContentBlock::Text { text } => Some(text.as_str()),
                            _ => None,
                        })
                        .collect::<Vec<_>>()
                        .join("");
                    input_words += text.split_whitespace().count() as u64;
                    messages.push(json!({
                        "role": msg.role,
                        "content": text,
                    }));
                }
            }
        }
    }

    if let Some(sys) = &req.system {
        input_words += sys.split_whitespace().count() as u64;
    }
    let input_tokens = input_words / 4;

    let mut body = json!({
        "model":    req.model,
        "messages": messages,
        "stream":   req.stream,
    });

    if let Some(max_tokens) = req.max_tokens {
        body["max_tokens"] = json!(max_tokens);
    }
    if let Some(temp) = req.temperature {
        body["temperature"] = json!(temp);
    }

    // Tool definitions: AnthropicTool → OpenAI tools[].function
    if let Some(tools) = &req.tools {
        let oai_tools: Vec<serde_json::Value> = tools
            .iter()
            .map(|t| {
                json!({
                    "type": "function",
                    "function": {
                        "name": t.name,
                        "description": t.description,
                        "parameters": t.input_schema,
                    }
                })
            })
            .collect();
        body["tools"] = json!(oai_tools);
    }

    // tool_choice
    if let Some(tc) = &req.tool_choice {
        body["tool_choice"] = match tc {
            ToolChoice::Auto => json!("auto"),
            ToolChoice::Any => json!("required"),
            ToolChoice::Tool { name } => {
                json!({"type": "function", "function": {"name": name}})
            }
        };
    }

    // stop_sequences → OpenAI stop
    if let Some(stops) = &req.stop_sequences {
        body["stop"] = json!(stops);
    }

    // top_p / top_k passthrough
    if let Some(top_p) = req.top_p {
        body["top_p"] = json!(top_p);
    }
    if let Some(top_k) = req.top_k {
        // top_k is not standard OpenAI but many local backends support it.
        body["top_k"] = json!(top_k);
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
        // Track tool call blocks seen in this stream: index → (id, name).
        let mut tool_call_state: HashMap<usize, (String, String)> = HashMap::new();

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

                            let delta = &v["choices"][0]["delta"];
                            let content = delta["content"].as_str().map(str::to_owned);
                            let finish_reason = v["choices"][0]["finish_reason"]
                                .as_str()
                                .map(str::to_owned);

                            if !sent_preamble {
                                for ev in preamble_events(&msg_id, &model, input_tokens_for_usage) {
                                    yield Ok(ev);
                                }
                                sent_preamble = true;
                            }

                            // --- Text delta ---
                            if let Some(text) = content {
                                if !text.is_empty() {
                                    yield Ok(content_delta_event(0, &text));
                                }
                            }

                            // --- Tool call deltas ---
                            if let Some(tc_deltas) = delta["tool_calls"].as_array() {
                                for tc_delta in tc_deltas {
                                    let tc_index =
                                        tc_delta["index"].as_u64().unwrap_or(0) as usize;
                                    // Block index: text is always at 0, tool calls at 1+
                                    let block_index = tc_index + 1;

                                    // First time we see this tool call: emit content_block_start.
                                    if let std::collections::hash_map::Entry::Vacant(e) =
                                        tool_call_state.entry(tc_index)
                                    {
                                        let id = tc_delta["id"]
                                            .as_str()
                                            .unwrap_or("")
                                            .to_owned();
                                        let name = tc_delta["function"]["name"]
                                            .as_str()
                                            .unwrap_or("")
                                            .to_owned();
                                        e.insert((id.clone(), name.clone()));
                                        yield Ok(Bytes::from(format!(
                                            "event: content_block_start\ndata: {}\n\n",
                                            json!({
                                                "type": "content_block_start",
                                                "index": block_index,
                                                "content_block": {
                                                    "type": "tool_use",
                                                    "id": id,
                                                    "name": name,
                                                    "input": {}
                                                }
                                            })
                                        )));
                                    }

                                    // Argument fragment delta.
                                    if let Some(partial) =
                                        tc_delta["function"]["arguments"].as_str()
                                    {
                                        if !partial.is_empty() {
                                            yield Ok(Bytes::from(format!(
                                                "event: content_block_delta\ndata: {}\n\n",
                                                json!({
                                                    "type": "content_block_delta",
                                                    "index": block_index,
                                                    "delta": {
                                                        "type": "input_json_delta",
                                                        "partial_json": partial
                                                    }
                                                })
                                            )));
                                        }
                                    }
                                }
                            }

                            // --- Finish reason ---
                            if finish_reason
                                .as_deref()
                                .map(|r| !r.is_empty())
                                .unwrap_or(false)
                                && !stream_ended
                            {
                                let stop_reason = finish_reason
                                    .as_deref()
                                    .map(finish_to_stop_reason)
                                    .unwrap_or("end_turn");

                                if stop_reason == "tool_use" && !tool_call_state.is_empty() {
                                    // Close text block at index 0.
                                    yield Ok(Bytes::from_static(
                                        b"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n",
                                    ));
                                    // Close each tool block in index order.
                                    let mut sorted: Vec<usize> =
                                        tool_call_state.keys().copied().collect();
                                    sorted.sort_unstable();
                                    for tc_index in &sorted {
                                        let block_index = tc_index + 1;
                                        yield Ok(Bytes::from(format!(
                                            "event: content_block_stop\ndata: {{\"type\":\"content_block_stop\",\"index\":{block_index}}}\n\n"
                                        )));
                                    }
                                    let msg_delta = json!({
                                        "type": "message_delta",
                                        "delta": {
                                            "stop_reason": "tool_use",
                                            "stop_sequence": null
                                        },
                                        "usage": {"output_tokens": out_tokens},
                                    });
                                    yield Ok(Bytes::from(format!(
                                        "event: message_delta\ndata: {}\n\n",
                                        msg_delta
                                    )));
                                    yield Ok(Bytes::from_static(
                                        b"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n",
                                    ));
                                } else {
                                    for ev in closing_events_with_stop(out_tokens, stop_reason) {
                                        yield Ok(ev);
                                    }
                                }
                                stream_ended = true;
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

    let message = &v["choices"][0]["message"];
    let finish_reason = v["choices"][0]["finish_reason"].as_str().unwrap_or("stop");
    let stop_reason = finish_to_stop_reason(finish_reason);

    let out_tokens = v["usage"]["completion_tokens"].as_u64().unwrap_or(0);
    let in_tokens = v["usage"]["prompt_tokens"].as_u64().unwrap_or(input_tokens);

    // Build content blocks.
    let mut content_blocks: Vec<serde_json::Value> = Vec::new();

    // Text content, if present, comes first.
    if let Some(text) = message["content"].as_str() {
        if !text.is_empty() {
            content_blocks.push(json!({"type": "text", "text": text}));
        }
    }

    // OpenAI tool_calls → Anthropic tool_use blocks.
    if let Some(tool_calls) = message["tool_calls"].as_array() {
        for tc in tool_calls {
            let args: serde_json::Value = tc["function"]["arguments"]
                .as_str()
                .and_then(|s| serde_json::from_str(s).ok())
                .unwrap_or(serde_json::Value::Object(Default::default()));
            content_blocks.push(json!({
                "type": "tool_use",
                "id":   tc["id"],
                "name": tc["function"]["name"],
                "input": args,
            }));
        }
    }

    // Fallback: always emit at least one (possibly empty) text block.
    if content_blocks.is_empty() {
        let text = message["content"].as_str().unwrap_or("").to_owned();
        content_blocks.push(json!({"type": "text", "text": text}));
    }

    json!({
        "id":            format!("msg_{}", msg_id),
        "type":          "message",
        "role":          "assistant",
        "content":       content_blocks,
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
        "tool_calls" => "tool_use",
        "max_tokens" | "length" => "max_tokens",
        _ => "end_turn",
    }
}

// ---------------------------------------------------------------------------
// SSE event builders
// ---------------------------------------------------------------------------

fn preamble_events(msg_id: &str, model: &str, input_tokens: u64) -> Vec<Bytes> {
    let msg_start = json!({
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
    let cb_start = json!({
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
    let msg_delta = json!({
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
    let body = json!({
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
        let body = json!({
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
            ..Default::default()
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
            ..Default::default()
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
            ..Default::default()
        };
        let (bytes, _) = translate_to_openai(&req).unwrap();
        let v: serde_json::Value = serde_json::from_slice(&bytes).unwrap();
        let messages = v["messages"].as_array().unwrap();
        assert_eq!(messages.len(), 1);
        assert_eq!(messages[0]["role"], "user");
    }

    #[test]
    fn openai_to_anthropic_json_maps_fields_correctly() {
        let openai = json!({
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

    // ---- New tool use unit tests ---------------------------------------------

    #[test]
    fn test_translate_tools_to_openai_format() {
        let req = MessagesRequest {
            model: "test".to_string(),
            messages: vec![AnthropicMessage {
                role: "user".to_string(),
                content: MessageContent::Text("search something".to_string()),
            }],
            tools: Some(vec![AnthropicTool {
                name: "search".to_string(),
                description: Some("Web search".to_string()),
                input_schema: json!({"type": "object", "properties": {"q": {"type": "string"}}}),
            }]),
            ..Default::default()
        };
        let (bytes, _) = translate_to_openai(&req).unwrap();
        let v: serde_json::Value = serde_json::from_slice(&bytes).unwrap();
        assert_eq!(v["tools"][0]["type"], "function");
        assert_eq!(v["tools"][0]["function"]["name"], "search");
        assert_eq!(v["tools"][0]["function"]["description"], "Web search");
        assert_eq!(v["tools"][0]["function"]["parameters"]["type"], "object");
    }

    #[test]
    fn test_tool_use_message_translated_to_openai_tool_calls() {
        let req = MessagesRequest {
            model: "m".to_string(),
            messages: vec![AnthropicMessage {
                role: "assistant".to_string(),
                content: MessageContent::Blocks(vec![ContentBlock::ToolUse {
                    id: "tu_123".to_string(),
                    name: "get_weather".to_string(),
                    input: json!({"location": "Paris"}),
                }]),
            }],
            ..Default::default()
        };
        let (bytes, _) = translate_to_openai(&req).unwrap();
        let v: serde_json::Value = serde_json::from_slice(&bytes).unwrap();
        let msg = &v["messages"][0];
        assert_eq!(msg["role"], "assistant");
        let tool_calls = msg["tool_calls"].as_array().unwrap();
        assert_eq!(tool_calls.len(), 1);
        assert_eq!(tool_calls[0]["id"], "tu_123");
        assert_eq!(tool_calls[0]["type"], "function");
        assert_eq!(tool_calls[0]["function"]["name"], "get_weather");
        // arguments must be a JSON string encoding the input
        let args_str = tool_calls[0]["function"]["arguments"].as_str().unwrap();
        let args: serde_json::Value = serde_json::from_str(args_str).unwrap();
        assert_eq!(args["location"], "Paris");
    }

    #[test]
    fn test_tool_result_translated_to_tool_message() {
        let req = MessagesRequest {
            model: "m".to_string(),
            messages: vec![AnthropicMessage {
                role: "user".to_string(),
                content: MessageContent::Blocks(vec![ContentBlock::ToolResult {
                    tool_use_id: "tu_123".to_string(),
                    content: vec![ContentBlock::Text {
                        text: "Sunny, 22°C".to_string(),
                    }],
                    is_error: false,
                }]),
            }],
            ..Default::default()
        };
        let (bytes, _) = translate_to_openai(&req).unwrap();
        let v: serde_json::Value = serde_json::from_slice(&bytes).unwrap();
        let msg = &v["messages"][0];
        assert_eq!(msg["role"], "tool");
        assert_eq!(msg["tool_call_id"], "tu_123");
        assert_eq!(msg["content"], "Sunny, 22°C");
    }

    #[test]
    fn test_finish_reason_tool_calls_maps_to_tool_use() {
        assert_eq!(finish_to_stop_reason("tool_calls"), "tool_use");
        // Regression: existing mappings still work.
        assert_eq!(finish_to_stop_reason("stop"), "end_turn");
        assert_eq!(finish_to_stop_reason("length"), "max_tokens");
    }

    #[test]
    fn test_stop_sequences_forwarded_to_upstream() {
        let req = MessagesRequest {
            model: "m".to_string(),
            messages: vec![AnthropicMessage {
                role: "user".to_string(),
                content: MessageContent::Text("hi".to_string()),
            }],
            stop_sequences: Some(vec!["Human:".to_string(), "Assistant:".to_string()]),
            ..Default::default()
        };
        let (bytes, _) = translate_to_openai(&req).unwrap();
        let v: serde_json::Value = serde_json::from_slice(&bytes).unwrap();
        let stops = v["stop"].as_array().unwrap();
        assert_eq!(stops.len(), 2);
        assert_eq!(stops[0], "Human:");
        assert_eq!(stops[1], "Assistant:");
    }

    // Also verify tool_use stop_reason in the buffered response translation.
    #[test]
    fn test_openai_tool_calls_response_translated_to_anthropic_tool_use() {
        let openai = json!({
            "choices": [{
                "message": {
                    "role": "assistant",
                    "content": null,
                    "tool_calls": [{
                        "id": "call_abc",
                        "type": "function",
                        "function": {
                            "name": "search",
                            "arguments": "{\"q\":\"rust async\"}"
                        }
                    }]
                },
                "finish_reason": "tool_calls"
            }],
            "usage": {"prompt_tokens": 20, "completion_tokens": 10, "total_tokens": 30}
        });
        let bytes = serde_json::to_vec(&openai).unwrap();
        let result = openai_to_anthropic_json(&bytes, "test-model", 20);

        assert_eq!(result["stop_reason"], "tool_use");
        // Find the tool_use block
        let content = result["content"].as_array().unwrap();
        let tool_block = content
            .iter()
            .find(|b| b["type"] == "tool_use")
            .expect("tool_use block must be present");
        assert_eq!(tool_block["id"], "call_abc");
        assert_eq!(tool_block["name"], "search");
        assert_eq!(tool_block["input"]["q"], "rust async");
    }

    // ---- HTTP tests (tower::ServiceExt) -------------------------------------

    use crate::auth::{ApiKeyInfo, AuthConfig};
    use crate::routes::app;
    use crate::state::{AppState, ModelRoute, MOCK_MODEL};
    use axum::body::Body;
    use axum::http::{header, Request, StatusCode};
    use axum::response::Response as AxumResponse;
    use http_body_util::BodyExt;
    use serde_json::Value;
    use std::collections::HashMap as StdHashMap;
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
        let mut keys = StdHashMap::new();
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
