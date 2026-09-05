//! Hardware probing.
//!
//! The agent's job here is purely observational: describe the physical machine
//! it runs on and hand a [`HardwareProfile`] to the control plane. It makes no
//! decisions about that hardware.
//!
//! Probing sits behind the [`HardwareProbe`] trait so that accelerator-specific
//! implementations (CUDA / Metal / ROCm) can be layered in during phase 2
//! without touching the gRPC layer or the CPU baseline. [`DefaultProbe`] is the
//! implemented Linux/CPU baseline built on the `sysinfo` crate.

use std::collections::HashMap;
use std::time::SystemTime;

use purser_proto::v1::{Arch, Backend, GpuInfo, HardwareProfile, NodeState, Os};
use sysinfo::{Disks, System};

/// Bytes in one binary gigabyte (GiB). All `*_gb` fields in [`HardwareProfile`]
/// are reported in these units.
const BYTES_PER_GB: f64 = 1024.0 * 1024.0 * 1024.0;

/// Abstracts "look at this machine and describe it".
///
/// Implementations must be cheap enough to call on demand (every `Probe` RPC)
/// and safe to share across threads — the gRPC layer holds one behind an `Arc`.
pub trait HardwareProbe: Send + Sync {
    /// Inspect the host and produce a fresh [`HardwareProfile`].
    fn probe(&self) -> HardwareProfile;
}

/// The baseline probe: real RAM / disk / hostname via `sysinfo`, OS & arch from
/// the compile target, and the CPU backend. GPU discovery and memory-bandwidth
/// microbenchmarking are phase-2 additions behind this same trait.
pub struct DefaultProbe {
    /// Node identity assigned by the control plane; empty before enrollment.
    node_id: String,
    /// Engine backend names registered on this agent (from the
    /// [`BackendRegistry`](crate::supervisor::BackendRegistry)).
    /// Always contains at least `"mock"` (the built-in GPU-free backend).
    /// Used to populate [`HardwareProfile::engine_versions`] at probe time.
    backends: Vec<String>,
}

impl DefaultProbe {
    /// Create a probe stamping profiles with `node_id` (may be empty pre-Join).
    ///
    /// Registers the built-in `"mock"` backend by default. Use
    /// [`DefaultProbe::with_backends`] to pass the full registry name list so
    /// that `engine_versions` reflects all installed adapters.
    pub fn new(node_id: impl Into<String>) -> Self {
        Self {
            node_id: node_id.into(),
            backends: vec!["mock".to_string()],
        }
    }

    /// As [`DefaultProbe::new`], but also records the full list of registered
    /// backend names for [`HardwareProfile::engine_versions`].
    ///
    /// Typically called from `main` with `registry.names()` so every installed
    /// adapter appears in the heartbeat payload. `"mock"` is always included
    /// even if absent from `backends`.
    pub fn with_backends(
        node_id: impl Into<String>,
        backends: impl IntoIterator<Item = impl AsRef<str>>,
    ) -> Self {
        let mut b: Vec<String> = backends.into_iter().map(|s| s.as_ref().to_string()).collect();
        if !b.contains(&"mock".to_string()) {
            b.insert(0, "mock".to_string());
        }
        Self {
            node_id: node_id.into(),
            backends: b,
        }
    }
}

impl HardwareProbe for DefaultProbe {
    fn probe(&self) -> HardwareProfile {
        // Refresh only what we read; sysinfo defaults to a lazy, empty System.
        let mut sys = System::new();
        sys.refresh_memory();

        let ram_total_gb = sys.total_memory() as f64 / BYTES_PER_GB;
        let ram_available_gb = sys.available_memory() as f64 / BYTES_PER_GB;
        let disk_free_gb = probe_disk_free_gb();
        let hostname = System::host_name().unwrap_or_else(|| "unknown".to_string());

        // Accelerator discovery sits behind this same trait: `detect_gpus`
        // enumerates NVIDIA GPUs when built with the `nvml` feature, and is a
        // no-op otherwise (see the two cfg variants below).
        let gpus = detect_gpus();
        let mut backends = vec![Backend::Cpu as i32];
        if !gpus.is_empty() {
            // Any enumerated NVIDIA GPU implies a CUDA backend.
            backends.push(Backend::Cuda as i32);
        }

        HardwareProfile {
            node_id: self.node_id.clone(),
            hostname,
            os: detect_os() as i32,
            arch: detect_arch() as i32,
            // Every node can at least run on CPU; CUDA is appended when GPUs are
            // found. Metal/ROCm probes layer in later behind this same trait.
            backends,
            gpus,
            ram_total_gb,
            ram_available_gb,
            // TODO(phase2): measure via a memory-bandwidth microbenchmark.
            mem_bandwidth_gbs: 0.0,
            disk_free_gb,
            engine_versions: build_engine_versions(&self.backends),
            last_seen: Some(prost_types::Timestamp::from(SystemTime::now())),
            // A node that can answer Probe is up and ready to be scheduled.
            state: NodeState::Ready as i32,
        }
    }
}

/// Free space on the root filesystem, in GiB. Falls back to the largest single
/// filesystem when no `/` mount is reported (best-effort, non-Linux hosts).
fn probe_disk_free_gb() -> f64 {
    let disks = Disks::new_with_refreshed_list();

    let root_free = disks
        .list()
        .iter()
        .find(|d| d.mount_point().as_os_str() == "/")
        .map(|d| d.available_space());

    let bytes = root_free.unwrap_or_else(|| {
        disks
            .list()
            .iter()
            .map(|d| d.available_space())
            .max()
            .unwrap_or(0)
    });

    bytes as f64 / BYTES_PER_GB
}

/// Map the compile-time target OS onto the proto enum.
fn detect_os() -> Os {
    match std::env::consts::OS {
        "linux" => Os::Linux,
        "macos" => Os::Darwin,
        "windows" => Os::Windows,
        _ => Os::Unspecified,
    }
}

/// Map the compile-time target architecture onto the proto enum.
fn detect_arch() -> Arch {
    match std::env::consts::ARCH {
        "x86_64" => Arch::X8664,
        "aarch64" | "arm64" => Arch::Arm64,
        _ => Arch::Unspecified,
    }
}

/// Enumerate NVIDIA GPUs via NVML.
///
/// Built only with the `nvml` feature (off by default), because it dlopens
/// `libnvidia-ml` at runtime — absent on CPU-only build/CI hosts. Any NVML error
/// (no driver, no GPU) degrades gracefully to "no GPUs" so a probe never fails.
#[cfg(feature = "nvml")]
fn detect_gpus() -> Vec<GpuInfo> {
    match enumerate_nvml() {
        Ok(gpus) => gpus,
        Err(e) => {
            tracing::debug!(error = %e, "NVML GPU enumeration unavailable");
            Vec::new()
        }
    }
}

#[cfg(feature = "nvml")]
fn enumerate_nvml() -> Result<Vec<GpuInfo>, nvml_wrapper::error::NvmlError> {
    use nvml_wrapper::Nvml;

    let nvml = Nvml::init()?;
    let count = nvml.device_count()?;
    let mut gpus = Vec::with_capacity(count as usize);
    for index in 0..count {
        let device = nvml.device_by_index(index)?;
        let name = device.name().unwrap_or_else(|_| "NVIDIA GPU".to_string());
        let vram_gb = device
            .memory_info()
            .map(|m| m.total as f64 / BYTES_PER_GB)
            .unwrap_or(0.0);
        // SM >= 10.0 (Blackwell+) natively supports FP4.
        // SM 8.9 (Ada Lovelace / RTX 4000 series) adds FP8 but not FP4.
        let fp4_native = device
            .cuda_compute_capability()
            .map(|cap| fp4_native_from_compute_cap(cap.major as i32, cap.minor as i32))
            .unwrap_or(false);
        gpus.push(GpuInfo {
            name,
            vram_gb,
            // Discrete NVIDIA GPUs do not share host memory.
            unified: false,
            fp4_native,
            count: 1,
        });
    }
    Ok(gpus)
}

/// No-op accelerator discovery on builds without the `nvml` feature.
///
/// TODO(phase2): additional accelerator probes behind their own features —
/// Metal (`metal`) for Apple silicon, ROCm (`rocm-smi`) for AMD — each returning
/// [`GpuInfo`] through this same seam.
#[cfg(not(feature = "nvml"))]
fn detect_gpus() -> Vec<GpuInfo> {
    Vec::new()
}

// ---- engine version helpers ------------------------------------------------

/// Build the `engine_versions` map from the list of registered backend names.
///
/// - `"mock"` → always `"built-in"`.
/// - `"llamacpp"` → tries `$PURSER_LLAMACPP_BIN --version`; falls back to
///   `"unknown"` when the variable is unset or the binary is unavailable.
/// - Any other name → `"unknown"`.
///
/// `"mock"` is always present even if absent from `backends`.
fn build_engine_versions(backends: &[String]) -> HashMap<String, String> {
    let mut map: HashMap<String, String> = backends
        .iter()
        .map(|name| (name.clone(), engine_version_for(name)))
        .collect();
    // Guarantee the invariant: mock is always present.
    map.entry("mock".to_string())
        .or_insert_with(|| "built-in".to_string());
    map
}

/// Return the version string for a single registered backend.
fn engine_version_for(name: &str) -> String {
    match name {
        "mock" => "built-in".to_string(),
        "llamacpp" => llamacpp_version(),
        _ => "unknown".to_string(),
    }
}

/// Query the llama.cpp binary version via `$PURSER_LLAMACPP_BIN --version`.
///
/// Returns `"unknown"` when:
/// - `PURSER_LLAMACPP_BIN` is unset or empty,
/// - the binary cannot be executed, or
/// - the combined stdout + stderr output is blank.
fn llamacpp_version() -> String {
    let bin = match std::env::var("PURSER_LLAMACPP_BIN") {
        Ok(b) if !b.is_empty() => b,
        _ => return "unknown".to_string(),
    };
    match std::process::Command::new(&bin).arg("--version").output() {
        Ok(out) => {
            // llama.cpp prints version info to stdout or stderr depending on the
            // build; combine both and return the first non-blank line.
            let combined = format!(
                "{}{}",
                String::from_utf8_lossy(&out.stdout),
                String::from_utf8_lossy(&out.stderr)
            );
            combined
                .lines()
                .find(|l| !l.trim().is_empty())
                .map(str::trim)
                .unwrap_or("unknown")
                .to_string()
        }
        Err(_) => "unknown".to_string(),
    }
}

// ---- FP4 compute-capability helper -----------------------------------------

/// Returns `true` when an NVIDIA GPU with CUDA compute capability
/// `(major, minor)` natively supports FP4 tensor operations.
///
/// | SM | Architecture | FP4 native |
/// |----|-------------|-----------|
/// | 10.0+ (SM ≥ 100) | Blackwell+ | yes |
/// | 8.9 (SM 89) | Ada Lovelace / RTX 4000 | no (FP8 only) |
/// | < 8.9 | older | no |
///
/// The threshold `major * 10 + minor >= 100` covers SM 10.0, 10.1, … and any
/// future Blackwell+ variant without changes.
///
/// Compiled in non-nvml builds only when running tests, so that the unit tests
/// for this pure helper can verify the SM threshold without GPU hardware.
#[cfg(any(feature = "nvml", test))]
fn fp4_native_from_compute_cap(major: i32, minor: i32) -> bool {
    major * 10 + minor >= 100
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn default_probe_reports_real_basics() {
        let profile = DefaultProbe::new("test-node").probe();

        assert_eq!(profile.node_id, "test-node");
        assert!(!profile.hostname.is_empty(), "hostname must not be empty");
        assert!(
            profile.ram_total_gb > 0.0,
            "ram_total_gb must be > 0, got {}",
            profile.ram_total_gb
        );
        assert!(profile.ram_available_gb >= 0.0);
        assert!(profile.disk_free_gb >= 0.0);
        assert!(
            profile.backends.contains(&(Backend::Cpu as i32)),
            "CPU backend must always be present"
        );
        assert_eq!(profile.state, NodeState::Ready as i32);
        assert!(profile.last_seen.is_some());
    }

    /// P3: engine_versions always contains {"mock": "built-in"}.
    #[test]
    fn engine_versions_contains_mock() {
        let profile = DefaultProbe::new("test-node").probe();
        assert_eq!(
            profile.engine_versions.get("mock").map(String::as_str),
            Some("built-in"),
            "engine_versions must always contain {{\"mock\": \"built-in\"}}"
        );
    }

    /// P3: with_backends propagates all registered names and resolves versions.
    #[test]
    fn engine_versions_with_backends_covers_all() {
        let probe = DefaultProbe::with_backends("test-node", ["mock"]);
        let profile = probe.probe();
        assert_eq!(
            profile.engine_versions.get("mock").map(String::as_str),
            Some("built-in")
        );
    }

    /// P3: with_backends always inserts mock even if caller omits it.
    #[test]
    fn engine_versions_always_includes_mock_even_when_omitted() {
        let probe = DefaultProbe::with_backends("test-node", ["llamacpp"]);
        let profile = probe.probe();
        assert_eq!(
            profile.engine_versions.get("mock").map(String::as_str),
            Some("built-in"),
            "mock must be present even when caller did not list it"
        );
    }

    /// P4: SM 10.0 (Blackwell) → fp4_native = true.
    #[test]
    fn fp4_native_true_for_sm100() {
        assert!(
            fp4_native_from_compute_cap(10, 0),
            "SM 10.0 (Blackwell) must report fp4_native = true"
        );
        // SM 10.1+ and beyond are also Blackwell+.
        assert!(fp4_native_from_compute_cap(10, 1));
        assert!(fp4_native_from_compute_cap(11, 0));
    }

    /// P4: SM 8.9 (Ada Lovelace / RTX 4000) → fp4_native = false.
    #[test]
    fn fp4_native_false_for_sm89() {
        assert!(
            !fp4_native_from_compute_cap(8, 9),
            "SM 8.9 (Ada Lovelace) must report fp4_native = false"
        );
        // Older architectures also false.
        assert!(!fp4_native_from_compute_cap(8, 6));
        assert!(!fp4_native_from_compute_cap(7, 5));
    }

    /// P4: no GPU in CI/no-nvml builds → all gpu entries have fp4_native = false.
    #[test]
    fn fp4_native_false_no_gpu() {
        let profile = DefaultProbe::new("test").probe();
        // Without the `nvml` feature, detect_gpus() returns an empty Vec, so
        // this assertion is trivially true; it also guards against regressions
        // if a future no-op path incorrectly sets fp4_native = true.
        assert!(
            profile.gpus.iter().all(|g| !g.fp4_native),
            "without nvml, no GPU should report fp4_native = true"
        );
    }
}
