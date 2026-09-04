//! Normalisation of llama.cpp's heterogeneous metric output into the
//! backend-agnostic [`EngineMetrics`] (`purser.v1`).
//!
//! llama.cpp exposes numbers in several shapes depending on version and flags:
//!
//! 1. **Prometheus** (`llama-server --metrics`, `GET /metrics`): lines like
//!    `llamacpp:predicted_tokens_seconds 22.54`.
//! 2. **Human server logs**: `prompt eval time = ... ( ..., 414.85 tokens per
//!    second)` and `eval time = ... ( ..., 22.54 tokens per second)`.
//! 3. **Model-load logs**: `... CUDA0 buffer size = 21500.00 MiB` (VRAM),
//!    `... CPU buffer size = 1024.00 MiB` (RAM).
//!
//! The parser is deliberately structured as a set of independent, additive
//! matchers so it *degrades gracefully*: unknown or reordered lines are ignored,
//! and if nothing matches it returns a valid all-zero [`EngineMetrics`] rather
//! than failing. Bump [`PARSER_VERSION`] and add a matcher when llama.cpp changes
//! its output; existing matchers keep working.

use purser_engine_adapter::EngineMetrics;

/// Version of the parsing contract. Increment when adding/altering matchers so
/// downstream logs can attribute a normalisation to a known parser revision.
pub const PARSER_VERSION: u32 = 1;

/// Which family of output a value was recovered from (for diagnostics).
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub enum MetricsFormat {
    /// Nothing recognised; caller got the zero fallback.
    #[default]
    Unknown,
    /// Prometheus `llamacpp:*` gauges.
    Prometheus,
    /// Human-readable server / load logs.
    HumanLog,
    /// Recognised in both families.
    Mixed,
}

/// Intermediate, presence-tracked view of what was recovered. Every field is
/// optional so callers can tell "absent" from "zero".
#[derive(Clone, Debug, Default, PartialEq)]
pub struct ParsedMetrics {
    pub prefill_tok_s: Option<f64>,
    pub decode_tok_s: Option<f64>,
    pub ram_used_gb: Option<f64>,
    pub vram_used_gb: Option<f64>,
    pub queue_depth: Option<u32>,
    pub accepted_tokens_ratio: Option<f64>,
    pub format: MetricsFormat,
}

impl ParsedMetrics {
    /// Collapse into the wire type, filling absent fields with safe defaults and
    /// clamping into the ranges the conformance suite requires
    /// (non-negative rates, ratio within `[0, 1]`).
    pub fn into_engine_metrics(self) -> EngineMetrics {
        EngineMetrics {
            prefill_tok_s: self.prefill_tok_s.unwrap_or(0.0).max(0.0),
            decode_tok_s: self.decode_tok_s.unwrap_or(0.0).max(0.0),
            ram_used_gb: self.ram_used_gb.unwrap_or(0.0).max(0.0),
            vram_used_gb: self.vram_used_gb.unwrap_or(0.0).max(0.0),
            queue_depth: self.queue_depth.unwrap_or(0),
            accepted_tokens_ratio: self.accepted_tokens_ratio.unwrap_or(0.0).clamp(0.0, 1.0),
        }
    }
}

/// Parse whatever metrics can be found in `text` (any mix of the supported
/// formats) into [`EngineMetrics`]. Never fails.
pub fn parse_metrics(text: &str) -> EngineMetrics {
    parse_all(text).into_engine_metrics()
}

/// Lower-level entry point returning the presence-tracked view.
pub fn parse_all(text: &str) -> ParsedMetrics {
    let mut m = ParsedMetrics::default();
    let mut saw_prom = false;
    let mut saw_log = false;

    // MiB accumulators for buffer-size lines (summed across devices).
    let mut vram_mib = 0.0f64;
    let mut ram_mib = 0.0f64;
    let mut saw_vram = false;
    let mut saw_ram = false;

    let mut req_processing: Option<f64> = None;
    let mut req_deferred: Option<f64> = None;

    for raw in text.lines() {
        let line = raw.trim();
        if line.is_empty() || line.starts_with('#') {
            continue;
        }

        // --- Prometheus gauges -------------------------------------------
        if let Some((key, val)) = prometheus_kv(line) {
            saw_prom = true;
            match key {
                "llamacpp:prompt_tokens_seconds" => set_if_none(&mut m.prefill_tok_s, val),
                "llamacpp:predicted_tokens_seconds" => set_if_none(&mut m.decode_tok_s, val),
                "llamacpp:requests_processing" => req_processing = Some(val),
                "llamacpp:requests_deferred" => req_deferred = Some(val),
                _ => {}
            }
            continue;
        }

        // --- Human server logs: tokens/second ----------------------------
        if line.contains("tokens per second") {
            if let Some(v) = last_float_before(line, "tokens per second") {
                saw_log = true;
                // "prompt eval time" is prefill; a bare "eval time" is decode.
                if line.contains("prompt eval") {
                    set_if_none(&mut m.prefill_tok_s, v);
                } else if line.contains("eval time") {
                    set_if_none(&mut m.decode_tok_s, v);
                }
            }
        }

        // --- Model-load logs: buffer sizes -> VRAM / RAM -----------------
        if line.contains("buffer size") {
            if let Some(mib) = mib_before(line, "buffer size") {
                saw_log = true;
                if is_gpu_line(line) {
                    vram_mib += mib;
                    saw_vram = true;
                } else if line.contains("CPU") {
                    ram_mib += mib;
                    saw_ram = true;
                }
            }
        }

        // --- Speculative acceptance (best effort) ------------------------
        if line.contains("accept") {
            if let Some(r) = acceptance_ratio(line) {
                saw_log = true;
                set_if_none(&mut m.accepted_tokens_ratio, r);
            }
        }
    }

    // Queue depth: prefer deferred (waiting) requests, else those in-flight.
    if let Some(d) = req_deferred.or(req_processing) {
        m.queue_depth = Some(d.max(0.0).round() as u32);
    }

    if saw_vram {
        m.vram_used_gb = Some(mib_to_gib(vram_mib));
    }
    if saw_ram {
        m.ram_used_gb = Some(mib_to_gib(ram_mib));
    }

    m.format = match (saw_prom, saw_log) {
        (true, true) => MetricsFormat::Mixed,
        (true, false) => MetricsFormat::Prometheus,
        (false, true) => MetricsFormat::HumanLog,
        (false, false) => MetricsFormat::Unknown,
    };
    m
}

fn set_if_none(slot: &mut Option<f64>, v: f64) {
    if slot.is_none() {
        *slot = Some(v);
    }
}

fn mib_to_gib(mib: f64) -> f64 {
    mib / 1024.0
}

/// A GPU/accelerator buffer line (as opposed to a CPU one).
fn is_gpu_line(line: &str) -> bool {
    const MARKERS: &[&str] = &["CUDA", "ROCm", "HIP", "Metal", "Vulkan", "SYCL", "GPU", "Kompute"];
    MARKERS.iter().any(|m| line.contains(m))
}

/// Parse a Prometheus-style `key value` line, returning the key and numeric
/// value. Accepts optional trailing tokens (e.g. timestamps) by taking the first
/// two whitespace-separated fields.
fn prometheus_kv(line: &str) -> Option<(&str, f64)> {
    let mut it = line.split_whitespace();
    let key = it.next()?;
    if !key.contains("llamacpp:") {
        return None;
    }
    let val = it.next()?.parse::<f64>().ok()?;
    Some((key, val))
}

/// Return the last float appearing before `needle` in `s` (used for the
/// `"<n> tokens per second"` pattern).
fn last_float_before(s: &str, needle: &str) -> Option<f64> {
    let idx = s.find(needle)?;
    let head = s[..idx].trim_end();
    let start = head
        .rfind(|c: char| !(c.is_ascii_digit() || c == '.' || c == '-' || c == '+' || c == 'e' || c == 'E'))
        .map(|i| i + 1)
        .unwrap_or(0);
    head[start..].parse::<f64>().ok()
}

/// Parse the MiB value from a `"... buffer size = 1234.00 MiB"` line, converting
/// GiB/KiB if that unit is used instead.
fn mib_before(line: &str, _anchor: &str) -> Option<f64> {
    for (unit, factor) in [("MiB", 1.0), ("GiB", 1024.0), ("KiB", 1.0 / 1024.0)] {
        if let Some(v) = last_float_before(line, unit) {
            return Some(v * factor);
        }
    }
    None
}

/// Best-effort speculative-decoding acceptance ratio from a log line. Accepts a
/// bare ratio in `[0, 1]` or a percentage; anything else is ignored.
fn acceptance_ratio(line: &str) -> Option<f64> {
    if let Some(pct) = last_float_before(line, "%") {
        return Some((pct / 100.0).clamp(0.0, 1.0));
    }
    // Look for "accept ... = <ratio>" style; take the last float on the line.
    let last = line
        .split(|c: char| c.is_whitespace() || c == '=' || c == ',')
        .filter_map(|tok| tok.parse::<f64>().ok())
        .last()?;
    if (0.0..=1.0).contains(&last) {
        Some(last)
    } else {
        None
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    const PROM_SAMPLE: &str = "\
# HELP llamacpp:prompt_tokens_seconds Average prompt throughput
# TYPE llamacpp:prompt_tokens_seconds gauge
llamacpp:prompt_tokens_seconds 414.85
llamacpp:predicted_tokens_seconds 22.54
llamacpp:kv_cache_usage_ratio 0.12
llamacpp:requests_processing 2
llamacpp:requests_deferred 3
";

    const LOG_SAMPLE: &str = "\
llama_print_timings: prompt eval time =    1234.56 ms /   512 tokens (    2.41 ms per token,   414.85 tokens per second)
llama_print_timings:        eval time =    5678.90 ms /   128 runs   (   44.37 ms per token,    22.54 tokens per second)
llm_load_tensors:      CUDA0 buffer size = 21504.00 MiB
llm_load_tensors:        CPU buffer size =  1024.00 MiB
";

    #[test]
    fn parses_prometheus() {
        let p = parse_all(PROM_SAMPLE);
        assert_eq!(p.format, MetricsFormat::Prometheus);
        assert_eq!(p.prefill_tok_s, Some(414.85));
        assert_eq!(p.decode_tok_s, Some(22.54));
        // queue depth prefers deferred (3).
        assert_eq!(p.queue_depth, Some(3));
    }

    #[test]
    fn parses_human_logs_and_buffers() {
        let p = parse_all(LOG_SAMPLE);
        assert_eq!(p.format, MetricsFormat::HumanLog);
        assert_eq!(p.prefill_tok_s, Some(414.85));
        assert_eq!(p.decode_tok_s, Some(22.54));
        // 21504 MiB / 1024 = 21 GiB VRAM; 1024 MiB / 1024 = 1 GiB RAM.
        assert_eq!(p.vram_used_gb, Some(21.0));
        assert_eq!(p.ram_used_gb, Some(1.0));
    }

    #[test]
    fn mixed_sources_merge() {
        let combined = format!("{PROM_SAMPLE}\n{LOG_SAMPLE}");
        let m = parse_metrics(&combined);
        assert!((m.prefill_tok_s - 414.85).abs() < 1e-9);
        assert!((m.decode_tok_s - 22.54).abs() < 1e-9);
        assert_eq!(m.vram_used_gb, 21.0);
        assert_eq!(m.queue_depth, 3);
    }

    #[test]
    fn garbage_falls_back_to_zeros_and_stays_valid() {
        let m = parse_metrics("total nonsense\nnothing to see here\n\n");
        assert_eq!(m.prefill_tok_s, 0.0);
        assert_eq!(m.decode_tok_s, 0.0);
        assert_eq!(m.vram_used_gb, 0.0);
        assert_eq!(m.queue_depth, 0);
        // Conformance invariants hold for the fallback.
        assert!(m.decode_tok_s >= 0.0);
        assert!((0.0..=1.0).contains(&m.accepted_tokens_ratio));
    }

    #[test]
    fn empty_input_is_unknown_format() {
        assert_eq!(parse_all("").format, MetricsFormat::Unknown);
    }

    #[test]
    fn acceptance_percentage_is_normalised_and_clamped() {
        let p = parse_all("speculative: draft acceptance rate = 73.5%");
        assert!((p.accepted_tokens_ratio.unwrap() - 0.735).abs() < 1e-9);
        let m = parse_metrics("draft acceptance rate = 150%");
        assert_eq!(m.accepted_tokens_ratio, 1.0); // clamped
    }

    #[test]
    fn negative_rates_are_clamped_non_negative() {
        let m = parse_metrics("llamacpp:predicted_tokens_seconds -5.0");
        assert_eq!(m.decode_tok_s, 0.0);
    }

    #[test]
    fn gib_buffer_units_are_converted() {
        let p = parse_all("llm_load_tensors: CUDA0 buffer size = 20.00 GiB");
        assert_eq!(p.vram_used_gb, Some(20.0));
    }
}
