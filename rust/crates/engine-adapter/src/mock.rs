//! A deterministic, GPU-free [`EngineBackend`] implementation for tests and
//! local development.
//!
//! [`MockEngine`] simulates the real lifecycle: it emits `LOADING`, waits a
//! short (configurable) delay, then transitions to `READY` and produces
//! synthetic-but-plausible [`EngineMetrics`]. It can also simulate an
//! unexpected crash after a configurable delay (see [`MockConfig::fail_after`])
//! so that downstream self-healing logic has something to react to.

use std::collections::HashMap;
use std::sync::{Arc, Mutex};
use std::time::Duration;

use async_trait::async_trait;
use purser_proto::v1::{EngineEvent, EngineEventKind, EngineMetrics, EngineParams, Role};
use tokio::sync::mpsc;

use crate::backend::{
    Capabilities, EngineBackend, EngineHandle, HostStart, SpecMethod, WorkerStart,
};
use crate::error::{EngineError, Result};

/// Bounded capacity of the per-engine event channel.
const EVENT_CHANNEL_CAP: usize = 32;

/// Tuning knobs for [`MockEngine`] behaviour.
#[derive(Clone, Debug)]
pub struct MockConfig {
    /// How long to stay in `LOADING` before transitioning to `READY`.
    pub load_delay: Duration,
    /// If set, the engine emits an `ERROR` and marks itself crashed this long
    /// after reaching `READY` (simulated crash for self-healing tests).
    pub fail_after: Option<Duration>,
    /// Whether to emit a `METRICS` event immediately after `READY`.
    pub emit_metrics: bool,
    /// Capabilities advertised by [`EngineBackend::capabilities`].
    pub capabilities: Capabilities,
}

impl Default for MockConfig {
    fn default() -> Self {
        Self {
            // Short but non-zero so the LOADING->READY transition is observable.
            load_delay: Duration::from_millis(10),
            fail_after: None,
            emit_metrics: true,
            capabilities: Capabilities {
                mixed_backend: true,
                moe: true,
                speculative: vec![SpecMethod::Ngram, SpecMethod::Mtp],
                quant_formats: vec!["Q4_K_M".to_string(), "Q8_0".to_string(), "FP16".to_string()],
            },
        }
    }
}

/// Lifecycle phase of a single simulated engine instance.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
enum Phase {
    Loading,
    Ready,
    Stopped,
    Crashed,
}

/// Per-handle bookkeeping.
struct HandleInfo {
    phase: Phase,
    #[allow(dead_code)]
    role: Role,
    #[allow(dead_code)]
    model_ref: String,
    crash_detail: Option<String>,
}

/// Shared mutable state, guarded by a std mutex (only held for tiny, sync ops).
#[derive(Default)]
struct State {
    handles: HashMap<String, HandleInfo>,
    next_id: u64,
}

/// A deterministic, in-process mock engine.
pub struct MockEngine {
    config: MockConfig,
    state: Arc<Mutex<State>>,
}

impl MockEngine {
    /// A mock with default configuration (fast load, no crash, metrics on).
    pub fn new() -> Self {
        Self::with_config(MockConfig::default())
    }

    /// A mock with explicit configuration.
    pub fn with_config(config: MockConfig) -> Self {
        Self {
            config,
            state: Arc::new(Mutex::new(State::default())),
        }
    }

    /// A mock that simulates a crash `fail_after` its READY transition.
    pub fn crashing(fail_after: Duration) -> Self {
        Self::with_config(MockConfig {
            fail_after: Some(fail_after),
            ..MockConfig::default()
        })
    }

    /// Read-only access to the active configuration.
    pub fn config(&self) -> &MockConfig {
        &self.config
    }

    /// Register a new handle and spawn its lifecycle task; returns the handle
    /// and the receiving end of its event stream.
    fn spawn(&self, role: Role, model_ref: &str) -> (EngineHandle, mpsc::Receiver<EngineEvent>) {
        let id = {
            let mut st = self.state.lock().unwrap();
            st.next_id += 1;
            let id = format!("mock-{}-{:04}", role_tag(role), st.next_id);
            st.handles.insert(
                id.clone(),
                HandleInfo {
                    phase: Phase::Loading,
                    role,
                    model_ref: model_ref.to_string(),
                    crash_detail: None,
                },
            );
            id
        };

        let (tx, rx) = mpsc::channel(EVENT_CHANNEL_CAP);
        let state = Arc::clone(&self.state);
        let config = self.config.clone();
        let lifecycle_id = id.clone();
        tokio::spawn(async move {
            run_lifecycle(state, lifecycle_id, config, tx).await;
        });

        (EngineHandle::new(id, role), rx)
    }
}

impl Default for MockEngine {
    fn default() -> Self {
        Self::new()
    }
}

#[async_trait]
impl EngineBackend for MockEngine {
    fn capabilities(&self) -> Capabilities {
        self.config.capabilities.clone()
    }

    async fn start_worker(
        &self,
        layer_start: u32,
        layer_end: u32,
        model_ref: &str,
        bind_addr: &str,
    ) -> Result<WorkerStart> {
        if layer_end < layer_start {
            return Err(EngineError::InvalidArgument(format!(
                "layer_end ({layer_end}) < layer_start ({layer_start})"
            )));
        }
        if model_ref.is_empty() {
            return Err(EngineError::InvalidArgument("empty model_ref".to_string()));
        }
        if bind_addr.is_empty() {
            return Err(EngineError::InvalidArgument("empty bind_addr".to_string()));
        }

        let (handle, events) = self.spawn(Role::Worker, model_ref);
        Ok(WorkerStart { handle, events })
    }

    async fn start_host(
        &self,
        model_ref: &str,
        worker_addrs: &[String],
        params: EngineParams,
    ) -> Result<HostStart> {
        if model_ref.is_empty() {
            return Err(EngineError::InvalidArgument("empty model_ref".to_string()));
        }
        // The mock doesn't drive real workers; it only validates the inputs are
        // well-formed and echoes tuning into the (deterministic) endpoint.
        let _ = (worker_addrs, &params);

        let (handle, events) = self.spawn(Role::Host, model_ref);
        // Deterministic, always-non-empty serving endpoint.
        let endpoint = format!("http://127.0.0.1:8000/engines/{}", handle.id());
        Ok(HostStart {
            endpoint,
            handle,
            events,
        })
    }

    async fn stop(&self, handle: &EngineHandle) -> Result<String> {
        let mut st = self.state.lock().unwrap();
        match st.handles.get_mut(handle.id()) {
            None => Err(EngineError::UnknownHandle(handle.id().to_string())),
            Some(info) => match info.phase {
                Phase::Stopped => Ok("already-stopped".to_string()),
                _ => {
                    info.phase = Phase::Stopped;
                    Ok("stopped".to_string())
                }
            },
        }
    }

    async fn metrics(&self, handle: &EngineHandle) -> Result<EngineMetrics> {
        let st = self.state.lock().unwrap();
        match st.handles.get(handle.id()) {
            None => Err(EngineError::UnknownHandle(handle.id().to_string())),
            Some(info) => match info.phase {
                Phase::Ready => Ok(synth_metrics()),
                Phase::Loading => Err(EngineError::NotReady(format!(
                    "handle {} is still loading",
                    handle.id()
                ))),
                Phase::Stopped => Err(EngineError::Unavailable(format!(
                    "handle {} has been stopped",
                    handle.id()
                ))),
                Phase::Crashed => Err(EngineError::Crashed(
                    info.crash_detail
                        .clone()
                        .unwrap_or_else(|| "engine crashed".to_string()),
                )),
            },
        }
    }
}

/// Drive one engine through its simulated lifecycle, pushing events onto `tx`.
async fn run_lifecycle(
    state: Arc<Mutex<State>>,
    id: String,
    config: MockConfig,
    tx: mpsc::Sender<EngineEvent>,
) {
    // LOADING
    let _ = tx
        .send(make_event(
            EngineEventKind::Loading,
            "loading model weights",
            None,
        ))
        .await;

    tokio::time::sleep(config.load_delay).await;

    // Transition LOADING -> READY, unless the handle was stopped meanwhile.
    {
        let mut st = state.lock().unwrap();
        match st.handles.get_mut(&id) {
            Some(info) if info.phase == Phase::Loading => info.phase = Phase::Ready,
            // Stopped/crashed/removed during load: abandon the lifecycle.
            _ => return,
        }
    }
    let _ = tx
        .send(make_event(
            EngineEventKind::Ready,
            "engine ready to serve",
            None,
        ))
        .await;

    // METRICS
    if config.emit_metrics {
        let _ = tx
            .send(make_event(
                EngineEventKind::Metrics,
                "initial metrics sample",
                Some(synth_metrics()),
            ))
            .await;
    }

    // Simulated crash.
    if let Some(delay) = config.fail_after {
        tokio::time::sleep(delay).await;
        let detail = "simulated crash (fail_after elapsed)";
        let should_emit = {
            let mut st = state.lock().unwrap();
            match st.handles.get_mut(&id) {
                Some(info) if info.phase == Phase::Ready => {
                    info.phase = Phase::Crashed;
                    info.crash_detail = Some(detail.to_string());
                    true
                }
                _ => false,
            }
        };
        if should_emit {
            let _ = tx
                .send(make_event(EngineEventKind::Error, detail, None))
                .await;
        }
    }
}

/// Deterministic, plausible metrics sample (no randomness, no GPU).
fn synth_metrics() -> EngineMetrics {
    EngineMetrics {
        prefill_tok_s: 850.0,
        decode_tok_s: 42.5,
        ram_used_gb: 12.0,
        vram_used_gb: 22.0,
        queue_depth: 3,
        accepted_tokens_ratio: 0.7,
    }
}

fn make_event(kind: EngineEventKind, detail: &str, metrics: Option<EngineMetrics>) -> EngineEvent {
    EngineEvent {
        kind: kind as i32,
        detail: detail.to_string(),
        metrics,
    }
}

fn role_tag(role: Role) -> &'static str {
    match role {
        Role::Host => "host",
        Role::Worker => "worker",
        _ => "node",
    }
}
