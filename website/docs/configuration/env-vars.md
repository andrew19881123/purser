# Environment Variables

All Purser components are configured via environment variables with sane
defaults so a daemon can boot on a fresh node with zero configuration.

---

## Agent (`purser-agent`)

| Variable | Default | Description |
|---|---|---|
| `PURSER_AGENT_BIND` | `0.0.0.0:50151` | Socket address `AgentService` (gRPC) binds to. |
| `PURSER_CONTROL_PLANE_ADDR` | _(unset)_ | Address of the control plane's `RegistrationService`, e.g. `https://cp.internal:50150`. |
| `PURSER_CLUSTER_ID` | `default` | Logical cluster this node belongs to. |
| `PURSER_NODE_ID` | _(unset)_ | Pre-assigned stable node identity. When set the node boots in `ENROLLED` state. |
| `PURSER_JOIN_TOKEN` | _(unset)_ | One-time token used during `RegistrationService::Join`. |
| `PURSER_HEALTH_INTERVAL_SECS` | `5` | Cadence (seconds) of `Health` stream reports and heartbeat ticks. Minimum 1. |
| `PURSER_INFERENCE_PORT` | `8000` | Port the on-node OpenAI-compatible inference endpoint listens on. |
| `PURSER_AGENT_ADVERTISED_ADDR` | _(derived)_ | Explicit `host:port` advertised to the control plane for `AgentService`. |
| `PURSER_INFERENCE_ADVERTISED_ADDR` | _(derived)_ | Explicit `host:port` advertised to the control plane for the inference endpoint. |
| `PURSER_ENGINE_BACKEND` | `mock` | Engine backend name. `mock` runs a local in-process stub; real backends are registered at compile time. |
| `PURSER_SEEDS` | _(unset)_ | Comma-separated `host:port` seed list for initial peer discovery. |

### Model weight fetching

| Variable | Default | Description |
|---|---|---|
| `PURSER_MODEL_FETCH_MAX_RETRIES` | `3` | Maximum number of retry attempts for `HttpFetcher` on transient errors (5xx, timeout). `0` means one attempt with no retries. Only relevant when the agent is built with the `http-fetch` Cargo feature. |

See [Model weight fetching](../install/linux-agent.md#model-weight-fetching)
for a full description of `FileMirrorFetcher` vs `HttpFetcher`.
