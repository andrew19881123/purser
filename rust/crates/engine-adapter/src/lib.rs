//! # Purser Engine Adapter
//!
//! Decouples Purser from any concrete inference engine. The central contract is
//! the [`EngineBackend`] trait: llama.cpp, DwarfStar, and the built-in
//! [`MockEngine`] each implement it, and Purser's control plane reasons purely
//! in terms of these abstractions.
//!
//! This crate ships:
//! - the [`EngineBackend`] trait and its supporting types
//!   ([`Capabilities`], [`SpecMethod`], [`EngineHandle`], [`WorkerStart`],
//!   [`HostStart`], [`EventStream`]),
//! - a deterministic, GPU-free [`MockEngine`] with a simulated crash mode, and
//! - a reusable [`conformance`] suite that every real adapter must pass.
//!
//! Wire-level types (`EngineMetrics`, `EngineEvent`, `EngineParams`, ...) come
//! from `purser-proto` and are re-exported here for convenience.

pub mod backend;
pub mod conformance;
pub mod error;
pub mod mock;

pub use backend::{
    into_event_stream, Capabilities, EngineBackend, EngineHandle, EventStream, HostStart,
    SpecMethod, WorkerStart,
};
pub use error::{EngineError, Result};
pub use mock::{MockConfig, MockEngine};

/// Parameters for starting an engine instance.
///
/// Carries both the logical model reference and the optional on-disk GGUF path
/// resolved by the agent's model cache. Real backends (e.g. llama.cpp, DwarfStar)
/// read `model_path` when set and pass it directly to the engine's `--model` flag
/// (see `flags.rs` in those adapters); the [`MockEngine`] ignores both fields.
///
/// This type is the source of truth for the "what to load" side of a start request.
/// The supervision layer passes it as part of an `EngineSpec` — the `model_path`
/// field is populated by the agent's `ModelCache` before the spec reaches the
/// backend, so adapters do not need to resolve model references themselves.
#[derive(Clone, Debug, Default)]
pub struct StartRequest {
    /// Logical model reference (e.g. `"llama-3.1-8b:Q4_K_M"`).
    pub model_ref: String,
    /// Resolved on-disk GGUF path from the model cache. `None` means the cache
    /// did not hold the artifact at start time; the backend must locate the file
    /// itself (e.g. via a configured model directory or the model_ref directly).
    pub model_path: Option<std::path::PathBuf>,
}

// Re-export the shared proto types adapters speak in, so downstream crates need
// only depend on `purser-engine-adapter`.
pub use purser_proto::v1::{EngineEvent, EngineEventKind, EngineMetrics, EngineParams, Role};

#[cfg(test)]
mod tests {
    use super::*;
    use std::time::Duration;

    #[tokio::test]
    async fn mock_passes_conformance_suite() {
        let engine = MockEngine::new();
        conformance::conformance_tests(&engine).await;
    }

    #[tokio::test]
    async fn mock_passes_conformance_as_trait_object() {
        // Proves the trait is dyn-compatible: the whole point of the adapter is
        // an `Arc<dyn EngineBackend>` registry selected at runtime.
        let engine: Box<dyn EngineBackend> = Box::new(MockEngine::new());
        conformance::conformance_tests(engine.as_ref()).await;
    }

    #[tokio::test]
    async fn capabilities_are_reported() {
        let engine = MockEngine::new();
        let caps = engine.capabilities();
        assert!(caps.moe);
        assert!(caps.mixed_backend);
        assert!(!caps.speculative.is_empty());
        assert!(caps.quant_formats.iter().any(|q| q == "Q4_K_M"));
    }

    #[tokio::test]
    async fn simulated_crash_marks_handle_crashed() {
        let engine = MockEngine::crashing(Duration::from_millis(20));
        let mut worker = engine
            .start_worker(0, 3, "crash/model", "127.0.0.1:7001")
            .await
            .expect("start_worker");

        // Observe the lifecycle: LOADING -> READY -> (METRICS) -> ERROR.
        let mut saw_loading = false;
        let mut saw_ready = false;
        let mut saw_error = false;
        let deadline = tokio::time::Instant::now() + Duration::from_secs(5);
        while tokio::time::Instant::now() < deadline {
            match tokio::time::timeout(Duration::from_millis(200), worker.events.recv()).await {
                Ok(Some(ev)) => match EngineEventKind::try_from(ev.kind).unwrap_or_default() {
                    EngineEventKind::Loading => saw_loading = true,
                    EngineEventKind::Ready => saw_ready = true,
                    EngineEventKind::Error => {
                        saw_error = true;
                        break;
                    }
                    _ => {}
                },
                Ok(None) => break,
                Err(_) => break,
            }
        }
        assert!(saw_loading, "expected a LOADING event before the crash");
        assert!(saw_ready, "expected a READY event before the crash");
        assert!(saw_error, "expected a simulated crash ERROR event");

        let err = engine
            .metrics(&worker.handle)
            .await
            .expect_err("metrics after crash must fail");
        assert!(
            matches!(err, EngineError::Crashed(_)),
            "post-crash metrics must report Crashed, got {err:?}"
        );
    }

    #[tokio::test]
    async fn metrics_on_unknown_handle_errors() {
        let engine = MockEngine::new();
        let bogus = EngineHandle::new("does-not-exist", Role::Worker);
        let err = engine.metrics(&bogus).await.expect_err("unknown handle");
        assert!(matches!(err, EngineError::UnknownHandle(_)));
    }

    #[tokio::test]
    async fn invalid_layer_range_rejected() {
        let engine = MockEngine::new();
        let err = engine
            .start_worker(10, 2, "m", "127.0.0.1:1")
            .await
            .expect_err("layer_end < layer_start must be rejected");
        assert!(matches!(err, EngineError::InvalidArgument(_)));
    }

    #[tokio::test]
    async fn host_endpoint_is_non_empty() {
        let engine = MockEngine::new();
        let host = engine
            .start_host("m", &[], EngineParams::default())
            .await
            .expect("start_host");
        assert!(host.endpoint.starts_with("http://"));
        assert!(!host.endpoint.is_empty());
    }
}
