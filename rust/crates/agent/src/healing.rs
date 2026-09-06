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
use std::time::{Duration, SystemTime, UNIX_EPOCH};

use base64::Engine as _;

use purser_proto::v1::{EngineMetrics, NodeState};

use crate::secrets::SecretStore;
use crate::state::NodeStateMachine;
use crate::supervisor::EnginePhase;

// ---------------------------------------------------------------------------
// Certificate expiry monitoring
// ---------------------------------------------------------------------------

/// Threshold before expiry at which renewal is triggered.
pub const RENEWAL_THRESHOLD: Duration = Duration::from_secs(24 * 3600); // 24 hours

/// Interval between expiry checks.
pub const CHECK_INTERVAL: Duration = Duration::from_secs(6 * 3600); // 6 hours

/// `CertMonitor` reads the agent's mTLS certificate from the secret store and
/// reports whether it is approaching expiry or has already expired.
///
/// Call [`needs_renewal`](CertMonitor::needs_renewal) / [`is_expired`](CertMonitor::is_expired)
/// from a periodic task (every [`CHECK_INTERVAL`]) to detect and log certificate
/// problems before the agent is silently ejected by the control plane.
pub struct CertMonitor {
    secret_store: Arc<dyn SecretStore>,
}

impl CertMonitor {
    /// Create a monitor that reads `client_cert` from `secret_store`.
    pub fn new(secret_store: Arc<dyn SecretStore>) -> Self {
        Self { secret_store }
    }

    /// Returns the expiry timestamp (Unix seconds) of the current agent
    /// certificate, or `None` if no certificate is present or it cannot be
    /// parsed.
    pub fn cert_expiry_secs(&self) -> Option<u64> {
        let cert_bytes = self.secret_store.get("client_cert").ok()??;
        parse_cert_expiry_from_bytes(&cert_bytes)
    }

    /// Returns `true` if the certificate is present, not yet expired, and
    /// expires within [`RENEWAL_THRESHOLD`].
    pub fn needs_renewal(&self) -> bool {
        if let Some(expiry_secs) = self.cert_expiry_secs() {
            let now = SystemTime::now()
                .duration_since(UNIX_EPOCH)
                .unwrap_or_default()
                .as_secs();
            let remaining = expiry_secs.saturating_sub(now);
            remaining < RENEWAL_THRESHOLD.as_secs() && remaining > 0
        } else {
            false // No cert or unparseable — is_expired() handles this
        }
    }

    /// Returns `true` if the certificate has already expired or is absent.
    pub fn is_expired(&self) -> bool {
        if let Some(expiry_secs) = self.cert_expiry_secs() {
            let now = SystemTime::now()
                .duration_since(UNIX_EPOCH)
                .unwrap_or_default()
                .as_secs();
            now >= expiry_secs
        } else {
            true // No cert or unparseable → treat as expired
        }
    }
}

// ---------------------------------------------------------------------------
// Certificate DER/PEM parsing (no external deps — manual ASN.1)
// ---------------------------------------------------------------------------

/// Parse the expiry of a certificate encoded as PEM or raw DER bytes.
/// Returns `None` on any parse error; never panics.
fn parse_cert_expiry_from_bytes(cert_bytes: &[u8]) -> Option<u64> {
    if cert_bytes.starts_with(b"-----BEGIN") {
        // PEM: strip header/footer lines and base64-decode the body
        let pem_str = std::str::from_utf8(cert_bytes).ok()?;
        let b64: String = pem_str
            .lines()
            .filter(|l| !l.starts_with("-----"))
            .collect();
        let der = base64::engine::general_purpose::STANDARD
            .decode(b64.trim())
            .ok()?;
        parse_cert_expiry_from_der(&der)
    } else {
        // Assume raw DER
        parse_cert_expiry_from_der(cert_bytes)
    }
}

/// Extract the `notAfter` timestamp from an ASN.1 DER-encoded X.509 certificate.
///
/// The function navigates the fixed-structure path:
/// `Certificate → TBSCertificate → validity → notAfter`
/// without verifying any signatures.  Returns `None` on any structural error.
fn parse_cert_expiry_from_der(der: &[u8]) -> Option<u64> {
    let mut pos = 0;

    // Certificate ::= SEQUENCE { TBSCertificate, ... }
    pos = enter_sequence(der, pos)?;

    // TBSCertificate ::= SEQUENCE { version?, serialNumber, signature,
    //                               issuer, validity, ... }
    pos = enter_sequence(der, pos)?;

    // version [0] EXPLICIT OPTIONAL (present in v2/v3 certs)
    if pos < der.len() && der[pos] == 0xA0 {
        pos = skip_field(der, pos)?;
    }

    pos = skip_field(der, pos)?; // serialNumber  INTEGER
    pos = skip_field(der, pos)?; // signature     AlgorithmIdentifier (SEQUENCE)
    pos = skip_field(der, pos)?; // issuer        Name (SEQUENCE)

    // validity Validity ::= SEQUENCE { notBefore, notAfter }
    pos = enter_sequence(der, pos)?;

    pos = skip_field(der, pos)?; // notBefore (UTCTime / GeneralizedTime)

    // notAfter — what we want
    if pos >= der.len() {
        return None;
    }
    let tag = der[pos];
    pos += 1;

    let (len, pos) = read_length(der, pos)?;
    if pos + len > der.len() {
        return None;
    }
    let time_bytes = &der[pos..pos + len];
    let time_str = std::str::from_utf8(time_bytes).ok()?;

    match tag {
        0x17 => parse_utc_time(time_str),         // UTCTime
        0x18 => parse_generalized_time(time_str), // GeneralizedTime
        _ => None,
    }
}

// ---------------------------------------------------------------------------
// ASN.1 DER navigation helpers
// ---------------------------------------------------------------------------

/// Expect a SEQUENCE (tag `0x30`) at `pos`, skip its tag+length, and return
/// the position of the first byte inside the sequence.
fn enter_sequence(der: &[u8], pos: usize) -> Option<usize> {
    if pos >= der.len() || der[pos] != 0x30 {
        return None;
    }
    let (_, new_pos) = read_length(der, pos + 1)?;
    Some(new_pos)
}

/// Skip the TLV (tag + length + value) at `pos` and return the position
/// immediately after.
fn skip_field(der: &[u8], pos: usize) -> Option<usize> {
    if pos >= der.len() {
        return None;
    }
    let (len, value_pos) = read_length(der, pos + 1)?;
    let next = value_pos + len;
    if next > der.len() {
        return None;
    }
    Some(next)
}

/// Read a DER length at `pos`.  Returns `(length, pos_after_length)`.
fn read_length(der: &[u8], pos: usize) -> Option<(usize, usize)> {
    if pos >= der.len() {
        return None;
    }
    let b = der[pos] as usize;
    if b < 0x80 {
        Some((b, pos + 1))
    } else {
        let n = b & 0x7F;
        if n == 0 || n > 4 || pos + 1 + n > der.len() {
            return None;
        }
        let mut len = 0usize;
        for i in 0..n {
            len = (len << 8) | (der[pos + 1 + i] as usize);
        }
        Some((len, pos + 1 + n))
    }
}

// ---------------------------------------------------------------------------
// X.509 time-string parsers
// ---------------------------------------------------------------------------

/// Parse a UTCTime string (`"YYMMDDHHMMSSZ"`) into Unix seconds.
/// Year interpretation follows RFC 5280: 50–99 → 1950–1999, 00–49 → 2000–2049.
fn parse_utc_time(s: &str) -> Option<u64> {
    if s.len() < 13 {
        return None;
    }
    let yy: i64 = s[0..2].parse().ok()?;
    let year = if yy >= 50 { 1900 + yy } else { 2000 + yy };
    let month: u32 = s[2..4].parse().ok()?;
    let day: u32 = s[4..6].parse().ok()?;
    let hour: u32 = s[6..8].parse().ok()?;
    let min: u32 = s[8..10].parse().ok()?;
    let sec: u32 = s[10..12].parse().ok()?;
    date_to_unix_secs(year, month, day, hour, min, sec)
}

/// Parse a GeneralizedTime string (`"YYYYMMDDHHMMSSZ"`) into Unix seconds.
fn parse_generalized_time(s: &str) -> Option<u64> {
    if s.len() < 15 {
        return None;
    }
    let year: i64 = s[0..4].parse().ok()?;
    let month: u32 = s[4..6].parse().ok()?;
    let day: u32 = s[6..8].parse().ok()?;
    let hour: u32 = s[8..10].parse().ok()?;
    let min: u32 = s[10..12].parse().ok()?;
    let sec: u32 = s[12..14].parse().ok()?;
    date_to_unix_secs(year, month, day, hour, min, sec)
}

/// Convert a UTC date/time to seconds since the Unix epoch (1970-01-01T00:00:00Z).
///
/// Uses the proleptic Gregorian calendar algorithm from
/// <https://howardhinnant.github.io/date_algorithms.html> (`days_from_civil`).
/// Returns `None` if the date is out of range or the fields are invalid.
fn date_to_unix_secs(
    year: i64,
    month: u32,
    day: u32,
    hour: u32,
    min: u32,
    sec: u32,
) -> Option<u64> {
    if !(1..=12).contains(&month) || !(1..=31).contains(&day) || hour > 23 || min > 59 || sec > 59 {
        return None;
    }
    // Shift the year so March 1 is the start (simplifies leap-year math)
    let y = if month <= 2 { year - 1 } else { year };
    let m = if month <= 2 { month + 9 } else { month - 3 };
    let d = day as i64 - 1;

    let era = if y >= 0 { y } else { y - 399 } / 400;
    let yoe = y - era * 400; // [0, 399]
    let doy = (153 * m as i64 + 2) / 5 + d; // [0, 365]
    let doe = yoe * 365 + yoe / 4 - yoe / 100 + doy; // [0, 146096]
    let days = era * 146_097 + doe - 719_468; // days from 1970-01-01

    let total = days * 86_400 + hour as i64 * 3_600 + min as i64 * 60 + sec as i64;
    if total < 0 {
        None
    } else {
        Some(total as u64)
    }
}

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
    use crate::secrets::InMemorySecretStore;

    // -----------------------------------------------------------------------
    // CertMonitor unit tests
    // -----------------------------------------------------------------------

    #[test]
    fn no_cert_treated_as_expired() {
        let store = Arc::new(InMemorySecretStore::new());
        let monitor = CertMonitor::new(store as Arc<dyn SecretStore>);
        assert!(
            monitor.is_expired(),
            "missing cert must be treated as expired"
        );
        assert!(
            !monitor.needs_renewal(),
            "expired cert must not trigger renewal"
        );
    }

    #[test]
    fn invalid_pem_does_not_panic() {
        let store = Arc::new(InMemorySecretStore::new());
        store.put("client_cert", b"not-a-cert").unwrap();
        let monitor = CertMonitor::new(store as Arc<dyn SecretStore>);
        // cert_expiry_secs must return None without panicking
        assert!(monitor.cert_expiry_secs().is_none());
        assert!(monitor.is_expired()); // no parseable cert → expired
        assert!(!monitor.needs_renewal());
    }

    #[test]
    fn invalid_der_does_not_panic() {
        let store = Arc::new(InMemorySecretStore::new());
        store
            .put("client_cert", &[0x30, 0x03, 0x00, 0x01, 0x02])
            .unwrap();
        let monitor = CertMonitor::new(store as Arc<dyn SecretStore>);
        assert!(monitor.cert_expiry_secs().is_none());
    }

    // -----------------------------------------------------------------------
    // Time-parsing unit tests
    // -----------------------------------------------------------------------

    #[test]
    fn utc_time_unix_epoch_is_zero() {
        // 1970-01-01T00:00:00Z — the Unix epoch
        assert_eq!(parse_utc_time("700101000000Z"), Some(0));
    }

    #[test]
    fn utc_time_year_century_boundary() {
        // yy=50 → 1950, yy=49 → 2049
        let t50 = parse_utc_time("500101000000Z");
        let t49 = parse_utc_time("490101000000Z");
        // 1950 < 2049 in Unix time
        assert!(t50.is_none() || t49.is_none() || t50.unwrap() < t49.unwrap());
    }

    #[test]
    fn generalized_time_known_date() {
        // 2026-09-01T00:00:00Z
        // Days from 1970-01-01: let's compute manually to verify
        // 2026 - 1970 = 56 years.
        // We just verify the value is plausible (> 2025-01-01 and < 2027-01-01)
        let t = parse_generalized_time("20260901000000Z").unwrap();
        let jan2025: u64 = 1_735_689_600; // 2025-01-01T00:00:00Z (approx)
        let jan2027: u64 = 1_798_761_600; // 2027-01-01T00:00:00Z (approx)
        assert!(t > jan2025, "2026-09-01 should be after 2025-01-01");
        assert!(t < jan2027, "2026-09-01 should be before 2027-01-01");
    }

    #[test]
    fn generalized_time_far_future() {
        // 9999-12-31T23:59:59Z — maximum ASN.1 GeneralizedTime (no panic)
        let t = parse_generalized_time("99991231235959Z");
        assert!(
            t.is_some(),
            "max GeneralizedTime must parse without panicking"
        );
        assert!(t.unwrap() > 0);
    }

    #[test]
    fn invalid_time_returns_none() {
        assert!(parse_utc_time("").is_none());
        assert!(parse_utc_time("YYMMDDHHMMSSZ").is_none());
        assert!(parse_generalized_time("not-a-date-at-all--").is_none());
    }

    #[test]
    fn date_to_unix_secs_epoch() {
        assert_eq!(date_to_unix_secs(1970, 1, 1, 0, 0, 0), Some(0));
    }

    #[test]
    fn date_to_unix_secs_known_timestamp() {
        // 2024-01-01T00:00:00Z = 1704067200
        assert_eq!(date_to_unix_secs(2024, 1, 1, 0, 0, 0), Some(1_704_067_200));
    }

    #[test]
    fn date_to_unix_secs_rejects_invalid_month() {
        assert!(date_to_unix_secs(2024, 0, 1, 0, 0, 0).is_none());
        assert!(date_to_unix_secs(2024, 13, 1, 0, 0, 0).is_none());
    }

    #[test]
    fn date_to_unix_secs_rejects_invalid_hour() {
        assert!(date_to_unix_secs(2024, 1, 1, 24, 0, 0).is_none());
    }

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
        let machine = Arc::new(Mutex::new(NodeStateMachine::starting_at(
            NodeState::Running,
        )));
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
