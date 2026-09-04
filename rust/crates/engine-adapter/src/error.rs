//! Error types for the engine-adapter contract.

use thiserror::Error;

/// Convenience alias used throughout the crate: `Result<T> = Result<T, EngineError>`.
pub type Result<T> = std::result::Result<T, EngineError>;

/// Failure modes an [`crate::EngineBackend`] may surface.
///
/// The variants are intentionally engine-agnostic: real adapters (llama.cpp,
/// DwarfStar, ...) map their native failures onto these so that Purser only ever
/// reasons about abstract errors. Marked `#[non_exhaustive]` so new variants can
/// be added without breaking downstream matches.
#[derive(Debug, Error)]
#[non_exhaustive]
pub enum EngineError {
    /// The engine process/handshake failed to come up.
    #[error("failed to start engine: {0}")]
    StartFailed(String),

    /// The handle refers to no known (or already reaped) engine.
    #[error("unknown or expired engine handle: {0}")]
    UnknownHandle(String),

    /// The engine was running but has since crashed / exited unexpectedly.
    #[error("engine crashed: {0}")]
    Crashed(String),

    /// The engine exists but has not finished loading (no metrics/serving yet).
    #[error("engine not ready: {0}")]
    NotReady(String),

    /// A caller-supplied argument was invalid (bad layer range, empty ref, ...).
    #[error("invalid argument: {0}")]
    InvalidArgument(String),

    /// The backend itself is unavailable (transport down, engine stopped, ...).
    #[error("backend unavailable: {0}")]
    Unavailable(String),

    /// A bounded wait (e.g. for READY) exceeded its deadline.
    #[error("timed out waiting for {what} after {elapsed_ms} ms")]
    Timeout { what: String, elapsed_ms: u64 },

    /// Catch-all for internal/unexpected failures.
    #[error("internal engine error: {0}")]
    Internal(String),
}
