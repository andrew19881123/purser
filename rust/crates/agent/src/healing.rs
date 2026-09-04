//! Local self-healing & self-update.
//!
//! The reliability layer that keeps the agent "dumb and reliable". It is strictly
//! node-local recovery; cluster-level rescheduling remains the control plane's
//! job. It provides:
//!   * a [`HeartbeatWatchdog`] — counts consecutive missed beats and flips
//!     unhealthy after a threshold,
//!   * a [`NodeHealthMonitor`] — ties the watchdog to the node
//!     [`NodeStateMachine`], flipping to `UNREACHABLE` on heartbeat loss and
//!     recovering on the next beat,
//!   * [`diagnose`] — a cheap self-diagnosis over engine phase / metrics / disk,
//!   * an [`UpdateVerifier`] seam for **signed** self-update (interface only).
//!
//! Crashed *engines* are restarted by [`crate::supervisor`] (backoff + crash-loop
//! detection); this module reacts to a crash loop by keeping the node DEGRADED
//! and surfacing it.

use std::sync::{Arc, Mutex};

use purser_proto::v1::{EngineMetrics, NodeState};

use crate::state::NodeStateMachine;
use crate::supervisor::EnginePhase;

/// Health verdict of a monitored signal.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Liveness {
    /// Beats are arriving within tolerance.
    Healthy,
    /// Too many consecutive beats were missed.
    Unhealthy,
}

/// Counts consecutive missed heartbeats and reports liveness once a threshold is
/// crossed. Deterministic and time-free — the caller decides when a beat or a
/// miss occurs (e.g. from an interval timer).
#[derive(Clone, Debug)]
pub struct HeartbeatWatchdog {
    max_missed: u32,
    missed: u32,
}

impl HeartbeatWatchdog {
    /// A watchdog that turns unhealthy after `max_missed` consecutive misses.
    pub fn new(max_missed: u32) -> Self {
        Self {
            max_missed: max_missed.max(1),
            missed: 0,
        }
    }

    /// Record a received beat, clearing the miss counter.
    pub fn beat(&mut self) {
        self.missed = 0;
    }

    /// Record a missed beat, returning the resulting liveness.
    pub fn record_miss(&mut self) -> Liveness {
        self.missed = self.missed.saturating_add(1);
        self.liveness()
    }

    /// Current liveness verdict.
    pub fn liveness(&self) -> Liveness {
        if self.missed >= self.max_missed {
            Liveness::Unhealthy
        } else {
            Liveness::Healthy
        }
    }

    /// Consecutive misses currently recorded.
    pub fn missed(&self) -> u32 {
        self.missed
    }
}

/// Ties heartbeat liveness to the node state machine: heartbeat loss drives the
/// node `UNREACHABLE`; the next beat recovers it to whatever it was doing.
pub struct NodeHealthMonitor {
    watchdog: HeartbeatWatchdog,
    machine: Arc<Mutex<NodeStateMachine>>,
}

impl NodeHealthMonitor {
    /// Monitor `machine`, flipping it UNREACHABLE after `max_missed` misses.
    pub fn new(machine: Arc<Mutex<NodeStateMachine>>, max_missed: u32) -> Self {
        Self {
            watchdog: HeartbeatWatchdog::new(max_missed),
            machine,
        }
    }

    /// A heartbeat (ack) arrived: reset the watchdog and, if the node was
    /// UNREACHABLE, recover it.
    pub fn on_heartbeat_ack(&mut self) {
        self.watchdog.beat();
        let mut sm = self.machine.lock().unwrap();
        if sm.current() == NodeState::Unreachable {
            if let Err(e) = sm.recover() {
                tracing::debug!(%e, "failed to recover node from UNREACHABLE");
            }
        }
    }

    /// A heartbeat was missed. Returns the resulting liveness; on `Unhealthy`
    /// the node is driven UNREACHABLE (unless terminal).
    pub fn on_heartbeat_miss(&mut self) -> Liveness {
        let live = self.watchdog.record_miss();
        if live == Liveness::Unhealthy {
            let mut sm = self.machine.lock().unwrap();
            if sm.current() != NodeState::Unreachable && !sm.is_terminal() {
                if let Err(e) = sm.unreachable() {
                    tracing::debug!(%e, "failed to mark node UNREACHABLE");
                }
            }
        }
        live
    }

    /// Consecutive missed beats.
    pub fn missed(&self) -> u32 {
        self.watchdog.missed()
    }
}

/// Inputs to a node self-diagnosis snapshot.
#[derive(Clone, Debug)]
pub struct DiagnosisInput {
    /// Current engine phase from the supervisor.
    pub engine_phase: EnginePhase,
    /// Latest engine metrics, if any.
    pub metrics: Option<EngineMetrics>,
    /// Free disk, GiB (from the probe).
    pub disk_free_gb: f64,
    /// Control-plane heartbeat liveness.
    pub control_plane: Liveness,
    /// Queue-depth level above which we flag backpressure.
    pub queue_depth_warn: u32,
    /// Free-disk level (GiB) below which we flag low disk.
    pub disk_free_warn_gb: f64,
}

impl Default for DiagnosisInput {
    fn default() -> Self {
        Self {
            engine_phase: EnginePhase::Idle,
            metrics: None,
            disk_free_gb: f64::INFINITY,
            control_plane: Liveness::Healthy,
            queue_depth_warn: 256,
            disk_free_warn_gb: 5.0,
        }
    }
}

/// Result of a self-diagnosis.
#[derive(Clone, Debug, PartialEq)]
pub struct Diagnosis {
    /// Node state the local evidence implies.
    pub implied_state: NodeState,
    /// Human-readable findings (empty when healthy).
    pub findings: Vec<String>,
}

impl Diagnosis {
    /// Whether the diagnosis found nothing wrong.
    pub fn is_healthy(&self) -> bool {
        self.findings.is_empty()
    }
}

/// Produce a self-diagnosis from local evidence. Pure and unit-testable.
pub fn diagnose(input: &DiagnosisInput) -> Diagnosis {
    let mut findings = Vec::new();
    let mut implied_state = match input.engine_phase {
        EnginePhase::Idle | EnginePhase::Stopped => NodeState::Ready,
        EnginePhase::Loading => NodeState::Loading,
        EnginePhase::Running => NodeState::Running,
        EnginePhase::Crashed | EnginePhase::Failed => NodeState::Degraded,
    };

    if input.engine_phase == EnginePhase::Failed {
        findings.push("engine gave up after a crash loop".to_string());
    } else if input.engine_phase == EnginePhase::Crashed {
        findings.push("engine crashed; restart in progress".to_string());
    }

    if input.control_plane == Liveness::Unhealthy {
        findings.push("control plane heartbeat lost".to_string());
        implied_state = NodeState::Unreachable;
    }

    if input.disk_free_gb < input.disk_free_warn_gb {
        findings.push(format!(
            "low disk: {:.1} GiB free (< {:.1} GiB)",
            input.disk_free_gb, input.disk_free_warn_gb
        ));
        if implied_state == NodeState::Running || implied_state == NodeState::Ready {
            implied_state = NodeState::Degraded;
        }
    }

    if let Some(m) = &input.metrics {
        if m.queue_depth > input.queue_depth_warn {
            findings.push(format!(
                "queue backing up: depth {} (> {})",
                m.queue_depth, input.queue_depth_warn
            ));
        }
    }

    Diagnosis {
        implied_state,
        findings,
    }
}

/// Verifies the signature of a self-update package against a pinned key.
///
/// TODO(phase2): a real implementation (e.g. ed25519 over the package digest,
/// key pinned at provision time) plus staging + supervised restart.
pub trait UpdateVerifier: Send + Sync {
    /// Verify `signature` over `payload`; `Ok(())` means the package is trusted.
    fn verify(&self, payload: &[u8], signature: &[u8]) -> anyhow::Result<()>;
}

/// The default verifier: refuses everything, so unsigned/unverified self-update
/// cannot proceed. Replaced by a real pinned-key verifier in phase 2.
pub struct RejectingVerifier;

impl UpdateVerifier for RejectingVerifier {
    fn verify(&self, _payload: &[u8], _signature: &[u8]) -> anyhow::Result<()> {
        Err(anyhow::anyhow!(
            "signed self-update not implemented: no pinned verification key configured"
        ))
    }
}

/// A requested self-update.
#[derive(Clone, Debug)]
pub struct UpdatePlan {
    /// Target agent version.
    pub version: String,
    /// Where to fetch the new binary.
    pub url: String,
    /// Detached signature over the binary.
    pub signature: Vec<u8>,
}

/// Attempt a signed self-update. Currently only the verification gate is wired;
/// downloading, staging and the supervised restart are TODO(phase2).
pub async fn apply_signed_update(
    verifier: &dyn UpdateVerifier,
    plan: &UpdatePlan,
    payload: &[u8],
) -> anyhow::Result<()> {
    verifier.verify(payload, &plan.signature)?;
    // TODO(phase2): stage the verified binary and hand off via a supervised
    // restart (exec into the new binary once in-flight work has drained).
    Err(anyhow::anyhow!(
        "update verified path reached, but staging/restart is not implemented"
    ))
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn watchdog_flips_after_threshold() {
        let mut wd = HeartbeatWatchdog::new(3);
        assert_eq!(wd.liveness(), Liveness::Healthy);
        assert_eq!(wd.record_miss(), Liveness::Healthy); // 1
        assert_eq!(wd.record_miss(), Liveness::Healthy); // 2
        assert_eq!(wd.record_miss(), Liveness::Unhealthy); // 3 == threshold
        // A beat clears it.
        wd.beat();
        assert_eq!(wd.liveness(), Liveness::Healthy);
        assert_eq!(wd.missed(), 0);
    }

    #[test]
    fn health_monitor_drives_state_machine() {
        let machine = Arc::new(Mutex::new(NodeStateMachine::starting_at(NodeState::Running)));
        let mut mon = NodeHealthMonitor::new(Arc::clone(&machine), 2);

        assert_eq!(mon.on_heartbeat_miss(), Liveness::Healthy); // 1
        assert_eq!(machine.lock().unwrap().current(), NodeState::Running);
        assert_eq!(mon.on_heartbeat_miss(), Liveness::Unhealthy); // 2 -> unreachable
        assert_eq!(machine.lock().unwrap().current(), NodeState::Unreachable);

        // Recovery on the next beat returns to RUNNING.
        mon.on_heartbeat_ack();
        assert_eq!(machine.lock().unwrap().current(), NodeState::Running);
        assert_eq!(mon.missed(), 0);
    }

    #[test]
    fn diagnose_healthy_running_node() {
        let d = diagnose(&DiagnosisInput {
            engine_phase: EnginePhase::Running,
            disk_free_gb: 500.0,
            ..DiagnosisInput::default()
        });
        assert!(d.is_healthy(), "unexpected findings: {:?}", d.findings);
        assert_eq!(d.implied_state, NodeState::Running);
    }

    #[test]
    fn diagnose_flags_crash_loop_and_low_disk() {
        let d = diagnose(&DiagnosisInput {
            engine_phase: EnginePhase::Failed,
            disk_free_gb: 1.0,
            disk_free_warn_gb: 5.0,
            ..DiagnosisInput::default()
        });
        assert_eq!(d.implied_state, NodeState::Degraded);
        assert!(d.findings.iter().any(|f| f.contains("crash loop")));
        assert!(d.findings.iter().any(|f| f.contains("low disk")));
    }

    #[test]
    fn diagnose_control_plane_loss_is_unreachable() {
        let d = diagnose(&DiagnosisInput {
            engine_phase: EnginePhase::Running,
            control_plane: Liveness::Unhealthy,
            disk_free_gb: 500.0,
            ..DiagnosisInput::default()
        });
        assert_eq!(d.implied_state, NodeState::Unreachable);
        assert!(d.findings.iter().any(|f| f.contains("control plane")));
    }

    #[tokio::test]
    async fn signed_update_is_refused_by_default() {
        let plan = UpdatePlan {
            version: "9.9.9".into(),
            url: "https://mirror/agent".into(),
            signature: vec![1, 2, 3],
        };
        let err = apply_signed_update(&RejectingVerifier, &plan, b"binary")
            .await
            .unwrap_err();
        assert!(err.to_string().contains("not implemented"));
    }
}
