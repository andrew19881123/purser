//! Outbound HTTP client to deployment hosts (the pipeline coordinators).
//!
//! The gateway is a transparent reverse proxy: it forwards the client's raw
//! request body to the resolved host's OpenAI-compatible endpoint and relays
//! the response — buffered for non-streaming calls, or streamed chunk-by-chunk
//! (SSE) with minimal buffering for streaming calls.
//!
//! Timeouts are split so failures map onto the right status:
//! * **connect timeout** and **time-to-first-byte** → `503`/`504` *before* any
//!   bytes are sent to the client;
//! * **idle timeout** between streamed chunks → the stream is terminated with a
//!   trailing error frame (the client keeps whatever partial output arrived).

use std::time::Duration;

use bytes::Bytes;
use reqwest::Client;

use crate::error::ApiError;

/// Environment overrides for upstream timeouts (milliseconds).
pub const ENV_CONNECT_MS: &str = "PURSER_GATEWAY_UPSTREAM_CONNECT_MS";
pub const ENV_TTFB_MS: &str = "PURSER_GATEWAY_UPSTREAM_TTFB_MS";
pub const ENV_IDLE_MS: &str = "PURSER_GATEWAY_UPSTREAM_IDLE_MS";

/// A configured reqwest client plus the gateway's proxy timeouts.
#[derive(Clone)]
pub struct HttpClient {
    pub client: Client,
    /// Max time to receive the response head (time-to-first-byte).
    pub ttfb: Duration,
    /// Max idle time between streamed chunks before the stream is cut.
    pub idle: Duration,
}

impl HttpClient {
    pub fn new(connect: Duration, ttfb: Duration, idle: Duration) -> Self {
        let client = Client::builder()
            .connect_timeout(connect)
            // No global request timeout: streaming responses are long-lived and
            // are bounded instead by the per-chunk idle timeout.
            .build()
            .expect("failed to build reqwest client");
        Self { client, ttfb, idle }
    }

    /// Sensible defaults for talking to a local/LAN deployment host.
    pub fn default_local() -> Self {
        Self::new(
            Duration::from_millis(2000),
            Duration::from_millis(30_000),
            Duration::from_millis(30_000),
        )
    }

    /// Build from environment overrides, falling back to [`Self::default_local`].
    pub fn from_env() -> Self {
        let connect = env_ms(ENV_CONNECT_MS, 2000);
        let ttfb = env_ms(ENV_TTFB_MS, 30_000);
        let idle = env_ms(ENV_IDLE_MS, 30_000);
        Self::new(connect, ttfb, idle)
    }

    /// POST a JSON body to `url`, bounded by the time-to-first-byte timeout.
    ///
    /// Maps transport failures to gateway errors: unreachable/refused host →
    /// `503`, timeout → `504`.
    pub async fn send_json(&self, url: &str, body: Bytes) -> Result<reqwest::Response, ApiError> {
        let request = self
            .client
            .post(url)
            .header(reqwest::header::CONTENT_TYPE, "application/json")
            .body(body)
            .send();

        match tokio::time::timeout(self.ttfb, request).await {
            Err(_elapsed) => Err(ApiError::Timeout(
                "The deployment host did not start responding in time.".to_string(),
            )),
            Ok(Err(err)) if err.is_timeout() => Err(ApiError::Timeout(
                "The deployment host timed out while starting the response.".to_string(),
            )),
            Ok(Err(err)) => {
                tracing::warn!(err = %err, url = %url, "upstream connection failed");
                Err(ApiError::NodeUnavailable(
                    "The inference backend is temporarily unavailable; retry shortly.".to_string(),
                ))
            }
            Ok(Ok(resp)) => Ok(resp),
        }
    }
}

fn env_ms(var: &str, default_ms: u64) -> Duration {
    let ms = std::env::var(var)
        .ok()
        .and_then(|v| v.trim().parse().ok())
        .unwrap_or(default_ms);
    Duration::from_millis(ms)
}

/// Approximate the number of output tokens carried by one SSE byte chunk, by
/// counting content-bearing `data:` frames. This is an *estimate* for
/// metrics/quota accounting, not exact tokenization.
pub fn count_sse_tokens(chunk: &[u8]) -> u64 {
    let text = match std::str::from_utf8(chunk) {
        Ok(t) => t,
        Err(_) => return 0,
    };
    let mut n = 0;
    for line in text.lines() {
        if let Some(rest) = line.trim_start().strip_prefix("data:") {
            let rest = rest.trim();
            if rest.is_empty() || rest == "[DONE]" {
                continue;
            }
            n += 1;
        }
    }
    n
}

/// Extract `usage.completion_tokens` from a buffered (non-streaming) JSON
/// response body, defaulting to `0` when absent.
pub fn json_completion_tokens(bytes: &[u8]) -> u64 {
    serde_json::from_slice::<serde_json::Value>(bytes)
        .ok()
        .and_then(|v| {
            v.get("usage")
                .and_then(|u| u.get("completion_tokens"))
                .and_then(serde_json::Value::as_u64)
        })
        .unwrap_or(0)
}
