//! The central contract that decouples Purser from any concrete inference engine.
//!
//! Every engine (llama.cpp, DwarfStar, the in-crate mock, ...) implements
//! [`EngineBackend`]. Purser's control plane only ever holds a trait object /
//! generic backend and reasons in terms of the abstractions defined here.
//!
//! ## Why `async_trait`?
//!
//! Native `async fn` in traits is stable, but such traits are not yet
//! *dyn-compatible* (you cannot build a `Box<dyn EngineBackend>` from them
//! without extra machinery). The whole point of the adapter is a pluggable
//! registry of backends selected at runtime, i.e. `Arc<dyn EngineBackend>`, so
//! we use [`async_trait`] which desugars to boxed, `Send` futures and keeps the
//! trait object-safe.

use async_trait::async_trait;
use purser_proto::v1::{EngineEvent, EngineMetrics, EngineParams, Role};
use tokio::sync::mpsc;
use tokio_stream::wrappers::ReceiverStream;

use crate::error::Result;

/// Ordered stream of lifecycle events emitted while an engine starts and runs.
///
/// Backends push `LOADING -> READY -> METRICS ...` (and `ERROR` on failure)
/// onto this channel. It is a plain [`mpsc::Receiver`] so it stays object-safe;
/// use [`into_event_stream`] when a `Stream` adapter is more convenient.
pub type EventStream = mpsc::Receiver<EngineEvent>;

/// Speculative-decoding method a backend can offer.
#[derive(Clone, Copy, Debug, PartialEq, Eq, Hash)]
pub enum SpecMethod {
    /// DwarfStar's DSpark drafting.
    Dspark,
    /// Multi-token prediction.
    Mtp,
    /// EAGLE-3 style drafting.
    Eagle3,
    /// N-gram / prompt-lookup drafting.
    Ngram,
    /// No speculative decoding.
    None,
}

/// Static description of what a backend supports. Used by the planner to decide
/// whether a given deployment shape is feasible on this engine.
#[derive(Clone, Debug, PartialEq, Eq, Default)]
pub struct Capabilities {
    /// Can mix heterogeneous compute backends (e.g. CUDA host + Metal worker).
    pub mixed_backend: bool,
    /// Supports mixture-of-experts models.
    pub moe: bool,
    /// Speculative-decoding methods available.
    pub speculative: Vec<SpecMethod>,
    /// Quantization formats the engine can load (e.g. `"Q4_K_M"`, `"FP16"`).
    pub quant_formats: Vec<String>,
}

/// Opaque handle to a started engine instance (host or worker).
///
/// The inner id is backend-defined and treated as opaque by Purser; adapters
/// mint handles via [`EngineHandle::new`] and are free to encode whatever they
/// need (pid, container id, remote uuid, ...) into the id string.
#[derive(Clone, Debug, PartialEq, Eq, Hash)]
pub struct EngineHandle {
    id: String,
    role: Role,
}

impl EngineHandle {
    /// Mint a new handle. Adapters call this after successfully launching an
    /// engine; the `id` must be unique within the backend.
    pub fn new(id: impl Into<String>, role: Role) -> Self {
        Self {
            id: id.into(),
            role,
        }
    }

    /// The opaque, backend-defined identifier.
    pub fn id(&self) -> &str {
        &self.id
    }

    /// Whether this handle is a pipeline host or a worker.
    pub fn role(&self) -> Role {
        self.role
    }
}

/// Outcome of [`EngineBackend::start_worker`]: a handle plus its event stream.
#[derive(Debug)]
pub struct WorkerStart {
    /// Handle used for subsequent `metrics`/`stop` calls.
    pub handle: EngineHandle,
    /// Lifecycle events emitted during startup and runtime.
    pub events: EventStream,
}

/// Outcome of [`EngineBackend::start_host`]: the serving endpoint, a handle, and
/// the event stream.
#[derive(Debug)]
pub struct HostStart {
    /// Client-facing serving endpoint (e.g. `http://host:port`). Never empty.
    pub endpoint: String,
    /// Handle used for subsequent `metrics`/`stop` calls.
    pub handle: EngineHandle,
    /// Lifecycle events emitted during startup and runtime.
    pub events: EventStream,
}

/// The backend-agnostic inference-engine contract.
///
/// Implementations must uphold the invariants exercised by
/// [`crate::conformance::conformance_tests`].
#[async_trait]
pub trait EngineBackend: Send + Sync {
    /// Static feature set of this backend. Must be cheap and side-effect free.
    fn capabilities(&self) -> Capabilities;

    /// Start a pipeline *worker* serving layers `[layer_start, layer_end]` of
    /// `model_ref`, listening on `bind_addr` for the host to connect.
    async fn start_worker(
        &self,
        layer_start: u32,
        layer_end: u32,
        model_ref: &str,
        bind_addr: &str,
    ) -> Result<WorkerStart>;

    /// Start a pipeline *host* for `model_ref`, wiring it to the given
    /// `worker_addrs`, tuned by `params`. Returns a non-empty serving endpoint.
    async fn start_host(
        &self,
        model_ref: &str,
        worker_addrs: &[String],
        params: EngineParams,
    ) -> Result<HostStart>;

    /// Stop the engine referenced by `handle`. Must be idempotent: stopping an
    /// already-stopped handle returns `Ok` with a status string.
    async fn stop(&self, handle: &EngineHandle) -> Result<String>;

    /// Fetch live metrics for a running engine. Only valid after the engine has
    /// reached READY; otherwise returns an error (`NotReady`/`Crashed`/...).
    async fn metrics(&self, handle: &EngineHandle) -> Result<EngineMetrics>;
}

/// Wrap an [`EventStream`] as a `Stream` for ergonomic `StreamExt` usage.
pub fn into_event_stream(events: EventStream) -> ReceiverStream<EngineEvent> {
    ReceiverStream::new(events)
}
