//! Runtime configuration for the llama.cpp adapter.
//!
//! Binary locations are *never* assumed to be installed. They are resolved from
//! configuration or environment variables and only touched when a worker/host is
//! actually started, so the pure logic (flag building, metrics parsing, GGUF
//! discovery) is testable on a machine without llama.cpp or a GPU.
//!
//! ## Environment variables
//!
//! - `PURSER_LLAMACPP_BIN` — directory that contains both `rpc-server` and
//!   `llama-server`.
//! - `PURSER_LLAMACPP_RPC_SERVER_BIN` — explicit path to the `rpc-server` binary
//!   (wins over `PURSER_LLAMACPP_BIN`).
//! - `PURSER_LLAMACPP_LLAMA_SERVER_BIN` — explicit path to the `llama-server`
//!   binary (wins over `PURSER_LLAMACPP_BIN`).
//! - `PURSER_LLAMACPP_TRUSTED_SUBNETS` — comma-separated CIDRs the `rpc-server`
//!   worker is allowed to bind to (see [`crate::security`]).
//! - `PURSER_LLAMACPP_HOST_BIND` / `PURSER_LLAMACPP_HOST_PORT` /
//!   `PURSER_LLAMACPP_ADVERTISE_HOST` / `PURSER_LLAMACPP_NGL` — host serving
//!   overrides.

use std::path::PathBuf;
use std::time::Duration;

use crate::security::{default_trusted_subnets, parse_subnets, Cidr};

/// Default filename of the compute-only worker binary.
pub const DEFAULT_RPC_SERVER_BIN: &str = "rpc-server";
/// Default filename of the model-hosting server binary.
pub const DEFAULT_LLAMA_SERVER_BIN: &str = "llama-server";
/// Default number of layers offloaded to the accelerator (`-ngl`). `99` means
/// "offload everything", which is the usual pipeline-parallel configuration.
pub const DEFAULT_NGL: u32 = 99;
/// Default client-facing serving port for `llama-server`.
pub const DEFAULT_HOST_PORT: u16 = 8080;

/// Everything the adapter needs to launch and reason about llama.cpp processes.
#[derive(Clone, Debug)]
pub struct LlamaCppConfig {
    /// Path (or bare name resolved via `PATH`) of the `rpc-server` worker binary.
    pub rpc_server_bin: PathBuf,
    /// Path (or bare name resolved via `PATH`) of the `llama-server` host binary.
    pub llama_server_bin: PathBuf,
    /// Value passed to `-ngl` when starting a host.
    pub n_gpu_layers: u32,
    /// Address the host's OpenAI-compatible API binds to (`--host`). Defaults to
    /// `0.0.0.0`: this is the *client* API, not the unsandboxed RPC compute port.
    pub host_bind: String,
    /// Client-facing serving port (`--port`).
    pub host_port: u16,
    /// Hostname/IP advertised back to callers in the serving endpoint URL. If
    /// empty, a sensible value is derived from `host_bind`.
    pub advertise_host: String,
    /// CIDRs a worker's `rpc-server` is permitted to bind to. The wildcard
    /// (`0.0.0.0` / `::`) is *always* rejected regardless of this list.
    pub trusted_subnets: Vec<Cidr>,
    /// How long to wait for a freshly launched process to reach READY.
    pub startup_timeout: Duration,
    /// Grace period between `SIGTERM` and `SIGKILL` when stopping a process.
    pub stop_grace: Duration,
}

impl Default for LlamaCppConfig {
    /// Pure, environment-independent defaults (deterministic for tests).
    fn default() -> Self {
        Self {
            rpc_server_bin: PathBuf::from(DEFAULT_RPC_SERVER_BIN),
            llama_server_bin: PathBuf::from(DEFAULT_LLAMA_SERVER_BIN),
            n_gpu_layers: DEFAULT_NGL,
            host_bind: "0.0.0.0".to_string(),
            host_port: DEFAULT_HOST_PORT,
            advertise_host: "127.0.0.1".to_string(),
            trusted_subnets: default_trusted_subnets(),
            startup_timeout: Duration::from_secs(120),
            stop_grace: Duration::from_secs(10),
        }
    }
}

impl LlamaCppConfig {
    /// Build a config from [`Default`], overlaying any recognised environment
    /// variables. Never fails: malformed values fall back to the default.
    pub fn from_env() -> Self {
        let mut cfg = Self::default();

        let bin_dir = std::env::var_os("PURSER_LLAMACPP_BIN").map(PathBuf::from);

        if let Some(p) = std::env::var_os("PURSER_LLAMACPP_RPC_SERVER_BIN") {
            cfg.rpc_server_bin = PathBuf::from(p);
        } else if let Some(dir) = &bin_dir {
            cfg.rpc_server_bin = dir.join(DEFAULT_RPC_SERVER_BIN);
        }

        if let Some(p) = std::env::var_os("PURSER_LLAMACPP_LLAMA_SERVER_BIN") {
            cfg.llama_server_bin = PathBuf::from(p);
        } else if let Some(dir) = &bin_dir {
            cfg.llama_server_bin = dir.join(DEFAULT_LLAMA_SERVER_BIN);
        }

        if let Ok(s) = std::env::var("PURSER_LLAMACPP_TRUSTED_SUBNETS") {
            if let Ok(subnets) = parse_subnets(&s) {
                if !subnets.is_empty() {
                    cfg.trusted_subnets = subnets;
                }
            }
        }

        if let Ok(s) = std::env::var("PURSER_LLAMACPP_HOST_BIND") {
            if !s.is_empty() {
                cfg.host_bind = s;
            }
        }
        if let Ok(s) = std::env::var("PURSER_LLAMACPP_ADVERTISE_HOST") {
            if !s.is_empty() {
                cfg.advertise_host = s;
            }
        }
        if let Ok(s) = std::env::var("PURSER_LLAMACPP_HOST_PORT") {
            if let Ok(p) = s.parse::<u16>() {
                cfg.host_port = p;
            }
        }
        if let Ok(s) = std::env::var("PURSER_LLAMACPP_NGL") {
            if let Ok(n) = s.parse::<u32>() {
                cfg.n_gpu_layers = n;
            }
        }

        cfg
    }

    /// Whether both binaries resolve to existing files. Used to gate the
    /// opt-in live conformance test. A bare name (no path separator) is treated
    /// as "resolved via `PATH`" and reported as present so PATH-based setups are
    /// not skipped.
    pub fn binaries_present(&self) -> bool {
        binary_present(&self.rpc_server_bin) && binary_present(&self.llama_server_bin)
    }
}

fn binary_present(p: &std::path::Path) -> bool {
    // A bare filename (no separator) will be resolved via PATH at spawn time; we
    // cannot cheaply confirm it here, so assume present and let spawn surface a
    // clear error otherwise.
    if p.components().count() <= 1 {
        return true;
    }
    p.is_file()
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn defaults_are_pure_and_sane() {
        let c = LlamaCppConfig::default();
        assert_eq!(c.rpc_server_bin, PathBuf::from("rpc-server"));
        assert_eq!(c.llama_server_bin, PathBuf::from("llama-server"));
        assert_eq!(c.n_gpu_layers, 99);
        assert_eq!(c.host_port, 8080);
        assert!(!c.trusted_subnets.is_empty(), "must ship default subnets");
    }

    #[test]
    fn bare_binary_names_are_reported_present() {
        // Bare names are PATH-resolved; we optimistically treat them as present.
        let c = LlamaCppConfig::default();
        assert!(c.binaries_present());
    }

    #[test]
    fn absolute_missing_binary_reported_absent() {
        let mut c = LlamaCppConfig::default();
        c.rpc_server_bin = PathBuf::from("/nonexistent/definitely/rpc-server");
        assert!(!c.binaries_present());
    }
}
