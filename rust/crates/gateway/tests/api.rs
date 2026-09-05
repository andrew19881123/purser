//! In-process HTTP tests.
//!
//! Most requests are driven straight through the axum `Router` via
//! `tower::ServiceExt::oneshot` (no socket bound). Proxy/streaming tests spin
//! up a small **mock llama-server** on a real loopback socket that the gateway
//! reaches over HTTP with its ordinary `reqwest` client — exercising the real
//! forwarding path without a real inference engine.

use std::collections::HashMap;
use std::sync::Arc;
use std::time::Duration;

use axum::body::{Body, Bytes};
use axum::http::{header, Request, StatusCode};
use axum::response::Response;
use axum::routing::post;
use axum::Router;
use http_body_util::BodyExt;
use purser_gateway::{
    app, ApiError, ApiKeyInfo, AppState, AuthConfig, Limiter, ModelRoute, QuotaConfig, MOCK_MODEL,
};
use serde_json::{json, Value};
use tower::ServiceExt;

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

async fn body_bytes(response: Response) -> Vec<u8> {
    response
        .into_body()
        .collect()
        .await
        .expect("collect body")
        .to_bytes()
        .to_vec()
}

async fn body_text(response: Response) -> String {
    String::from_utf8(body_bytes(response).await).expect("utf8 body")
}

async fn body_json(response: Response) -> Value {
    serde_json::from_slice(&body_bytes(response).await).expect("valid JSON body")
}

fn get(uri: &str) -> Request<Body> {
    Request::builder().uri(uri).body(Body::empty()).unwrap()
}

fn post_json(uri: &str, bearer: Option<&str>, payload: &Value) -> Request<Body> {
    let mut builder = Request::builder()
        .method("POST")
        .uri(uri)
        .header(header::CONTENT_TYPE, "application/json");
    if let Some(token) = bearer {
        builder = builder.header(header::AUTHORIZATION, format!("Bearer {token}"));
    }
    builder.body(Body::from(payload.to_string())).unwrap()
}

/// A mock llama-server that echoes the request's prompt back as tokens. Used to
/// prove real routing/streaming and tenant isolation.
async fn spawn_mock_host() -> String {
    let router = Router::new()
        .route("/v1/chat/completions", post(mock_inference))
        .route("/v1/completions", post(mock_inference));
    let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
    let addr = listener.local_addr().unwrap();
    tokio::spawn(async move {
        axum::serve(listener, router).await.unwrap();
    });
    format!("http://{addr}")
}

/// A mock embedding server that returns a canned 128-dim float32 embedding.
async fn spawn_mock_embed_host() -> String {
    let router = Router::new()
        .route("/v1/embeddings", post(mock_embeddings));
    let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
    let addr = listener.local_addr().unwrap();
    tokio::spawn(async move {
        axum::serve(listener, router).await.unwrap();
    });
    format!("http://{addr}")
}

async fn mock_embeddings(_body: Bytes) -> Response {
    let embedding: Vec<f32> = (0..128).map(|i| (i as f32) / 128.0).collect();
    let body = json!({
        "object": "list",
        "data": [{"object": "embedding", "embedding": embedding, "index": 0}],
        "model": "mock-embed",
        "usage": {"prompt_tokens": 5, "total_tokens": 5}
    });
    Response::builder()
        .status(200)
        .header(header::CONTENT_TYPE, "application/json")
        .body(Body::from(body.to_string()))
        .unwrap()
}

async fn mock_inference(body: Bytes) -> Response {
    let v: Value = serde_json::from_slice(&body).unwrap_or_else(|_| json!({}));
    let marker = v["messages"][0]["content"]
        .as_str()
        .or_else(|| v["prompt"].as_str())
        .unwrap_or("")
        .to_string();
    // Small delay so concurrent requests genuinely overlap in-flight.
    tokio::time::sleep(Duration::from_millis(40)).await;

    if v["stream"].as_bool().unwrap_or(false) {
        let d1 = json!({"choices":[{"index":0,"delta":{"content": format!("echo:{marker}")},"finish_reason":null}]});
        let d2 = json!({"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]});
        let sse = format!("data: {d1}\n\ndata: {d2}\n\ndata: [DONE]\n\n");
        Response::builder()
            .status(200)
            .header(header::CONTENT_TYPE, "text/event-stream")
            .body(Body::from(sse))
            .unwrap()
    } else {
        let full = json!({
            "id":"chatcmpl-mock","object":"chat.completion",
            "choices":[{"index":0,"message":{"role":"assistant","content":format!("echo:{marker}")},"finish_reason":"stop"}],
            "usage":{"prompt_tokens":1,"completion_tokens":2,"total_tokens":3}
        });
        Response::builder()
            .status(200)
            .header(header::CONTENT_TYPE, "application/json")
            .body(Body::from(full.to_string()))
            .unwrap()
    }
}

/// Gateway state wired to a live mock host for `MOCK_MODEL`.
async fn state_with_mock_host() -> (AppState, String) {
    let host = spawn_mock_host().await;
    let state = AppState::with_mock();
    state
        .insert_route(MOCK_MODEL, ModelRoute::active(&host, "dep-1", "Q4_K_M"))
        .await;
    (state, host)
}

// ---------------------------------------------------------------------------
// health / readiness / models
// ---------------------------------------------------------------------------

#[tokio::test]
async fn healthz_returns_ok() {
    let response = app(AppState::with_mock())
        .oneshot(get("/healthz"))
        .await
        .unwrap();
    assert_eq!(response.status(), StatusCode::OK);
    assert_eq!(body_bytes(response).await, b"ok");
}

#[tokio::test]
async fn readyz_reports_route_counts() {
    let response = app(AppState::with_mock())
        .oneshot(get("/readyz"))
        .await
        .unwrap();
    assert_eq!(response.status(), StatusCode::OK);
    let body = body_json(response).await;
    assert_eq!(body["status"], "ready");
    assert_eq!(body["active_routes"], 1);
    assert_eq!(body["total_routes"], 1);
}

#[tokio::test]
async fn models_lists_only_active_routes() {
    let state = AppState::with_mock();
    // A draining route must not surface in /v1/models.
    state
        .insert_route(
            "draining-model",
            ModelRoute::draining("http://127.0.0.1:9", "dep-x", "Q8_0"),
        )
        .await;

    let response = app(state).oneshot(get("/v1/models")).await.unwrap();
    assert_eq!(response.status(), StatusCode::OK);
    let body = body_json(response).await;
    assert_eq!(body["object"], "list");
    let ids: Vec<String> = body["data"]
        .as_array()
        .unwrap()
        .iter()
        .map(|m| m["id"].as_str().unwrap().to_string())
        .collect();
    assert!(ids.contains(&MOCK_MODEL.to_string()), "active model listed");
    assert!(
        !ids.contains(&"draining-model".to_string()),
        "draining model must be hidden from /v1/models: {ids:?}"
    );
}

// ---------------------------------------------------------------------------
// auth
// ---------------------------------------------------------------------------

#[tokio::test]
async fn chat_without_auth_is_401() {
    let payload = json!({"model": MOCK_MODEL, "messages": [{"role":"user","content":"hi"}]});
    let response = app(AppState::with_mock())
        .oneshot(post_json("/v1/chat/completions", None, &payload))
        .await
        .unwrap();
    assert_eq!(response.status(), StatusCode::UNAUTHORIZED);
    assert_eq!(
        body_json(response).await["error"]["type"],
        "authentication_error"
    );
}

#[tokio::test]
async fn strict_auth_rejects_unknown_key_but_accepts_known() {
    let mut keys = HashMap::new();
    keys.insert(
        "sk-live".to_string(),
        ApiKeyInfo {
            id: "id-live".to_string(),
            tenant: "team-a".to_string(),
        },
    );
    let state = AppState::with_mock().with_auth(AuthConfig::strict(keys, None));

    // Unknown key -> 401.
    let payload = json!({"model": "anything", "messages": [{"role":"user","content":"hi"}]});
    let response = app(state.clone())
        .oneshot(post_json(
            "/v1/chat/completions",
            Some("sk-bogus"),
            &payload,
        ))
        .await
        .unwrap();
    assert_eq!(response.status(), StatusCode::UNAUTHORIZED);

    // Known key gets *past* auth: unknown model now yields 404, not 401.
    let response = app(state)
        .oneshot(post_json("/v1/chat/completions", Some("sk-live"), &payload))
        .await
        .unwrap();
    assert_eq!(response.status(), StatusCode::NOT_FOUND);
}

// ---------------------------------------------------------------------------
// route-sync contract  (PUT/DELETE /api/v1/routes)
// ---------------------------------------------------------------------------

fn put_route(bearer_token: Option<&str>, payload: &Value) -> Request<Body> {
    let mut builder = Request::builder()
        .method("PUT")
        .uri("/api/v1/routes")
        .header(header::CONTENT_TYPE, "application/json");
    if let Some(token) = bearer_token {
        builder = builder.header("X-Purser-Internal-Token", token);
    }
    builder.body(Body::from(payload.to_string())).unwrap()
}

#[tokio::test]
async fn route_sync_put_requires_internal_token() {
    let payload = json!({
        "model_id":"llama-3-8b","endpoint":"http://10.0.0.4:8080",
        "deployment_id":"dep-9","quantization":"Q4_K_M","state":"active"
    });

    // Missing header -> 401.
    let response = app(AppState::with_mock())
        .oneshot(put_route(None, &payload))
        .await
        .unwrap();
    assert_eq!(response.status(), StatusCode::UNAUTHORIZED);

    // Wrong header -> 403.
    let response = app(AppState::with_mock())
        .oneshot(put_route(Some("wrong-secret"), &payload))
        .await
        .unwrap();
    assert_eq!(response.status(), StatusCode::FORBIDDEN);
}

#[tokio::test]
async fn route_sync_put_and_delete_update_the_table() {
    let state = AppState::new().with_auth(AuthConfig::allow_any_dev(Some("secret".to_string())));
    let token = "secret";

    // PUT active -> appears in /v1/models.
    let payload = json!({
        "model_id":"llama-3-8b","endpoint":"http://10.0.0.4:8080",
        "deployment_id":"dep-9","quantization":"Q4_K_M","state":"active"
    });
    let response = app(state.clone())
        .oneshot(put_route(Some(token), &payload))
        .await
        .unwrap();
    assert_eq!(response.status(), StatusCode::OK);
    assert!(state
        .active_model_ids()
        .await
        .contains(&"llama-3-8b".to_string()));

    let models = body_json(app(state.clone()).oneshot(get("/v1/models")).await.unwrap()).await;
    let ids: Vec<String> = models["data"]
        .as_array()
        .unwrap()
        .iter()
        .map(|m| m["id"].as_str().unwrap().to_string())
        .collect();
    assert_eq!(ids, vec!["llama-3-8b".to_string()]);

    // PUT stopped -> removed.
    let stop = json!({"model_id":"llama-3-8b","endpoint":"","deployment_id":"","quantization":"","state":"stopped"});
    let response = app(state.clone())
        .oneshot(put_route(Some(token), &stop))
        .await
        .unwrap();
    assert_eq!(response.status(), StatusCode::OK);
    assert!(body_json(response).await["removed"].as_bool().unwrap());
    assert!(state.active_model_ids().await.is_empty());

    // DELETE is idempotent and also requires the token.
    state
        .insert_route("m2", ModelRoute::active("http://h:1", "d", "q"))
        .await;
    let del = Request::builder()
        .method("DELETE")
        .uri("/api/v1/routes/m2")
        .header("X-Purser-Internal-Token", token)
        .body(Body::empty())
        .unwrap();
    let response = app(state.clone()).oneshot(del).await.unwrap();
    assert_eq!(response.status(), StatusCode::OK);
    assert!(body_json(response).await["removed"].as_bool().unwrap());
    assert!(state.model_ids().await.is_empty());

    // DELETE without the token -> 401.
    let del_noauth = Request::builder()
        .method("DELETE")
        .uri("/api/v1/routes/whatever")
        .body(Body::empty())
        .unwrap();
    let response = app(state).oneshot(del_noauth).await.unwrap();
    assert_eq!(response.status(), StatusCode::UNAUTHORIZED);
}

// ---------------------------------------------------------------------------
// routing / proxy / streaming
// ---------------------------------------------------------------------------

#[tokio::test]
async fn streaming_proxies_real_tokens_from_host() {
    let (state, _host) = state_with_mock_host().await;
    let payload = json!({
        "model": MOCK_MODEL,
        "messages":[{"role":"user","content":"hello-stream"}],
        "stream": true
    });
    let response = app(state)
        .oneshot(post_json(
            "/v1/chat/completions",
            Some("client-key"),
            &payload,
        ))
        .await
        .unwrap();

    assert_eq!(response.status(), StatusCode::OK);
    let ct = response
        .headers()
        .get(header::CONTENT_TYPE)
        .and_then(|v| v.to_str().ok())
        .unwrap_or_default()
        .to_string();
    assert!(ct.starts_with("text/event-stream"), "got {ct}");

    let text = body_text(response).await;
    assert!(
        text.contains("echo:hello-stream"),
        "streamed tokens: {text}"
    );
    assert!(text.trim_end().ends_with("data: [DONE]"), "SSE terminator");
}

#[tokio::test]
async fn non_streaming_proxies_json_from_host() {
    let (state, _host) = state_with_mock_host().await;
    let payload = json!({
        "model": MOCK_MODEL,
        "messages":[{"role":"user","content":"hello-json"}],
        "stream": false
    });
    let response = app(state)
        .oneshot(post_json(
            "/v1/chat/completions",
            Some("client-key"),
            &payload,
        ))
        .await
        .unwrap();
    assert_eq!(response.status(), StatusCode::OK);
    let body = body_json(response).await;
    assert_eq!(body["choices"][0]["message"]["content"], "echo:hello-json");
}

#[tokio::test]
async fn unknown_model_is_404_with_available_list() {
    let (state, _host) = state_with_mock_host().await;
    let payload = json!({"model":"does-not-exist","messages":[{"role":"user","content":"hi"}]});
    let response = app(state)
        .oneshot(post_json(
            "/v1/chat/completions",
            Some("client-key"),
            &payload,
        ))
        .await
        .unwrap();
    assert_eq!(response.status(), StatusCode::NOT_FOUND);
    let body = body_json(response).await;
    assert_eq!(body["error"]["code"], "model_not_found");
    assert!(body["error"]["message"]
        .as_str()
        .unwrap()
        .contains(MOCK_MODEL));
}

#[tokio::test]
async fn host_down_is_503() {
    // with_mock points MOCK_MODEL at a closed port; no live host registered.
    let payload =
        json!({"model": MOCK_MODEL, "messages":[{"role":"user","content":"hi"}], "stream": false});
    let response = app(AppState::with_mock())
        .oneshot(post_json(
            "/v1/chat/completions",
            Some("client-key"),
            &payload,
        ))
        .await
        .unwrap();
    assert_eq!(response.status(), StatusCode::SERVICE_UNAVAILABLE);
    assert_eq!(
        body_json(response).await["error"]["code"],
        "node_unavailable"
    );
}

// ---------------------------------------------------------------------------
// quota / rate limiting / backpressure
// ---------------------------------------------------------------------------

#[tokio::test]
async fn token_rate_limit_returns_429_with_retry_after() {
    let state = AppState::with_mock().with_quota(QuotaConfig {
        tokens_per_min: 1, // tiny bucket -> a multi-word prompt is rejected
        max_concurrent: 100,
        max_inflight: 100,
        retry_after_secs: 7,
    });
    let payload = json!({
        "model": MOCK_MODEL,
        "messages":[{"role":"user","content":"one two three four five"}],
        "stream": false
    });
    let response = app(state)
        .oneshot(post_json(
            "/v1/chat/completions",
            Some("client-key"),
            &payload,
        ))
        .await
        .unwrap();

    assert_eq!(response.status(), StatusCode::TOO_MANY_REQUESTS);
    assert_eq!(
        response.headers().get(header::RETRY_AFTER).unwrap(),
        "7",
        "429 must advertise Retry-After"
    );
    assert_eq!(
        body_json(response).await["error"]["type"],
        "rate_limit_error"
    );
}

#[test]
fn backpressure_ceiling_sheds_load_deterministically() {
    // Global in-flight ceiling of 1: the second concurrent admission is shed
    // with a 429 (backpressure) regardless of key, until a slot is released.
    let limiter = Arc::new(Limiter::new());
    let quota = QuotaConfig {
        tokens_per_min: 1_000_000,
        max_concurrent: 100,
        max_inflight: 1,
        retry_after_secs: 3,
    };

    let g1 = limiter.acquire("key-a", &quota, 1).expect("first admitted");
    assert_eq!(limiter.global_inflight(), 1);

    let err = limiter
        .acquire("key-b", &quota, 1)
        .expect_err("second must be shed");
    assert!(
        matches!(err, ApiError::RateLimited { retry_after_secs, .. } if retry_after_secs == 3),
        "backpressure must be a 429 with Retry-After"
    );
    // A shed request must not leak an in-flight slot.
    assert_eq!(limiter.global_inflight(), 1);

    drop(g1);
    assert_eq!(limiter.global_inflight(), 0);
    let _g3 = limiter
        .acquire("key-c", &quota, 1)
        .expect("admitted after release");
    assert_eq!(limiter.global_inflight(), 1);
}

#[test]
fn per_key_concurrency_is_isolated_between_tenants() {
    // max_concurrent = 1 per key; a second concurrent request for the SAME key
    // is rejected, but a DIFFERENT key is still admitted (no cross-tenant
    // starvation).
    let limiter = Arc::new(Limiter::new());
    let quota = QuotaConfig {
        tokens_per_min: 1_000_000,
        max_concurrent: 1,
        max_inflight: 0, // unlimited global, isolate the per-key check
        retry_after_secs: 1,
    };

    let _a1 = limiter.acquire("key-a", &quota, 1).expect("A#1 admitted");
    assert!(
        limiter.acquire("key-a", &quota, 1).is_err(),
        "A#2 must hit the per-key concurrency limit"
    );
    let _b1 = limiter
        .acquire("key-b", &quota, 1)
        .expect("B#1 admitted despite A being saturated");
}

// ---------------------------------------------------------------------------
// multi-tenant isolation  (SECURITY INVARIANT)
// ---------------------------------------------------------------------------

#[tokio::test]
async fn concurrent_tenants_never_see_each_others_output() {
    let host = spawn_mock_host().await;

    let mut keys = HashMap::new();
    keys.insert(
        "key-a".to_string(),
        ApiKeyInfo {
            id: "id-a".to_string(),
            tenant: "tenant-a".to_string(),
        },
    );
    keys.insert(
        "key-b".to_string(),
        ApiKeyInfo {
            id: "id-b".to_string(),
            tenant: "tenant-b".to_string(),
        },
    );
    let state = AppState::with_mock().with_auth(AuthConfig::strict(keys, None));
    state
        .insert_route(MOCK_MODEL, ModelRoute::active(&host, "dep-1", "Q4_K_M"))
        .await;

    let router = app(state);

    let req_a = post_json(
        "/v1/chat/completions",
        Some("key-a"),
        &json!({"model": MOCK_MODEL, "messages":[{"role":"user","content":"SECRET-ALPHA"}], "stream": true}),
    );
    let req_b = post_json(
        "/v1/chat/completions",
        Some("key-b"),
        &json!({"model": MOCK_MODEL, "messages":[{"role":"user","content":"SECRET-BRAVO"}], "stream": true}),
    );

    // Fire both concurrently so their proxied streams overlap in the gateway.
    let (resp_a, resp_b) = tokio::join!(router.clone().oneshot(req_a), router.oneshot(req_b));
    let text_a = body_text(resp_a.unwrap()).await;
    let text_b = body_text(resp_b.unwrap()).await;

    assert!(
        text_a.contains("echo:SECRET-ALPHA"),
        "A sees its own: {text_a}"
    );
    assert!(
        !text_a.contains("SECRET-BRAVO"),
        "ISOLATION VIOLATION: tenant A saw tenant B's content: {text_a}"
    );
    assert!(
        text_b.contains("echo:SECRET-BRAVO"),
        "B sees its own: {text_b}"
    );
    assert!(
        !text_b.contains("SECRET-ALPHA"),
        "ISOLATION VIOLATION: tenant B saw tenant A's content: {text_b}"
    );
}

// ---------------------------------------------------------------------------
// embeddings
// ---------------------------------------------------------------------------

#[tokio::test]
async fn embeddings_valid_model_returns_200_with_expected_shape() {
    let host = spawn_mock_embed_host().await;
    let state = AppState::with_mock();
    state
        .insert_route(MOCK_MODEL, ModelRoute::active(&host, "dep-embed", "fp32"))
        .await;

    let payload = json!({
        "model": MOCK_MODEL,
        "input": "hello embeddings",
        "encoding_format": "float"
    });
    let response = app(state)
        .oneshot(post_json("/v1/embeddings", Some("client-key"), &payload))
        .await
        .unwrap();

    assert_eq!(response.status(), StatusCode::OK);
    let body = body_json(response).await;
    assert_eq!(body["object"], "list", "outer object must be 'list'");
    assert_eq!(
        body["data"][0]["object"], "embedding",
        "data[0].object must be 'embedding'"
    );
    let embedding = body["data"][0]["embedding"].as_array().expect("embedding array");
    assert_eq!(embedding.len(), 128, "expected 128-dim vector from mock host");
    assert!(
        body["usage"]["prompt_tokens"].as_u64().is_some(),
        "usage.prompt_tokens must be present"
    );
}

#[tokio::test]
async fn embeddings_host_down_is_503() {
    // AppState::with_mock() registers MOCK_MODEL pointing at a closed port.
    let payload = json!({
        "model": MOCK_MODEL,
        "input": "test"
    });
    let response = app(AppState::with_mock())
        .oneshot(post_json("/v1/embeddings", Some("client-key"), &payload))
        .await
        .unwrap();
    assert_eq!(response.status(), StatusCode::SERVICE_UNAVAILABLE);
    assert_eq!(
        body_json(response).await["error"]["code"],
        "node_unavailable"
    );
}

// ---------------------------------------------------------------------------
// observability
// ---------------------------------------------------------------------------

#[tokio::test]
async fn metrics_endpoint_exposes_prometheus_after_a_request() {
    let (state, _host) = state_with_mock_host().await;

    // Drive one request so at least one metric is emitted.
    let payload =
        json!({"model": MOCK_MODEL, "messages":[{"role":"user","content":"hi"}], "stream": false});
    let _ = app(state.clone())
        .oneshot(post_json(
            "/v1/chat/completions",
            Some("client-key"),
            &payload,
        ))
        .await
        .unwrap();

    let response = app(state).oneshot(get("/metrics")).await.unwrap();
    assert_eq!(response.status(), StatusCode::OK);
    let ct = response
        .headers()
        .get(header::CONTENT_TYPE)
        .and_then(|v| v.to_str().ok())
        .unwrap_or_default()
        .to_string();
    assert!(ct.starts_with("text/plain"), "prometheus text format: {ct}");
    let text = body_text(response).await;
    assert!(
        text.contains("purser_gateway_requests_total"),
        "expected request counter in /metrics output"
    );
}
