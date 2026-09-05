# Environment Variables

All Purser components are configured through environment variables. The table
below lists the variables recognised by the **fleet agent** (`purser-agent`).

## purser-agent

| Variable | Default | Description |
|---|---|---|
| `PURSER_AGENT_BIND` | `0.0.0.0:7100` | `host:port` the agent gRPC service listens on. Prefer a trusted-subnet address in production. |
| `PURSER_CLUSTER_ID` | _(required)_ | Cluster identifier; must match the control plane's cluster ID. |
| `PURSER_CONTROL_PLANE` | _(unset)_ | `host:port` of the control plane. When set, the agent enrolls and sends heartbeats. |
| `PURSER_JOIN_TOKEN` | _(unset)_ | Shared secret used during the `Join` enrollment handshake. |
| `PURSER_NODE_ID` | _(unset)_ | Pre-assign a node identity. When unset the control plane assigns one at enrollment. |
| `PURSER_ENGINE_BACKEND` | `mock` | Inference engine backend to activate. Known values: `mock`. Register additional adapters in `BackendRegistry`. |
| `PURSER_INFERENCE_PORT` | `8000` | Port the mock OpenAI-compatible inference server binds to when `PURSER_ENGINE_BACKEND=mock`. |
| `PURSER_LLAMACPP_BIN` | _(unset)_ | Absolute path to the `llama-cli` (or `llama.cpp` server) binary. When set, the agent runs `$PURSER_LLAMACPP_BIN --version` at startup to populate the `llamacpp` entry in `engine_versions` on the node's `HardwareProfile`. If unset or the binary is not executable the version is reported as `"unknown"`. |
| `PURSER_SEEDS` | _(unset)_ | Comma-separated list of additional `host:port` seed addresses for peer discovery. |
| `PURSER_HEALTH_INTERVAL` | `30s` | Interval between self-diagnosis cycles and control-plane reachability checks. |

## Notes

### Engine version detection

`HardwareProfile.engine_versions` is a map of _backend name → version string_
sent to the control plane on every heartbeat. The agent populates it as follows:

- **`mock`** — always `"built-in"` (the GPU-free in-process backend).
- **`llamacpp`** — the first non-blank line of `$PURSER_LLAMACPP_BIN --version`
  output (stdout + stderr combined). Falls back to `"unknown"` when
  `PURSER_LLAMACPP_BIN` is unset, the path does not exist, or the binary cannot
  be executed.
- Any other registered backend — `"unknown"` until a version probe is added.

### FP4 native detection

When the agent is built with the `nvml` Cargo feature and an NVIDIA GPU is
present, the probe reads the CUDA compute capability and sets
`GpuInfo.fp4_native`:

- **SM ≥ 10.0 (Blackwell and later):** `fp4_native = true` — hardware FP4
  tensor cores are available.
- **SM 8.9 (Ada Lovelace / RTX 4000 series):** `fp4_native = false` — hardware
  FP8 is supported but not FP4.
- **Older GPUs or no CUDA:** `fp4_native = false`.
