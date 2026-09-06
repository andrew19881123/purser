# Environment Variables Reference

This page exhaustively documents every environment variable read by each Purser component at startup. All values are extracted directly from the source code.

---

## Control Plane

Source: `go/controlplane/main.go` (`loadConfig()`)

| Variable | Default | Description |
|---|---|---|
| `PURSER_DB` | `purser-registry.db` | Path to the SQLite registry file. Under Kubernetes, this lives on the PVC at `/data/purser-registry.db`. Under systemd, set to `/var/lib/purser/purser-registry.db`. |
| `PURSER_ADDR` | `:8080` | Management REST API listen address (`/api/v1` + Dashboard backend). |
| `PURSER_GRPC_ADDR` | `:9443` | RegistrationService gRPC listen address (Agent `Join` / `Heartbeat`, mTLS). |
| `PURSER_PKI_DIR` | `pki-state` | Directory for the internal CA key and certificate persistence. Under Kubernetes, set to `/data/pki-state`. |
| `PURSER_GATEWAY_ADDR` | (empty) | Gateway base URL the Orchestrator pushes route updates to (e.g. `http://gateway:8080`). When empty, route sync is a no-op. |
| `PURSER_GATEWAY_TOKEN` | (empty) | Shared secret sent by the Control Plane to the Gateway in the `X-Purser-Internal-Token` header for route sync. Must match `PURSER_GATEWAY_INTERNAL_TOKEN` on the Gateway. |
| `PURSER_CLUSTER_ID` | `default` | Cluster identifier echoed in join-token responses so an enrolling Agent knows which cluster it is joining. |
| `PURSER_AGENT_PORT` | `0` | Port the Orchestrator dials on each node to reach `AgentService`. `0` uses the default `50151`. |
| `PURSER_LICENSE_KEY` | (empty) | Enterprise license key. Verified **offline** against the embedded ed25519 public key — no phone-home. Absent = community edition (enterprise features disabled). A present-but-invalid key causes a fatal startup error. |
| `PURSER_HF_TOKEN` | (empty) | HuggingFace API token used by `POST /api/v1/models/import` when the caller does not supply an `X-HF-Token` header. Required for private and gated models; leave empty for public-model-only access. |
| `PURSER_OIDC_ISSUER` | (empty) | OIDC provider discovery URL. When set, the Control Plane enforces OIDC authentication on the admin UI and management REST API. Example: `https://login.microsoftonline.com/<tenant>/v2.0`. Must be paired with `PURSER_OIDC_CLIENT_ID`. |
| `PURSER_OIDC_CLIENT_ID` | (empty) | Expected audience (client ID) claim in tokens issued by the OIDC provider. Required when `PURSER_OIDC_ISSUER` is set; startup fails if the issuer is set but the client ID is empty. |
| `PURSER_PLANNER_ORDERING_THRESHOLD` | `10` | Fleet size at or below which the planner uses the exact Held-Karp algorithm to find the minimum-cost pipeline ordering. Above this threshold the planner switches to the nearest-neighbour + 2-opt heuristic. Held-Karp has O(2^N·N²) complexity and is feasible up to ~12 nodes; raise this value only on planners with abundant memory and CPU. Read once at startup; restart the process to apply a new value. |

### Reconciler tuning

These variables tune the self-healing reconciler control loop. All values are
parsed as Go `time.Duration` strings (e.g. `30s`, `2m`, `1h`). Unset or
unparseable values fall back to the compiled defaults shown below.

| Variable | Default | Description |
|---|---|---|
| `PURSER_RECONCILER_INTERVAL` | `10s` | Interval between reconcile passes. |
| `PURSER_RECONCILER_NODE_OFFLINE_AFTER` | `45s` | How long since the last heartbeat before a node is considered offline (NodeTimeout). |
| `PURSER_RECONCILER_HYSTERESIS` | `30s` | Minimum dwell time a discrepancy must persist before the loop acts (time-based anti-churn). |
| `PURSER_RECONCILER_ACTION_COOLDOWN` | `2m` | Minimum time between re-issuing the same corrective action (prevents hammering while a prior action takes effect). |
| `PURSER_RECONCILER_WEBHOOK_URL` | (empty) | HTTP(S) endpoint that receives a `POST` request whenever the reconciler raises an `approval_required` event (e.g. a node going down that requires operator sign-off before failover). See [Webhook Notifications](./webhook.md) for payload format and retry behaviour. When empty, no webhook is sent. |
| `PURSER_RECONCILER_WEBHOOK_RETRIES` | `3` | Maximum number of POST attempts before the webhook delivery is abandoned. Each retry uses exponential backoff (500 ms, 1 s, 2 s, …). Must be a positive integer; values ≤ 0 fall back to 3. |

### Control Plane: Helm wiring

The Helm chart wires these via `controlPlane.extraEnv` and from `values.yaml` fields:

| Helm value | Maps to env var |
|---|---|
| `controlPlane.clusterId` | `PURSER_CLUSTER_ID` |
| `controlPlane.agentPort` | `PURSER_AGENT_PORT` |
| `license.key` | `PURSER_LICENSE_KEY` (via a Kubernetes Secret) |
| `gateway.internalToken` | `PURSER_GATEWAY_TOKEN` (via a Kubernetes Secret, shared with the Gateway) |

---

## Agent

Source: `rust/crates/agent/src/config.rs` (`AgentConfig::from_env()`) and `rust/crates/agent/src/main.rs`

| Variable | Default | Description |
|---|---|---|
| `PURSER_AGENT_BIND` | `0.0.0.0:50151` | Socket address `AgentService` (gRPC) binds to. **Security:** bind on a trusted-subnet interface only — the inference engine worker is not sandboxed. |
| `PURSER_CONTROL_PLANE_ADDR` | (none) | Control Plane `RegistrationService` gRPC address for enrollment and heartbeat (e.g. `http://cp.internal:9443`). When absent, the agent serves `AgentService` but does not enroll. |
| `PURSER_CLUSTER_ID` | `default` | Logical cluster this node belongs to. Must match the Control Plane's `PURSER_CLUSTER_ID`. |
| `PURSER_NODE_ID` | (none) | Stable node identity. Normally assigned by the Control Plane at `Join`. Setting this pre-assigns an identity; the node boots in `ENROLLED` state. |
| `PURSER_JOIN_TOKEN` | (none) | One-time join token issued by the Control Plane (`POST /api/v1/join-token`). Required for enrollment. |
| `PURSER_HEALTH_INTERVAL_SECS` | `5` | Heartbeat cadence in seconds. Minimum effective value is 1. |
| `PURSER_INFERENCE_PORT` | `8000` | Port the local inference engine serves the OpenAI-compatible API on. Must match the Control Plane's `DefaultInferencePort` (the advertised host endpoint). |
| `PURSER_AGENT_ADVERTISED_ADDR` | (derived) | Explicit `host:port` this node's `AgentService` is reachable at as seen by the Control Plane. When absent, derived from `PURSER_AGENT_BIND` (primary local non-loopback IPv4 when bound to `0.0.0.0`). |
| `PURSER_INFERENCE_ADVERTISED_ADDR` | (derived) | Explicit `host:port` where this node serves inference, as seen by the Gateway. When absent, derived from the advertised host plus `PURSER_INFERENCE_PORT`. |
| `PURSER_ENGINE_BACKEND` | `mock` | Engine backend name. Only `mock` is registered today; real GPU adapters register here without changing the service. Set `llamacpp` for the llama.cpp adapter. |
| `PURSER_LLAMACPP_BIN` | (unset) | Absolute path to the `llama-cli` (or `llama.cpp` server) binary. When set, the agent runs `$PURSER_LLAMACPP_BIN --version` at startup to populate the `llamacpp` entry in `engine_versions` on the node's `HardwareProfile`. If unset or the binary is not executable the version is reported as `"unknown"`. |
| `PURSER_SEEDS` | (none) | Comma-separated extra discovery seed peers (`host:port`) in addition to mDNS LAN discovery. |
| `RUST_LOG` | `info` | Log level for the agent (uses `tracing_subscriber`). Examples: `debug`, `purser_agent=debug,info`. |
| `PURSER_SWIM_ENABLED` | `false` | Set `true`, `1`, or `yes` to enable the SWIM gossip membership layer (wraps `foca`). When enabled, each node's SWIM identity carries **both** the UDP gossip address and the gRPC `AgentService` address (derived from `PURSER_AGENT_ADVERTISED_ADDR` or `PURSER_AGENT_BIND`). On `MemberUp` the gRPC address is added to the local membership view and logged at `INFO` (`swim_addr`, `grpc_addr`), so operators and observability pipelines see the correct dial target. When disabled (the default) the existing mDNS + seed path runs unchanged. If the UDP bind fails while enabled, the agent logs a warning and falls back to mDNS + seeds. |
| `PURSER_SWIM_BIND_ADDR` | `0.0.0.0:7946` | UDP bind address for the SWIM gossip layer. Only used when `PURSER_SWIM_ENABLED=true`. |
| `PURSER_SWIM_SEED_ADDRS` | (empty) | Comma-separated `host:port` SWIM peers to announce to on startup (UDP addresses, matching `PURSER_SWIM_BIND_ADDR` on those nodes). Complements `PURSER_SEEDS` (the mDNS + gRPC seed path); the two discovery mechanisms run in parallel when SWIM is enabled. |
| `PURSER_SECRET_STORE_DIR` | `$HOME/.purser/secrets` (or `/var/lib/purser/secrets` when `$HOME` is unset) | Directory where encrypted secret files (`*.enc`) and the auto-generated key file (`.secret_key`) are stored. Created with mode 0700 on first use. |
| `PURSER_SECRET_KEY` | (unset — auto-generated) | 32-byte AES-256 encryption key, hex- or base64-encoded (64 hex chars or 44 base64 chars). When set it takes precedence over the key file. When unset, the key is loaded from `{PURSER_SECRET_STORE_DIR}/.secret_key` or freshly generated and saved there. Consumed directly by `EncryptedFileSecretStore`, not stored in `AgentConfig`. |
| `PURSER_AGENT_MEM_BW_OVERRIDE_GBS` | (none) | Synthetic memory-bandwidth value in GB/s (`f32`). When set, the agent skips the 100 ms DRAM microbenchmark and reports this value in the `HardwareProfile` sent to the Control Plane. Useful in CI environments or for manual calibration. |
| `PURSER_MODEL_FETCH_MAX_RETRIES` | `3` | Number of additional HTTP fetch attempts after the first failure when downloading model weights (`HttpFetcher`). `0` means try once with no retries. Transient errors (5xx, network/timeout) are retried; 4xx errors fail immediately without retrying. |

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

---

## Gateway

Source: `rust/crates/gateway/src/config.rs`, `auth.rs`, `quota.rs`, `upstream.rs`

### Bind address (required)

Both variables are **required** — the gateway refuses to start with a clear error if either is missing.

| Variable | Default | Description |
|---|---|---|
| `PURSER_GATEWAY_HOST` | (required) | Bind IP address (e.g. `0.0.0.0`). |
| `PURSER_GATEWAY_PORT` | (required) | Bind TCP port (e.g. `8080`). |

### Control-plane reporting

| Variable | Default | Description |
|---|---|---|
| `PURSER_CONTROL_PLANE_URL` | (empty) | Control Plane base URL for usage reporting (e.g. `http://control-plane:8080`). When set, the gateway posts per-request token counts to `POST /api/v1/usage` after each inference call completes. When unset, usage recording is skipped (backward-compatible). |

### Authentication

| Variable | Default | Description |
|---|---|---|
| `PURSER_GATEWAY_INTERNAL_TOKEN` | (none) | Shared secret for the management plane (route sync). The Control Plane sends it in the `X-Purser-Internal-Token` header. When absent, route sync is disabled (fail-closed). Must match `PURSER_GATEWAY_TOKEN` on the Control Plane. |
| `PURSER_GATEWAY_API_KEYS` | (none) | Comma-separated client bearer tokens. Format: `secret[:tenant[:key_id]]`. Example: `sk-abc:team-a,sk-def:team-b:key2`. When absent or empty, the gateway runs in **OPEN DEV MODE** — any non-empty bearer token is accepted. **Always set this in production.** |

### Quota and rate limiting

A value of `0` disables the corresponding limit.

| Variable | Default | Description |
|---|---|---|
| `PURSER_GATEWAY_TOKENS_PER_MIN` | `60000` | Per-key token-bucket capacity and per-minute refill. Prompt tokens charged up front; completion tokens charged as they stream. Exhaustion returns `429`. |
| `PURSER_GATEWAY_MAX_CONCURRENT` | `32` | Maximum concurrent in-flight requests per API key. |
| `PURSER_GATEWAY_MAX_INFLIGHT` | `512` | Global in-flight ceiling across all keys (backpressure). Crossing it returns `429` with `Retry-After`. |
| `PURSER_GATEWAY_RETRY_AFTER_SECS` | `2` | Value of the `Retry-After` header on `429` responses. |

### Upstream timeouts

| Variable | Default | Description |
|---|---|---|
| `PURSER_GATEWAY_UPSTREAM_CONNECT_MS` | `2000` | Connect timeout to the deployment host (milliseconds). Connect failure returns `503`. |
| `PURSER_GATEWAY_UPSTREAM_TTFB_MS` | `30000` | Time-to-first-byte timeout (milliseconds). Exceeded before response head = `504`. |
| `PURSER_GATEWAY_UPSTREAM_IDLE_MS` | `30000` | Idle timeout between streamed chunks (milliseconds). The stream is terminated with a trailing error frame if no chunk arrives in time. |

### Logging

| Variable | Default | Description |
|---|---|---|
| `RUST_LOG` | `info` | Log level for the gateway. Structured JSON output. |

---

## OpenTelemetry

OpenTelemetry instrumentation is **fully implemented** in the Control Plane (Go) and Gateway (Rust). All three variables below are actively read at startup. When `OTEL_EXPORTER_OTLP_ENDPOINT` is unset, both components stay on their built-in no-op providers — zero overhead, nothing phoned home.

| Variable | Default | Description |
|---|---|---|
| `OTEL_EXPORTER_OTLP_ENDPOINT` | (empty) | OTLP exporter base URL. Setting this activates trace and metric export. Control Plane exports over OTLP/HTTP; Gateway exports over OTLP/gRPC. Examples: `http://otel-collector:4318` (self-hosted), `https://abc12345.live.dynatrace.com/api/v2/otlp` (Dynatrace). |
| `OTEL_SERVICE_NAME` | `purser-control-plane` / `purser-gateway` | Service name stamped on all spans and metrics. Defaults differ per component; set explicitly when running multiple instances. |
| `OTEL_EXPORTER_OTLP_HEADERS` | (empty) | Comma-separated `key=value` pairs added to OTLP requests for authentication. Example: `Authorization=Api-Token dt0c01.xxx` (Dynatrace), `Authorization=Basic <base64>` (Grafana Cloud). |

See [OpenTelemetry configuration](otel.md) for full details: emitted signals, metric names, the audit-log span-event bridge, and per-backend configuration examples.

---

## Complete list by component

### Control Plane env vars (19)

`PURSER_DB`, `PURSER_ADDR`, `PURSER_GRPC_ADDR`, `PURSER_PKI_DIR`, `PURSER_GATEWAY_ADDR`, `PURSER_GATEWAY_TOKEN`, `PURSER_CLUSTER_ID`, `PURSER_AGENT_PORT`, `PURSER_LICENSE_KEY`, `PURSER_HF_TOKEN`, `PURSER_OIDC_ISSUER`, `PURSER_OIDC_CLIENT_ID`, `PURSER_PLANNER_ORDERING_THRESHOLD`, `PURSER_RECONCILER_INTERVAL`, `PURSER_RECONCILER_NODE_OFFLINE_AFTER`, `PURSER_RECONCILER_HYSTERESIS`, `PURSER_RECONCILER_ACTION_COOLDOWN`, `PURSER_RECONCILER_WEBHOOK_URL`, `PURSER_RECONCILER_WEBHOOK_RETRIES`

### Agent env vars (20)

`PURSER_AGENT_BIND`, `PURSER_CONTROL_PLANE_ADDR`, `PURSER_CLUSTER_ID`, `PURSER_NODE_ID`, `PURSER_JOIN_TOKEN`, `PURSER_HEALTH_INTERVAL_SECS`, `PURSER_INFERENCE_PORT`, `PURSER_AGENT_ADVERTISED_ADDR`, `PURSER_INFERENCE_ADVERTISED_ADDR`, `PURSER_ENGINE_BACKEND`, `PURSER_LLAMACPP_BIN`, `PURSER_SEEDS`, `RUST_LOG`, `PURSER_SWIM_ENABLED`, `PURSER_SWIM_BIND_ADDR`, `PURSER_SWIM_SEED_ADDRS`, `PURSER_SECRET_STORE_DIR`, `PURSER_SECRET_KEY`, `PURSER_AGENT_MEM_BW_OVERRIDE_GBS`, `PURSER_MODEL_FETCH_MAX_RETRIES`

### Gateway env vars (13)

`PURSER_GATEWAY_HOST`, `PURSER_GATEWAY_PORT`, `PURSER_CONTROL_PLANE_URL`, `PURSER_GATEWAY_INTERNAL_TOKEN`, `PURSER_GATEWAY_API_KEYS`, `PURSER_GATEWAY_TOKENS_PER_MIN`, `PURSER_GATEWAY_MAX_CONCURRENT`, `PURSER_GATEWAY_MAX_INFLIGHT`, `PURSER_GATEWAY_RETRY_AFTER_SECS`, `PURSER_GATEWAY_UPSTREAM_CONNECT_MS`, `PURSER_GATEWAY_UPSTREAM_TTFB_MS`, `PURSER_GATEWAY_UPSTREAM_IDLE_MS`, `RUST_LOG`

---

!!! note "Helm chart mapping"
    All env vars above can also be set via the Helm chart — see `deploy/helm/purser/values.yaml` for the mapping. Component-specific vars are best passed via `controlPlane.extraEnv`, `gateway.extraEnv`, or `ui.extraEnv`.
