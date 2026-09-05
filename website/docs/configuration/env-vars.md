# Environment variables

All Purser components are configured through environment variables.  No config
file is required; every variable has a sane default so a node can boot with
zero configuration and join a cluster using only a control-plane address and a
join token.

## Agent (`purser-agent`)

| Variable | Default | Description |
|---|---|---|
| `PURSER_AGENT_BIND` | `0.0.0.0:50151` | `host:port` the gRPC `AgentService` listens on. |
| `PURSER_CONTROL_PLANE_ADDR` | _(unset)_ | Address of the control plane's `RegistrationService` (`https://host:port`). Required for enrollment and heartbeating. |
| `PURSER_CLUSTER_ID` | `default` | Logical cluster name this node belongs to. |
| `PURSER_NODE_ID` | _(unset)_ | Pre-assigned node identity. When set the agent boots directly into the `ENROLLED` state; otherwise an identity is assigned during `Join`. |
| `PURSER_JOIN_TOKEN` | _(unset)_ | One-time token used to enroll into the cluster. |
| `PURSER_HEALTH_INTERVAL_SECS` | `5` | Cadence (seconds, minimum 1) at which health reports are streamed to the control plane. |
| `PURSER_INFERENCE_PORT` | `8000` | Port the local inference engine serves the OpenAI-compatible API on. Must match the control plane's `DefaultInferencePort`. |
| `PURSER_AGENT_ADVERTISED_ADDR` | _(derived)_ | `host:port` to advertise to the control plane for agent-to-control-plane traffic. Derived from `PURSER_AGENT_BIND` when unset. |
| `PURSER_INFERENCE_ADVERTISED_ADDR` | _(derived)_ | `host:port` advertised for inference traffic. Derived from the agent host plus `PURSER_INFERENCE_PORT` when unset. |
| `PURSER_ENGINE_BACKEND` | `mock` | Engine backend to load (`mock`, or a real backend name such as `llamacpp`). |
| `PURSER_SEEDS` | _(unset)_ | Comma-separated additional `host:port` peers for initial discovery (supplements `PURSER_CONTROL_PLANE_ADDR`). |

### Secret store

The agent encrypts all sensitive material (join tokens, mTLS certificates) at
rest using AES-256-GCM.  See [Secret persistence](../install/linux-agent.md#secret-persistence)
for a full explanation of the file format and key lifecycle.

| Variable | Default | Description |
|---|---|---|
| `PURSER_SECRET_STORE_DIR` | `$HOME/.purser/secrets` (or `/var/lib/purser/secrets` when `$HOME` is unset) | Directory where encrypted secret files (`*.enc`) and the auto-generated key file (`.secret_key`) are stored. Created with mode 0700 on first use. |
| `PURSER_SECRET_KEY` | _(unset — auto-generated)_ | 32-byte AES-256 encryption key, hex- or base64-encoded (64 hex chars or 44 base64 chars). When set it takes precedence over the key file. When unset, the key is loaded from `{PURSER_SECRET_STORE_DIR}/.secret_key` or freshly generated and saved there. |

#### Generating a key manually

```bash
# Hex (64 chars)
openssl rand -hex 32

# Base64 (44 chars)
openssl rand -base64 32
```

Pass the output as `PURSER_SECRET_KEY`.  Keep it in a secrets manager (Vault,
AWS Secrets Manager, etc.) rather than a plain-text file.

## Tracing / logging

| Variable | Default | Description |
|---|---|---|
| `RUST_LOG` | `info` | Log level filter, e.g. `purser_agent=debug,info`. Uses the `tracing-subscriber` `EnvFilter` syntax. |
