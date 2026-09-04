//! Agent runtime configuration.
//!
//! The agent is deliberately "dumb and reliable": all of its knobs are simple,
//! flat values loaded from the environment with sane defaults so the daemon can
//! boot on a fresh node with zero configuration. Orchestration decisions are
//! made by the control plane, never here.

use std::net::SocketAddr;
use std::time::Duration;

use anyhow::{Context, Result};

/// The default port `AgentService` listens on for control-plane traffic.
pub const DEFAULT_AGENT_PORT: u16 = 50151;

/// The default port a HOST engine serves OpenAI-compatible inference on.
///
/// Must match the control plane's `DefaultInferencePort`: the control plane
/// advertises a deployment host as `http://<host>:8000` and the gateway proxies
/// chat requests to `{endpoint}/v1/chat/completions`, so the node has to be
/// listening here for a real chat to reach the engine.
pub const DEFAULT_INFERENCE_PORT: u16 = 8000;

/// Static configuration for a running agent.
#[derive(Debug, Clone)]
pub struct AgentConfig {
    /// Socket address `AgentService` (gRPC) binds to.
    pub bind_addr: SocketAddr,

    /// Address of the control plane's `RegistrationService`, used to enroll and
    /// heartbeat. `None` until provisioning wires it in.
    ///
    /// TODO(phase2): consumed by discovery/enrollment (see `discovery.rs`).
    pub control_plane_addr: Option<String>,

    /// Logical cluster this node belongs to.
    pub cluster_id: String,

    /// Stable node identity. Empty/`None` before the control plane assigns one
    /// during `RegistrationService::Join`.
    pub node_id: Option<String>,

    /// One-time join token used to enroll into the cluster.
    ///
    /// TODO(phase2): used by the enrollment flow (see `discovery.rs`).
    pub join_token: Option<String>,

    /// Cadence at which `Health` streams `HealthReport`s.
    pub health_interval: Duration,

    /// Port a HOST engine serves OpenAI-compatible inference on
    /// (`POST /v1/chat/completions`, `/v1/completions`, `GET /v1/models`).
    ///
    /// Defaults to [`DEFAULT_INFERENCE_PORT`] to match the control plane's
    /// advertised host endpoint. Overridable via `PURSER_INFERENCE_PORT`.
    pub inference_port: u16,
}

impl Default for AgentConfig {
    fn default() -> Self {
        Self {
            bind_addr: SocketAddr::from(([0, 0, 0, 0], DEFAULT_AGENT_PORT)),
            control_plane_addr: None,
            cluster_id: "default".to_string(),
            node_id: None,
            join_token: None,
            health_interval: Duration::from_secs(5),
            inference_port: DEFAULT_INFERENCE_PORT,
        }
    }
}

impl AgentConfig {
    /// Build a config from the environment, falling back to [`AgentConfig::default`]
    /// for any variable that is unset.
    ///
    /// Recognized variables:
    /// - `PURSER_AGENT_BIND`           — e.g. `0.0.0.0:50151`
    /// - `PURSER_CONTROL_PLANE_ADDR`   — e.g. `https://cp.internal:50150`
    /// - `PURSER_CLUSTER_ID`
    /// - `PURSER_NODE_ID`
    /// - `PURSER_JOIN_TOKEN`
    /// - `PURSER_HEALTH_INTERVAL_SECS`
    /// - `PURSER_INFERENCE_PORT`         — e.g. `8000`
    pub fn from_env() -> Result<Self> {
        let mut cfg = AgentConfig::default();

        if let Ok(bind) = std::env::var("PURSER_AGENT_BIND") {
            cfg.bind_addr = bind
                .parse()
                .with_context(|| format!("invalid PURSER_AGENT_BIND: {bind:?}"))?;
        }
        cfg.control_plane_addr = non_empty(std::env::var("PURSER_CONTROL_PLANE_ADDR").ok());
        if let Some(cluster) = non_empty(std::env::var("PURSER_CLUSTER_ID").ok()) {
            cfg.cluster_id = cluster;
        }
        cfg.node_id = non_empty(std::env::var("PURSER_NODE_ID").ok());
        cfg.join_token = non_empty(std::env::var("PURSER_JOIN_TOKEN").ok());
        if let Ok(secs) = std::env::var("PURSER_HEALTH_INTERVAL_SECS") {
            let secs: u64 = secs
                .parse()
                .with_context(|| format!("invalid PURSER_HEALTH_INTERVAL_SECS: {secs:?}"))?;
            cfg.health_interval = Duration::from_secs(secs.max(1));
        }
        if let Ok(port) = std::env::var("PURSER_INFERENCE_PORT") {
            cfg.inference_port = port
                .parse()
                .with_context(|| format!("invalid PURSER_INFERENCE_PORT: {port:?}"))?;
        }

        Ok(cfg)
    }
}

/// Treat empty strings as absent — avoids `Some("")` surprises from the env.
fn non_empty(v: Option<String>) -> Option<String> {
    v.filter(|s| !s.trim().is_empty())
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn default_is_sane() {
        let cfg = AgentConfig::default();
        assert_eq!(cfg.bind_addr.port(), DEFAULT_AGENT_PORT);
        assert_eq!(cfg.cluster_id, "default");
        assert!(cfg.node_id.is_none());
        assert!(cfg.health_interval.as_secs() >= 1);
        assert_eq!(cfg.inference_port, DEFAULT_INFERENCE_PORT);
    }

    #[test]
    fn non_empty_filters_blanks() {
        assert_eq!(non_empty(Some("  ".into())), None);
        assert_eq!(non_empty(Some("x".into())), Some("x".into()));
        assert_eq!(non_empty(None), None);
    }
}
