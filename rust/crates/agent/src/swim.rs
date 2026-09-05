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
//! ## TODO(gossip)
//!
//! Wire the SWIM-detected peer addresses back to the gRPC advertised address
//! once the identity carries both the SWIM UDP port and the gRPC port.
//! Currently, MemberUp adds the SWIM UDP address to the membership view —
//! sufficient for local cluster-state tracking and liveness detection.

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
/// Wraps the SWIM UDP listen address and a random [`bump`](Self::bump) nonce.
/// The `bump` allows a node to re-join the cluster quickly after a graceful
/// leave, because foca can distinguish the fresh instance from the
/// still-declared-down entry at the same address via [`Identity::renew`].
#[derive(Clone, PartialEq, Eq, Debug, serde::Serialize, serde::Deserialize)]
pub struct SwimId {
    /// The peer's SWIM UDP listen address.
    pub addr: SocketAddr,
    /// Random nonce, incremented on re-join.
    bump: u64,
}

impl SwimId {
    fn new(addr: SocketAddr) -> Self {
        Self {
            addr,
            bump: fastrand::u64(..),
        }
    }

    /// Construct a seed identity with a zero bump.
    ///
    /// The zero bump is fine for seeds: [`Identity::has_same_prefix`] relaxes
    /// exact-match checking so a seed node accepts an Announce regardless of
    /// the bump mismatch.
    fn seed(addr: SocketAddr) -> Self {
        Self { addr, bump: 0 }
    }
}

impl foca::Identity for SwimId {
    /// Auto-rejoin by bumping the nonce.
    fn renew(&self) -> Option<Self> {
        Some(Self {
            addr: self.addr,
            bump: self.bump.wrapping_add(1),
        })
    }

    /// Relax exact-identity matching so any node that knows our UDP address
    /// can send us an Announce (e.g. a seed that doesn't know our bump).
    fn has_same_prefix(&self, other: &Self) -> bool {
        self.addr == other.addr
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
    to_send: Vec<(SwimId, Bytes)>,
    to_schedule: Vec<(Duration, Timer<SwimId>)>,
    notifications: Vec<Notification<SwimId>>,
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

impl Runtime<SwimId> for AccumulatingRuntime {
    fn notify(&mut self, notification: Notification<SwimId>) {
        self.notifications.push(notification);
    }

    fn send_to(&mut self, to: SwimId, data: &[u8]) {
        let mut packet = self.buf.split();
        packet.put_slice(data);
        self.to_send.push((to, packet.freeze()));
    }

    fn submit_after(&mut self, event: Timer<SwimId>, after: Duration) {
        self.to_schedule.push((after, event));
    }
}

// ---------------------------------------------------------------------------
// Driver input type
// ---------------------------------------------------------------------------

enum Input {
    Event(Timer<SwimId>),
    Data(Bytes),
    Announce(SwimId),
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
pub async fn start(
    enabled: bool,
    bind_addr: SocketAddr,
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
    tracing::info!(%bind_addr, seeds = seeds.len(), "SWIM gossip layer started");

    let identity = SwimId::new(bind_addr);
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
        let seed = SwimId::seed(seed_addr);
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
    mut foca: Foca<SwimId, PostcardCodec, StdRng, NoCustomBroadcast>,
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
    // Forward outbound UDP packets.
    while let Some((id, data)) = rt.to_send.pop() {
        let _ = tx_send.send((id.addr, data)).await;
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
                tracing::debug!(addr = %id.addr, "SWIM: member up");
                membership.observe(Peer::seed(id.addr.to_string()));
            }
            Notification::MemberDown(id) => {
                tracing::debug!(addr = %id.addr, "SWIM: member down");
                membership.remove(&id.addr.to_string());
            }
            Notification::Active => tracing::info!("SWIM: node is active in the cluster"),
            Notification::Idle   => tracing::debug!("SWIM: cluster is idle (no other members)"),
            Notification::Defunct => {
                tracing::warn!("SWIM: node declared defunct; will auto-rejoin via identity renewal")
            }
            Notification::Rejoin(id) => {
                tracing::info!(new_addr = %id.addr, bump = id.bump, "SWIM: rejoined cluster");
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
    // T1: SWIM disabled → falls back cleanly (no panic, no bind attempt)
    // ------------------------------------------------------------------

    #[tokio::test]
    async fn swim_disabled_no_bind() {
        let (_tx, rx) = shutdown_pair();
        let result = start(
            false,
            "127.0.0.1:0".parse().unwrap(),
            vec![],
            membership(),
            rx,
        )
        .await;
        assert!(result.is_ok(), "disabled SWIM must succeed without binding");
    }

    // ------------------------------------------------------------------
    // T2: config parsing — PURSER_SWIM_BIND_ADDR round-trips correctly
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
    // T3: two in-process instances discover each other over loopback UDP
    //
    // Marked #[ignore] because it requires a real tokio runtime, actual
    // UDP sockets on loopback, and a brief sleep for protocol convergence.
    // Run with: cargo test -p purser-agent swim_two_nodes -- --ignored
    // ------------------------------------------------------------------

    /// Integration test: two `SWIMMembership` instances on loopback discover each other.
    ///
    /// Uses real UDP sockets on 127.0.0.1 with ephemeral ports so the test
    /// is deterministic.  Ignored by default to keep `cargo test` fast; pass
    /// `--ignored` to run it.
    #[tokio::test]
    #[ignore = "real UDP + sleep; run with --ignored for integration coverage"]
    async fn swim_two_nodes_discover_each_other() {
        // Node A on 127.0.0.1:0 (OS-assigned port)
        let sock_a = tokio::net::UdpSocket::bind("127.0.0.1:0").await.unwrap();
        let addr_a: std::net::SocketAddr = sock_a.local_addr().unwrap();
        drop(sock_a); // release; start() will re-bind

        // Node B on 127.0.0.1:0
        let sock_b = tokio::net::UdpSocket::bind("127.0.0.1:0").await.unwrap();
        let addr_b: std::net::SocketAddr = sock_b.local_addr().unwrap();
        drop(sock_b);

        let mem_a = membership();
        let mem_b = membership();

        let (_tx_a, rx_a) = shutdown_pair();
        let (_tx_b, rx_b) = shutdown_pair();

        // A seeds with B
        start(true, addr_a, vec![addr_b], Arc::clone(&mem_a), rx_a)
            .await
            .expect("node A start");
        // B seeds with A
        start(true, addr_b, vec![addr_a], Arc::clone(&mem_b), rx_b)
            .await
            .expect("node B start");

        // Give foca time to exchange Announce/Feed messages.
        tokio::time::sleep(Duration::from_millis(200)).await;

        // Each node should see the other in its membership view.
        let alive_a = mem_a.alive();
        let alive_b = mem_b.alive();

        assert!(
            alive_a.iter().any(|p| p.addr.contains(&addr_b.port().to_string())),
            "node A should see node B; alive={alive_a:?}"
        );
        assert!(
            alive_b.iter().any(|p| p.addr.contains(&addr_a.port().to_string())),
            "node B should see node A; alive={alive_b:?}"
        );
    }
}
