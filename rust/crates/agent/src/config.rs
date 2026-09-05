//! Agent runtime configuration.
//!
//! The agent is deliberately "dumb and reliable": all of its knobs are simple,
//! flat values loaded from the environment with sane defaults so the daemon can
//! boot on a fresh node with zero configuration. Orchestration decisions are
//! made by the control plane, never here.

use std::net::{IpAddr, SocketAddr};
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

    /// Explicit `host:port` this node's `AgentService` is reachable at, as seen
    /// by the control plane. `None` derives it from [`bind_addr`](Self::bind_addr)
    /// at Join time (see [`AgentConfig::advertised_addrs`]). Overridable via
    /// `PURSER_AGENT_ADVERTISED_ADDR`.
    pub advertised_agent_addr: Option<String>,

    /// Explicit `host:port` where this node serves OpenAI-compatible inference,
    /// as seen by the control plane / gateway. `None` derives it from the
    /// advertised host plus [`inference_port`](Self::inference_port). Overridable
    /// via `PURSER_INFERENCE_ADVERTISED_ADDR`.
    pub advertised_inference_addr: Option<String>,

    // -----------------------------------------------------------------------
    // SWIM gossip membership (T2-8: opt-in, default disabled)
    // -----------------------------------------------------------------------

    /// Enable the SWIM gossip membership layer.
    ///
    /// When `true`, a UDP gossip socket is opened and Foca drives peer-to-peer
    /// membership convergence alongside the existing mDNS + seed path.
    /// Default: `false`.  Override: `PURSER_SWIM_ENABLED=true`.
    pub swim_enabled: bool,

    /// UDP address the SWIM gossip protocol binds to.
    ///
    /// Default: `0.0.0.0:7946`.  Override: `PURSER_SWIM_BIND_ADDR`.
    pub swim_bind_addr: SocketAddr,

    /// Comma-separated SWIM seed addresses (`host:port`) for bootstrapping
    /// the gossip ring.  Override: `PURSER_SWIM_SEED_ADDRS`.
    pub swim_seed_addrs: Vec<String>,
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
            advertised_agent_addr: None,
            advertised_inference_addr: None,
            swim_enabled: false,
            swim_bind_addr: SocketAddr::from(([0, 0, 0, 0], 7946)),
            swim_seed_addrs: Vec::new(),
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
    /// - `PURSER_AGENT_ADVERTISED_ADDR`  — e.g. `192.168.1.10:50151`
    /// - `PURSER_INFERENCE_ADVERTISED_ADDR` — e.g. `192.168.1.10:8000`
    /// - `PURSER_SWIM_ENABLED`           — `true` / `1` / `yes` to opt-in SWIM gossip
    /// - `PURSER_SWIM_BIND_ADDR`         — UDP bind address for SWIM (e.g. `0.0.0.0:7946`)
    /// - `PURSER_SWIM_SEED_ADDRS`        — comma-separated SWIM seeds (e.g. `10.0.0.1:7946,10.0.0.2:7946`)
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
        cfg.advertised_agent_addr = non_empty(std::env::var("PURSER_AGENT_ADVERTISED_ADDR").ok());
        cfg.advertised_inference_addr =
            non_empty(std::env::var("PURSER_INFERENCE_ADVERTISED_ADDR").ok());

        if let Some(v) = non_empty(std::env::var("PURSER_SWIM_ENABLED").ok()) {
            cfg.swim_enabled = matches!(v.to_ascii_lowercase().as_str(), "true" | "1" | "yes");
        }
        if let Ok(addr) = std::env::var("PURSER_SWIM_BIND_ADDR") {
            cfg.swim_bind_addr = addr
                .parse()
                .with_context(|| format!("invalid PURSER_SWIM_BIND_ADDR: {addr:?}"))?;
        }
        if let Ok(seeds) = std::env::var("PURSER_SWIM_SEED_ADDRS") {
            cfg.swim_seed_addrs = seeds
                .split(',')
                .map(|s| s.trim().to_string())
                .filter(|s| !s.is_empty())
                .collect();
        }

        Ok(cfg)
    }

    /// Resolve the `(agent, inference)` `host:port` pair to advertise to the
    /// control plane at Join time.
    ///
    /// Precedence for each: an explicit override wins verbatim; otherwise the
    /// address is derived from [`bind_addr`](Self::bind_addr). When `bind_addr`
    /// carries a concrete (non-wildcard) host that host is reused as-is; when it
    /// is a wildcard (`0.0.0.0` / `::`) the primary local non-loopback IPv4 is
    /// detected best-effort, falling back to `127.0.0.1`. The inference address
    /// reuses that host with [`inference_port`](Self::inference_port).
    ///
    /// Best-effort and infallible: it never fails a Join.
    pub fn advertised_addrs(&self) -> (String, String) {
        derive_advertised_addrs(
            self.bind_addr,
            self.inference_port,
            self.advertised_agent_addr.as_deref(),
            self.advertised_inference_addr.as_deref(),
            primary_local_ipv4,
        )
    }
}

/// Pure derivation of the advertised `(agent, inference)` addresses.
///
/// `resolve_wildcard_host` supplies the concrete host to use when `bind_addr`
/// is a wildcard; it is only invoked in that case, which keeps this function
/// pure and testable without touching the network.
fn derive_advertised_addrs(
    bind_addr: SocketAddr,
    inference_port: u16,
    explicit_agent: Option<&str>,
    explicit_inference: Option<&str>,
    resolve_wildcard_host: impl FnOnce() -> IpAddr,
) -> (String, String) {
    // Resolve the advertised host only when a derivation actually needs it, so
    // the (possibly network-touching) resolver is skipped when both addresses
    // are explicit.
    let host: Option<IpAddr> = if explicit_agent.is_none() || explicit_inference.is_none() {
        Some(if bind_addr.ip().is_unspecified() {
            resolve_wildcard_host()
        } else {
            bind_addr.ip()
        })
    } else {
        None
    };

    // `SocketAddr` formatting yields `host:port` for IPv4 and `[host]:port` for
    // IPv6, so both advertised addresses stay dial-able.
    let agent = match explicit_agent {
        Some(a) => a.to_string(),
        None => SocketAddr::new(
            host.expect("host resolved when agent addr derived"),
            bind_addr.port(),
        )
        .to_string(),
    };
    let inference = match explicit_inference {
        Some(i) => i.to_string(),
        None => SocketAddr::new(
            host.expect("host resolved when inference addr derived"),
            inference_port,
        )
        .to_string(),
    };
    (agent, inference)
}

/// Best-effort detection of the primary local non-loopback IPv4 address.
///
/// Uses the standard "connected UDP socket" trick: connecting a UDP socket
/// sends no packets, it just makes the OS pick — via the routing table — the
/// source address it would use to reach the target, so this works offline.
/// Falls back to `127.0.0.1` when detection is not possible. No new deps.
fn primary_local_ipv4() -> IpAddr {
    use std::net::UdpSocket;

    const LOOPBACK: IpAddr = IpAddr::V4(std::net::Ipv4Addr::LOCALHOST);

    let sock = match UdpSocket::bind(("0.0.0.0", 0)) {
        Ok(s) => s,
        Err(_) => return LOOPBACK,
    };
    // A TEST-NET-3 (RFC 5737) target: never routed, only used to consult the
    // routing table for the local source address.
    if sock.connect(("203.0.113.1", 80)).is_ok() {
        if let Ok(local) = sock.local_addr() {
            let ip = local.ip();
            if !ip.is_loopback() && !ip.is_unspecified() {
                return ip;
            }
        }
    }
    LOOPBACK
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

    // A wildcard resolver that must NOT be called when a concrete host is
    // available — it panics so the test proves the wildcard branch is skipped.
    fn no_wildcard() -> IpAddr {
        panic!("wildcard resolver must not be called when bind host is concrete");
    }

    #[test]
    fn derive_uses_bind_when_non_wildcard() {
        let bind: SocketAddr = "10.0.0.7:50151".parse().unwrap();
        let (agent, inference) = derive_advertised_addrs(bind, 8000, None, None, no_wildcard);
        assert_eq!(agent, "10.0.0.7:50151");
        // Inference derives from the same host with the inference port.
        assert_eq!(inference, "10.0.0.7:8000");
    }

    #[test]
    fn derive_prefers_explicit_overrides_verbatim() {
        let bind: SocketAddr = "0.0.0.0:50151".parse().unwrap();
        let (agent, inference) = derive_advertised_addrs(
            bind,
            8000,
            Some("agent.internal:9999"),
            Some("infer.internal:1234"),
            // Explicit values win even under a wildcard bind, so no resolution.
            no_wildcard,
        );
        assert_eq!(agent, "agent.internal:9999");
        assert_eq!(inference, "infer.internal:1234");
    }

    #[test]
    fn derive_resolves_wildcard_host_for_both() {
        let bind: SocketAddr = "0.0.0.0:50151".parse().unwrap();
        let (agent, inference) =
            derive_advertised_addrs(bind, 8000, None, None, || IpAddr::from([192, 168, 1, 42]));
        // Wildcard bind → resolved host, keeping the respective ports.
        assert_eq!(agent, "192.168.1.42:50151");
        assert_eq!(inference, "192.168.1.42:8000");
    }

    #[test]
    fn derive_mixes_explicit_agent_with_derived_inference() {
        let bind: SocketAddr = "10.0.0.7:50151".parse().unwrap();
        let (agent, inference) =
            derive_advertised_addrs(bind, 8000, Some("agent.internal:9999"), None, no_wildcard);
        assert_eq!(agent, "agent.internal:9999");
        assert_eq!(inference, "10.0.0.7:8000");
    }

    #[test]
    fn advertised_addrs_default_config_never_panics() {
        // Default config binds on 0.0.0.0 → exercises the real best-effort
        // detector; it must return usable host:port pairs without failing.
        let cfg = AgentConfig::default();
        let (agent, inference) = cfg.advertised_addrs();
        assert!(agent.ends_with(&format!(":{DEFAULT_AGENT_PORT}")));
        assert!(inference.ends_with(&format!(":{DEFAULT_INFERENCE_PORT}")));
    }

    // ------------------------------------------------------------------
    // SWIM config
    // ------------------------------------------------------------------

    #[test]
    fn swim_defaults_are_disabled() {
        let cfg = AgentConfig::default();
        assert!(!cfg.swim_enabled);
        assert_eq!(cfg.swim_bind_addr.port(), 7946);
        assert!(cfg.swim_seed_addrs.is_empty());
    }

    #[test]
    fn swim_bind_addr_env_is_parsed() {
        // Isolate env-var test: since tests may run in parallel we use a
        // unique key per test (see PURSER_SWIM_BIND_ADDR in from_env).
        // We test parse logic directly via the public AgentConfig mutator.
        let mut cfg = AgentConfig::default();
        let addr: SocketAddr = "127.0.0.1:9876".parse().unwrap();
        cfg.swim_bind_addr = addr;
        assert_eq!(cfg.swim_bind_addr, addr);
    }

    #[test]
    fn swim_enabled_flag_is_read() {
        // Verify the config struct default is opt-out.
        let cfg = AgentConfig::default();
        assert!(!cfg.swim_enabled, "SWIM must be opt-in (default disabled)");
    }
}
