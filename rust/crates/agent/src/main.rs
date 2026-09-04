//! Purser fleet agent daemon.
//!
//! A thin binary that wires the [`purser_agent`] subsystems together and serves
//! [`AgentService`](purser_proto::v1::agent_service_server) over gRPC toward the
//! control plane. All logic lives in the library crate; this binary handles
//! configuration, task orchestration, and graceful shutdown.

use std::net::SocketAddr;
use std::sync::{Arc, Mutex};
use std::time::Duration;

use anyhow::Context;
use purser_proto::v1::agent_service_server::AgentServiceServer;
use purser_proto::v1::NodeState;
use tonic::transport::Server;

use purser_agent::config::AgentConfig;
use purser_agent::discovery::{self, Membership};
use purser_agent::healing::{diagnose, DiagnosisInput, Liveness, NodeHealthMonitor};
use purser_agent::linkbench::BandwidthReflector;
use purser_agent::probe::{DefaultProbe, HardwareProbe};
use purser_agent::secrets::{self, InMemorySecretStore, SecretStore};
use purser_agent::service::{AgentHeartbeatSource, AgentSvc};
use purser_agent::state::NodeStateMachine;
use purser_agent::supervisor::{BackendRegistry, RestartPolicy, Supervisor};

#[tokio::main]
async fn main() -> anyhow::Result<()> {
    init_tracing();

    let config = AgentConfig::from_env().context("loading agent configuration")?;
    let config = Arc::new(config);

    // Security: warn if bound on all interfaces rather than a trusted subnet.
    if config.bind_addr.ip().is_unspecified() {
        tracing::warn!(
            bind_addr = %config.bind_addr,
            "binding on an unspecified address; prefer a trusted-subnet address (PURSER_AGENT_BIND)"
        );
    }

    // Node identity is empty until the control plane assigns one at enrollment.
    let node_id = config.node_id.clone().unwrap_or_default();

    // Node lifecycle state machine: a pre-assigned identity boots ENROLLED.
    let machine = Arc::new(Mutex::new(if node_id.is_empty() {
        NodeStateMachine::new()
    } else {
        NodeStateMachine::starting_at(NodeState::Enrolled)
    }));

    // Engine backend selection (only `mock` is registered today; real adapters
    // register here without changing the supervisor).
    let registry = BackendRegistry::with_builtins();
    let backend_name =
        std::env::var("PURSER_ENGINE_BACKEND").unwrap_or_else(|_| "mock".to_string());
    let backend = registry.build(&backend_name).with_context(|| {
        format!(
            "unknown engine backend {backend_name:?}; known: {:?}",
            registry.names()
        )
    })?;
    // The GPU-free `mock` backend has no serving process of its own, so a HOST
    // start must also stand up an in-process OpenAI-compatible server on the
    // inference port. Real backends serve their own endpoint — leave it unset.
    let supervisor = if backend_name == "mock" {
        Supervisor::with_mock_inference(
            backend,
            RestartPolicy::default(),
            Arc::clone(&machine),
            config.inference_port,
        )
    } else {
        Supervisor::with_state_machine(backend, RestartPolicy::default(), Arc::clone(&machine))
    };

    let probe = Arc::new(DefaultProbe::new(node_id.clone()));
    let svc = AgentSvc::new(
        Arc::clone(&probe) as Arc<dyn HardwareProbe>,
        Arc::clone(&config),
        Arc::clone(&supervisor),
        Arc::clone(&machine),
    );

    tracing::info!(
        bind_addr = %config.bind_addr,
        cluster_id = %config.cluster_id,
        engine_backend = %backend_name,
        control_plane = config.control_plane_addr.as_deref().unwrap_or("<unset>"),
        "purser-agent starting AgentService"
    );

    // Link-benchmark reflector for peers, on the agent port + 1 (best-effort).
    let reflector_addr = SocketAddr::new(
        config.bind_addr.ip(),
        config.bind_addr.port().wrapping_add(1),
    );
    match BandwidthReflector::bind(&reflector_addr.to_string()).await {
        Ok(reflector) => {
            tracing::info!(%reflector_addr, "link-benchmark reflector listening");
            tokio::spawn(reflector.serve());
        }
        Err(e) => tracing::warn!(%reflector_addr, error = %e, "failed to start link reflector"),
    }

    // Advertise over mDNS so LAN peers can discover us (best-effort).
    #[cfg(feature = "mdns")]
    let _mdns = {
        let instance = if node_id.is_empty() {
            probe.probe().hostname
        } else {
            node_id.clone()
        };
        match discovery::MdnsResponder::advertise(
            &instance,
            config.bind_addr.port(),
            &node_id,
            &config.cluster_id,
        ) {
            Ok(responder) => Some(responder),
            Err(e) => {
                tracing::warn!(error = %e, "mDNS advertisement failed");
                None
            }
        }
    };

    // Encrypted-at-rest secret storage (interface). In-memory today; the
    // enrollment certificates land here rather than in logs or plaintext files.
    let secret_store: Arc<dyn SecretStore> = Arc::new(InMemorySecretStore::new());

    // Initial peer discovery (seeds + mDNS) into a membership view (best-effort).
    {
        let mut seeds: Vec<String> = Vec::new();
        if let Some(cp) = &config.control_plane_addr {
            seeds.push(strip_scheme(cp));
        }
        if let Ok(extra) = std::env::var("PURSER_SEEDS") {
            seeds.extend(
                extra
                    .split(',')
                    .map(|s| s.trim().to_string())
                    .filter(|s| !s.is_empty()),
            );
        }
        let membership = Arc::new(Membership::new(Duration::from_secs(30)));
        tokio::spawn(async move {
            let peers = discovery::discover_peers(&seeds, Duration::from_secs(2)).await;
            for peer in peers {
                membership.observe(peer);
            }
            tracing::info!(
                known = membership.len(),
                alive = membership.alive().len(),
                "initial peer discovery complete"
            );
        });
    }

    // Enroll + heartbeat against the control plane, if configured (best-effort,
    // non-fatal: the daemon still serves AgentService if the CP is unreachable).
    if let Some(cp_addr) = config.control_plane_addr.clone() {
        let profile = probe.probe();
        let join_token = config.join_token.clone().unwrap_or_default();
        let (advertised_agent_addr, advertised_inference_addr) = config.advertised_addrs();
        let hb_source = Arc::new(AgentHeartbeatSource::new(
            Arc::clone(&machine),
            Arc::clone(&supervisor),
        ));
        let machine_for_task = Arc::clone(&machine);
        let health_interval = config.health_interval;
        let fallback_node_id = node_id.clone();
        let secret_store = Arc::clone(&secret_store);
        tokio::spawn(async move {
            // The token is a secret: log it only through the redacting wrapper.
            tracing::debug!(
                token = %secrets::Redacted::new(&join_token),
                "attempting enrollment"
            );
            match discovery::join(
                &cp_addr,
                &join_token,
                profile,
                advertised_agent_addr,
                advertised_inference_addr,
            )
            .await
            {
                Ok(enrollment) => {
                    // Persist certificates via the secret store (never logged).
                    let _ = secret_store.put("client_cert", &enrollment.client_cert);
                    let _ = secret_store.put("ca_cert", &enrollment.ca_cert);
                    {
                        let mut sm = machine_for_task.lock().unwrap();
                        let _ = sm.enrolled();
                        let _ = sm.ready();
                    }
                    if let Err(e) = discovery::run_heartbeat(
                        &cp_addr,
                        enrollment.node_id,
                        hb_source,
                        health_interval,
                    )
                    .await
                    {
                        tracing::warn!(error = %e, "heartbeat stream ended");
                    }
                }
                Err(e) => {
                    tracing::warn!(
                        node_id = %fallback_node_id,
                        error = %e,
                        "enrollment failed; serving without control-plane registration"
                    );
                }
            }
        });
    }

    // Periodic self-healing: probe control-plane reachability (driving the node
    // UNREACHABLE after repeated loss) and run a local self-diagnosis.
    if let Some(cp_addr) = config.control_plane_addr.clone() {
        let machine_h = Arc::clone(&machine);
        let supervisor_h = Arc::clone(&supervisor);
        let probe_h = Arc::clone(&probe);
        let interval = config.health_interval;
        let dial = strip_scheme(&cp_addr);
        tokio::spawn(async move {
            let mut monitor = NodeHealthMonitor::new(machine_h, 3);
            let mut ticker = tokio::time::interval(interval);
            loop {
                ticker.tick().await;
                let reachable = tokio::time::timeout(
                    Duration::from_secs(2),
                    tokio::net::TcpStream::connect(&dial),
                )
                .await
                .map(|r| r.is_ok())
                .unwrap_or(false);
                let control_plane = if reachable {
                    monitor.on_heartbeat_ack();
                    Liveness::Healthy
                } else {
                    monitor.on_heartbeat_miss()
                };

                let profile = probe_h.probe();
                let diagnosis = diagnose(&DiagnosisInput {
                    engine_phase: supervisor_h.phase(),
                    metrics: supervisor_h.latest_metrics(),
                    disk_free_gb: profile.disk_free_gb,
                    control_plane,
                    ..DiagnosisInput::default()
                });
                if !diagnosis.is_healthy() {
                    tracing::warn!(
                        findings = ?diagnosis.findings,
                        implied_state = diagnosis.implied_state.as_str_name(),
                        "self-diagnosis flagged issues"
                    );
                }
            }
        });
    }

    Server::builder()
        .add_service(AgentServiceServer::new(svc))
        .serve_with_shutdown(config.bind_addr, shutdown_signal())
        .await
        .context("AgentService gRPC server terminated with error")?;

    tracing::info!("purser-agent stopped");
    Ok(())
}

/// Initialize tracing/logging, honoring `RUST_LOG` (default: `info`).
fn init_tracing() {
    use tracing_subscriber::{fmt, EnvFilter};

    let filter = EnvFilter::try_from_default_env().unwrap_or_else(|_| EnvFilter::new("info"));
    // `try_init` so tests or repeated init never panic.
    let _ = fmt().with_env_filter(filter).try_init();
}

/// Resolve when the process receives Ctrl-C (SIGINT), triggering graceful
/// shutdown of the gRPC server.
async fn shutdown_signal() {
    if let Err(err) = tokio::signal::ctrl_c().await {
        tracing::error!(%err, "failed to listen for shutdown signal");
    }
    tracing::info!("shutdown signal received, draining");
}

/// Strip a URL scheme (`scheme://`) to leave a bare `host:port` for dialing.
fn strip_scheme(addr: &str) -> String {
    addr.split_once("://")
        .map(|(_, rest)| rest.to_string())
        .unwrap_or_else(|| addr.to_string())
}
