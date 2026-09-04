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
}

impl DefaultProbe {
    /// Create a probe stamping profiles with `node_id` (may be empty pre-Join).
    pub fn new(node_id: impl Into<String>) -> Self {
        Self {
            node_id: node_id.into(),
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
            // TODO(phase2): populated by the supervisor from installed engines.
            engine_versions: HashMap::new(),
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
        gpus.push(GpuInfo {
            name,
            vram_gb,
            // Discrete NVIDIA GPUs do not share host memory.
            unified: false,
            // TODO(phase2): detect native FP4 (Blackwell+) from compute cap.
            fp4_native: false,
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
}
