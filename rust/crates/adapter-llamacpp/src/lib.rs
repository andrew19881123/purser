//! # Purser llama.cpp adapter
//!
//! A real [`EngineBackend`](purser_engine_adapter::EngineBackend) that drives
//! llama.cpp's RPC deployment model, translating Purser's abstract engine
//! commands into concrete `rpc-server` / `llama-server` process launches.
//!
//! ## Deployment shape
//!
//! - A **worker** runs `rpc-server -H <ip> -p <port>` and offers *compute only*.
//!   llama.cpp assigns layers to workers from the host based on available memory,
//!   so the adapter does not pass a layer range to `rpc-server` (it validates the
//!   range for the abstract contract but the host owns the actual sharding).
//! - A **host** runs `llama-server -m <model.gguf> --rpc <ip:port,...> -ngl 99
//!   --host 0.0.0.0 --port <p> -c <ctx>`: it loads the GGUF and shards its layers
//!   across the workers.
//!
//! ## Security: the RPC compute port is not sandboxed
//!
//! `rpc-server` executes graphs it receives over the wire and has **no**
//! sandbox. The adapter therefore refuses to start a worker on the wildcard
//! address (`0.0.0.0` / `::`) or on any address outside the configured trusted
//! subnet (private/loopback by default). See [`security`]. The host's own
//! OpenAI-compatible API may bind `0.0.0.0` (it is the client API, not the RPC
//! compute plane), configurable via [`config::LlamaCppConfig`].
//!
//! ## Testability without llama.cpp / a GPU
//!
//! All engine-specific *logic* is factored into pure, unit-tested functions:
//! command/flag construction ([`flags`]), metrics normalisation ([`metrics`]),
//! and GGUF discovery ([`gguf`]). Binaries are never assumed installed and are
//! only spawned when a worker/host actually starts. The full live conformance
//! run against real processes is opt-in (see `tests/backend.rs`).

pub mod config;
pub mod flags;
pub mod gguf;
pub mod metrics;
pub mod security;

use std::collections::{HashMap, VecDeque};
use std::path::Path;
use std::process::Stdio;
use std::sync::{Arc, Mutex};
use std::time::Duration;

use async_trait::async_trait;
use tokio::io::{AsyncBufReadExt, AsyncRead, BufReader, Lines};
use tokio::process::{Child, Command};
use tokio::sync::{mpsc, Notify};

use purser_engine_adapter::{
    Capabilities, EngineBackend, EngineError, EngineEvent, EngineEventKind, EngineHandle,
    EngineMetrics, EngineParams, EventStream, HostStart, Result, Role, SpecMethod, WorkerStart,
};

pub use config::LlamaCppConfig;
pub use gguf::{GgufDiscovery, GgufError, GgufMetadata};

/// Bounded capacity of each engine's lifecycle-event channel.
const EVENT_CHANNEL_CAP: usize = 64;

/// How many recent log lines to retain per instance for metrics parsing.
const LOG_TAIL_CAP: usize = 512;

/// GGUF quantization formats llama.cpp can load. Advertised via
/// [`EngineBackend::capabilities`].
const QUANT_FORMATS: &[&str] = &[
    "GGUF", "F32", "F16", "BF16", "Q8_0", "Q6_K", "Q5_K_M", "Q5_K_S", "Q5_0", "Q4_K_M", "Q4_K_S",
    "Q4_0", "Q3_K_L", "Q3_K_M", "Q3_K_S", "Q2_K", "IQ4_XS", "IQ4_NL", "IQ3_M", "IQ3_S", "IQ2_M",
    "IQ2_XS", "IQ2_XXS", "IQ1_M", "IQ1_S",
];

/// Substrings that mark a process as ready to serve (case-insensitive). Covers
/// both `rpc-server` ("RPC server listening ...") and `llama-server` ("server is
/// listening on http://...").
const READY_MARKERS: &[&str] = &[
    "listening",
    "waiting for",
    "rpc server",
    "all slots are idle",
];

/// Lifecycle phase of a single managed process.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
enum Phase {
    Loading,
    Ready,
    Stopped,
    Crashed,
}

/// Per-instance bookkeeping shared between the public API and the background
/// lifecycle task.
struct Instance {
    #[allow(dead_code)]
    role: Role,
    phase: Mutex<Phase>,
    /// Recent stdout/stderr lines, used to parse live metrics on demand.
    log_tail: Mutex<VecDeque<String>>,
    crash_detail: Mutex<Option<String>>,
    /// Fired by `stop()` to request a graceful shutdown.
    stop: Arc<Notify>,
}

#[derive(Default)]
struct Registry {
    instances: HashMap<String, Arc<Instance>>,
    next_id: u64,
}

/// A real llama.cpp [`EngineBackend`] over local processes.
pub struct LlamaCppBackend {
    config: LlamaCppConfig,
    state: Arc<Mutex<Registry>>,
}

impl LlamaCppBackend {
    /// Build a backend, resolving binary paths and trusted subnets from the
    /// environment (see [`config`]).
    pub fn new() -> Self {
        Self::with_config(LlamaCppConfig::from_env())
    }

    /// Build a backend with an explicit configuration.
    pub fn with_config(config: LlamaCppConfig) -> Self {
        Self {
            config,
            state: Arc::new(Mutex::new(Registry::default())),
        }
    }

    /// Read-only access to the active configuration.
    pub fn config(&self) -> &LlamaCppConfig {
        &self.config
    }

    /// Discover model metadata from a GGUF file (architecture, layer count,
    /// context length, quantization, MoE-ness). Independent of any running
    /// engine and safe to call without llama.cpp installed.
    pub fn inspect_gguf<P: AsRef<Path>>(&self, path: P) -> std::result::Result<GgufDiscovery, GgufError> {
        gguf::read_metadata_from_file(path).map(|m| m.discovery())
    }

    /// Register a freshly spawned process, emit its initial `LOADING`, and start
    /// its background lifecycle task. Returns the handle and event stream.
    fn register_and_run(
        &self,
        role: Role,
        child: Child,
        loading_detail: String,
    ) -> (EngineHandle, EventStream) {
        let stop = Arc::new(Notify::new());
        let instance = Arc::new(Instance {
            role,
            phase: Mutex::new(Phase::Loading),
            log_tail: Mutex::new(VecDeque::with_capacity(LOG_TAIL_CAP)),
            crash_detail: Mutex::new(None),
            stop: Arc::clone(&stop),
        });

        let id = {
            let mut st = self.state.lock().unwrap();
            st.next_id += 1;
            let id = format!("llamacpp-{}-{:04}", role_tag(role), st.next_id);
            st.instances.insert(id.clone(), Arc::clone(&instance));
            id
        };

        let (tx, rx) = mpsc::channel(EVENT_CHANNEL_CAP);
        let grace = self.config.stop_grace;
        let inst = Arc::clone(&instance);
        tokio::spawn(async move {
            let _ = tx
                .send(make_event(EngineEventKind::Loading, &loading_detail, None))
                .await;
            run_lifecycle(child, inst, stop, grace, tx).await;
        });

        (EngineHandle::new(id, role), rx)
    }

    fn lookup(&self, handle: &EngineHandle) -> Option<Arc<Instance>> {
        self.state.lock().unwrap().instances.get(handle.id()).cloned()
    }
}

impl Default for LlamaCppBackend {
    fn default() -> Self {
        Self::new()
    }
}

#[async_trait]
impl EngineBackend for LlamaCppBackend {
    fn capabilities(&self) -> Capabilities {
        Capabilities {
            // llama.cpp can mix heterogeneous backends across host/workers via RPC.
            mixed_backend: true,
            // GGUF supports mixture-of-experts architectures.
            moe: true,
            // llama.cpp offers prompt-lookup / n-gram drafting. (Generic
            // draft-model speculative decoding is also supported but has no
            // dedicated SpecMethod variant.)
            speculative: vec![SpecMethod::Ngram],
            quant_formats: QUANT_FORMATS.iter().map(|s| s.to_string()).collect(),
        }
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

        // Security gate BEFORE launching the unsandboxed compute worker.
        let addr = security::validate_worker_bind(bind_addr, &self.config.trusted_subnets)?;

        // rpc-server takes only host/port; the host assigns layers by memory.
        let args = flags::build_worker_args(&addr);
        let child = spawn_process(&self.config.rpc_server_bin, &args)?;

        let (handle, events) = self.register_and_run(
            Role::Worker,
            child,
            format!("starting rpc-server worker on {addr} for {model_ref}"),
        );
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

        let launch = flags::build_host_launch(&self.config, model_ref, worker_addrs, &params)?;
        let child = spawn_process(&self.config.llama_server_bin, &launch.args)?;

        let (handle, events) = self.register_and_run(
            Role::Host,
            child,
            format!("starting llama-server host for {model_ref}"),
        );
        Ok(HostStart {
            endpoint: launch.endpoint,
            handle,
            events,
        })
    }

    async fn stop(&self, handle: &EngineHandle) -> Result<String> {
        let Some(inst) = self.lookup(handle) else {
            return Err(EngineError::UnknownHandle(handle.id().to_string()));
        };
        let mut ph = inst.phase.lock().unwrap();
        match *ph {
            Phase::Stopped => Ok("already-stopped".to_string()),
            _ => {
                *ph = Phase::Stopped;
                drop(ph);
                // Wake the lifecycle task, which owns the child and performs the
                // SIGTERM -> SIGKILL shutdown.
                inst.stop.notify_one();
                Ok("stopping".to_string())
            }
        }
    }

    async fn metrics(&self, handle: &EngineHandle) -> Result<EngineMetrics> {
        let Some(inst) = self.lookup(handle) else {
            return Err(EngineError::UnknownHandle(handle.id().to_string()));
        };
        let phase = *inst.phase.lock().unwrap();
        match phase {
            Phase::Ready => Ok(current_metrics(&inst)),
            Phase::Loading => Err(EngineError::NotReady(format!(
                "handle {} is still loading",
                handle.id()
            ))),
            Phase::Stopped => Err(EngineError::Unavailable(format!(
                "handle {} has been stopped",
                handle.id()
            ))),
            Phase::Crashed => Err(EngineError::Crashed(
                inst.crash_detail
                    .lock()
                    .unwrap()
                    .clone()
                    .unwrap_or_else(|| "engine crashed".to_string()),
            )),
        }
    }
}

/// Spawn a llama.cpp process with piped stdout/stderr and `kill_on_drop` so a
/// dropped handle never leaks a process.
fn spawn_process(program: &Path, args: &[String]) -> Result<Child> {
    Command::new(program)
        .args(args)
        .stdin(Stdio::null())
        .stdout(Stdio::piped())
        .stderr(Stdio::piped())
        .kill_on_drop(true)
        .spawn()
        .map_err(|e| {
            EngineError::StartFailed(format!(
                "failed to spawn {} (is llama.cpp installed / PURSER_LLAMACPP_BIN set?): {e}",
                program.display()
            ))
        })
}

/// Background task: read the process's output, drive the LOADING -> READY (->
/// METRICS) transitions, handle graceful stop, and report crashes.
async fn run_lifecycle(
    mut child: Child,
    inst: Arc<Instance>,
    stop: Arc<Notify>,
    grace: Duration,
    tx: mpsc::Sender<EngineEvent>,
) {
    let mut so = child.stdout.take().map(|s| BufReader::new(s).lines());
    let mut se = child.stderr.take().map(|s| BufReader::new(s).lines());

    loop {
        tokio::select! {
            // Prefer draining output and honouring stop over noticing exit.
            biased;

            _ = stop.notified() => {
                shutdown(&mut child, grace).await;
                *inst.phase.lock().unwrap() = Phase::Stopped;
                break;
            }
            line = next_line(&mut so), if so.is_some() => {
                match line {
                    Some(l) => handle_line(&inst, &tx, l).await,
                    None => so = None,
                }
            }
            line = next_line(&mut se), if se.is_some() => {
                match line {
                    Some(l) => handle_line(&inst, &tx, l).await,
                    None => se = None,
                }
            }
            status = child.wait() => {
                on_unexpected_exit(&inst, &tx, status).await;
                break;
            }
        }
    }
}

/// Read the next line from an optional line reader. When the reader is `None`
/// the future never resolves (the branch is guarded by `is_some()`), so this is
/// only ever polled with `Some`.
async fn next_line<R: AsyncRead + Unpin>(reader: &mut Option<Lines<BufReader<R>>>) -> Option<String> {
    match reader.as_mut() {
        Some(lines) => match lines.next_line().await {
            Ok(Some(l)) => Some(l),
            // EOF or read error: signal the caller to retire this reader.
            _ => None,
        },
        None => std::future::pending().await,
    }
}

/// Handle one output line: buffer it and, if it signals readiness, transition to
/// READY and emit an initial metrics sample.
async fn handle_line(inst: &Arc<Instance>, tx: &mpsc::Sender<EngineEvent>, line: String) {
    push_tail(inst, &line);

    let became_ready = {
        let mut ph = inst.phase.lock().unwrap();
        if *ph == Phase::Loading && is_ready_marker(&line) {
            *ph = Phase::Ready;
            true
        } else {
            false
        }
    };

    if became_ready {
        let _ = tx
            .send(make_event(EngineEventKind::Ready, "engine ready to serve", None))
            .await;
        let m = current_metrics(inst);
        let _ = tx
            .send(make_event(
                EngineEventKind::Metrics,
                "initial metrics sample",
                Some(m),
            ))
            .await;
    }
}

/// The process exited without an explicit `stop()`; mark it crashed and emit an
/// ERROR (unless a stop already transitioned it to `Stopped`).
async fn on_unexpected_exit(
    inst: &Arc<Instance>,
    tx: &mpsc::Sender<EngineEvent>,
    status: std::io::Result<std::process::ExitStatus>,
) {
    {
        let mut ph = inst.phase.lock().unwrap();
        if *ph == Phase::Stopped {
            return; // expected shutdown, not a crash
        }
        *ph = Phase::Crashed;
    }
    let detail = match status {
        Ok(st) => format!("llama.cpp process exited unexpectedly ({st})"),
        Err(e) => format!("failed to wait on llama.cpp process: {e}"),
    };
    *inst.crash_detail.lock().unwrap() = Some(detail.clone());
    let _ = tx.send(make_event(EngineEventKind::Error, &detail, None)).await;
}

/// Gracefully stop a child: `SIGTERM`, wait up to `grace`, then `SIGKILL`.
async fn shutdown(child: &mut Child, grace: Duration) {
    #[cfg(unix)]
    {
        if let Some(pid) = child.id() {
            // SAFETY: kill(2) with a plain signal number on a pid we own.
            unsafe {
                libc::kill(pid as libc::pid_t, libc::SIGTERM);
            }
        }
        if tokio::time::timeout(grace, child.wait()).await.is_err() {
            let _ = child.start_kill();
            let _ = child.wait().await;
        }
    }
    #[cfg(not(unix))]
    {
        let _ = grace;
        let _ = child.start_kill();
        let _ = child.wait().await;
    }
}

fn push_tail(inst: &Arc<Instance>, line: &str) {
    let mut tail = inst.log_tail.lock().unwrap();
    if tail.len() >= LOG_TAIL_CAP {
        tail.pop_front();
    }
    tail.push_back(line.to_string());
}

fn current_metrics(inst: &Arc<Instance>) -> EngineMetrics {
    let joined = {
        let tail = inst.log_tail.lock().unwrap();
        tail.iter().cloned().collect::<Vec<_>>().join("\n")
    };
    metrics::parse_metrics(&joined)
}

fn is_ready_marker(line: &str) -> bool {
    let l = line.to_ascii_lowercase();
    READY_MARKERS.iter().any(|m| l.contains(m))
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

#[cfg(test)]
mod tests {
    use super::*;

    /// Compile-time proof that the adapter is a valid trait object: the whole
    /// point of the engine-adapter is an `Arc<dyn EngineBackend>` registry.
    const _: fn() = || {
        fn assert_backend<T: EngineBackend>() {}
        assert_backend::<LlamaCppBackend>();
    };

    fn test_config() -> LlamaCppConfig {
        // Absolute, guaranteed-missing binaries so spawn attempts fail
        // deterministically regardless of whether llama.cpp is installed.
        let mut c = LlamaCppConfig::default();
        c.rpc_server_bin = "/nonexistent/purser/rpc-server".into();
        c.llama_server_bin = "/nonexistent/purser/llama-server".into();
        c
    }

    #[test]
    fn dyn_compatible_and_capabilities_are_declared() {
        let backend: Box<dyn EngineBackend> = Box::new(LlamaCppBackend::with_config(test_config()));
        let caps = backend.capabilities();
        assert!(caps.mixed_backend);
        assert!(caps.moe);
        assert!(caps.speculative.contains(&SpecMethod::Ngram));
        assert!(caps.quant_formats.iter().any(|q| q == "Q4_K_M"));
        // Deterministic / side-effect free.
        assert_eq!(caps, backend.capabilities());
    }

    #[tokio::test]
    async fn worker_rejects_invalid_layer_range_before_spawn() {
        let b = LlamaCppBackend::with_config(test_config());
        let err = b
            .start_worker(10, 2, "m", "127.0.0.1:7000")
            .await
            .unwrap_err();
        assert!(matches!(err, EngineError::InvalidArgument(_)));
    }

    #[tokio::test]
    async fn worker_rejects_wildcard_bind_before_spawn() {
        let b = LlamaCppBackend::with_config(test_config());
        let err = b
            .start_worker(0, 10, "m", "0.0.0.0:7000")
            .await
            .unwrap_err();
        assert!(matches!(err, EngineError::InvalidArgument(_)));
    }

    #[tokio::test]
    async fn worker_rejects_untrusted_bind_before_spawn() {
        let b = LlamaCppBackend::with_config(test_config());
        let err = b
            .start_worker(0, 10, "m", "8.8.8.8:7000")
            .await
            .unwrap_err();
        assert!(matches!(err, EngineError::InvalidArgument(_)));
    }

    #[tokio::test]
    async fn host_rejects_empty_model() {
        let b = LlamaCppBackend::with_config(test_config());
        let err = b
            .start_host("", &[], EngineParams::default())
            .await
            .unwrap_err();
        assert!(matches!(err, EngineError::InvalidArgument(_)));
    }

    #[tokio::test]
    async fn missing_binary_maps_to_start_failed() {
        // Valid, trusted bind so we pass the security gate and actually attempt
        // to spawn; the missing binary must surface as StartFailed (not a panic).
        let b = LlamaCppBackend::with_config(test_config());
        let err = b
            .start_worker(0, 10, "m", "127.0.0.1:7000")
            .await
            .unwrap_err();
        assert!(matches!(err, EngineError::StartFailed(_)), "got {err:?}");
    }

    #[tokio::test]
    async fn metrics_on_unknown_handle_errors() {
        let b = LlamaCppBackend::with_config(test_config());
        let bogus = EngineHandle::new("does-not-exist", Role::Worker);
        let err = b.metrics(&bogus).await.unwrap_err();
        assert!(matches!(err, EngineError::UnknownHandle(_)));
    }

    #[tokio::test]
    async fn stop_unknown_handle_errors() {
        let b = LlamaCppBackend::with_config(test_config());
        let bogus = EngineHandle::new("nope", Role::Host);
        let err = b.stop(&bogus).await.unwrap_err();
        assert!(matches!(err, EngineError::UnknownHandle(_)));
    }

    // -----------------------------------------------------------------------
    // Live conformance (opt-in; requires a real llama.cpp build + GGUF model)
    // -----------------------------------------------------------------------

    /// Wraps [`LlamaCppBackend`] so the shared conformance suite (which uses a
    /// fixed, fake `model_ref` and a per-phase worker) can run against real
    /// llama.cpp on a single machine: it substitutes a real GGUF path and forces
    /// single-node hosting (no RPC workers).
    struct LiveBackend {
        inner: LlamaCppBackend,
        model: String,
    }

    #[async_trait::async_trait]
    impl EngineBackend for LiveBackend {
        fn capabilities(&self) -> Capabilities {
            self.inner.capabilities()
        }
        async fn start_worker(
            &self,
            layer_start: u32,
            layer_end: u32,
            _model_ref: &str,
            bind_addr: &str,
        ) -> Result<WorkerStart> {
            self.inner
                .start_worker(layer_start, layer_end, &self.model, bind_addr)
                .await
        }
        async fn start_host(
            &self,
            _model_ref: &str,
            _worker_addrs: &[String],
            params: EngineParams,
        ) -> Result<HostStart> {
            // Single-node: the suite's worker was already stopped, so load
            // llama-server standalone against the real model.
            self.inner.start_host(&self.model, &[], params).await
        }
        async fn stop(&self, handle: &EngineHandle) -> Result<String> {
            self.inner.stop(handle).await
        }
        async fn metrics(&self, handle: &EngineHandle) -> Result<EngineMetrics> {
            self.inner.metrics(handle).await
        }
    }

    /// Opt-in live conformance run against real `rpc-server` / `llama-server`
    /// processes. `#[ignore]`d by default and additionally self-skips unless the
    /// binaries and a model are configured, so it is safe to leave in the suite.
    ///
    /// To run it once llama.cpp is available:
    /// ```text
    /// export PURSER_LLAMACPP_BIN=/path/to/llama.cpp/build/bin  # rpc-server + llama-server
    /// export PURSER_LLAMACPP_MODEL=/path/to/model.gguf         # a loadable GGUF
    /// cargo test -p purser-adapter-llamacpp -- --ignored
    /// ```
    #[tokio::test]
    #[ignore = "requires a real llama.cpp build: set PURSER_LLAMACPP_BIN and PURSER_LLAMACPP_MODEL, then run with `-- --ignored`"]
    async fn live_conformance() {
        let cfg = LlamaCppConfig::from_env();
        if !cfg.binaries_present() {
            eprintln!(
                "SKIP live_conformance: rpc-server/llama-server not found. \
                 Set PURSER_LLAMACPP_BIN to the llama.cpp build/bin directory."
            );
            return;
        }
        let model = match std::env::var("PURSER_LLAMACPP_MODEL") {
            Ok(m) if !m.is_empty() => m,
            _ => {
                eprintln!(
                    "SKIP live_conformance: set PURSER_LLAMACPP_MODEL to a loadable .gguf path."
                );
                return;
            }
        };
        let backend = LiveBackend {
            inner: LlamaCppBackend::with_config(cfg),
            model,
        };
        purser_engine_adapter::conformance::conformance_tests(&backend).await;
    }
}
