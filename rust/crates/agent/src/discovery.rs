//! Node discovery & control-plane enrollment.
//!
//! Three concerns live here:
//!
//! 1. **Enrollment** — [`join`] calls `RegistrationService::Join` with the join
//!    token and local [`HardwareProfile`], receiving the assigned `node_id` and
//!    certificates; [`run_heartbeat`] then keeps a `Heartbeat` stream open.
//! 2. **Discovery** — a [`Discoverer`] trait with two implementations:
//!    [`SeedDiscoverer`] (static addresses from config, always available) and,
//!    behind the default `mdns` feature, [`MdnsDiscoverer`] / [`MdnsResponder`]
//!    for zero-config LAN discovery of the `_purser._tcp` service.
//! 3. **Membership** — a minimal in-memory [`Membership`] view fed by discovery
//!    and heartbeats.
//!
//! ## Deviation: gossip
//!
//! A SWIM-style gossip layer (e.g. the `foca` crate) would disseminate
//! membership and failure detection peer-to-peer. Per the task's priority
//! ordering (mDNS + seed + heartbeat first) it is intentionally **not** wired
//! yet; [`Membership`] is the clean interface such a layer would populate.
//! TODO(gossip): integrate `foca` (needs a runtime + wire codec + identity),
//! feeding `Membership::observe`/`remove` from gossip events.
//!
//! ## Security
//!
//! The join token and returned certificates are secrets: they are never logged,
//! and [`Enrollment`]'s `Debug` is redacted. Persisting them is delegated to a
//! [`SecretStore`](crate::secrets::SecretStore).

use std::collections::HashMap;
use std::sync::Arc;
use std::sync::Mutex;
use std::time::{Duration, Instant, SystemTime};

use async_trait::async_trait;
use purser_proto::v1::registration_service_client::RegistrationServiceClient;
use purser_proto::v1::{EngineMetrics, HardwareProfile, Heartbeat, JoinRequest, NodeState};
use tokio_stream::Stream;

/// DNS-SD service type advertised & browsed by Purser agents.
pub const SERVICE_TYPE: &str = "_purser._tcp.local.";

/// A discovered peer or control-plane candidate.
#[derive(Clone, Debug, PartialEq, Eq, Hash)]
pub struct Peer {
    /// Assigned node id, if known (mDNS TXT record / gossip). `None` for a raw
    /// seed address.
    pub node_id: Option<String>,
    /// Dial address, `host:port`.
    pub addr: String,
}

impl Peer {
    /// A peer known only by its address (e.g. a config seed).
    pub fn seed(addr: impl Into<String>) -> Self {
        Self {
            node_id: None,
            addr: addr.into(),
        }
    }
}

/// Result of enrolling into the cluster. **Contains secrets** — its `Debug` is
/// deliberately redacted and it must never be logged in full.
#[derive(Clone)]
pub struct Enrollment {
    /// Stable node identity assigned by the control plane.
    pub node_id: String,
    /// Client certificate (secret).
    pub client_cert: Vec<u8>,
    /// Cluster CA certificate.
    pub ca_cert: Vec<u8>,
}

impl std::fmt::Debug for Enrollment {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.debug_struct("Enrollment")
            .field("node_id", &self.node_id)
            .field("client_cert", &"<redacted>")
            .field("ca_cert", &"<redacted>")
            .finish()
    }
}

/// Enroll into the cluster: `Join` with the token + hardware profile, receiving
/// the assigned identity and certificates.
///
/// `advertised_agent_addr` / `advertised_inference_addr` are the `host:port`
/// endpoints (see [`AgentConfig::advertised_addrs`](crate::config::AgentConfig::advertised_addrs))
/// the control plane should use to reach this node's `AgentService` and to route
/// inference traffic — advertised here so the CP need not guess "hostname + fixed
/// port", which breaks with multiple agents per host.
///
/// The `join_token` is never logged.
pub async fn join(
    control_plane_addr: &str,
    join_token: &str,
    profile: HardwareProfile,
    advertised_agent_addr: String,
    advertised_inference_addr: String,
) -> anyhow::Result<Enrollment> {
    let endpoint = normalize_endpoint(control_plane_addr);
    let mut client = RegistrationServiceClient::connect(endpoint).await?;
    tracing::info!(
        agent_addr = %advertised_agent_addr,
        inference_addr = %advertised_inference_addr,
        "advertising endpoints in Join"
    );
    let reply = client
        .join(JoinRequest {
            join_token: join_token.to_string(),
            hardware_profile: Some(profile),
            advertised_agent_addr,
            advertised_inference_addr,
        })
        .await?
        .into_inner();

    // Never log the token or certificate bytes.
    tracing::info!(node_id = %reply.node_id, "enrolled into cluster");
    Ok(Enrollment {
        node_id: reply.node_id,
        client_cert: reply.client_cert,
        ca_cert: reply.ca_cert,
    })
}

/// Supplies the current node state + metrics for each heartbeat.
pub trait HeartbeatSource: Send + Sync {
    /// A point-in-time snapshot of what to report.
    fn snapshot(&self) -> (NodeState, Option<EngineMetrics>);
}

/// Build the outbound `Heartbeat` message stream at a fixed cadence.
///
/// `max` bounds the number of messages (handy in tests); `None` streams
/// forever.
pub fn heartbeat_messages<S: HeartbeatSource + 'static>(
    node_id: String,
    source: Arc<S>,
    interval: Duration,
    max: Option<usize>,
) -> impl Stream<Item = Heartbeat> {
    async_stream::stream! {
        let mut ticker = tokio::time::interval(interval);
        let mut count = 0usize;
        loop {
            ticker.tick().await;
            let (state, metrics) = source.snapshot();
            yield Heartbeat {
                node_id: node_id.clone(),
                state: state as i32,
                metrics,
                node_metrics: None,
                ts: Some(prost_types::Timestamp::from(SystemTime::now())),
            };
            count += 1;
            if let Some(max) = max {
                if count >= max {
                    break;
                }
            }
        }
    }
}

/// Open and drive a `Heartbeat` stream to the control plane until it ends or the
/// connection drops.
pub async fn run_heartbeat<S: HeartbeatSource + 'static>(
    control_plane_addr: &str,
    node_id: String,
    source: Arc<S>,
    interval: Duration,
) -> anyhow::Result<()> {
    let endpoint = normalize_endpoint(control_plane_addr);
    let mut client = RegistrationServiceClient::connect(endpoint).await?;
    let stream = heartbeat_messages(node_id, source, interval, None);
    client.heartbeat(stream).await?;
    Ok(())
}

/// Discovers peers / control-plane candidates.
#[async_trait]
pub trait Discoverer: Send + Sync {
    /// Return the peers currently discoverable through this mechanism.
    async fn discover(&self) -> anyhow::Result<Vec<Peer>>;
}

/// Static seed-list discovery — the routed/cloud fallback when multicast mDNS
/// is unavailable. Always works, needs no network round-trip.
#[derive(Clone, Debug, Default)]
pub struct SeedDiscoverer {
    seeds: Vec<String>,
}

impl SeedDiscoverer {
    /// A discoverer over the given seed addresses.
    pub fn new(seeds: Vec<String>) -> Self {
        Self { seeds }
    }
}

#[async_trait]
impl Discoverer for SeedDiscoverer {
    async fn discover(&self) -> anyhow::Result<Vec<Peer>> {
        Ok(self.seeds.iter().map(Peer::seed).collect())
    }
}

/// mDNS/DNS-SD browser for the `_purser._tcp` service (default `mdns` feature).
#[cfg(feature = "mdns")]
#[derive(Clone, Debug)]
pub struct MdnsDiscoverer {
    /// How long to collect responses before returning.
    browse_window: Duration,
}

#[cfg(feature = "mdns")]
impl MdnsDiscoverer {
    /// A discoverer that browses for `browse_window` before returning.
    pub fn new(browse_window: Duration) -> Self {
        Self { browse_window }
    }
}

#[cfg(feature = "mdns")]
#[async_trait]
impl Discoverer for MdnsDiscoverer {
    async fn discover(&self) -> anyhow::Result<Vec<Peer>> {
        use mdns_sd::{ServiceDaemon, ServiceEvent};

        let daemon = ServiceDaemon::new().map_err(|e| anyhow::anyhow!("mdns daemon: {e}"))?;
        let receiver = daemon
            .browse(SERVICE_TYPE)
            .map_err(|e| anyhow::anyhow!("mdns browse: {e}"))?;

        let mut peers = Vec::new();
        let deadline = Instant::now() + self.browse_window;
        loop {
            let remaining = deadline.saturating_duration_since(Instant::now());
            if remaining.is_zero() {
                break;
            }
            match tokio::time::timeout(remaining, receiver.recv_async()).await {
                Ok(Ok(ServiceEvent::ServiceResolved(info))) => {
                    let node_id = info.get_property_val_str("node_id").map(|s| s.to_string());
                    let port = info.get_port();
                    for scoped in info.get_addresses() {
                        let sock = std::net::SocketAddr::new(scoped.to_ip_addr(), port);
                        peers.push(Peer {
                            node_id: node_id.clone(),
                            addr: sock.to_string(),
                        });
                    }
                }
                Ok(Ok(_)) => {}      // other events: ignore
                Ok(Err(_)) => break, // channel closed
                Err(_) => break,     // browse window elapsed
            }
        }
        let _ = daemon.shutdown();
        Ok(peers)
    }
}

/// Advertises this node's `AgentService` over mDNS so peers can find it.
#[cfg(feature = "mdns")]
pub struct MdnsResponder {
    daemon: mdns_sd::ServiceDaemon,
}

#[cfg(feature = "mdns")]
impl MdnsResponder {
    /// Register the `_purser._tcp` service, tagging the TXT record with the
    /// node id and cluster. Addresses are auto-detected.
    pub fn advertise(
        instance_name: &str,
        port: u16,
        node_id: &str,
        cluster_id: &str,
    ) -> anyhow::Result<Self> {
        use mdns_sd::{ServiceDaemon, ServiceInfo};

        let daemon = ServiceDaemon::new().map_err(|e| anyhow::anyhow!("mdns daemon: {e}"))?;
        let host_name = format!("{}.local.", sanitize_label(instance_name));
        let mut props: HashMap<String, String> = HashMap::new();
        props.insert("node_id".to_string(), node_id.to_string());
        props.insert("cluster".to_string(), cluster_id.to_string());

        let info = ServiceInfo::new(SERVICE_TYPE, instance_name, &host_name, "", port, props)
            .map_err(|e| anyhow::anyhow!("mdns service info: {e}"))?
            .enable_addr_auto();
        daemon
            .register(info)
            .map_err(|e| anyhow::anyhow!("mdns register: {e}"))?;
        tracing::info!(%instance_name, port, "advertising via mDNS ({SERVICE_TYPE})");
        Ok(Self { daemon })
    }

    /// Stop advertising.
    pub fn shutdown(self) {
        let _ = self.daemon.shutdown();
    }
}

/// Discover peers using the seed list plus, when the `mdns` feature is enabled,
/// LAN mDNS. Results are de-duplicated by address.
pub async fn discover_peers(seeds: &[String], mdns_window: Duration) -> Vec<Peer> {
    // `peers` is only mutated further when the `mdns` feature adds LAN results.
    #[cfg_attr(not(feature = "mdns"), allow(unused_mut))]
    let mut peers = SeedDiscoverer::new(seeds.to_vec())
        .discover()
        .await
        .unwrap_or_default();

    #[cfg(feature = "mdns")]
    {
        match MdnsDiscoverer::new(mdns_window).discover().await {
            Ok(mut found) => peers.append(&mut found),
            Err(e) => tracing::warn!(error = %e, "mDNS discovery failed; using seeds only"),
        }
    }
    #[cfg(not(feature = "mdns"))]
    {
        let _ = mdns_window;
    }

    dedup_by_addr(peers)
}

/// A known cluster member and its liveness bookkeeping.
#[derive(Clone, Debug)]
pub struct Member {
    /// The peer's address & (optional) id.
    pub peer: Peer,
    /// When we last had contact.
    pub last_seen: Instant,
    /// Last known state (from heartbeat / gossip).
    pub state: NodeState,
}

/// Minimal in-memory cluster membership view. Keyed by node id when known, else
/// by address. This is the interface a gossip layer would populate (see the
/// module-level deviation note).
pub struct Membership {
    members: Mutex<HashMap<String, Member>>,
    /// A member unseen for longer than this is considered suspect/failed.
    failure_after: Duration,
}

impl Membership {
    /// A membership view whose members go suspect after `failure_after`.
    pub fn new(failure_after: Duration) -> Self {
        Self {
            members: Mutex::new(HashMap::new()),
            failure_after,
        }
    }

    fn key(peer: &Peer) -> String {
        peer.node_id.clone().unwrap_or_else(|| peer.addr.clone())
    }

    /// Record (or refresh) a peer as seen just now.
    pub fn observe(&self, peer: Peer) {
        let key = Self::key(&peer);
        let mut members = self.members.lock().unwrap();
        let entry = members.entry(key).or_insert_with(|| Member {
            peer: peer.clone(),
            last_seen: Instant::now(),
            state: NodeState::Unspecified,
        });
        entry.peer = peer;
        entry.last_seen = Instant::now();
    }

    /// Refresh liveness + state for a known member (e.g. on heartbeat).
    pub fn mark_seen(&self, node_id: &str, state: NodeState) {
        if let Some(m) = self.members.lock().unwrap().get_mut(node_id) {
            m.last_seen = Instant::now();
            m.state = state;
        }
    }

    /// Forget a member (e.g. graceful departure / gossip removal).
    pub fn remove(&self, node_id: &str) {
        self.members.lock().unwrap().remove(node_id);
    }

    /// Members seen within the failure window.
    pub fn alive(&self) -> Vec<Peer> {
        self.members
            .lock()
            .unwrap()
            .values()
            .filter(|m| m.last_seen.elapsed() < self.failure_after)
            .map(|m| m.peer.clone())
            .collect()
    }

    /// Members not seen within the failure window (suspect / failed).
    pub fn suspect(&self) -> Vec<Peer> {
        self.members
            .lock()
            .unwrap()
            .values()
            .filter(|m| m.last_seen.elapsed() >= self.failure_after)
            .map(|m| m.peer.clone())
            .collect()
    }

    /// Total number of known members.
    pub fn len(&self) -> usize {
        self.members.lock().unwrap().len()
    }

    /// Whether no members are known.
    pub fn is_empty(&self) -> bool {
        self.len() == 0
    }
}

/// Normalize a control-plane address into a URI tonic can dial (adds a scheme
/// if the caller passed a bare `host:port`).
fn normalize_endpoint(addr: &str) -> String {
    if addr.contains("://") {
        addr.to_string()
    } else {
        format!("http://{addr}")
    }
}

/// De-duplicate peers by address, preferring an entry that carries a node id.
fn dedup_by_addr(peers: Vec<Peer>) -> Vec<Peer> {
    let mut by_addr: HashMap<String, Peer> = HashMap::new();
    for peer in peers {
        by_addr
            .entry(peer.addr.clone())
            .and_modify(|existing| {
                if existing.node_id.is_none() && peer.node_id.is_some() {
                    existing.node_id = peer.node_id.clone();
                }
            })
            .or_insert(peer);
    }
    let mut out: Vec<Peer> = by_addr.into_values().collect();
    out.sort_by(|a, b| a.addr.cmp(&b.addr));
    out
}

/// Sanitize a string into a safe DNS label component (used when advertising).
#[cfg(feature = "mdns")]
fn sanitize_label(name: &str) -> String {
    name.chars()
        .map(|c| if c.is_ascii_alphanumeric() { c } else { '-' })
        .collect()
}

#[cfg(test)]
mod tests {
    use super::*;

    struct FixedSource(NodeState);
    impl HeartbeatSource for FixedSource {
        fn snapshot(&self) -> (NodeState, Option<EngineMetrics>) {
            (self.0, Some(EngineMetrics::default()))
        }
    }

    #[tokio::test]
    async fn seed_discoverer_returns_seeds() {
        let d = SeedDiscoverer::new(vec!["10.0.0.1:50151".into(), "10.0.0.2:50151".into()]);
        let peers = d.discover().await.unwrap();
        assert_eq!(peers.len(), 2);
        assert!(peers.iter().all(|p| p.node_id.is_none()));
        assert_eq!(peers[0].addr, "10.0.0.1:50151");
    }

    #[test]
    fn normalize_endpoint_adds_scheme() {
        assert_eq!(normalize_endpoint("host:50150"), "http://host:50150");
        assert_eq!(
            normalize_endpoint("https://cp.internal:50150"),
            "https://cp.internal:50150"
        );
    }

    #[test]
    fn dedup_prefers_identified_peer() {
        let peers = vec![
            Peer::seed("1.2.3.4:50151"),
            Peer {
                node_id: Some("node-x".into()),
                addr: "1.2.3.4:50151".into(),
            },
            Peer::seed("5.6.7.8:50151"),
        ];
        let out = dedup_by_addr(peers);
        assert_eq!(out.len(), 2);
        let x = out.iter().find(|p| p.addr == "1.2.3.4:50151").unwrap();
        assert_eq!(x.node_id.as_deref(), Some("node-x"));
    }

    #[test]
    fn enrollment_debug_is_redacted() {
        let e = Enrollment {
            node_id: "node-1".into(),
            client_cert: b"SECRET-CERT".to_vec(),
            ca_cert: b"CA".to_vec(),
        };
        let dbg = format!("{e:?}");
        assert!(dbg.contains("node-1"));
        assert!(dbg.contains("<redacted>"));
        assert!(!dbg.contains("SECRET-CERT"), "cert bytes must not appear");
    }

    #[test]
    fn membership_alive_and_suspect() {
        // failure_after 0 => everything is immediately suspect.
        let m = Membership::new(Duration::ZERO);
        m.observe(Peer {
            node_id: Some("a".into()),
            addr: "1.1.1.1:1".into(),
        });
        assert_eq!(m.len(), 1);
        assert!(m.alive().is_empty());
        assert_eq!(m.suspect().len(), 1);

        // A generous window keeps a freshly-observed member alive.
        let m2 = Membership::new(Duration::from_secs(60));
        m2.observe(Peer::seed("2.2.2.2:2"));
        assert_eq!(m2.alive().len(), 1);
        assert!(m2.suspect().is_empty());
        m2.remove("2.2.2.2:2");
        assert!(m2.is_empty());
    }

    #[tokio::test]
    async fn heartbeat_stream_yields_bounded_messages() {
        use tokio_stream::StreamExt;

        let source = Arc::new(FixedSource(NodeState::Running));
        let stream = heartbeat_messages(
            "node-7".to_string(),
            source,
            Duration::from_millis(5),
            Some(3),
        );
        tokio::pin!(stream);

        let mut seen = Vec::new();
        while let Some(hb) = stream.next().await {
            seen.push(hb);
        }
        assert_eq!(seen.len(), 3);
        assert!(seen.iter().all(|h| h.node_id == "node-7"));
        assert!(seen.iter().all(|h| h.state == NodeState::Running as i32));
        assert!(seen.iter().all(|h| h.ts.is_some()));
    }
}
