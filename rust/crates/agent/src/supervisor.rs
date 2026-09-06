//! Engine process supervision.
//!
//! Owns the lifecycle of an inference engine started via
//! `AgentService::start_engine`. It drives an [`EngineBackend`] (from
//! `purser-engine-adapter`) and:
//!   * spawns the engine with the requested model / role / layer-range / peers,
//!   * merges the backend's `EngineEvent`s (LOADING → READY → METRICS, ERROR)
//!     into a single stream returned to the control plane,
//!   * tracks the running handle so `stop_engine` can terminate it (graceful
//!     stop with a grace window, then a conceptual forced kill),
//!   * **restarts crashed engines with exponential backoff + jitter**, capping
//!     consecutive failures to detect a crash loop and park the node DEGRADED,
//!   * surfaces the latest [`EngineMetrics`] and node state for the `Health`
//!     stream.
//!
//! The backend is chosen at boot from a [`BackendRegistry`] (only `mock` is
//! wired today); because the supervisor is written against the
//! [`EngineBackend`] trait object, adding a real llama.cpp / DwarfStar adapter
//! later needs no change here.
//!
//! Kept deliberately separate from the gRPC layer so the transport stays a thin
//! adapter over the supervisor's state.

use std::collections::HashMap;
use std::net::SocketAddr;
use std::sync::atomic::{AtomicBool, AtomicU64, Ordering};
use std::sync::{Arc, Mutex};
use std::time::{Duration, Instant};

use purser_engine_adapter::{
    EngineBackend, EngineError, EngineEvent, EngineEventKind, EngineHandle, EngineMetrics,
    EngineParams, EventStream, MockEngine, Role,
};
use purser_proto::v1::NodeState;
use tokio::sync::mpsc;
use tokio::sync::Notify;

use crate::mock_inference::MockInferenceServer;
use crate::state::NodeStateMachine;

/// Capacity of the merged event channel handed to the RPC caller.
const OUT_CHANNEL_CAP: usize = 64;

/// A factory that builds a fresh backend instance on demand.
pub type BackendBuilder = Arc<dyn Fn() -> Arc<dyn EngineBackend> + Send + Sync>;

/// Runtime registry of selectable engine backends, keyed by a short name
/// (`"mock"`, later `"llamacpp"`, `"dwarfstar"`, ...).
///
/// This is the seam for adding real adapters: register a builder and the
/// supervisor can drive it unchanged.
#[derive(Clone, Default)]
pub struct BackendRegistry {
    builders: HashMap<String, BackendBuilder>,
}

impl BackendRegistry {
    /// An empty registry.
    pub fn new() -> Self {
        Self::default()
    }

    /// A registry pre-populated with the built-in GPU-free `mock` backend and,
    /// when the crate is compiled with `--features llamacpp`, the real
    /// llama.cpp backend.
    pub fn with_builtins() -> Self {
        let mut reg = Self::new();
        reg.register("mock", || Arc::new(MockEngine::new()));
        #[cfg(feature = "llamacpp")]
        reg.register("llamacpp", || {
            Arc::new(purser_adapter_llamacpp::LlamaCppBackend::default())
        });
        reg
    }

    /// Register `builder` under `name`, replacing any existing entry.
    pub fn register<F>(&mut self, name: impl Into<String>, builder: F)
    where
        F: Fn() -> Arc<dyn EngineBackend> + Send + Sync + 'static,
    {
        self.builders.insert(name.into(), Arc::new(builder));
    }

    /// Instantiate the backend registered under `name`, if any.
    pub fn build(&self, name: &str) -> Option<Arc<dyn EngineBackend>> {
        self.builders.get(name).map(|b| b())
    }

    /// Names of all registered backends (sorted for stable output).
    pub fn names(&self) -> Vec<String> {
        let mut names: Vec<String> = self.builders.keys().cloned().collect();
        names.sort();
        names
    }
}

/// Build a human-readable error message for an unrecognised (or feature-gated)
/// backend name.
///
/// When the binary was *not* compiled with `--features llamacpp` and the caller
/// requests the `llamacpp` backend, the generic "unknown engine backend" message
/// would be confusing — this returns the specific compilation hint instead.
pub fn backend_error_msg(name: &str, registry: &BackendRegistry) -> String {
    #[cfg(not(feature = "llamacpp"))]
    if name == "llamacpp" {
        return "llama.cpp backend requested but binary was not compiled with \
                --features llamacpp"
            .to_string();
    }
    format!(
        "unknown engine backend {:?}; known: {:?}",
        name,
        registry.names()
    )
}

/// What to start and how, distilled from a `StartEngineRequest`.
#[derive(Clone, Debug)]
pub struct EngineSpec {
    /// Model to load (opaque reference the backend understands).
    pub model_ref: String,
    /// Whether this node hosts the pipeline or serves a worker layer-range.
    pub role: Role,
    /// First layer served (workers only).
    pub layer_start: u32,
    /// Last layer served, inclusive (workers only).
    pub layer_end: u32,
    /// Peer worker addresses (host only).
    pub peers: Vec<String>,
    /// Address a worker binds to for the host to connect.
    pub bind_addr: String,
    /// Engine tuning parameters (host only).
    pub params: EngineParams,
    /// Resolved on-disk GGUF path from the agent's model cache. `None` if the
    /// cache did not hold the artifact at request time; the backend falls back to
    /// locating the model itself. Populated by `AgentSvc::start_engine` before
    /// the spec reaches the supervisor.
    pub model_path: Option<std::path::PathBuf>,
}

impl EngineSpec {
    /// A minimal worker spec (handy in tests).
    pub fn worker(model_ref: impl Into<String>, layer_start: u32, layer_end: u32) -> Self {
        Self {
            model_ref: model_ref.into(),
            role: Role::Worker,
            layer_start,
            layer_end,
            peers: Vec::new(),
            bind_addr: "127.0.0.1:0".to_string(),
            params: EngineParams::default(),
            model_path: None,
        }
    }
}

/// Restart / backoff policy for a supervised engine.
#[derive(Clone, Debug)]
pub struct RestartPolicy {
    /// Backoff for the first restart.
    pub initial: Duration,
    /// Upper bound on any single backoff interval.
    pub max: Duration,
    /// Growth factor per consecutive failure.
    pub multiplier: f64,
    /// Fraction (`0.0..=1.0`) of the interval subtracted at random — "full
    /// jitter" below the capped delay, to avoid thundering-herd restarts.
    pub jitter: f64,
    /// Consecutive failures tolerated before giving up and parking the node
    /// DEGRADED (crash-loop detection). `None` = retry forever.
    pub max_consecutive_failures: Option<u32>,
    /// How long an engine must stay READY before a subsequent crash is treated
    /// as isolated (resetting the consecutive-failure counter) rather than part
    /// of a crash loop.
    pub stability_window: Duration,
    /// Cadence at which the fallback metrics poll runs when the backend's event
    /// stream has gone quiet after READY.
    pub monitor_interval: Duration,
    /// Grace period a graceful stop is given before escalating to a forced kill.
    pub stop_grace: Duration,
}

impl Default for RestartPolicy {
    fn default() -> Self {
        Self {
            initial: Duration::from_millis(200),
            max: Duration::from_secs(30),
            multiplier: 2.0,
            jitter: 0.3,
            max_consecutive_failures: Some(8),
            stability_window: Duration::from_secs(30),
            monitor_interval: Duration::from_secs(5),
            stop_grace: Duration::from_secs(5),
        }
    }
}

impl RestartPolicy {
    /// Jittered delay for a given 0-based `attempt` (attempt 0 = first restart).
    pub fn delay(&self, attempt: u32) -> Duration {
        let base = backoff_base(self.initial, self.max, self.multiplier, attempt);
        apply_jitter(base, self.jitter)
    }
}

/// Deterministic (jitter-free) exponential backoff — pure and unit-testable.
///
/// `attempt` is 0-based: `attempt == 0` yields `initial`, capped at `max`.
pub fn backoff_base(initial: Duration, max: Duration, multiplier: f64, attempt: u32) -> Duration {
    let secs = initial.as_secs_f64() * multiplier.powi(attempt as i32);
    let capped = secs.min(max.as_secs_f64());
    Duration::from_secs_f64(capped.max(0.0))
}

/// Subtract up to `jitter` (a `0.0..=1.0` fraction) of `base` at random.
fn apply_jitter(base: Duration, jitter: f64) -> Duration {
    let jitter = jitter.clamp(0.0, 1.0);
    if jitter == 0.0 {
        return base;
    }
    let span = base.as_secs_f64() * jitter;
    let low = base.as_secs_f64() - span;
    let val = low + fastrand::f64() * span;
    Duration::from_secs_f64(val.max(0.0))
}

/// Coarse phase of the currently-supervised engine.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum EnginePhase {
    /// No engine has been started (or the last was stopped).
    Idle,
    /// Starting / loading weights.
    Loading,
    /// Reached READY, serving.
    Running,
    /// Crashed; a restart is scheduled.
    Crashed,
    /// Cleanly stopped.
    Stopped,
    /// Gave up after a crash loop; parked DEGRADED.
    Failed,
}

/// State shared between the supervise task, `stop`, and the `Health` readers.
struct Shared {
    latest_metrics: Mutex<Option<EngineMetrics>>,
    handle: Mutex<Option<EngineHandle>>,
    phase: Mutex<EnginePhase>,
    /// The OpenAI-compatible mock inference server standing behind a HOST
    /// engine, when the `mock` backend is in use. `None` for workers, real
    /// backends, or before the first HOST start.
    inference: Mutex<Option<MockInferenceServer>>,
    /// Client-facing serving endpoint of the running HOST engine, if any.
    endpoint: Mutex<Option<String>>,
    /// Best-effort fast-wake for sleeping supervise loops; `stopping` is the
    /// authoritative signal, re-checked at every loop boundary.
    wake: Notify,
    stopping: AtomicBool,
    /// Bumped on each `start` so a superseded supervise task exits.
    generation: AtomicU64,
}

impl Default for Shared {
    fn default() -> Self {
        Self {
            latest_metrics: Mutex::new(None),
            handle: Mutex::new(None),
            phase: Mutex::new(EnginePhase::Idle),
            inference: Mutex::new(None),
            endpoint: Mutex::new(None),
            wake: Notify::new(),
            stopping: AtomicBool::new(false),
            generation: AtomicU64::new(0),
        }
    }
}

/// Supervises a single engine instance on this node.
pub struct Supervisor {
    backend: Arc<dyn EngineBackend>,
    policy: RestartPolicy,
    shared: Arc<Shared>,
    /// Authoritative node state machine, driven by engine lifecycle events.
    machine: Option<Arc<Mutex<NodeStateMachine>>>,
    /// When `Some(port)`, a HOST start also stands up the in-process
    /// OpenAI-compatible [`MockInferenceServer`] on `0.0.0.0:<port>` and reports
    /// its address as the serving endpoint. Set only for the GPU-free `mock`
    /// backend; real backends (`None`) serve their own endpoint process.
    mock_inference_port: Option<u16>,
}

/// Outcome of pumping a backend's event stream.
enum PumpOutcome {
    /// Reached READY, then the stream closed — engine presumed alive; monitor it.
    Ready,
    /// Saw an ERROR (or closed before READY) — restart per policy.
    Crashed(String),
    /// A stop was requested, or the consumer went away — end supervision.
    End,
}

impl Supervisor {
    /// Supervise `backend` under `policy`, without a shared state machine
    /// (engine-driven node-state transitions are skipped — handy in tests).
    pub fn new(backend: Arc<dyn EngineBackend>, policy: RestartPolicy) -> Arc<Self> {
        Self::build(backend, policy, None, None)
    }

    /// As [`Supervisor::new`], but wired to drive `machine` on lifecycle events.
    pub fn with_state_machine(
        backend: Arc<dyn EngineBackend>,
        policy: RestartPolicy,
        machine: Arc<Mutex<NodeStateMachine>>,
    ) -> Arc<Self> {
        Self::build(backend, policy, Some(machine), None)
    }

    /// As [`Supervisor::with_state_machine`], but a HOST start also stands up the
    /// in-process mock OpenAI inference server on `inference_port` (for the
    /// GPU-free `mock` backend, which has no serving process of its own).
    pub fn with_mock_inference(
        backend: Arc<dyn EngineBackend>,
        policy: RestartPolicy,
        machine: Arc<Mutex<NodeStateMachine>>,
        inference_port: u16,
    ) -> Arc<Self> {
        Self::build(backend, policy, Some(machine), Some(inference_port))
    }

    fn build(
        backend: Arc<dyn EngineBackend>,
        policy: RestartPolicy,
        machine: Option<Arc<Mutex<NodeStateMachine>>>,
        mock_inference_port: Option<u16>,
    ) -> Arc<Self> {
        Arc::new(Self {
            backend,
            policy,
            shared: Arc::new(Shared::default()),
            machine,
            mock_inference_port,
        })
    }

    /// The most recent metrics sample observed from the engine, if any.
    pub fn latest_metrics(&self) -> Option<EngineMetrics> {
        *self.shared.latest_metrics.lock().unwrap()
    }

    /// Current engine phase.
    pub fn phase(&self) -> EnginePhase {
        *self.shared.phase.lock().unwrap()
    }

    /// Node state implied purely by the engine phase (composed with enrollment /
    /// drain state by the caller).
    pub fn engine_node_state(&self) -> NodeState {
        match self.phase() {
            EnginePhase::Idle | EnginePhase::Stopped => NodeState::Ready,
            EnginePhase::Loading => NodeState::Loading,
            EnginePhase::Running => NodeState::Running,
            EnginePhase::Crashed | EnginePhase::Failed => NodeState::Degraded,
        }
    }

    /// Id of the currently-tracked engine handle, if one is running.
    pub fn current_handle_id(&self) -> Option<String> {
        self.shared
            .handle
            .lock()
            .unwrap()
            .as_ref()
            .map(|h| h.id().to_string())
    }

    /// Client-facing serving endpoint of the running HOST engine, if any
    /// (`http://<host>:<inference_port>` for the mock backend). `None` for
    /// workers or when no engine is running.
    pub fn serving_endpoint(&self) -> Option<String> {
        self.shared.endpoint.lock().unwrap().clone()
    }

    /// Start (and thereafter supervise) an engine, returning a receiver of the
    /// merged lifecycle event stream. Starting again supersedes any prior engine.
    pub fn start(self: &Arc<Self>, spec: EngineSpec) -> mpsc::Receiver<EngineEvent> {
        let (tx, rx) = mpsc::channel::<EngineEvent>(OUT_CHANNEL_CAP);
        let generation = self.shared.generation.fetch_add(1, Ordering::SeqCst) + 1;
        self.shared.stopping.store(false, Ordering::SeqCst);
        *self.shared.handle.lock().unwrap() = None;
        self.set_phase(EnginePhase::Loading);

        let this = Arc::clone(self);
        tokio::spawn(async move { this.supervise(generation, spec, tx).await });
        rx
    }

    /// Stop the supervised engine. Idempotent. Attempts a graceful stop, then
    /// escalates to a forced kill (conceptual on backends without a process) if
    /// the engine does not settle within the grace window.
    pub async fn stop(&self, handle_id: &str) -> String {
        self.shared.stopping.store(true, Ordering::SeqCst);
        self.shared.wake.notify_waiters();

        let handle = self.shared.handle.lock().unwrap().clone();
        let Some(handle) = handle else {
            // No engine, but a HOST inference server may still be up — tear it
            // down so the port is never leaked.
            self.shutdown_inference().await;
            self.set_phase(EnginePhase::Stopped);
            return "no engine running".to_string();
        };
        if !handle_id.is_empty() && handle_id != handle.id() {
            tracing::warn!(
                requested = %handle_id,
                current = %handle.id(),
                "stop_engine handle mismatch; stopping the current engine"
            );
        }

        // Graceful stop (conceptually SIGTERM).
        let status = match self.backend.stop(&handle).await {
            Ok(s) => s,
            Err(e) => format!("stop error: {e}"),
        };

        // Escalation: give the engine a grace window to actually settle. If it
        // has not, escalate to a forced stop (conceptually SIGKILL).
        tokio::select! {
            _ = self.await_stopped(&handle) => {}
            _ = tokio::time::sleep(self.policy.stop_grace) => {
                tracing::warn!(
                    handle = %handle.id(),
                    grace_ms = self.policy.stop_grace.as_millis() as u64,
                    "engine did not stop within grace window; escalating to forced kill (conceptual)"
                );
                let _ = self.backend.stop(&handle).await;
            }
        }

        self.shutdown_inference().await;
        self.set_phase(EnginePhase::Stopped);
        *self.shared.handle.lock().unwrap() = None;
        status
    }

    /// Poll until the engine handle is no longer serving metrics.
    async fn await_stopped(&self, handle: &EngineHandle) {
        loop {
            match self.backend.metrics(handle).await {
                Err(EngineError::Unavailable(_))
                | Err(EngineError::UnknownHandle(_))
                | Err(EngineError::Crashed(_)) => return,
                _ => tokio::time::sleep(Duration::from_millis(20)).await,
            }
        }
    }

    /// The supervise loop: (re)start the engine and restart on crash per policy.
    async fn supervise(
        self: Arc<Self>,
        generation: u64,
        spec: EngineSpec,
        out: mpsc::Sender<EngineEvent>,
    ) {
        let mut attempt: u32 = 0;

        loop {
            if self.superseded(generation) || self.stopping() {
                break;
            }

            self.set_phase(EnginePhase::Loading);
            self.drive_state(NodeState::Loading);

            let mut ready_at: Option<Instant> = None;
            let restart = match self.spawn_engine(&spec).await {
                Ok((handle, events)) => {
                    *self.shared.handle.lock().unwrap() = Some(handle.clone());
                    match self.pump(events, &out).await {
                        PumpOutcome::Ready => {
                            ready_at = Some(Instant::now());
                            // Monitor via metrics polling for backends whose
                            // event stream goes quiet after READY.
                            let crashed = self.monitor(generation, &handle, &out).await;
                            if self.superseded(generation) || self.stopping() {
                                break;
                            }
                            crashed
                        }
                        PumpOutcome::Crashed(detail) => {
                            if self.superseded(generation) || self.stopping() {
                                break;
                            }
                            tracing::warn!(
                                model = %spec.model_ref,
                                %detail,
                                attempt,
                                "engine crashed; scheduling restart"
                            );
                            true
                        }
                        PumpOutcome::End => break,
                    }
                }
                Err(e) => {
                    let _ = out.send(err_event(&format!("start failed: {e}"))).await;
                    if self.superseded(generation) || self.stopping() {
                        break;
                    }
                    tracing::warn!(
                        model = %spec.model_ref,
                        error = %e,
                        attempt,
                        "engine start failed; scheduling restart"
                    );
                    true
                }
            };

            if !restart {
                break;
            }

            // A crash after a stable run is isolated, not a loop: reset the
            // consecutive-failure counter so backoff starts fresh.
            let was_stable = ready_at
                .map(|t| t.elapsed() >= self.policy.stability_window)
                .unwrap_or(false);
            if was_stable {
                attempt = 0;
            }

            // Crash-loop detection.
            if let Some(cap) = self.policy.max_consecutive_failures {
                if attempt + 1 >= cap {
                    self.set_phase(EnginePhase::Failed);
                    self.drive_state(NodeState::Degraded);
                    // The engine is dead for good — tear down any HOST inference
                    // server so it does not keep answering behind a failed node.
                    self.shutdown_inference().await;
                    let _ = out
                        .send(err_event(&format!(
                            "giving up after {} consecutive failures (crash loop)",
                            attempt + 1
                        )))
                        .await;
                    break;
                }
            }

            self.set_phase(EnginePhase::Crashed);
            self.drive_state(NodeState::Degraded);

            let delay = self.policy.delay(attempt);
            let _ = out
                .send(loading_event(&format!(
                    "restarting in {} ms (attempt {})",
                    delay.as_millis(),
                    attempt + 1
                )))
                .await;

            // Interruptible backoff sleep.
            tokio::select! {
                _ = tokio::time::sleep(delay) => {}
                _ = self.shared.wake.notified() => {}
            }
            if self.stopping() || self.superseded(generation) {
                break;
            }

            attempt += 1;
        }

        if self.stopping() {
            self.set_phase(EnginePhase::Stopped);
            self.drive_state(NodeState::Ready);
        }
        // Dropping `out` ends the caller's stream.
    }

    /// Start the engine appropriate to the spec's role.
    async fn spawn_engine(
        &self,
        spec: &EngineSpec,
    ) -> purser_engine_adapter::Result<(EngineHandle, EventStream)> {
        match spec.role {
            Role::Worker => {
                let w = self
                    .backend
                    .start_worker(
                        spec.layer_start,
                        spec.layer_end,
                        &spec.model_ref,
                        &spec.bind_addr,
                    )
                    .await?;
                Ok((w.handle, w.events))
            }
            // Host (and Unspecified, defaulting to host) coordinate the pipeline.
            _ => {
                let h = self
                    .backend
                    .start_host(&spec.model_ref, &spec.peers, spec.params.clone())
                    .await?;

                // For the GPU-free `mock` backend, `start_host` returns a
                // synthetic endpoint but nothing actually listens. Stand up a
                // real OpenAI-compatible server so the gateway can proxy a live
                // chat; the reported endpoint becomes its root address.
                let endpoint = self
                    .start_mock_inference(&spec.model_ref)
                    .await
                    .unwrap_or(h.endpoint);
                *self.shared.endpoint.lock().unwrap() = Some(endpoint.clone());
                tracing::info!(endpoint = %endpoint, "engine host serving");
                Ok((h.handle, h.events))
            }
        }
    }

    /// Forward the backend's events to `out`, capturing phase & metrics, until
    /// the stream closes or an ERROR is seen.
    async fn pump(&self, mut events: EventStream, out: &mpsc::Sender<EngineEvent>) -> PumpOutcome {
        let mut saw_ready = false;
        loop {
            tokio::select! {
                maybe = events.recv() => match maybe {
                    Some(ev) => {
                        if let Some(m) = ev.metrics {
                            self.set_metrics(m);
                        }
                        match kind_of(ev.kind) {
                            EngineEventKind::Loading => self.set_phase(EnginePhase::Loading),
                            EngineEventKind::Ready => {
                                saw_ready = true;
                                self.set_phase(EnginePhase::Running);
                                self.drive_state(NodeState::Running);
                            }
                            EngineEventKind::Error => {
                                self.set_phase(EnginePhase::Crashed);
                                let detail = ev.detail.clone();
                                if out.send(ev).await.is_err() {
                                    return PumpOutcome::End;
                                }
                                return PumpOutcome::Crashed(detail);
                            }
                            _ => {}
                        }
                        if out.send(ev).await.is_err() {
                            return PumpOutcome::End;
                        }
                    }
                    None => break,
                },
                _ = self.shared.wake.notified() => {
                    if self.stopping() {
                        return PumpOutcome::End;
                    }
                }
            }
        }
        if saw_ready {
            PumpOutcome::Ready
        } else {
            PumpOutcome::Crashed("event stream closed before READY".to_string())
        }
    }

    /// Poll metrics as a liveness check after the event stream has gone quiet.
    /// Returns `true` if the engine crashed (restart needed), `false` on a clean
    /// stop / supersession.
    async fn monitor(
        &self,
        generation: u64,
        handle: &EngineHandle,
        out: &mpsc::Sender<EngineEvent>,
    ) -> bool {
        loop {
            if self.superseded(generation) || self.stopping() {
                return false;
            }
            tokio::select! {
                _ = tokio::time::sleep(self.policy.monitor_interval) => {}
                _ = self.shared.wake.notified() => {}
            }
            if self.superseded(generation) || self.stopping() {
                return false;
            }
            match self.backend.metrics(handle).await {
                Ok(m) => {
                    self.set_metrics(m);
                    // Best-effort periodic METRICS event; ignore a full/closed channel.
                    let _ = out.try_send(metrics_event(m));
                }
                Err(EngineError::NotReady(_)) => { /* transient, keep polling */ }
                Err(EngineError::Unavailable(_)) => return false, // stopped externally
                Err(EngineError::Crashed(detail)) => {
                    self.set_phase(EnginePhase::Crashed);
                    let _ = out
                        .send(err_event(&format!("engine crashed: {detail}")))
                        .await;
                    return true;
                }
                Err(e) => {
                    let _ = out
                        .send(err_event(&format!("metrics unavailable: {e}")))
                        .await;
                    return true;
                }
            }
        }
    }

    /// Stand up (or restart) the mock OpenAI inference server for a HOST start,
    /// returning the client-facing endpoint. `None` when mock inference is not
    /// enabled (worker / real backend) or if the port could not be bound (the
    /// caller then falls back to the backend's own endpoint).
    async fn start_mock_inference(&self, model_ref: &str) -> Option<String> {
        let port = self.mock_inference_port?;

        // Free any previous server first so a restart can rebind the same port.
        self.shutdown_inference().await;

        let bind = SocketAddr::from(([0, 0, 0, 0], port));
        match MockInferenceServer::start(bind, model_ref.to_string()).await {
            Ok(server) => {
                // Serve OpenAI paths at the root; advertise a reachable
                // loopback host on the bound port (matches the control plane's
                // `http://<host>:<inference_port>` assumption).
                let endpoint = format!("http://127.0.0.1:{}", server.addr().port());
                *self.shared.inference.lock().unwrap() = Some(server);
                Some(endpoint)
            }
            Err(e) => {
                tracing::warn!(
                    error = %e,
                    port,
                    "failed to start mock inference server; falling back to backend endpoint"
                );
                None
            }
        }
    }

    /// Shut down the mock inference server if one is running, awaiting the
    /// serving task so the port is released. Idempotent.
    async fn shutdown_inference(&self) {
        let server = self.shared.inference.lock().unwrap().take();
        if let Some(mut server) = server {
            server.shutdown().await;
        }
        *self.shared.endpoint.lock().unwrap() = None;
    }

    // ---- small shared-state helpers ----------------------------------------

    fn set_phase(&self, phase: EnginePhase) {
        *self.shared.phase.lock().unwrap() = phase;
    }

    fn set_metrics(&self, metrics: EngineMetrics) {
        *self.shared.latest_metrics.lock().unwrap() = Some(metrics);
    }

    fn stopping(&self) -> bool {
        self.shared.stopping.load(Ordering::SeqCst)
    }

    fn superseded(&self, generation: u64) -> bool {
        self.shared.generation.load(Ordering::SeqCst) != generation
    }

    /// Advance the shared node state machine, if present. Guard rejections are
    /// logged, not fatal — the engine phase in `shared` remains authoritative
    /// for metrics.
    fn drive_state(&self, to: NodeState) {
        if let Some(machine) = &self.machine {
            let mut sm = machine.lock().unwrap();
            if let Err(e) = sm.transition(to) {
                tracing::debug!(%e, "state machine rejected engine-driven transition");
            }
        }
    }
}

/// Decode a raw proto discriminant into [`EngineEventKind`].
fn kind_of(raw: i32) -> EngineEventKind {
    EngineEventKind::try_from(raw).unwrap_or_default()
}

fn event(kind: EngineEventKind, detail: &str, metrics: Option<EngineMetrics>) -> EngineEvent {
    EngineEvent {
        kind: kind as i32,
        detail: detail.to_string(),
        metrics,
    }
}

fn err_event(detail: &str) -> EngineEvent {
    event(EngineEventKind::Error, detail, None)
}

fn loading_event(detail: &str) -> EngineEvent {
    event(EngineEventKind::Loading, detail, None)
}

fn metrics_event(metrics: EngineMetrics) -> EngineEvent {
    event(EngineEventKind::Metrics, "metrics sample", Some(metrics))
}

#[cfg(test)]
mod tests {
    use super::*;
    use purser_engine_adapter::MockConfig;
    use std::time::Duration;

    fn drain_kind(ev: &EngineEvent) -> EngineEventKind {
        kind_of(ev.kind)
    }

    // Deterministic backoff grows geometrically and is capped.
    #[test]
    fn backoff_base_is_geometric_and_capped() {
        let initial = Duration::from_millis(100);
        let max = Duration::from_secs(1);
        assert_eq!(
            backoff_base(initial, max, 2.0, 0),
            Duration::from_millis(100)
        );
        assert_eq!(
            backoff_base(initial, max, 2.0, 1),
            Duration::from_millis(200)
        );
        assert_eq!(
            backoff_base(initial, max, 2.0, 2),
            Duration::from_millis(400)
        );
        assert_eq!(
            backoff_base(initial, max, 2.0, 3),
            Duration::from_millis(800)
        );
        // Capped at max.
        assert_eq!(backoff_base(initial, max, 2.0, 4), Duration::from_secs(1));
        assert_eq!(backoff_base(initial, max, 2.0, 20), Duration::from_secs(1));
    }

    // Jitter stays within [base*(1-j), base] and never exceeds base.
    #[test]
    fn jitter_stays_within_bounds() {
        let base = Duration::from_millis(1000);
        for _ in 0..1000 {
            let d = apply_jitter(base, 0.3);
            assert!(d <= base, "jitter must not exceed base: {d:?}");
            assert!(
                d >= Duration::from_millis(700),
                "jitter must not drop below base*(1-j): {d:?}"
            );
        }
        // Zero jitter is identity.
        assert_eq!(apply_jitter(base, 0.0), base);
    }

    #[test]
    fn registry_builds_mock() {
        let reg = BackendRegistry::with_builtins();
        assert!(reg.build("mock").is_some());
        assert!(reg.build("nope").is_none());
    }

    /// `mock` must always be present regardless of which features are compiled in.
    #[test]
    fn test_mock_always_registered() {
        let reg = BackendRegistry::with_builtins();
        assert!(
            reg.build("mock").is_some(),
            "mock backend must always be registered"
        );
        assert!(
            reg.names().contains(&"mock".to_string()),
            "mock must appear in names()"
        );
    }

    /// When compiled *with* `--features llamacpp`, the registry must contain the
    /// llamacpp backend.
    #[cfg(feature = "llamacpp")]
    #[test]
    fn test_llamacpp_registered_with_feature() {
        let reg = BackendRegistry::with_builtins();
        assert!(
            reg.build("llamacpp").is_some(),
            "llamacpp backend must be registered when compiled with --features llamacpp"
        );
        assert!(
            reg.names().contains(&"llamacpp".to_string()),
            "llamacpp must appear in names() when feature is active"
        );
    }

    /// Without the llamacpp feature, requesting that backend must produce the
    /// helpful compilation-hint message rather than the generic "unknown backend".
    #[cfg(not(feature = "llamacpp"))]
    #[test]
    fn test_unknown_backend_error_message() {
        let reg = BackendRegistry::with_builtins();
        // llamacpp is not registered when the feature is absent.
        assert!(
            reg.build("llamacpp").is_none(),
            "llamacpp must not be registered without --features llamacpp"
        );
        let msg = backend_error_msg("llamacpp", &reg);
        assert!(
            msg.contains("--features llamacpp"),
            "error message should mention the missing feature flag; got: {msg}"
        );
    }

    // Happy path: start -> READY -> metrics -> stop.
    #[tokio::test]
    async fn start_reaches_ready_metrics_then_stops() {
        let sup = Supervisor::new(Arc::new(MockEngine::new()), RestartPolicy::default());
        let mut rx = sup.start(EngineSpec::worker("test/model", 0, 3));

        let mut saw_loading = false;
        let mut saw_ready = false;
        // Collect the startup burst.
        for _ in 0..8 {
            match tokio::time::timeout(Duration::from_secs(2), rx.recv()).await {
                Ok(Some(ev)) => match drain_kind(&ev) {
                    EngineEventKind::Loading => saw_loading = true,
                    EngineEventKind::Ready => saw_ready = true,
                    _ => {}
                },
                _ => break,
            }
            if saw_ready {
                break;
            }
        }
        assert!(saw_loading, "expected a LOADING event");
        assert!(saw_ready, "expected a READY event");

        // Metrics captured from the READY burst.
        // Give the METRICS event a moment to be processed.
        tokio::time::sleep(Duration::from_millis(50)).await;
        assert!(
            sup.latest_metrics().is_some(),
            "supervisor should have captured metrics"
        );
        assert_eq!(sup.engine_node_state(), NodeState::Running);

        let handle_id = sup.current_handle_id().unwrap_or_default();
        let status = sup.stop(&handle_id).await;
        assert!(!status.is_empty());
        assert_eq!(sup.phase(), EnginePhase::Stopped);
        assert_eq!(sup.engine_node_state(), NodeState::Ready);
    }

    // Crash → restart with backoff, capped by crash-loop detection.
    #[tokio::test]
    async fn crash_triggers_restart_then_gives_up() {
        // Mock that always crashes shortly after READY.
        let backend = Arc::new(MockEngine::with_config(MockConfig {
            load_delay: Duration::from_millis(5),
            fail_after: Some(Duration::from_millis(10)),
            emit_metrics: true,
            ..MockConfig::default()
        }));
        // Tight, fast policy: give up after 3 consecutive failures.
        let policy = RestartPolicy {
            initial: Duration::from_millis(5),
            max: Duration::from_millis(20),
            multiplier: 2.0,
            jitter: 0.0,
            max_consecutive_failures: Some(3),
            stability_window: Duration::from_secs(60),
            monitor_interval: Duration::from_millis(50),
            stop_grace: Duration::from_millis(50),
        };
        let sup = Supervisor::new(backend, policy);
        let mut rx = sup.start(EngineSpec::worker("crash/model", 0, 1));

        let mut loading = 0;
        let mut errors = 0;
        let mut gave_up = false;
        let deadline = tokio::time::Instant::now() + Duration::from_secs(10);
        while tokio::time::Instant::now() < deadline {
            match tokio::time::timeout(Duration::from_secs(2), rx.recv()).await {
                Ok(Some(ev)) => match drain_kind(&ev) {
                    EngineEventKind::Loading => loading += 1,
                    EngineEventKind::Error => {
                        errors += 1;
                        if ev.detail.contains("crash loop") {
                            gave_up = true;
                        }
                    }
                    _ => {}
                },
                // Stream ended (task exited after giving up) or timed out.
                Ok(None) => break,
                Err(_) => break,
            }
        }

        assert!(
            loading >= 2,
            "expected at least one restart (LOADING); got {loading}"
        );
        assert!(errors >= 2, "expected repeated crash ERRORs; got {errors}");
        assert!(gave_up, "expected a crash-loop give-up event");
        assert_eq!(sup.phase(), EnginePhase::Failed);
        assert_eq!(sup.engine_node_state(), NodeState::Degraded);
    }

    // The supervisor drives the shared node state machine on lifecycle events.
    #[tokio::test]
    async fn drives_node_state_machine() {
        let machine = Arc::new(Mutex::new(NodeStateMachine::starting_at(NodeState::Ready)));
        let sup = Supervisor::with_state_machine(
            Arc::new(MockEngine::new()),
            RestartPolicy::default(),
            Arc::clone(&machine),
        );
        let mut rx = sup.start(EngineSpec::worker("test/model", 0, 3));

        // Wait until READY drives the machine to RUNNING.
        let deadline = tokio::time::Instant::now() + Duration::from_secs(3);
        loop {
            if machine.lock().unwrap().current() == NodeState::Running {
                break;
            }
            if tokio::time::Instant::now() > deadline {
                panic!("state machine never reached RUNNING");
            }
            let _ = tokio::time::timeout(Duration::from_millis(100), rx.recv()).await;
        }
        assert_eq!(machine.lock().unwrap().current(), NodeState::Running);
    }

    // A HOST start with mock inference wired stands up a real, reachable OpenAI
    // server at the reported endpoint, and `stop` tears it down (no port leak).
    #[tokio::test]
    async fn host_start_serves_and_stops_mock_inference() {
        use std::net::SocketAddr;
        use tokio::net::TcpStream;

        let machine = Arc::new(Mutex::new(NodeStateMachine::starting_at(NodeState::Ready)));
        // Ephemeral port (0) so the test never fights over :8000.
        let sup = Supervisor::with_mock_inference(
            Arc::new(MockEngine::new()),
            RestartPolicy::default(),
            Arc::clone(&machine),
            0,
        );

        let host_spec = EngineSpec {
            model_ref: "test/host-model".to_string(),
            role: Role::Host,
            layer_start: 0,
            layer_end: 0,
            peers: Vec::new(),
            bind_addr: "0.0.0.0:0".to_string(),
            params: EngineParams::default(),
            model_path: None,
        };
        let mut rx = sup.start(host_spec);

        // Wait for the serving endpoint to be published (set during spawn).
        let deadline = tokio::time::Instant::now() + Duration::from_secs(3);
        let endpoint = loop {
            if let Some(ep) = sup.serving_endpoint() {
                break ep;
            }
            if tokio::time::Instant::now() > deadline {
                panic!("host never published a serving endpoint");
            }
            let _ = tokio::time::timeout(Duration::from_millis(50), rx.recv()).await;
        };
        assert!(
            endpoint.starts_with("http://127.0.0.1:"),
            "endpoint should be a root OpenAI address, got {endpoint}"
        );

        // The reported endpoint is actually listening.
        let addr: SocketAddr = endpoint
            .trim_start_matches("http://")
            .parse()
            .expect("endpoint host:port");
        assert!(
            TcpStream::connect(addr).await.is_ok(),
            "server should be listening at {addr}"
        );

        let handle_id = sup.current_handle_id().unwrap_or_default();
        let _ = sup.stop(&handle_id).await;
        assert!(
            sup.serving_endpoint().is_none(),
            "endpoint should be cleared on stop"
        );
        assert!(
            TcpStream::connect(addr).await.is_err(),
            "server should no longer be listening after stop"
        );
    }
}
