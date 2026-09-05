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
use std::sync::OnceLock;
use std::time::{Duration, Instant, SystemTime};

use purser_proto::v1::{Arch, Backend, GpuInfo, HardwareProfile, NodeState, Os};
use sysinfo::{Disks, System};

/// Bytes in one binary gigabyte (GiB). All `*_gb` fields in [`HardwareProfile`]
/// are reported in these units.
const BYTES_PER_GB: f64 = 1024.0 * 1024.0 * 1024.0;

/// Cached memory bandwidth in GB/s, measured once at first probe.
/// Subsequent calls to [`DefaultProbe::probe`] return the stored value without
/// re-running the benchmark.
static CACHED_MEM_BW_GBS: OnceLock<f32> = OnceLock::new();

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

        // Memory bandwidth: measured once at startup; subsequent probes are free.
        let mem_bandwidth_gbs =
            *CACHED_MEM_BW_GBS.get_or_init(measure_mem_bandwidth_gbs) as f64;

        // Accelerator discovery: each backend enumerates independently so we can
        // emit precise backend tags (CUDA / ROCm / Metal) per GPU vendor.
        let mut gpus: Vec<GpuInfo> = Vec::new();
        let mut backends = vec![Backend::Cpu as i32];

        // NVIDIA: feature-gated NVML discovery.
        let nvidia = detect_nvml_gpus();
        if !nvidia.is_empty() {
            backends.push(Backend::Cuda as i32);
            gpus.extend(nvidia);
        }

        // AMD: sysfs-based ROCm discovery (Linux only, compile-time gated on
        // non-Linux to avoid dead code warnings).
        let rocm = detect_rocm_gpus();
        if !rocm.is_empty() {
            backends.push(Backend::Rocm as i32);
            gpus.extend(rocm);
        }

        // Apple: Metal detection (macOS only, compile-time gated).
        let metal = detect_metal_gpus();
        if !metal.is_empty() {
            backends.push(Backend::Metal as i32);
            gpus.extend(metal);
        }

        HardwareProfile {
            node_id: self.node_id.clone(),
            hostname,
            os: detect_os() as i32,
            arch: detect_arch() as i32,
            backends,
            gpus,
            ram_total_gb,
            ram_available_gb,
            mem_bandwidth_gbs,
            disk_free_gb,
            engine_versions: build_engine_versions(&self.backends),
            last_seen: Some(prost_types::Timestamp::from(SystemTime::now())),
            // A node that can answer Probe is up and ready to be scheduled.
            state: NodeState::Ready as i32,
        }
    }
}

// ---------------------------------------------------------------------------
// Memory bandwidth microbenchmark
// ---------------------------------------------------------------------------

/// Measure host RAM read+write bandwidth by streaming a 256 MiB buffer for
/// ~100 ms. Returns GB/s (SI: 1 GB = 1e9 bytes, matching industry convention
/// for memory bandwidth figures). Returns 0.0 only if the timer shows zero
/// elapsed (should never happen in practice).
///
/// Methodology: a single `Vec<u64>` large enough to exceed a typical server L3
/// cache is page-faulted in before the timed loop starts. Each pass reads every
/// element (accumulating into `sink` to defeat dead-code elimination) then
/// writes it back. `std::hint::black_box` prevents the compiler from eliding
/// `sink` entirely. The result is both-direction bandwidth — reads + writes —
/// consistent with how STREAM and similar tools report bandwidth.
///
/// An env override `PURSER_AGENT_MEM_BW_OVERRIDE_GBS` (f32) bypasses the
/// benchmark entirely, which is useful for calibration or CI environments where
/// the measurement is meaningless.
fn measure_mem_bandwidth_gbs() -> f32 {
    if let Ok(val) = std::env::var("PURSER_AGENT_MEM_BW_OVERRIDE_GBS") {
        if let Ok(v) = val.parse::<f32>() {
            tracing::debug!(override_gbs = v, "using mem-bandwidth env override");
            return v;
        }
    }

    // 256 MiB buffer: large enough to exceed L3 cache on most server CPUs
    // (typical L3 is 8–64 MiB), ensuring we actually measure DRAM bandwidth.
    const BUF_ELEMS: usize = (256 * 1024 * 1024) / std::mem::size_of::<u64>();
    const MEASURE_DURATION: Duration = Duration::from_millis(100);

    // Allocate with a non-zero pattern so the OS actually backs all pages
    // before the timer starts (avoids measuring page-fault cost).
    let mut buf: Vec<u64> = vec![1u64; BUF_ELEMS];

    let start = Instant::now();
    let mut passes: u64 = 0;
    let mut sink: u64 = 0;

    while start.elapsed() < MEASURE_DURATION {
        for v in buf.iter_mut() {
            sink = sink.wrapping_add(*v);
            *v = passes;
        }
        passes += 1;
    }

    // Prevent the optimizer from treating `sink` as dead code.
    let _ = std::hint::black_box(sink);

    let elapsed = start.elapsed();
    if elapsed.is_zero() || passes == 0 {
        return 0.0;
    }

    // Each pass reads BUF_ELEMS * 8 bytes and writes the same amount.
    let bytes_per_pass = BUF_ELEMS * std::mem::size_of::<u64>() * 2; // read + write
    let total_bytes = bytes_per_pass as f64 * passes as f64;
    (total_bytes / 1e9 / elapsed.as_secs_f64()) as f32
}

// ---------------------------------------------------------------------------
// NVIDIA / NVML
// ---------------------------------------------------------------------------

/// Enumerate NVIDIA GPUs via NVML.
///
/// Built only with the `nvml` feature (off by default), because it dlopens
/// `libnvidia-ml` at runtime — absent on CPU-only build/CI hosts. Any NVML error
/// (no driver, no GPU) degrades gracefully to "no GPUs" so a probe never fails.
#[cfg(feature = "nvml")]
fn detect_nvml_gpus() -> Vec<GpuInfo> {
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

/// No-op NVML discovery when the feature is not enabled.
#[cfg(not(feature = "nvml"))]
fn detect_nvml_gpus() -> Vec<GpuInfo> {
    Vec::new()
}

// ---------------------------------------------------------------------------
// AMD ROCm (Linux only)
// ---------------------------------------------------------------------------

/// Detect AMD ROCm GPUs via sysfs and optionally `rocm-smi`.
///
/// Detection strategy:
/// 1. Gate on `/dev/kfd` — the ROCm Kernel Fusion Driver device node that must
///    exist for ROCm to be usable at all.
/// 2. Walk `/sys/class/drm/card*` entries; for each card whose
///    `device/uevent` contains `DRIVER=amdgpu`, read
///    `device/mem_info_vram_total` for VRAM in bytes.
/// 3. If `rocm-smi --showmeminfo vram --noheader` is in PATH and succeeds,
///    its VRAM values replace the sysfs figures (more precise on some kernels).
///
/// Any I/O error is treated as "no AMD GPUs" — this function must never panic.
#[cfg(target_os = "linux")]
fn detect_rocm_gpus() -> Vec<GpuInfo> {
    use std::path::Path;

    // Quick gate: without /dev/kfd the ROCm stack is non-functional.
    if !Path::new("/dev/kfd").exists() {
        return Vec::new();
    }

    let drm_dir = match std::fs::read_dir("/sys/class/drm") {
        Ok(d) => d,
        Err(_) => return Vec::new(),
    };

    let mut gpus: Vec<GpuInfo> = Vec::new();

    for entry in drm_dir.flatten() {
        let fname = entry.file_name();
        let name_str = fname.to_string_lossy();

        // Accept only `card0`, `card1`, … — exclude `renderD128`, connector
        // entries like `card0-DP-1`, and other non-card DRM nodes.
        if !name_str.starts_with("card") {
            continue;
        }
        let suffix = &name_str[4..];
        if suffix.is_empty() || !suffix.chars().all(|c| c.is_ascii_digit()) {
            continue;
        }

        // Confirm this card is bound to the amdgpu driver.
        let uevent_path = entry.path().join("device/uevent");
        match std::fs::read_to_string(&uevent_path) {
            Ok(uevent) if uevent.contains("DRIVER=amdgpu") => {}
            _ => continue,
        }

        // Read VRAM total from sysfs (bytes → GiB).
        let vram_path = entry.path().join("device/mem_info_vram_total");
        let vram_gb = std::fs::read_to_string(&vram_path)
            .ok()
            .and_then(|s| s.trim().parse::<u64>().ok())
            .map(|bytes| bytes as f64 / BYTES_PER_GB)
            .unwrap_or(0.0);

        gpus.push(GpuInfo {
            name: "AMD GPU (ROCm)".to_string(),
            vram_gb,
            unified: false,
            fp4_native: false,
            count: 1,
        });
    }

    // Optional: let rocm-smi refine the VRAM figures.
    if !gpus.is_empty() {
        if let Some(smi_vrams) = rocm_smi_query(gpus.len()) {
            for (gpu, vram) in gpus.iter_mut().zip(smi_vrams) {
                gpu.vram_gb = vram;
            }
        }
    }

    gpus
}

/// Query VRAM totals from `rocm-smi`. Returns per-GPU VRAM in GiB, or `None`
/// if the binary is absent, returns an error, or the count doesn't match.
#[cfg(target_os = "linux")]
fn rocm_smi_query(expected_count: usize) -> Option<Vec<f64>> {
    use std::process::Command;

    let output = Command::new("rocm-smi")
        .args(["--showmeminfo", "vram", "--noheader"])
        .output()
        .ok()?;

    if !output.status.success() {
        return None;
    }

    let stdout = String::from_utf8_lossy(&output.stdout);
    // Lines of the form: "GPU[0] : VRAM Total Memory (B): 17163091968"
    let mut vrams: Vec<f64> = Vec::new();
    for line in stdout.lines() {
        if line.contains("VRAM Total Memory (B):") {
            if let Some(bytes_str) = line.split(':').last() {
                if let Ok(bytes) = bytes_str.trim().parse::<u64>() {
                    vrams.push(bytes as f64 / BYTES_PER_GB);
                }
            }
        }
    }

    if vrams.len() == expected_count {
        Some(vrams)
    } else {
        None
    }
}

/// No-op ROCm discovery on non-Linux targets.
#[cfg(not(target_os = "linux"))]
fn detect_rocm_gpus() -> Vec<GpuInfo> {
    Vec::new()
}

// ---------------------------------------------------------------------------
// Apple Metal (macOS only)
// ---------------------------------------------------------------------------

/// Detect Apple GPU via `system_profiler SPDisplaysDataType`.
///
/// On macOS there is no sysfs, so we parse the human-readable text output of
/// `system_profiler`. The GPU "Chipset Model" and VRAM (when present) are
/// extracted from the indented key-value pairs. On Apple Silicon the GPU shares
/// host memory (`unified = true`); VRAM may not appear in the output, in which
/// case we report 0.0 (unknown) and let the control plane infer it from RAM.
///
/// If `system_profiler` is unavailable or fails we fall back to a minimal stub
/// that at least signals Metal availability to the backend selector.
#[cfg(target_os = "macos")]
fn detect_metal_gpus() -> Vec<GpuInfo> {
    use std::process::Command;

    let output = Command::new("system_profiler")
        .args(["SPDisplaysDataType"])
        .output();

    // Fallback stub: report Metal is present even if we can't parse details.
    let text = match output {
        Ok(o) if o.status.success() => String::from_utf8_lossy(&o.stdout).into_owned(),
        _ => {
            return vec![GpuInfo {
                name: "Apple GPU (Metal)".to_string(),
                vram_gb: 0.0,
                unified: cfg!(target_arch = "aarch64"),
                fp4_native: false,
                count: 1,
            }]
        }
    };

    let mut gpus: Vec<GpuInfo> = Vec::new();
    let mut current_name: Option<String> = None;
    let mut vram_gb: f64 = 0.0;

    for line in text.lines() {
        let trimmed = line.trim();

        // "Chipset Model: Apple M3 Pro" or "Chipset Model: AMD Radeon Pro 5500M"
        if let Some(name) = trimmed.strip_prefix("Chipset Model:") {
            // Flush the previous GPU entry if any.
            if let Some(prev) = current_name.take() {
                gpus.push(GpuInfo {
                    name: prev,
                    vram_gb,
                    unified: cfg!(target_arch = "aarch64"),
                    fp4_native: false,
                    count: 1,
                });
                vram_gb = 0.0;
            }
            current_name = Some(name.trim().to_string());
        }

        // "VRAM (Total): 16 GB" or "VRAM (Dynamic, Max): 2048 MB"
        let vram_str = trimmed
            .strip_prefix("VRAM (Total):")
            .or_else(|| trimmed.strip_prefix("VRAM (Dynamic, Max):"));
        if let Some(raw) = vram_str {
            let raw = raw.trim();
            if let Some(gb) = raw.strip_suffix(" GB") {
                vram_gb = gb.trim().parse::<f64>().unwrap_or(0.0);
            } else if let Some(mb) = raw.strip_suffix(" MB") {
                vram_gb = mb.trim().parse::<f64>().unwrap_or(0.0) / 1024.0;
            }
        }
    }

    // Flush the last GPU entry.
    if let Some(name) = current_name {
        gpus.push(GpuInfo {
            name,
            vram_gb,
            unified: cfg!(target_arch = "aarch64"),
            fp4_native: false,
            count: 1,
        });
    }

    if gpus.is_empty() {
        // Parsing produced nothing; return a minimal stub so Metal is not lost.
        gpus.push(GpuInfo {
            name: "Apple GPU (Metal)".to_string(),
            vram_gb: 0.0,
            unified: cfg!(target_arch = "aarch64"),
            fp4_native: false,
            count: 1,
        });
    }

    gpus
}

/// No-op Metal discovery on non-macOS targets.
#[cfg(not(target_os = "macos"))]
fn detect_metal_gpus() -> Vec<GpuInfo> {
    Vec::new()
}

// ---------------------------------------------------------------------------
// OS / arch helpers
// ---------------------------------------------------------------------------

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

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

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

    /// The benchmark must return a positive value on any real machine.
    #[test]
    fn mem_bandwidth_positive() {
        let bw = measure_mem_bandwidth_gbs();
        assert!(bw > 0.0, "mem_bandwidth_gbs must be positive, got {bw}");
    }

    /// When the env override is set, the benchmark is skipped and the override
    /// value is returned directly.
    ///
    /// Note: env-var mutation in tests is inherently racy when tests run in
    /// parallel. The variable name is long and unlikely to collide, and the
    /// test restores state before returning, so flakiness risk is low.
    #[test]
    fn mem_bandwidth_env_override() {
        // Use a value that a real benchmark could never produce.
        std::env::set_var("PURSER_AGENT_MEM_BW_OVERRIDE_GBS", "999.5");
        let bw = measure_mem_bandwidth_gbs();
        std::env::remove_var("PURSER_AGENT_MEM_BW_OVERRIDE_GBS");
        assert!(
            (bw - 999.5_f32).abs() < 0.01,
            "env override should return 999.5, got {bw}"
        );
    }

    /// `detect_rocm_gpus` must not panic regardless of whether AMD hardware is
    /// present. On a non-AMD machine it simply returns an empty list.
    #[test]
    fn rocm_detection_does_not_panic() {
        let gpus = detect_rocm_gpus();
        for gpu in &gpus {
            assert!(!gpu.name.is_empty(), "GPU name must not be empty");
            assert!(gpu.vram_gb >= 0.0, "VRAM must be non-negative");
        }
    }

    /// On macOS, Metal detection must not panic.
    #[test]
    #[cfg(target_os = "macos")]
    fn metal_detection_does_not_panic() {
        let gpus = detect_metal_gpus();
        // Must report at least one GPU (the stub fallback guarantees this).
        assert!(!gpus.is_empty(), "Metal detection must return at least one GPU");
        for gpu in &gpus {
            assert!(!gpu.name.is_empty(), "GPU name must not be empty");
        }
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
        // Without the `nvml` feature, detect_nvml_gpus() returns an empty Vec, so
        // this assertion is trivially true; it also guards against regressions
        // if a future no-op path incorrectly sets fp4_native = true.
        assert!(
            profile.gpus.iter().all(|g| !g.fp4_native),
            "without nvml, no GPU should report fp4_native = true"
        );
    }
}
