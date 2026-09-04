//! Per-tenant quota, rate limiting and backpressure.
//!
//! Three independent guards protect the cluster, all keyed by **api-key id**
//! so one tenant can never starve another:
//!
//! 1. **Token rate** — a per-key [token bucket] of `tokens_per_min` capacity,
//!    refilled continuously. Prompt tokens are charged up front; completion
//!    tokens are charged as they stream. Exhaustion → `429`.
//! 2. **Concurrency** — at most `max_concurrent` in-flight requests per key.
//! 3. **Backpressure** — a global in-flight ceiling `max_inflight`; crossing it
//!    sheds load with `429` + `Retry-After` rather than queueing unboundedly.
//!
//! Every threshold is configurable ([`QuotaConfig`]) so it can be calibrated
//! against real cluster capacity. A threshold of `0` means "unlimited".
//!
//! [token bucket]: https://en.wikipedia.org/wiki/Token_bucket

use std::collections::HashMap;
use std::sync::atomic::{AtomicU32, Ordering};
use std::sync::{Arc, Mutex};
use std::time::Instant;

use crate::error::ApiError;

/// Environment overrides for the quota knobs.
pub const ENV_TOKENS_PER_MIN: &str = "PURSER_GATEWAY_TOKENS_PER_MIN";
pub const ENV_MAX_CONCURRENT: &str = "PURSER_GATEWAY_MAX_CONCURRENT";
pub const ENV_MAX_INFLIGHT: &str = "PURSER_GATEWAY_MAX_INFLIGHT";
pub const ENV_RETRY_AFTER: &str = "PURSER_GATEWAY_RETRY_AFTER_SECS";

/// Tunable quota/limit thresholds. `0` disables the corresponding limit.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub struct QuotaConfig {
    /// Per-key token-bucket capacity and per-minute refill.
    pub tokens_per_min: u64,
    /// Max concurrent in-flight requests per key.
    pub max_concurrent: u32,
    /// Global in-flight ceiling across all keys (backpressure).
    pub max_inflight: u32,
    /// `Retry-After` value (seconds) advertised on `429`.
    pub retry_after_secs: u64,
}

impl Default for QuotaConfig {
    fn default() -> Self {
        // Deliberately generous starting points — CALIBRATE against real
        // cluster throughput before production.
        Self {
            tokens_per_min: 60_000,
            max_concurrent: 32,
            max_inflight: 512,
            retry_after_secs: 2,
        }
    }
}

impl QuotaConfig {
    /// Load thresholds from the environment, falling back to [`Default`].
    pub fn from_env() -> Self {
        let d = Self::default();
        Self {
            tokens_per_min: env_u64(ENV_TOKENS_PER_MIN, d.tokens_per_min),
            max_concurrent: env_u64(ENV_MAX_CONCURRENT, d.max_concurrent as u64) as u32,
            max_inflight: env_u64(ENV_MAX_INFLIGHT, d.max_inflight as u64) as u32,
            retry_after_secs: env_u64(ENV_RETRY_AFTER, d.retry_after_secs),
        }
    }
}

fn env_u64(var: &str, default: u64) -> u64 {
    std::env::var(var)
        .ok()
        .and_then(|v| v.trim().parse().ok())
        .unwrap_or(default)
}

/// A continuously-refilling token bucket.
#[derive(Debug)]
struct TokenBucket {
    tokens: f64,
    capacity: f64,
    refill_per_sec: f64,
    last: Instant,
}

impl TokenBucket {
    fn new(capacity: f64, refill_per_sec: f64) -> Self {
        Self {
            tokens: capacity,
            capacity,
            refill_per_sec,
            last: Instant::now(),
        }
    }

    fn refill(&mut self) {
        let now = Instant::now();
        let dt = now.duration_since(self.last).as_secs_f64();
        if dt > 0.0 {
            self.tokens = (self.tokens + dt * self.refill_per_sec).min(self.capacity);
            self.last = now;
        }
    }

    /// Take `n` tokens if available, refilling first. Returns whether taken.
    fn try_take(&mut self, n: f64) -> bool {
        self.refill();
        if self.tokens >= n {
            self.tokens -= n;
            true
        } else {
            false
        }
    }

    /// Charge `n` tokens best-effort (may drive the bucket to zero, never
    /// negative). Used for completion tokens counted after admission.
    fn charge(&mut self, n: f64) {
        self.refill();
        self.tokens = (self.tokens - n).max(0.0);
    }
}

#[derive(Debug)]
struct KeyState {
    concurrent: u32,
    bucket: TokenBucket,
}

/// Runtime limiter state, shared across all requests via `Arc`.
#[derive(Debug, Default)]
pub struct Limiter {
    global_inflight: AtomicU32,
    keys: Mutex<HashMap<String, KeyState>>,
}

impl Limiter {
    pub fn new() -> Self {
        Self::default()
    }

    /// Current global in-flight count (for observability/tests).
    pub fn global_inflight(&self) -> u32 {
        self.global_inflight.load(Ordering::SeqCst)
    }

    /// Admit a request: enforce backpressure, per-key concurrency and the
    /// token bucket. On success returns an RAII [`RequestGuard`] that releases
    /// the concurrency slots when dropped (i.e. at end of request/stream).
    pub fn acquire(
        self: &Arc<Self>,
        key_id: &str,
        quota: &QuotaConfig,
        prompt_tokens: u64,
    ) -> Result<RequestGuard, ApiError> {
        // 1) Global backpressure ceiling.
        let prev = self.global_inflight.fetch_add(1, Ordering::SeqCst);
        if quota.max_inflight > 0 && prev + 1 > quota.max_inflight {
            self.global_inflight.fetch_sub(1, Ordering::SeqCst);
            return Err(ApiError::RateLimited {
                message: "Gateway is saturated (backpressure). Retry after a short delay."
                    .to_string(),
                retry_after_secs: quota.retry_after_secs,
            });
        }

        // 2) Per-key concurrency + 3) token bucket, under one lock.
        let admit = {
            let mut map = self.keys.lock().expect("limiter mutex poisoned");
            let ks = map.entry(key_id.to_string()).or_insert_with(|| KeyState {
                concurrent: 0,
                bucket: TokenBucket::new(
                    quota.tokens_per_min as f64,
                    quota.tokens_per_min as f64 / 60.0,
                ),
            });

            if quota.max_concurrent > 0 && ks.concurrent + 1 > quota.max_concurrent {
                Err(ApiError::RateLimited {
                    message: format!(
                        "Concurrency limit reached ({} in-flight requests for this key).",
                        quota.max_concurrent
                    ),
                    retry_after_secs: quota.retry_after_secs,
                })
            } else if quota.tokens_per_min > 0 && !ks.bucket.try_take(prompt_tokens.max(1) as f64) {
                Err(ApiError::RateLimited {
                    message: format!(
                        "Token rate limit exceeded ({} tokens/min for this key).",
                        quota.tokens_per_min
                    ),
                    retry_after_secs: quota.retry_after_secs,
                })
            } else {
                ks.concurrent += 1;
                Ok(())
            }
        };

        match admit {
            Ok(()) => Ok(RequestGuard {
                limiter: Arc::clone(self),
                key_id: key_id.to_string(),
            }),
            Err(e) => {
                self.global_inflight.fetch_sub(1, Ordering::SeqCst);
                Err(e)
            }
        }
    }

    /// Charge additional (completion) tokens against a key's bucket, best
    /// effort. No-op if the key was evicted.
    pub fn charge_tokens(&self, key_id: &str, tokens: u64) {
        if tokens == 0 {
            return;
        }
        if let Ok(mut map) = self.keys.lock() {
            if let Some(ks) = map.get_mut(key_id) {
                ks.bucket.charge(tokens as f64);
            }
        }
    }

    fn release(&self, key_id: &str) {
        self.global_inflight.fetch_sub(1, Ordering::SeqCst);
        if let Ok(mut map) = self.keys.lock() {
            if let Some(ks) = map.get_mut(key_id) {
                ks.concurrent = ks.concurrent.saturating_sub(1);
            }
        }
    }
}

/// RAII admission guard. Holds a concurrency slot (per-key and global) for the
/// lifetime of the request — for streaming responses it is moved into the
/// response body stream so the slot is held until the last token is sent.
#[derive(Debug)]
pub struct RequestGuard {
    limiter: Arc<Limiter>,
    key_id: String,
}

impl Drop for RequestGuard {
    fn drop(&mut self) {
        self.limiter.release(&self.key_id);
    }
}
