//! `AgentService` gRPC implementation.
//!
//! This is the single point of contact between the control plane and the
//! machine. It executes, observes and reports — it never plans. The transport
//! is a thin adapter: hardware description comes from [`HardwareProbe`], engine
//! lifecycle from the [`Supervisor`], link measurement from the [`LinkBencher`],
//! and the node's lifecycle state from the [`NodeStateMachine`].

use std::net::TcpListener;
use std::pin::Pin;
use std::sync::{Arc, Mutex};

use purser_proto::v1::agent_service_server::AgentService;
use purser_proto::v1::{
    DrainReply, DrainRequest, EngineEvent, EngineMetrics, HardwareProfile, HealthReport,
    HealthRequest, LinkMetric, LinkRequest, NodeState, ProbeRequest, Role, StartEngineRequest,
    StopEngineRequest, StopReply, UpdateReply, UpdateRequest,
};
use tokio_stream::wrappers::ReceiverStream;
use tokio_stream::{Stream, StreamExt};
use tonic::{Request, Response, Status};

use crate::config::AgentConfig;
use crate::discovery::HeartbeatSource;
use crate::healing::{RejectingVerifier, UpdateVerifier};
use crate::linkbench::LinkBencher;
use crate::probe::HardwareProbe;
use crate::state::NodeStateMachine;
use crate::supervisor::{EngineSpec, Supervisor};

/// A boxed server-streaming response, the shape tonic expects for the streaming
/// RPCs on this service.
type ResponseStream<T> = Pin<Box<dyn Stream<Item = Result<T, Status>> + Send>>;

/// Concrete `AgentService` handler wired with its collaborators.
pub struct AgentSvc {
    probe: Arc<dyn HardwareProbe>,
    config: Arc<AgentConfig>,
    supervisor: Arc<Supervisor>,
    machine: Arc<Mutex<NodeStateMachine>>,
    bencher: Arc<LinkBencher>,
}

impl AgentSvc {
    /// Assemble the service from its collaborators.
    pub fn new(
        probe: Arc<dyn HardwareProbe>,
        config: Arc<AgentConfig>,
        supervisor: Arc<Supervisor>,
        machine: Arc<Mutex<NodeStateMachine>>,
    ) -> Self {
        let from_node = config
            .node_id
            .clone()
            .unwrap_or_else(|| config.cluster_id.clone());
        let bencher = Arc::new(LinkBencher::with_tcp_probe(from_node));
        Self {
            probe,
            config,
            supervisor,
            machine,
            bencher,
        }
    }

    /// Compose the reported node state: authoritative lifecycle states
    /// (provisioning / enrolled / draining / unreachable / decommissioned) win;
    /// otherwise the engine-driven substate (ready / loading / running /
    /// degraded) is reported.
    pub fn composed_state(&self) -> NodeState {
        composed_node_state(&self.machine, &self.supervisor)
    }
}

/// See [`AgentSvc::composed_state`]. Free function so heartbeat/health share it.
pub fn composed_node_state(
    machine: &Arc<Mutex<NodeStateMachine>>,
    supervisor: &Arc<Supervisor>,
) -> NodeState {
    let base = machine.lock().unwrap().current();
    match base {
        NodeState::Provisioning
        | NodeState::Enrolled
        | NodeState::Draining
        | NodeState::Unreachable
        | NodeState::Decommissioned => base,
        _ => supervisor.engine_node_state(),
    }
}

/// Snapshots node state + engine metrics for the heartbeat stream.
pub struct AgentHeartbeatSource {
    machine: Arc<Mutex<NodeStateMachine>>,
    supervisor: Arc<Supervisor>,
}

impl AgentHeartbeatSource {
    /// Build a heartbeat source over the shared state machine + supervisor.
    pub fn new(machine: Arc<Mutex<NodeStateMachine>>, supervisor: Arc<Supervisor>) -> Self {
        Self {
            machine,
            supervisor,
        }
    }
}

impl HeartbeatSource for AgentHeartbeatSource {
    fn snapshot(&self) -> (NodeState, Option<EngineMetrics>) {
        (
            composed_node_state(&self.machine, &self.supervisor),
            self.supervisor.latest_metrics(),
        )
    }
}

#[tonic::async_trait]
impl AgentService for AgentSvc {
    // ---- Probe: REAL --------------------------------------------------------

    async fn probe(
        &self,
        _request: Request<ProbeRequest>,
    ) -> Result<Response<HardwareProfile>, Status> {
        let mut profile = self.probe.probe();
        // Stamp the profile with the node's live lifecycle state.
        profile.state = self.composed_state() as i32;
        tracing::debug!(
            hostname = %profile.hostname,
            ram_total_gb = profile.ram_total_gb,
            gpus = profile.gpus.len(),
            "probe served"
        );
        Ok(Response::new(profile))
    }

    // ---- BenchmarkLink: REAL ------------------------------------------------
    // Measures RTT + bandwidth to the first requested target. The proto returns
    // a single LinkMetric; targets beyond the first are measured lazily on
    // subsequent calls (results are amortized + cached per target).
    async fn benchmark_link(
        &self,
        request: Request<LinkRequest>,
    ) -> Result<Response<LinkMetric>, Status> {
        let targets = request.into_inner().target_nodes;
        let Some(primary) = targets.first() else {
            return Err(Status::invalid_argument("no target_nodes to benchmark"));
        };
        // Warm the moving average for the remaining targets, best-effort.
        if targets.len() > 1 {
            let _ = self.bencher.benchmark_all(&targets[1..]).await;
        }
        match self.bencher.benchmark(primary).await {
            Ok(metric) => Ok(Response::new(metric)),
            Err(e) => Err(Status::unavailable(format!(
                "link benchmark to {primary} failed: {e}"
            ))),
        }
    }

    // ---- StartEngine: REAL --------------------------------------------------
    // Drives the supervisor, which starts the selected backend and streams real
    // lifecycle events (LOADING -> READY -> METRICS, ERROR + restart on crash).
    type StartEngineStream = ResponseStream<EngineEvent>;

    async fn start_engine(
        &self,
        request: Request<StartEngineRequest>,
    ) -> Result<Response<Self::StartEngineStream>, Status> {
        let req = request.into_inner();
        let role = Role::try_from(req.role).unwrap_or(Role::Unspecified);
        tracing::info!(model_ref = %req.model_ref, ?role, "start_engine requested");

        // Fast-forward the lifecycle to READY for a locally-driven start so the
        // supervisor's LOADING/RUNNING transitions are valid.
        {
            let mut sm = self.machine.lock().unwrap();
            if sm.current() == NodeState::Provisioning {
                let _ = sm.enrolled();
            }
            if sm.current() == NodeState::Enrolled {
                let _ = sm.ready();
            }
        }

        // Allocate a free TCP port for the engine: bind to port 0 so the OS
        // assigns a free port, read it back, then drop the listener so the
        // engine can bind the same port itself.  There is a small TOCTOU
        // window between releasing the listener and the engine binding, but
        // this is standard practice for port allocation in daemon code.
        let port = {
            let listener = TcpListener::bind("0.0.0.0:0")
                .map_err(|e| Status::internal(format!("failed to allocate engine port: {e}")))?;
            listener
                .local_addr()
                .map_err(|e| {
                    Status::internal(format!("failed to read allocated engine port: {e}"))
                })?
                .port()
            // listener dropped here — port is free for the engine to bind
        };
        tracing::debug!(%port, "allocated engine bind port");

        let spec = EngineSpec {
            model_ref: req.model_ref,
            role,
            layer_start: req.layer_start,
            layer_end: req.layer_end,
            peers: req.peers,
            bind_addr: format!("0.0.0.0:{port}"),
            params: req.params.unwrap_or_default(),
        };

        let rx = self.supervisor.start(spec);
        let stream = ReceiverStream::new(rx).map(Ok::<_, Status>);
        Ok(Response::new(Box::pin(stream)))
    }

    // ---- StopEngine: REAL ---------------------------------------------------
    async fn stop_engine(
        &self,
        request: Request<StopEngineRequest>,
    ) -> Result<Response<StopReply>, Status> {
        let handle = request.into_inner().handle;
        tracing::info!(%handle, "stop_engine requested");
        let status = self.supervisor.stop(&handle).await;
        Ok(Response::new(StopReply { status }))
    }

    // ---- Health: REAL -------------------------------------------------------
    // Streams the composed node state + live engine metrics at the configured
    // cadence.
    type HealthStream = ResponseStream<HealthReport>;

    async fn health(
        &self,
        _request: Request<HealthRequest>,
    ) -> Result<Response<Self::HealthStream>, Status> {
        let period = self.config.health_interval;
        let machine = Arc::clone(&self.machine);
        let supervisor = Arc::clone(&self.supervisor);

        let stream = async_stream::stream! {
            let mut ticker = tokio::time::interval(period);
            loop {
                ticker.tick().await;
                let state = composed_node_state(&machine, &supervisor);
                let metrics = supervisor.latest_metrics().unwrap_or_default();
                yield Ok::<_, Status>(HealthReport {
                    state: state as i32,
                    metrics: Some(metrics),
                });
            }
        };

        Ok(Response::new(Box::pin(stream)))
    }

    // ---- Drain: REAL --------------------------------------------------------
    // Quiesce: flip to DRAINING and stop the running engine.
    async fn drain(&self, _request: Request<DrainRequest>) -> Result<Response<DrainReply>, Status> {
        tracing::info!("drain requested");
        {
            let mut sm = self.machine.lock().unwrap();
            if let Err(e) = sm.draining() {
                tracing::warn!(%e, "drain transition rejected");
            }
        }
        let _ = self.supervisor.stop("").await;
        Ok(Response::new(DrainReply {}))
    }

    // ---- UpdateAgent: interface wired, verification gate closed -------------
    // Self-update requires signature verification against a pinned key, which is
    // not yet configured, so we decline safely (see `healing::UpdateVerifier`).
    async fn update_agent(
        &self,
        request: Request<UpdateRequest>,
    ) -> Result<Response<UpdateReply>, Status> {
        let req = request.into_inner();
        // The verifier refuses by default; never proceed without verification.
        let verified = RejectingVerifier
            .verify(req.version.as_bytes(), &req.signature)
            .is_ok();
        if !verified {
            tracing::warn!(version = %req.version, "update_agent declined: signature not verified");
        }
        Ok(Response::new(UpdateReply { accepted: verified }))
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::probe::DefaultProbe;
    use crate::supervisor::RestartPolicy;
    use purser_engine_adapter::MockEngine;
    use purser_proto::v1::EngineEventKind;

    fn svc() -> AgentSvc {
        let machine = Arc::new(Mutex::new(NodeStateMachine::starting_at(NodeState::Ready)));
        let supervisor = Supervisor::with_state_machine(
            Arc::new(MockEngine::new()),
            RestartPolicy::default(),
            Arc::clone(&machine),
        );
        AgentSvc::new(
            Arc::new(DefaultProbe::new("unit-test-node")),
            Arc::new(AgentConfig::default()),
            supervisor,
            machine,
        )
    }

    #[tokio::test]
    async fn probe_rpc_returns_real_profile() {
        let resp = svc()
            .probe(Request::new(ProbeRequest {}))
            .await
            .expect("probe rpc");
        let profile = resp.into_inner();

        assert_eq!(profile.node_id, "unit-test-node");
        assert!(!profile.hostname.is_empty());
        assert!(profile.ram_total_gb > 0.0);
        // With a Ready machine and no engine, the composed state is READY.
        assert_eq!(profile.state, NodeState::Ready as i32);
    }

    #[tokio::test]
    async fn benchmark_link_rejects_empty_targets() {
        let err = svc()
            .benchmark_link(Request::new(LinkRequest {
                target_nodes: vec![],
            }))
            .await
            .expect_err("empty targets must be rejected");
        assert_eq!(err.code(), tonic::Code::InvalidArgument);
    }

    #[tokio::test]
    async fn start_then_stop_engine_streams_events() {
        let s = svc();
        let resp = s
            .start_engine(Request::new(StartEngineRequest {
                model_ref: "test/model".into(),
                role: Role::Worker as i32,
                layer_start: 0,
                layer_end: 3,
                peers: vec![],
                quantization: String::new(),
                draft: false,
                params: None,
            }))
            .await
            .expect("start_engine");
        let mut stream = resp.into_inner();

        let mut saw_ready = false;
        for _ in 0..8 {
            match tokio::time::timeout(std::time::Duration::from_secs(2), stream.next()).await {
                Ok(Some(Ok(ev))) => {
                    if EngineEventKind::try_from(ev.kind).unwrap_or_default()
                        == EngineEventKind::Ready
                    {
                        saw_ready = true;
                        break;
                    }
                }
                _ => break,
            }
        }
        assert!(saw_ready, "expected a READY event from the engine stream");

        let stopped = s
            .stop_engine(Request::new(StopEngineRequest {
                handle: String::new(),
            }))
            .await
            .expect("stop_engine")
            .into_inner();
        assert!(!stopped.status.is_empty());
    }

    #[tokio::test]
    async fn update_agent_declines_unverified() {
        let upd = svc()
            .update_agent(Request::new(UpdateRequest {
                version: "9.9.9".into(),
                url: String::new(),
                signature: vec![],
            }))
            .await
            .expect("update_agent")
            .into_inner();
        assert!(!upd.accepted);
    }

    #[tokio::test]
    async fn drain_transitions_to_draining() {
        let s = svc();
        s.drain(Request::new(DrainRequest {})).await.expect("drain");
        assert_eq!(s.machine.lock().unwrap().current(), NodeState::Draining);
    }

    /// Verify the port-allocation mechanism used by `start_engine`:
    /// binding on port 0 yields a valid, OS-assigned, non-privileged port,
    /// and dropping the listener before the engine binds is the standard
    /// TOCTOU trade-off for daemon port allocation.
    #[test]
    fn start_engine_allocates_nonzero_port() {
        let listener =
            TcpListener::bind("0.0.0.0:0").expect("TcpListener::bind should succeed on loopback");
        let port = listener.local_addr().expect("local_addr").port();
        drop(listener); // release before the engine would bind
        assert!(
            port > 1024,
            "OS-allocated engine port {port} must be in the non-privileged range (> 1024)"
        );
    }
}
