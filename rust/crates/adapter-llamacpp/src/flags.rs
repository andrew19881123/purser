//! Pure translation of the abstract engine contract into concrete llama.cpp
//! command lines. These functions perform **no I/O** and launch no processes, so
//! they are exhaustively unit-testable without llama.cpp or a GPU.
//!
//! Concrete commands (from the Purser design):
//!
//! - Worker: `rpc-server -H <ip> -p <port>` — provides compute only.
//! - Host:   `llama-server -m <model.gguf> --rpc <ip1:port,ip2:port,...> -ngl 99
//!   --host 0.0.0.0 --port <p> -c <ctx>` — loads the GGUF and shards
//!   layers across the workers.

use std::collections::BTreeMap;
use std::net::SocketAddr;

use purser_engine_adapter::{EngineError, EngineParams};

use crate::config::LlamaCppConfig;

/// Argument vector for `rpc-server` (excludes the program itself), built from an
/// already-validated bind address (see [`crate::security::validate_worker_bind`]).
pub fn build_worker_args(bind: &SocketAddr) -> Vec<String> {
    vec![
        "-H".to_string(),
        bind.ip().to_string(),
        "-p".to_string(),
        bind.port().to_string(),
    ]
}

/// A fully-resolved host launch: the `llama-server` argument vector plus the
/// client-facing endpoint URL derived from the resolved bind/port.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct HostLaunch {
    /// Arguments for `llama-server` (excludes the program itself).
    pub args: Vec<String>,
    /// Non-empty client-facing serving endpoint, e.g. `http://127.0.0.1:8080`.
    pub endpoint: String,
}

/// Build the `llama-server` command for a pipeline host.
///
/// Maps the abstract inputs onto concrete flags:
/// - `model_ref`            → `-m <model_ref>` (path to the `.gguf`),
/// - `worker_addrs`         → `--rpc a,b,c` (omitted when empty: single-node),
/// - `params.context`       → `-c <ctx>` (omitted when `0`: engine default),
/// - `params.draft_block_len` → `--draft-max <n>` (speculative draft length),
/// - `config.n_gpu_layers`  → `-ngl <n>`,
/// - `config.host_bind`/`host_port` → `--host` / `--port`.
///
/// The `params.extra` map is a documented escape hatch (see [`apply_extra`]).
/// `params.pipe_depth` / `params.pipe_fill` have no native llama.cpp flag and are
/// intentionally not emitted (llama.cpp schedules the RPC pipeline itself).
pub fn build_host_launch(
    config: &LlamaCppConfig,
    model_ref: &str,
    worker_addrs: &[String],
    params: &EngineParams,
) -> Result<HostLaunch, EngineError> {
    if model_ref.is_empty() {
        return Err(EngineError::InvalidArgument("empty model_ref".to_string()));
    }

    let extra = &params.extra;

    // Resolve overridable values (extra map wins over config defaults).
    let host_bind = extra
        .get("host")
        .filter(|s| !s.is_empty())
        .cloned()
        .unwrap_or_else(|| config.host_bind.clone());

    let port = extra
        .get("port")
        .and_then(|s| s.parse::<u16>().ok())
        .unwrap_or(config.host_port);

    let ngl = extra
        .get("ngl")
        .or_else(|| extra.get("n_gpu_layers"))
        .and_then(|s| s.parse::<u32>().ok())
        .unwrap_or(config.n_gpu_layers);

    let mut args: Vec<String> = vec![
        "-m".to_string(),
        model_ref.to_string(),
        "--host".to_string(),
        host_bind.clone(),
        "--port".to_string(),
        port.to_string(),
        "-ngl".to_string(),
        ngl.to_string(),
    ];

    if params.context > 0 {
        args.push("-c".to_string());
        args.push(params.context.to_string());
    }

    if !worker_addrs.is_empty() {
        args.push("--rpc".to_string());
        args.push(worker_addrs.join(","));
    }

    if let Some(draft) = params.draft_block_len {
        args.push("--draft-max".to_string());
        args.push(draft.to_string());
    }

    // Explicit flash-attention field takes precedence; the extra["flash_attn"]
    // key is a backward-compat fallback for callers that have not yet migrated
    // to this explicit field.  Pass the flag through so apply_extra avoids a
    // double emit.
    if params.flash_attn {
        args.push("-fa".to_string());
    }

    apply_extra(extra, &mut args, params.flash_attn);

    let endpoint = format!("http://{}:{}", advertise_host(config, &host_bind), port);

    Ok(HostLaunch { args, endpoint })
}

/// Interpret recognised keys of the `extra` map and append the corresponding
/// flags. Iteration is over a sorted view so the output is deterministic.
///
/// Recognised keys (besides `host`/`port`/`ngl` handled by the caller):
/// - `flash_attn` = truthy → `-fa` (skipped when `flash_attn_emitted` is true
///   to avoid a duplicate from the explicit [`EngineParams::flash_attn`] field)
/// - `threads`    = N       → `-t N`
/// - `batch`      = N       → `-b N`
/// - `parallel`   = N       → `--parallel N`
/// - `raw.<flag>` = V       → `--<flag> [V]` (raw passthrough; value omitted when
///   empty, giving a boolean flag)
fn apply_extra(
    extra: &std::collections::HashMap<String, String>,
    args: &mut Vec<String>,
    flash_attn_emitted: bool,
) {
    // Sort for deterministic argument ordering.
    let sorted: BTreeMap<&String, &String> = extra.iter().collect();

    if !flash_attn_emitted {
        if let Some(v) = sorted.get(&"flash_attn".to_string()) {
            if is_truthy(v) {
                args.push("-fa".to_string());
            }
        }
    }
    if let Some(v) = sorted.get(&"threads".to_string()) {
        if !v.is_empty() {
            args.push("-t".to_string());
            args.push((*v).clone());
        }
    }
    if let Some(v) = sorted.get(&"batch".to_string()) {
        if !v.is_empty() {
            args.push("-b".to_string());
            args.push((*v).clone());
        }
    }
    if let Some(v) = sorted.get(&"parallel".to_string()) {
        if !v.is_empty() {
            args.push("--parallel".to_string());
            args.push((*v).clone());
        }
    }

    for (k, v) in &sorted {
        if let Some(flag) = k.strip_prefix("raw.") {
            if flag.is_empty() {
                continue;
            }
            args.push(format!("--{flag}"));
            if !v.is_empty() {
                args.push((*v).clone());
            }
        }
    }
}

fn is_truthy(v: &str) -> bool {
    matches!(
        v.trim().to_ascii_lowercase().as_str(),
        "1" | "true" | "yes" | "on"
    )
}

/// Client-facing host to advertise in the endpoint URL: the explicit
/// `advertise_host` if set, else the bind host when it is concrete, else
/// loopback (never advertise a wildcard).
fn advertise_host(config: &LlamaCppConfig, host_bind: &str) -> String {
    if !config.advertise_host.is_empty() {
        return config.advertise_host.clone();
    }
    match host_bind {
        "0.0.0.0" | "::" | "" => "127.0.0.1".to_string(),
        other => other.to_string(),
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::collections::HashMap;

    fn cfg() -> LlamaCppConfig {
        LlamaCppConfig::default()
    }

    #[test]
    fn worker_args_are_host_port() {
        let bind: SocketAddr = "192.168.1.10:50052".parse().unwrap();
        assert_eq!(
            build_worker_args(&bind),
            vec!["-H", "192.168.1.10", "-p", "50052"]
        );
    }

    #[test]
    fn host_args_minimal_single_node() {
        let params = EngineParams {
            context: 4096,
            ..EngineParams::default()
        };
        let launch = build_host_launch(&cfg(), "/models/m.gguf", &[], &params).unwrap();
        assert_eq!(
            launch.args,
            vec![
                "-m",
                "/models/m.gguf",
                "--host",
                "0.0.0.0",
                "--port",
                "8080",
                "-ngl",
                "99",
                "-c",
                "4096",
            ]
        );
        // Wildcard bind must not leak into the advertised endpoint.
        assert_eq!(launch.endpoint, "http://127.0.0.1:8080");
    }

    #[test]
    fn host_args_with_workers_and_draft() {
        let params = EngineParams {
            context: 8192,
            draft_block_len: Some(16),
            ..EngineParams::default()
        };
        let workers = vec!["10.0.0.2:50052".to_string(), "10.0.0.3:50052".to_string()];
        let launch = build_host_launch(&cfg(), "/m.gguf", &workers, &params).unwrap();
        let joined = launch.args.join(" ");
        assert!(
            joined.contains("--rpc 10.0.0.2:50052,10.0.0.3:50052"),
            "{joined}"
        );
        assert!(joined.contains("--draft-max 16"), "{joined}");
        assert!(joined.contains("-c 8192"), "{joined}");
    }

    #[test]
    fn context_zero_is_omitted() {
        let params = EngineParams::default(); // context == 0
        let launch = build_host_launch(&cfg(), "/m.gguf", &[], &params).unwrap();
        assert!(!launch.args.iter().any(|a| a == "-c"));
    }

    #[test]
    fn extra_overrides_and_passthrough_are_deterministic() {
        let mut extra = HashMap::new();
        extra.insert("port".to_string(), "9090".to_string());
        extra.insert("ngl".to_string(), "40".to_string());
        extra.insert("flash_attn".to_string(), "true".to_string());
        extra.insert("threads".to_string(), "8".to_string());
        extra.insert("raw.mlock".to_string(), "".to_string()); // boolean flag
        extra.insert("raw.rope-freq-base".to_string(), "1000000".to_string());
        let params = EngineParams {
            context: 2048,
            extra,
            ..EngineParams::default()
        };
        let launch = build_host_launch(&cfg(), "/m.gguf", &[], &params).unwrap();
        let joined = launch.args.join(" ");
        assert!(joined.contains("--port 9090"), "{joined}");
        assert!(joined.contains("-ngl 40"), "{joined}");
        assert!(joined.contains("-fa"), "{joined}");
        assert!(joined.contains("-t 8"), "{joined}");
        assert!(joined.contains("--mlock"), "{joined}");
        assert!(joined.contains("--rope-freq-base 1000000"), "{joined}");
        assert_eq!(launch.endpoint, "http://127.0.0.1:9090");

        // Determinism: identical inputs produce identical args.
        let again = build_host_launch(&cfg(), "/m.gguf", &[], &params).unwrap();
        assert_eq!(launch, again);
    }

    #[test]
    fn empty_model_ref_rejected() {
        let err = build_host_launch(&cfg(), "", &[], &EngineParams::default()).unwrap_err();
        assert!(matches!(err, EngineError::InvalidArgument(_)));
    }

    // ── I5: flash_attn explicit field ────────────────────────────────────────

    /// `EngineParams { flash_attn: true }` must produce `-fa` in the arg list.
    #[test]
    fn explicit_flash_attn_true_produces_fa_flag() {
        let params = EngineParams {
            flash_attn: true,
            ..EngineParams::default()
        };
        let launch = build_host_launch(&cfg(), "/m.gguf", &[], &params).unwrap();
        assert!(
            launch.args.iter().any(|a| a == "-fa"),
            "expected -fa in args: {:?}",
            launch.args
        );
    }

    /// `EngineParams { flash_attn: false }` must NOT produce `-fa`.
    #[test]
    fn explicit_flash_attn_false_omits_fa_flag() {
        let params = EngineParams {
            flash_attn: false,
            ..EngineParams::default()
        };
        let launch = build_host_launch(&cfg(), "/m.gguf", &[], &params).unwrap();
        assert!(
            !launch.args.iter().any(|a| a == "-fa"),
            "unexpected -fa in args: {:?}",
            launch.args
        );
    }

    /// When both `params.flash_attn` and `extra["flash_attn"]` are set, `-fa`
    /// must appear exactly once (no double-emit).
    #[test]
    fn flash_attn_explicit_and_extra_no_double_emit() {
        let mut extra = HashMap::new();
        extra.insert("flash_attn".to_string(), "true".to_string());
        let params = EngineParams {
            flash_attn: true,
            extra,
            ..EngineParams::default()
        };
        let launch = build_host_launch(&cfg(), "/m.gguf", &[], &params).unwrap();
        let fa_count = launch.args.iter().filter(|a| *a == "-fa").count();
        assert_eq!(
            fa_count, 1,
            "-fa must appear exactly once; args: {:?}",
            launch.args
        );
    }

    /// Backward compat: `extra["flash_attn"]` alone (without the explicit field)
    /// still produces `-fa`.
    #[test]
    fn flash_attn_via_extra_still_works() {
        let mut extra = HashMap::new();
        extra.insert("flash_attn".to_string(), "true".to_string());
        let params = EngineParams {
            flash_attn: false,
            extra,
            ..EngineParams::default()
        };
        let launch = build_host_launch(&cfg(), "/m.gguf", &[], &params).unwrap();
        assert!(
            launch.args.iter().any(|a| a == "-fa"),
            "expected -fa from extra map; args: {:?}",
            launch.args
        );
    }

    #[test]
    fn advertise_host_used_when_set() {
        let mut c = cfg();
        c.advertise_host = "10.0.0.1".to_string();
        let launch = build_host_launch(&c, "/m.gguf", &[], &EngineParams::default()).unwrap();
        assert_eq!(launch.endpoint, "http://10.0.0.1:8080");
    }
}
