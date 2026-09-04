//! A reusable conformance suite every [`EngineBackend`] must satisfy.
//!
//! Real adapters (llama.cpp, DwarfStar, ...) are expected to pass exactly the
//! same checks the mock does, so each adapter crate can simply write:
//!
//! ```ignore
//! #[tokio::test]
//! async fn my_adapter_is_conformant() {
//!     let backend = MyAdapter::new(/* ... */);
//!     purser_engine_adapter::conformance::conformance_tests(&backend).await;
//! }
//! ```
//!
//! The suite asserts the invariants shared by all backends. It must be run
//! against a *healthy* backend (one not configured to crash), since it checks
//! the happy-path lifecycle end to end.

use std::time::Duration;

use purser_proto::v1::{EngineEventKind, EngineParams};

use crate::backend::{EngineBackend, EventStream};

/// Generous upper bound on how long a backend may take to reach READY before
/// the suite considers it hung. Backends far exceeding this are a bug.
const READY_TIMEOUT: Duration = Duration::from_secs(30);

/// Decode a raw proto enum discriminant into [`EngineEventKind`].
fn kind_of(raw: i32) -> EngineEventKind {
    EngineEventKind::try_from(raw).unwrap_or_default()
}

/// Drain `events` until READY or ERROR (or timeout / channel close), returning
/// the ordered kinds observed up to and including the terminating event.
async fn drain_until_ready(events: &mut EventStream, timeout: Duration) -> Vec<EngineEventKind> {
    let mut seen = Vec::new();
    let deadline = tokio::time::Instant::now() + timeout;
    loop {
        let remaining = deadline.saturating_duration_since(tokio::time::Instant::now());
        if remaining.is_zero() {
            break;
        }
        match tokio::time::timeout(remaining, events.recv()).await {
            Ok(Some(ev)) => {
                let kind = kind_of(ev.kind);
                seen.push(kind);
                if matches!(kind, EngineEventKind::Ready | EngineEventKind::Error) {
                    break;
                }
            }
            // Channel closed before READY.
            Ok(None) => break,
            // Timed out.
            Err(_) => break,
        }
    }
    seen
}

/// Run the full conformance suite against `backend`, panicking (via assertions)
/// on the first violated invariant. Works with both concrete backends and
/// `&dyn EngineBackend` (note the `?Sized` bound).
pub async fn conformance_tests<B: EngineBackend + ?Sized>(backend: &B) {
    check_capabilities(backend);
    check_worker_lifecycle(backend).await;
    check_host_lifecycle(backend).await;
}

/// `capabilities()` must be callable and side-effect free (invoked twice, must
/// return equal results).
fn check_capabilities<B: EngineBackend + ?Sized>(backend: &B) {
    let a = backend.capabilities();
    let b = backend.capabilities();
    assert_eq!(a, b, "capabilities() must be deterministic / side-effect free");
}

/// start_worker -> valid handle; LOADING precedes READY; metrics after READY;
/// stop is idempotent.
async fn check_worker_lifecycle<B: EngineBackend + ?Sized>(backend: &B) {
    let mut worker = backend
        .start_worker(0, 15, "conformance/model:Q4_K_M", "127.0.0.1:6001")
        .await
        .expect("start_worker should succeed for a healthy backend");

    assert!(
        !worker.handle.id().is_empty(),
        "start_worker must return a non-empty handle id"
    );

    let kinds = drain_until_ready(&mut worker.events, READY_TIMEOUT).await;
    assert!(
        kinds.contains(&EngineEventKind::Loading),
        "worker must emit a LOADING event; saw {kinds:?}"
    );
    assert_eq!(
        kinds.last(),
        Some(&EngineEventKind::Ready),
        "worker must transition to READY; saw {kinds:?}"
    );
    let loading_at = kinds
        .iter()
        .position(|k| *k == EngineEventKind::Loading)
        .expect("LOADING present");
    let ready_at = kinds
        .iter()
        .position(|k| *k == EngineEventKind::Ready)
        .expect("READY present");
    assert!(
        loading_at < ready_at,
        "LOADING must precede READY; saw {kinds:?}"
    );

    // metrics only valid once READY.
    let m = backend
        .metrics(&worker.handle)
        .await
        .expect("metrics must succeed after READY");
    assert_plausible_metrics(&m);

    // stop must be idempotent.
    backend
        .stop(&worker.handle)
        .await
        .expect("first stop must succeed");
    backend
        .stop(&worker.handle)
        .await
        .expect("second stop must succeed (idempotent)");
}

/// start_host -> non-empty endpoint + valid handle; reaches READY; metrics work;
/// stop is idempotent.
async fn check_host_lifecycle<B: EngineBackend + ?Sized>(backend: &B) {
    let params = EngineParams {
        context: 4096,
        ..EngineParams::default()
    };
    let mut host = backend
        .start_host(
            "conformance/model:Q4_K_M",
            &["127.0.0.1:6001".to_string()],
            params,
        )
        .await
        .expect("start_host should succeed for a healthy backend");

    assert!(
        !host.endpoint.is_empty(),
        "start_host must return a non-empty endpoint"
    );
    assert!(
        !host.handle.id().is_empty(),
        "start_host must return a non-empty handle id"
    );

    let kinds = drain_until_ready(&mut host.events, READY_TIMEOUT).await;
    assert_eq!(
        kinds.last(),
        Some(&EngineEventKind::Ready),
        "host must transition to READY; saw {kinds:?}"
    );

    let m = backend
        .metrics(&host.handle)
        .await
        .expect("host metrics must succeed after READY");
    assert_plausible_metrics(&m);

    backend.stop(&host.handle).await.expect("host stop must succeed");
    backend
        .stop(&host.handle)
        .await
        .expect("host stop must be idempotent");
}

/// Invariants every backend's metrics must satisfy regardless of engine.
fn assert_plausible_metrics(m: &purser_proto::v1::EngineMetrics) {
    assert!(m.decode_tok_s >= 0.0, "decode_tok_s must be non-negative");
    assert!(m.prefill_tok_s >= 0.0, "prefill_tok_s must be non-negative");
    assert!(
        (0.0..=1.0).contains(&m.accepted_tokens_ratio),
        "accepted_tokens_ratio must be within [0, 1]; got {}",
        m.accepted_tokens_ratio
    );
}
