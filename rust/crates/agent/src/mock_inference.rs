//! In-process OpenAI-compatible HTTP server backing the **mock** engine's HOST
//! role.
//!
//! The real llama.cpp / DwarfStar adapters serve their own OpenAI endpoint
//! (`llama-server`, ...). The GPU-free `mock` engine has no such process, so the
//! supervisor stands one up here whenever it starts a mock engine in the HOST
//! role. This closes the end-to-end chat loop: the control plane advertises the
//! deployment host as `http://<host>:<inference_port>` and the gateway
//! reverse-proxies chat requests to `{endpoint}/v1/chat/completions` — which now
//! actually resolves to a listening server that speaks the OpenAI wire format.
//!
//! Routes (served at the **root**, not under `/engines/...`):
//!   * `POST /v1/chat/completions` — canned assistant reply. `stream:true`
//!     returns an SSE token stream (`data: {chunk}` ... `data: [DONE]`);
//!     otherwise a single `chat.completion` JSON object.
//!   * `POST /v1/completions`       — legacy text-completion analogue.
//!   * `GET  /v1/models`            — the single served model.
//!
//! The server is deliberately dumb and deterministic (no GPU, no randomness in
//! the payload). It is owned by the [`crate::supervisor::Supervisor`], which
//! shuts it down on `stop`/`StopEngine` so the inference port is never leaked.

use std::convert::Infallible;
use std::net::SocketAddr;
use std::time::{SystemTime, UNIX_EPOCH};

use axum::body::Bytes;
use axum::extract::State;
use axum::response::sse::{Event, Sse};
use axum::response::{IntoResponse, Json, Response};
use axum::routing::{get, post};
use axum::Router;
use serde_json::{json, Value};
use tokio::net::TcpListener;
use tokio::sync::oneshot;
use tokio::task::JoinHandle;

/// `owned_by` field reported for the served model.
const OWNED_BY: &str = "purser-mock";

/// A running mock inference server.
///
/// Dropping it (or calling [`MockInferenceServer::shutdown`]) signals a graceful
/// shutdown; `shutdown` additionally awaits the serving task so the listener —
/// and therefore the port — is fully released before it returns (important when
/// the supervisor restarts a crashed engine and rebinds the same port).
pub struct MockInferenceServer {
    addr: SocketAddr,
    shutdown: Option<oneshot::Sender<()>>,
    task: Option<JoinHandle<()>>,
}

/// Handler state: the model this host serves.
#[derive(Clone)]
struct AppState {
    model: String,
}

impl MockInferenceServer {
    /// Bind `bind` and start serving the OpenAI routes for `model_ref`.
    ///
    /// If `bind`'s port is `0`, an ephemeral port is chosen; the actual bound
    /// address is available via [`MockInferenceServer::addr`] (handy in tests).
    pub async fn start(bind: SocketAddr, model_ref: String) -> std::io::Result<Self> {
        let listener = TcpListener::bind(bind).await?;
        let addr = listener.local_addr()?;

        let app = Router::new()
            .route("/v1/chat/completions", post(chat_completions))
            .route("/v1/completions", post(completions))
            .route("/v1/models", get(models))
            .route("/health", get(health))
            .with_state(AppState { model: model_ref });

        let (tx, rx) = oneshot::channel::<()>();
        let task = tokio::spawn(async move {
            let serve = axum::serve(listener, app).with_graceful_shutdown(async move {
                let _ = rx.await;
            });
            if let Err(e) = serve.await {
                tracing::warn!(error = %e, "mock inference server exited with error");
            }
        });

        tracing::info!(%addr, "mock inference server listening");
        Ok(Self {
            addr,
            shutdown: Some(tx),
            task: Some(task),
        })
    }

    /// The address the server is bound to.
    pub fn addr(&self) -> SocketAddr {
        self.addr
    }

    /// Signal graceful shutdown and wait for the serving task to finish,
    /// guaranteeing the port is released. Idempotent.
    pub async fn shutdown(&mut self) {
        if let Some(tx) = self.shutdown.take() {
            let _ = tx.send(());
        }
        if let Some(task) = self.task.take() {
            let _ = task.await;
        }
    }
}

impl Drop for MockInferenceServer {
    fn drop(&mut self) {
        // Best-effort: signal shutdown. The task is detached; we cannot await it
        // in `drop`, so callers that need the port freed synchronously must use
        // `shutdown().await`.
        if let Some(tx) = self.shutdown.take() {
            let _ = tx.send(());
        }
    }
}

// ---------------------------------------------------------------------------
// Handlers
// ---------------------------------------------------------------------------

async fn health() -> &'static str {
    "ok"
}

async fn models(State(state): State<AppState>) -> Json<Value> {
    Json(json!({
        "object": "list",
        "data": [{
            "id": state.model,
            "object": "model",
            "created": unix_now(),
            "owned_by": OWNED_BY,
        }],
    }))
}

async fn chat_completions(State(state): State<AppState>, body: Bytes) -> Response {
    let req: Value = serde_json::from_slice(&body).unwrap_or(Value::Null);
    let model = requested_model(&req, &state.model);
    let want_stream = req.get("stream").and_then(Value::as_bool).unwrap_or(false);
    let reply = canned_reply(&model);

    if want_stream {
        chat_sse(model, reply)
    } else {
        let completion_tokens = word_count(&reply);
        Json(json!({
            "id": gen_id("chatcmpl"),
            "object": "chat.completion",
            "created": unix_now(),
            "model": model,
            "choices": [{
                "index": 0,
                "message": {"role": "assistant", "content": reply},
                "finish_reason": "stop",
            }],
            "usage": usage(completion_tokens),
        }))
        .into_response()
    }
}

async fn completions(State(state): State<AppState>, body: Bytes) -> Response {
    let req: Value = serde_json::from_slice(&body).unwrap_or(Value::Null);
    let model = requested_model(&req, &state.model);
    let want_stream = req.get("stream").and_then(Value::as_bool).unwrap_or(false);
    let reply = canned_reply(&model);

    if want_stream {
        completion_sse(model, reply)
    } else {
        let completion_tokens = word_count(&reply);
        Json(json!({
            "id": gen_id("cmpl"),
            "object": "text_completion",
            "created": unix_now(),
            "model": model,
            "choices": [{
                "index": 0,
                "text": reply,
                "finish_reason": "stop",
            }],
            "usage": usage(completion_tokens),
        }))
        .into_response()
    }
}

// ---------------------------------------------------------------------------
// SSE builders
// ---------------------------------------------------------------------------

/// Stream a `chat.completion.chunk` sequence terminated by `data: [DONE]`.
fn chat_sse(model: String, content: String) -> Response {
    let id = gen_id("chatcmpl");
    let created = unix_now();
    let tokens = tokenize(&content);

    let stream = async_stream::stream! {
        // Opening chunk announces the assistant role.
        yield sse_event(&chat_chunk(&id, created, &model, json!({"role": "assistant"}), None));
        for tok in tokens {
            yield sse_event(&chat_chunk(&id, created, &model, json!({"content": tok}), None));
        }
        // Final chunk carries the finish reason, then the OpenAI sentinel.
        yield sse_event(&chat_chunk(&id, created, &model, json!({}), Some("stop")));
        yield Ok::<Event, Infallible>(Event::default().data("[DONE]"));
    };
    Sse::new(stream).into_response()
}

/// Stream a legacy `text_completion` chunk sequence terminated by `[DONE]`.
fn completion_sse(model: String, content: String) -> Response {
    let id = gen_id("cmpl");
    let created = unix_now();
    let tokens = tokenize(&content);

    let stream = async_stream::stream! {
        for tok in tokens {
            let chunk = json!({
                "id": id,
                "object": "text_completion",
                "created": created,
                "model": model,
                "choices": [{"index": 0, "text": tok, "finish_reason": null}],
            });
            yield sse_event(&chunk.to_string());
        }
        let fin = json!({
            "id": id,
            "object": "text_completion",
            "created": created,
            "model": model,
            "choices": [{"index": 0, "text": "", "finish_reason": "stop"}],
        });
        yield sse_event(&fin.to_string());
        yield Ok::<Event, Infallible>(Event::default().data("[DONE]"));
    };
    Sse::new(stream).into_response()
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

fn sse_event(data: &str) -> Result<Event, Infallible> {
    Ok(Event::default().data(data))
}

fn chat_chunk(id: &str, created: u64, model: &str, delta: Value, finish: Option<&str>) -> String {
    json!({
        "id": id,
        "object": "chat.completion.chunk",
        "created": created,
        "model": model,
        "choices": [{"index": 0, "delta": delta, "finish_reason": finish}],
    })
    .to_string()
}

/// The canned assistant reply. Mentions the served model so tests (and humans)
/// can see the loop is live end-to-end.
fn canned_reply(model: &str) -> String {
    format!(
        "Hello from the Purser mock host serving model {model}. The end-to-end chat loop is live."
    )
}

/// Split a reply into OpenAI-style streamed pieces (whitespace-preserving so the
/// concatenated deltas reconstruct the original text).
fn tokenize(content: &str) -> Vec<String> {
    content.split_inclusive(' ').map(str::to_string).collect()
}

fn word_count(s: &str) -> u64 {
    s.split_whitespace().count() as u64
}

fn usage(completion_tokens: u64) -> Value {
    json!({
        "prompt_tokens": 0,
        "completion_tokens": completion_tokens,
        "total_tokens": completion_tokens,
    })
}

/// The model to echo back: the client's requested `model` if present, else the
/// model this host was started to serve.
fn requested_model(req: &Value, served: &str) -> String {
    req.get("model")
        .and_then(Value::as_str)
        .filter(|s| !s.is_empty())
        .unwrap_or(served)
        .to_string()
}

fn unix_now() -> u64 {
    SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .map(|d| d.as_secs())
        .unwrap_or(0)
}

/// OpenAI-style opaque id (`chatcmpl-1a2b3c4d`). Uniqueness is best-effort — the
/// mock does not depend on it.
fn gen_id(prefix: &str) -> String {
    format!("{prefix}-{:08x}", fastrand::u32(..))
}

#[cfg(test)]
mod tests {
    use super::*;
    use tokio::io::{AsyncReadExt, AsyncWriteExt};
    use tokio::net::TcpStream;

    /// Send one HTTP/1.1 request with `Connection: close` and read the whole
    /// response (headers + body) to EOF. Keeps the test dependency-free: no HTTP
    /// client crate, and `Connection: close` lets us read streamed SSE to the
    /// end without decoding chunked framing.
    async fn http_request(addr: SocketAddr, method: &str, path: &str, body: &str) -> String {
        let mut stream = TcpStream::connect(addr).await.expect("connect");
        let req = format!(
            "{method} {path} HTTP/1.1\r\nHost: localhost\r\nContent-Type: application/json\r\n\
             Content-Length: {}\r\nConnection: close\r\n\r\n{body}",
            body.len()
        );
        stream.write_all(req.as_bytes()).await.expect("write");
        let mut buf = Vec::new();
        stream.read_to_end(&mut buf).await.expect("read");
        String::from_utf8_lossy(&buf).into_owned()
    }

    #[tokio::test]
    async fn serves_openai_routes() {
        let model = "purser/mock-7b";
        let mut server =
            MockInferenceServer::start(SocketAddr::from(([127, 0, 0, 1], 0)), model.to_string())
                .await
                .expect("start server");
        let addr = server.addr();

        // GET /v1/models -> the served model.
        let models_resp = http_request(addr, "GET", "/v1/models", "").await;
        assert!(
            models_resp.contains("200 OK"),
            "models status: {models_resp}"
        );
        assert!(
            models_resp.contains(model),
            "models body missing model: {models_resp}"
        );
        assert!(models_resp.contains("\"object\":\"list\""));

        // POST /v1/chat/completions, stream:false -> full chat.completion JSON.
        let non_stream = http_request(
            addr,
            "POST",
            "/v1/chat/completions",
            r#"{"model":"purser/mock-7b","messages":[{"role":"user","content":"hi"}],"stream":false}"#,
        )
        .await;
        assert!(non_stream.contains("200 OK"), "chat status: {non_stream}");
        assert!(non_stream.contains("\"object\":\"chat.completion\""));
        assert!(non_stream.contains("\"role\":\"assistant\""));
        assert!(non_stream.contains(model), "reply should mention the model");
        assert!(non_stream.contains("\"finish_reason\":\"stop\""));

        // POST /v1/chat/completions, stream:true -> SSE ending in [DONE].
        let streamed = http_request(
            addr,
            "POST",
            "/v1/chat/completions",
            r#"{"model":"purser/mock-7b","messages":[{"role":"user","content":"hi"}],"stream":true}"#,
        )
        .await;
        assert!(
            streamed.contains("text/event-stream"),
            "expected SSE content-type: {streamed}"
        );
        assert!(
            streamed.contains("chat.completion.chunk"),
            "expected SSE chunks"
        );
        assert!(streamed.contains("\"delta\""), "expected delta frames");
        assert!(
            streamed.contains("data: [DONE]"),
            "SSE must terminate with [DONE]"
        );

        server.shutdown().await;

        // After shutdown the port is released: a fresh connect should fail.
        let reconnect = TcpStream::connect(addr).await;
        assert!(reconnect.is_err(), "server should no longer be listening");
    }
}
