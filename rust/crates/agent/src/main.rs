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
use purser_agent::secrets::{self, EncryptedFileSecretStore, InMemorySecretStore, SecretStore};
use purser_agent::service::{AgentHeartbeatSource, AgentSvc};
use purser_agent::state::NodeStateMachine;
use purser_agent::supervisor::{backend_error_msg, BackendRegistry, RestartPolicy, Supervisor};
use purser_agent::swim;

#[tokio::main]
async fn main() -> anyhow::Result<()> {
    init_tracing();

    let config = AgentConfig::from_env().context("loading agent configuration")?;
    let config = Arc::new(config);

    // ── Model cache ──────────────────────────────────────────────────────────
    // The cache resolves logical model refs (e.g. "llama-3.1-8b:Q4_K_M") to
    // local GGUF file paths before StartEngine reaches the engine adapter.
    //
    // Cache directory: PURSER_MODEL_CACHE_DIR (default: ~/.purser/model-cache).
    // Cache budget:    PURSER_MODEL_CACHE_MAX_BYTES (default: 50 GiB).
    //
    // When http-fetch is enabled the HttpFetcher is used (with proxy/CA-bundle
    // settings from config); otherwise the FileMirrorFetcher copies from a
    // rack-local NFS/mounted mirror (PURSER_MODEL_MIRROR_DIR, default: same as
    // cache dir, effectively a no-op until a mirror is configured).
    let model_cache: Option<Arc<purser_agent::modelcache::ModelCache>> = {
        let cache_dir: std::path::PathBuf = std::env::var("PURSER_MODEL_CACHE_DIR")
            .map(std::path::PathBuf::from)
            .unwrap_or_else(|_| {
                std::env::var_os("HOME")
                    .map(|h| {
                        std::path::PathBuf::from(h)
                            .join(".purser")
                            .join("model-cache")
                    })
                    .unwrap_or_else(|| std::path::PathBuf::from("/var/lib/purser/model-cache"))
            });

        let max_bytes: u64 = std::env::var("PURSER_MODEL_CACHE_MAX_BYTES")
            .ok()
            .and_then(|v| v.parse().ok())
            .unwrap_or(50 * 1024 * 1024 * 1024); // 50 GiB

        #[cfg(not(feature = "http-fetch"))]
        let fetcher: Box<dyn purser_agent::modelcache::Fetcher> = {
            let mirror_root = std::env::var("PURSER_MODEL_MIRROR_DIR")
                .map(std::path::PathBuf::from)
                .ok();
            Box::new(purser_agent::modelcache::FileMirrorFetcher {
                mirror_root,
            })
        };

        #[cfg(feature = "http-fetch")]
        let fetcher: Box<dyn purser_agent::modelcache::Fetcher> = {
            let client = purser_agent::http_client::build_http_client(&config)
                .context("building HTTP client for model fetcher")?;
            Box::new(purser_agent::modelcache::HttpFetcher::with_client(
                client,
                config.model_fetch_max_retries,
            ))
        };

        match purser_agent::modelcache::ModelCache::open(&cache_dir, max_bytes, fetcher).await {
            Ok(cache) => {
                tracing::info!(
                    dir = %cache_dir.display(),
                    max_bytes,
                    "model cache opened"
                );
                Some(Arc::new(cache))
            }
            Err(e) => {
                tracing::warn!(
                    error = %e,
                    dir = %cache_dir.display(),
                    "failed to open model cache; StartEngine will skip path resolution"
                );
                None
            }
        }
    };

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

    // Engine backend selection. Backends registered at compile time:
    //   - `mock`    — always (GPU-free in-process mock)
    //   - `llamacpp`— only when compiled with `--features llamacpp`
    // Set PURSER_ENGINE_BACKEND to choose; defaults to `mock`.
    let registry = BackendRegistry::with_builtins();
    let backend_name =
        std::env::var("PURSER_ENGINE_BACKEND").unwrap_or_else(|_| "mock".to_string());

    // Emit a clear, actionable message when llamacpp is requested but the
    // binary was compiled without the feature (generic "unknown backend" would
    // be confusing in that case).
    let backend = registry
        .build(&backend_name)
        .with_context(|| backend_error_msg(&backend_name, &registry))?;

    // Warn when the llamacpp backend is active but its binary directory is not
    // configured — the adapter will fall back to searching PATH, which may not
    // find the binaries on a fresh node.
    #[cfg(feature = "llamacpp")]
    if backend_name == "llamacpp" && std::env::var("PURSER_LLAMACPP_BIN").is_err() {
        tracing::warn!(
            "PURSER_ENGINE_BACKEND=llamacpp but PURSER_LLAMACPP_BIN is not set; \
             llama.cpp binaries (rpc-server, llama-server) will be searched in PATH"
        );
    }
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

    let probe = Arc::new(
        DefaultProbe::with_backends(node_id.clone(), registry.names())
            .with_prefix_caching_factor(config.prefix_caching_factor),
    );
    let svc = AgentSvc::new(
        Arc::clone(&probe) as Arc<dyn HardwareProbe>,
        Arc::clone(&config),
        Arc::clone(&supervisor),
        Arc::clone(&machine),
        model_cache,
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

    // Encrypted-at-rest secret storage. The store directory and optional
    // explicit key are sourced from the environment; if no key is set a random
    // one is auto-generated and persisted so secrets survive restarts.
    let secret_store: Arc<dyn SecretStore> = {
        let dir = &config.secret_store_dir;
        match EncryptedFileSecretStore::from_env_or_generate(dir) {
            Ok(store) => {
                tracing::info!(
                    dir = %dir.display(),
                    "encrypted file secret store initialised"
                );
                Arc::new(store)
            }
            Err(e) => {
                tracing::warn!(
                    error = %e,
                    dir = %dir.display(),
                    "encrypted secret store unavailable, falling back to in-memory (secrets will not persist)"
                );
                Arc::new(InMemorySecretStore::new())
            }
        }
    };

    // Shutdown broadcast: sent when Ctrl-C fires so SWIM and other
    // gracefully-shutdown tasks can stop cleanly.
    let (swim_shutdown_tx, swim_shutdown_rx) = tokio::sync::watch::channel(false);

    // Initial peer discovery (seeds + mDNS) into a membership view (best-effort).
    let membership = {
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
        let membership_for_spawn = Arc::clone(&membership);
        tokio::spawn(async move {
            let peers = discovery::discover_peers(&seeds, Duration::from_secs(2)).await;
            for peer in peers {
                membership_for_spawn.observe(peer);
            }
            tracing::info!(
                known = membership_for_spawn.len(),
                alive = membership_for_spawn.alive().len(),
                "initial peer discovery complete"
            );
        });
        membership
    };

    // SWIM gossip layer (opt-in, gated by PURSER_SWIM_ENABLED=true).
    {
        let swim_seeds: Vec<std::net::SocketAddr> = config
            .swim_seed_addrs
            .iter()
            .filter_map(|s| {
                s.parse().map_err(|e| {
                    tracing::warn!(addr = %s, error = %e, "ignoring unparseable SWIM seed address");
                }).ok()
            })
            .collect();

        let bind = config.swim_bind_addr;
        let enabled = config.swim_enabled;
        // Derive the gRPC address to embed in the SWIM identity.  The same
        // address is later sent to the control plane at Join, so peers that
        // learn about us via gossip already have the correct dial target.
        // `advertised_addrs()` resolves wildcards (0.0.0.0 → primary LAN IP)
        // and honours `PURSER_AGENT_ADVERTISED_ADDR` overrides.
        let grpc_addr: SocketAddr = {
            let (agent_str, _) = config.advertised_addrs();
            agent_str.parse::<SocketAddr>().unwrap_or(config.bind_addr)
        };
        let membership_swim = Arc::clone(&membership);
        let shutdown_rx = swim_shutdown_rx.clone();
        tokio::spawn(async move {
            match swim::start(
                enabled,
                bind,
                grpc_addr,
                swim_seeds,
                membership_swim,
                shutdown_rx,
            )
            .await
            {
                Ok(()) => {
                    if enabled {
                        // start() returned Ok after spawning the background tasks — nothing
                        // more to do in this wrapper task.
                    }
                }
                Err(e) => {
                    tracing::warn!(
                        error = %e,
                        "SWIM gossip layer failed to start; falling back to mDNS + seed discovery"
                    );
                }
            }
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
                        let mut sm =
                            machine_for_task.lock().unwrap_or_else(|p| p.into_inner());
                        let _ = sm.enrolled();
                        let _ = sm.ready();
                    }
                    // H6: reconnect loop with exponential backoff so a transient
                    // control-plane outage does not permanently sever heartbeating.
                    let node_id_for_hb = enrollment.node_id;
                    let mut hb_delay = Duration::from_secs(1);
                    loop {
                        match discovery::run_heartbeat(
                            &cp_addr,
                            node_id_for_hb.clone(),
                            Arc::clone(&hb_source),
                            health_interval,
                        )
                        .await
                        {
                            Ok(()) => {
                                tracing::info!("heartbeat stream ended gracefully");
                                break;
                            }
                            Err(e) => {
                                tracing::warn!(
                                    error = %e,
                                    retry_in = ?hb_delay,
                                    "heartbeat disconnected, retrying"
                                );
                                tokio::time::sleep(hb_delay).await;
                                hb_delay = (hb_delay * 2).min(Duration::from_secs(60));
                            }
                        }
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

    // Compose the shutdown future so SWIM tasks also receive the signal.
    let graceful_shutdown = async move {
        shutdown_signal().await;
        let _ = swim_shutdown_tx.send(true);
    };

    Server::builder()
        .add_service(AgentServiceServer::new(svc))
        .serve_with_shutdown(config.bind_addr, graceful_shutdown)
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
