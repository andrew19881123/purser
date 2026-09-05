//! Opt-in Gossip SWIM membership layer (wraps [`foca`]).
//!
//! Provides scalable, peer-to-peer membership convergence alongside
//! (not replacing) the existing mDNS + seed discovery path.
//!
//! ## How it fits in
//!
//! When enabled, [`start`] binds a UDP socket and runs three background tasks:
//!
//! 1. **writer** — flushes outgoing UDP datagrams produced by foca.
//! 2. **driver** — receives foca inputs (timers, incoming data, announce
//!    requests) one at a time, calls the appropriate foca method, then drains
//!    the accumulated side-effects.  Also translates [`foca::Notification`]
//!    into [`crate::discovery::Membership`] calls.
//! 3. **receiver** — reads incoming UDP datagrams and forwards them to the
//!    driver.
//!
//! All three tasks honour a `tokio::sync::watch` shutdown channel: send
//! `true` to stop them cleanly.
//!
//! ## Identity
//!
//! Each node's SWIM identity ([`SwimIdentity`]) carries **two addresses**:
//! the UDP gossip port (`swim_addr`) used by foca for all SWIM traffic, and
//! the gRPC `AgentService` port (`grpc_addr`) that the control plane and
//! peers need in order to dial this node.  On `MemberUp` the membership view
//! receives `grpc_addr`, not `swim_addr`, so the rest of the system (heartbeat
//! path, gateway routing) sees the correct dial target.
//!
//! ## Configuration
//!
//! | Env variable              | Default           | Description                          |
//! |---------------------------|-------------------|--------------------------------------|
//! | `PURSER_SWIM_ENABLED`     | `false`           | Set `true`/`1`/`yes` to opt-in       |
//! | `PURSER_SWIM_BIND_ADDR`   | `0.0.0.0:7946`    | UDP bind address                     |
//! | `PURSER_SWIM_SEED_ADDRS`  | _(empty)_         | Comma-separated `host:port` seeds    |
//!
//! When disabled (the default) or when the UDP bind fails, the existing
//! mDNS + seed path runs unchanged.
//!
//! ## N4 — surfacing peers to the control plane
//!
//! When `MemberUp` fires the gRPC address is logged at `INFO` and added to
//! the local [`Membership`] view.  A future step is to include these addresses
//! in the `HeartbeatRequest` so the control plane learns about SWIM-discovered
//! peers without a separate out-of-band channel — this requires adding a
//! `repeated string seen_peers` field to the proto, which is deferred to avoid
//! regenerating the bindings in this PR.

use std::net::SocketAddr;
use std::sync::Arc;
use std::time::Duration;

use bytes::{BufMut, Bytes, BytesMut};
use foca::{Config as FocaConfig, Foca, NoCustomBroadcast, Notification, PostcardCodec, Runtime, Timer};
use rand::rngs::StdRng;
use rand::SeedableRng;
use tokio::net::UdpSocket;
use tokio::sync::{mpsc, watch};

use crate::discovery::{Membership, Peer};

// ---------------------------------------------------------------------------
// Identity
// ---------------------------------------------------------------------------

/// SWIM cluster-member identity.
///
/// Carries the node's **UDP gossip address** (`swim_addr`, used by foca for
/// all SWIM traffic) and its **gRPC `AgentService` address** (`grpc_addr`,
/// the port the control plane and peers must dial).  Separating the two
/// allows the membership view to be populated with the correct dial target
/// on `MemberUp`, regardless of whether the SWIM and gRPC ports are the same.
///
/// A random `bump` nonce enables quick re-join after a graceful leave: foca
/// can distinguish the fresh instance from the still-declared-down entry at
/// the same address via [`Identity::renew`].
#[derive(Clone, PartialEq, Eq, Hash, Debug, serde::Serialize, serde::Deserialize)]
pub struct SwimIdentity {
    /// UDP gossip address — used by foca to route all SWIM protocol traffic.
    pub swim_addr: SocketAddr,
    /// gRPC `AgentService` address — what peers and the control plane dial.
    pub grpc_addr: SocketAddr,
    /// Random nonce, incremented on re-join.
    bump: u64,
}

impl SwimIdentity {
    fn new(swim_addr: SocketAddr, grpc_addr: SocketAddr) -> Self {
        Self {
            swim_addr,
            grpc_addr,
            bump: fastrand::u64(..),
        }
    }

    /// Construct a seed identity for bootstrapping.
    ///
    /// Seeds are used only to kick off the initial `Announce`; foca never
    /// places them in the live-member set itself.  The zero bump is fine:
    /// [`Identity::has_same_prefix`] relaxes exact-match checking so the seed
    /// target accepts our `Announce` regardless of bump mismatch.  The
    /// `grpc_addr` is set to the same value as `swim_addr` because we do not
    /// know the remote's gRPC port at seed time — `MemberUp` will fire with
    /// the actual full identity once the peer joins.
    fn seed(swim_addr: SocketAddr) -> Self {
        Self {
            swim_addr,
            grpc_addr: swim_addr,
            bump: 0,
        }
    }
}

impl foca::Identity for SwimIdentity {
    /// Auto-rejoin by bumping the nonce while keeping both addresses.
    fn renew(&self) -> Option<Self> {
        Some(Self {
            swim_addr: self.swim_addr,
            grpc_addr: self.grpc_addr,
            bump: self.bump.wrapping_add(1),
        })
    }

    /// Prefix-match on the UDP gossip address only so any node that knows our
    /// SWIM address can Announce to us (e.g. a seed that doesn't know our bump
    /// or our gRPC port).
    fn has_same_prefix(&self, other: &Self) -> bool {
        self.swim_addr == other.swim_addr
    }
}

// ---------------------------------------------------------------------------
// Runtime implementation
// ---------------------------------------------------------------------------

/// Accumulating foca `Runtime`.
///
/// Collects all side-effects from one foca call so the async driver can act
/// on them *outside* the synchronous foca lock.
struct AccumulatingRuntime {
    to_send: Vec<(SwimIdentity, Bytes)>,
    to_schedule: Vec<(Duration, Timer<SwimIdentity>)>,
    notifications: Vec<Notification<SwimIdentity>>,
    /// Reusable buffer for packet construction.
    buf: BytesMut,
}

impl AccumulatingRuntime {
    fn new() -> Self {
        Self {
            to_send: Vec::new(),
            to_schedule: Vec::new(),
            notifications: Vec::new(),
            buf: BytesMut::new(),
        }
    }
}

impl Runtime<SwimIdentity> for AccumulatingRuntime {
    fn notify(&mut self, notification: Notification<SwimIdentity>) {
        self.notifications.push(notification);
    }

    fn send_to(&mut self, to: SwimIdentity, data: &[u8]) {
        let mut packet = self.buf.split();
        packet.put_slice(data);
        self.to_send.push((to, packet.freeze()));
    }

    fn submit_after(&mut self, event: Timer<SwimIdentity>, after: Duration) {
        self.to_schedule.push((after, event));
    }
}

// ---------------------------------------------------------------------------
// Driver input type
// ---------------------------------------------------------------------------

enum Input {
    Event(Timer<SwimIdentity>),
    Data(Bytes),
    Announce(SwimIdentity),
}

// ---------------------------------------------------------------------------
// Public API
// ---------------------------------------------------------------------------

/// Start the SWIM gossip layer and return.
///
/// Returns `Ok(())` immediately when `enabled` is `false` — no sockets are
/// opened and no tasks are spawned.
///
/// Returns an error when `enabled` is `true` but the UDP socket fails to
/// bind (e.g. port already in use).  Callers SHOULD log this at `warn` and
/// fall through to the existing mDNS + seed path.
///
/// On success, three background [`tokio::spawn`]ed tasks run until `shutdown`
/// receives `true`.
///
/// `grpc_addr` is the `AgentService` gRPC address this node advertises in its
/// SWIM identity so peers learn the correct dial target on `MemberUp`.
pub async fn start(
    enabled: bool,
    bind_addr: SocketAddr,
    grpc_addr: SocketAddr,
    seeds: Vec<SocketAddr>,
    membership: Arc<Membership>,
    shutdown: watch::Receiver<bool>,
) -> anyhow::Result<()> {
    if !enabled {
        return Ok(());
    }

    let socket = Arc::new(
        UdpSocket::bind(bind_addr)
            .await
            .map_err(|e| anyhow::anyhow!("SWIM: cannot bind UDP {bind_addr}: {e}"))?,
    );
    tracing::info!(%bind_addr, %grpc_addr, seeds = seeds.len(), "SWIM gossip layer started");

    let identity = SwimIdentity::new(bind_addr, grpc_addr);
    let rng = StdRng::from_entropy();
    let mut config = FocaConfig::simple();
    config.notify_down_members = true;
    let buf_len = config.max_packet_size.get();

    let foca = Foca::new(identity, config, rng, PostcardCodec);

    let (tx_foca, rx_foca) = mpsc::channel::<Input>(256);
    let (tx_send, rx_send) = mpsc::channel::<(SocketAddr, Bytes)>(256);

    spawn_writer(Arc::clone(&socket), rx_send);
    spawn_driver(
        foca,
        rx_foca,
        tx_foca.clone(),
        tx_send,
        Arc::clone(&membership),
        shutdown.clone(),
    );
    spawn_receiver(Arc::clone(&socket), tx_foca.clone(), buf_len, shutdown);

    // Kick off the gossip ring by announcing to all configured seeds.
    for seed_addr in seeds {
        let seed = SwimIdentity::seed(seed_addr);
        let _ = tx_foca.send(Input::Announce(seed)).await;
    }

    Ok(())
}

// ---------------------------------------------------------------------------
// Background tasks
// ---------------------------------------------------------------------------

fn spawn_writer(socket: Arc<UdpSocket>, mut rx: mpsc::Receiver<(SocketAddr, Bytes)>) {
    tokio::spawn(async move {
        while let Some((dst, data)) = rx.recv().await {
            if let Err(e) = socket.send_to(&data, dst).await {
                tracing::debug!(error = %e, "SWIM: UDP send error (ignored)");
            }
        }
    });
}

fn spawn_driver(
    mut foca: Foca<SwimIdentity, PostcardCodec, StdRng, NoCustomBroadcast>,
    mut rx: mpsc::Receiver<Input>,
    tx: mpsc::Sender<Input>,
    tx_send: mpsc::Sender<(SocketAddr, Bytes)>,
    membership: Arc<Membership>,
    mut shutdown: watch::Receiver<bool>,
) {
    tokio::spawn(async move {
        let mut rt = AccumulatingRuntime::new();
        loop {
            tokio::select! {
                // Process one foca input at a time; drain all side-effects
                // before accepting the next input (as per foca's design
                // contract: only one operation in flight at a time).
                msg = rx.recv() => {
                    let Some(input) = msg else { break };
                    let result = match input {
                        Input::Event(timer) => foca.handle_timer(timer, &mut rt),
                        Input::Data(data)   => foca.handle_data(&data, &mut rt),
                        Input::Announce(id) => foca.announce(id, &mut rt),
                    };
                    if let Err(e) = result {
                        tracing::debug!(error = %e, "SWIM: foca error (non-fatal)");
                    }
                    drain_runtime(&mut rt, &tx, &tx_send, &membership).await;
                }
                _ = shutdown.changed() => {
                    if *shutdown.borrow() {
                        tracing::info!("SWIM: driver task stopping");
                        break;
                    }
                }
            }
        }
    });
}

fn spawn_receiver(
    socket: Arc<UdpSocket>,
    tx: mpsc::Sender<Input>,
    buf_len: usize,
    mut shutdown: watch::Receiver<bool>,
) {
    tokio::spawn(async move {
        let mut recv_buf = vec![0u8; buf_len];
        let mut databuf = BytesMut::new();
        loop {
            tokio::select! {
                res = socket.recv_from(&mut recv_buf) => {
                    match res {
                        Ok((n, _from)) => {
                            databuf.put_slice(&recv_buf[..n]);
                            if tx.send(Input::Data(databuf.split().freeze())).await.is_err() {
                                break; // driver closed
                            }
                        }
                        Err(e) => tracing::debug!(error = %e, "SWIM: UDP recv error"),
                    }
                }
                _ = shutdown.changed() => {
                    if *shutdown.borrow() {
                        tracing::info!("SWIM: receiver task stopping");
                        break;
                    }
                }
            }
        }
    });
}

/// Drain all accumulated side-effects from one foca call.
async fn drain_runtime(
    rt: &mut AccumulatingRuntime,
    tx: &mpsc::Sender<Input>,
    tx_send: &mpsc::Sender<(SocketAddr, Bytes)>,
    membership: &Arc<Membership>,
) {
    // Forward outbound UDP packets — route to the *SWIM* (UDP) address, not
    // the gRPC address.
    while let Some((id, data)) = rt.to_send.pop() {
        let _ = tx_send.send((id.swim_addr, data)).await;
    }
    // Spawn timer tasks; each fires a single `Input::Event` after the delay.
    while let Some((delay, event)) = rt.to_schedule.pop() {
        let tx_timer = tx.clone();
        tokio::spawn(async move {
            tokio::time::sleep(delay).await;
            let _ = tx_timer.send(Input::Event(event)).await;
        });
    }
    // Translate membership notifications → Membership view.
    while let Some(notification) = rt.notifications.pop() {
        match notification {
            Notification::MemberUp(id) => {
                // N4: log at INFO so operators and the control plane heartbeat
                // path can see newly discovered peers.  The gRPC address (not
                // the SWIM UDP address) is what the rest of the system dials.
                //
                // TODO(N4-proto): add `repeated string seen_peers` to
                // HeartbeatRequest so the control plane learns about
                // SWIM-discovered gRPC endpoints; requires `make gen` —
                // deferred to a follow-up PR.
                tracing::info!(
                    swim_addr = %id.swim_addr,
                    grpc_addr = %id.grpc_addr,
                    "SWIM discovered peer"
                );
                membership.observe(Peer::seed(id.grpc_addr.to_string()));
            }
            Notification::MemberDown(id) => {
                tracing::debug!(
                    swim_addr = %id.swim_addr,
                    grpc_addr = %id.grpc_addr,
                    "SWIM: member down"
                );
                membership.remove(&id.grpc_addr.to_string());
            }
            Notification::Active => tracing::info!("SWIM: node is active in the cluster"),
            Notification::Idle   => tracing::debug!("SWIM: cluster is idle (no other members)"),
            Notification::Defunct => {
                tracing::warn!("SWIM: node declared defunct; will auto-rejoin via identity renewal")
            }
            Notification::Rejoin(id) => {
                tracing::info!(
                    swim_addr = %id.swim_addr,
                    grpc_addr = %id.grpc_addr,
                    bump = id.bump,
                    "SWIM: rejoined cluster"
                );
            }
        }
    }
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

#[cfg(test)]
mod tests {
    use std::sync::Arc;
    use std::time::Duration;

    use foca::Identity as _;
    use tokio::sync::watch;

    use super::*;
    use crate::discovery::Membership;

    fn membership() -> Arc<Membership> {
        Arc::new(Membership::new(Duration::from_secs(60)))
    }

    fn shutdown_pair() -> (watch::Sender<bool>, watch::Receiver<bool>) {
        watch::channel(false)
    }

    // ------------------------------------------------------------------
    // P2-T1: SwimIdentity carries both addresses and they are accessible
    // ------------------------------------------------------------------

    /// `SwimIdentity` must expose both `swim_addr` and `grpc_addr` as public
    /// fields — this is the structural contract the rest of the SWIM layer
    /// relies on when populating the membership view (P2 requirement).
    #[test]
    fn swim_identity_carries_both_addrs() {
        let swim: std::net::SocketAddr = "127.0.0.1:7946".parse().unwrap();
        let grpc: std::net::SocketAddr = "127.0.0.1:50151".parse().unwrap();

        let id = SwimIdentity::new(swim, grpc);

        assert_eq!(id.swim_addr, swim, "swim_addr must match the UDP bind address");
        assert_eq!(id.grpc_addr, grpc, "grpc_addr must match the gRPC bind address");
        assert_ne!(id.swim_addr, id.grpc_addr, "the two addresses are distinct");

        // renew() keeps both addresses, only bumps the nonce
        let renewed = id.renew().expect("renew must return Some");
        assert_eq!(renewed.swim_addr, swim);
        assert_eq!(renewed.grpc_addr, grpc);

        // prefix-matching ignores grpc_addr; two identities with the same
        // swim_addr but a different grpc_addr are the same prefix.
        let other = SwimIdentity::new(swim, "10.0.0.1:50151".parse().unwrap());
        assert!(
            id.has_same_prefix(&other),
            "has_same_prefix must match on swim_addr only"
        );
    }

    // ------------------------------------------------------------------
    // P2-T2: MemberUp feeds the gRPC address (not the SWIM address) into
    //        the membership view.
    // ------------------------------------------------------------------

    /// Simulate a `MemberUp` notification and verify that `membership.observe`
    /// receives the `grpc_addr`, not `swim_addr`.
    #[tokio::test]
    async fn member_up_uses_grpc_addr() {
        let swim: std::net::SocketAddr = "127.0.0.1:7946".parse().unwrap();
        let grpc: std::net::SocketAddr = "127.0.0.1:50151".parse().unwrap();

        let mem = membership();
        let (tx, _rx_send) = tokio::sync::mpsc::channel::<(SocketAddr, Bytes)>(8);
        let (tx_input, _rx_input) = tokio::sync::mpsc::channel::<Input>(8);

        let mut rt = AccumulatingRuntime::new();
        // Inject a MemberUp notification directly into the runtime.
        rt.notifications.push(Notification::MemberUp(SwimIdentity::new(swim, grpc)));

        drain_runtime(&mut rt, &tx_input, &tx, &mem).await;

        let alive = mem.alive();
        assert_eq!(alive.len(), 1, "one peer must be observed after MemberUp");
        assert_eq!(
            alive[0].addr,
            grpc.to_string(),
            "MemberUp must register the gRPC address in the membership view, not the SWIM address"
        );
        assert_ne!(
            alive[0].addr,
            swim.to_string(),
            "the SWIM UDP address must NOT appear in the membership view"
        );
    }

    // ------------------------------------------------------------------
    // N4-T1: SWIM disabled → no membership changes, no panics
    // ------------------------------------------------------------------

    /// When SWIM is disabled the start() function returns Ok immediately
    /// without touching the membership view or binding any sockets.
    #[tokio::test]
    async fn swim_disabled_no_notification() {
        let swim: std::net::SocketAddr = "127.0.0.1:7946".parse().unwrap();
        let grpc: std::net::SocketAddr = "127.0.0.1:50151".parse().unwrap();
        let (_tx, rx) = shutdown_pair();
        let mem = membership();

        let result = start(false, swim, grpc, vec![], Arc::clone(&mem), rx).await;

        assert!(result.is_ok(), "disabled SWIM must succeed without binding");
        assert!(
            mem.is_empty(),
            "no membership changes when SWIM is disabled"
        );
    }

    // ------------------------------------------------------------------
    // T-existing: SWIM disabled → falls back cleanly (no panic, no bind attempt)
    // ------------------------------------------------------------------

    #[tokio::test]
    async fn swim_disabled_no_bind() {
        let (_tx, rx) = shutdown_pair();
        let result = start(
            false,
            "127.0.0.1:0".parse().unwrap(),
            "127.0.0.1:50151".parse().unwrap(),
            vec![],
            membership(),
            rx,
        )
        .await;
        assert!(result.is_ok(), "disabled SWIM must succeed without binding");
    }

    // ------------------------------------------------------------------
    // T-existing: config parsing — PURSER_SWIM_BIND_ADDR round-trips correctly
    // ------------------------------------------------------------------

    #[test]
    fn swim_config_bind_addr_parses() {
        use crate::config::AgentConfig;
        let mut cfg = AgentConfig::default();
        assert!(!cfg.swim_enabled, "default must be disabled");
        assert_eq!(cfg.swim_bind_addr.port(), 7946, "default SWIM port is 7946");

        cfg.swim_bind_addr = "127.0.0.1:9999".parse().unwrap();
        assert_eq!(cfg.swim_bind_addr.port(), 9999);

        cfg.swim_seed_addrs = vec!["10.0.0.1:7946".into(), "10.0.0.2:7946".into()];
        assert_eq!(cfg.swim_seed_addrs.len(), 2);
    }

    // ------------------------------------------------------------------
    // T-existing: two in-process instances discover each other over loopback
    //
    // Marked #[ignore] because it requires a real tokio runtime, actual
    // UDP sockets on loopback, and a brief sleep for protocol convergence.
    // Run with: cargo test -p purser-agent swim_two_nodes -- --ignored
    // ------------------------------------------------------------------

    /// Integration test: two SWIM instances on loopback discover each other.
    ///
    /// Uses real UDP sockets on 127.0.0.1 with ephemeral ports so the test
    /// is deterministic.  Ignored by default to keep `cargo test` fast; pass
    /// `--ignored` to run it.
    #[tokio::test]
    #[ignore = "real UDP + sleep; run with --ignored for integration coverage"]
    async fn swim_two_nodes_discover_each_other() {
        // Node A: SWIM on 127.0.0.1:0 (OS-assigned), gRPC on 127.0.0.1:50151
        let sock_a = tokio::net::UdpSocket::bind("127.0.0.1:0").await.unwrap();
        let swim_a: std::net::SocketAddr = sock_a.local_addr().unwrap();
        drop(sock_a);
        let grpc_a: std::net::SocketAddr = "127.0.0.1:50151".parse().unwrap();

        // Node B: SWIM on 127.0.0.1:0 (OS-assigned), gRPC on 127.0.0.1:50152
        let sock_b = tokio::net::UdpSocket::bind("127.0.0.1:0").await.unwrap();
        let swim_b: std::net::SocketAddr = sock_b.local_addr().unwrap();
        drop(sock_b);
        let grpc_b: std::net::SocketAddr = "127.0.0.1:50152".parse().unwrap();

        let mem_a = membership();
        let mem_b = membership();

        let (_tx_a, rx_a) = shutdown_pair();
        let (_tx_b, rx_b) = shutdown_pair();

        // A seeds with B's SWIM address
        start(true, swim_a, grpc_a, vec![swim_b], Arc::clone(&mem_a), rx_a)
            .await
            .expect("node A start");
        // B seeds with A's SWIM address
        start(true, swim_b, grpc_b, vec![swim_a], Arc::clone(&mem_b), rx_b)
            .await
            .expect("node B start");

        // Give foca time to exchange Announce/Feed messages.
        tokio::time::sleep(Duration::from_millis(200)).await;

        // Each node should see the other's gRPC address in its membership view.
        let alive_a = mem_a.alive();
        let alive_b = mem_b.alive();

        assert!(
            alive_a.iter().any(|p| p.addr == grpc_b.to_string()),
            "node A should see node B's gRPC addr; alive={alive_a:?}"
        );
        assert!(
            alive_b.iter().any(|p| p.addr == grpc_a.to_string()),
            "node B should see node A's gRPC addr; alive={alive_b:?}"
        );
    }
}
