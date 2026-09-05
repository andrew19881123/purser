# Environment Variables

All Purser components are configured primarily through environment variables.
Command-line flags of the same name are also accepted; flags take precedence
over env vars.

---

## Control Plane (`purser-controlplane`)

| Variable | Default | Description |
|---|---|---|
| `PURSER_DB` | `purser-registry.db` | Path to the SQLite registry file. |
| `PURSER_ADDR` | `:8080` | Management API listen address. |
| `PURSER_GRPC_ADDR` | `:9443` | RegistrationService gRPC listen address. |
| `PURSER_PKI_DIR` | `pki-state` | Directory for CA key/cert persistence. |
| `PURSER_GATEWAY_ADDR` | _(empty)_ | Gateway base URL for route sync. |
| `PURSER_GATEWAY_TOKEN` | _(empty)_ | Shared secret for Gateway route sync. |
| `PURSER_CLUSTER_ID` | `default` | Cluster identifier echoed in join tokens. |
| `PURSER_AGENT_PORT` | `0` (→ 50151) | AgentService port the orchestrator dials on each node. |
| `PURSER_LICENSE_KEY` | _(empty)_ | Enterprise license key; absent = community edition. |

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

---

## Gateway (`purser-gateway`)

| Variable | Default | Description |
|---|---|---|
| `PURSER_GATEWAY_ADDR` | `:8443` | Gateway listen address. |
| `PURSER_GATEWAY_TLS_CERT` | _(empty)_ | Path to the TLS certificate file. |
| `PURSER_GATEWAY_TLS_KEY` | _(empty)_ | Path to the TLS private key file. |

---

## Agent (`purser-agent`)

| Variable | Default | Description |
|---|---|---|
| `PURSER_AGENT_ADDR` | `:50151` | AgentService gRPC listen address. |
| `PURSER_CONTROL_PLANE_ADDR` | _(required)_ | Control-plane RegistrationService address for join/heartbeat. |
| `PURSER_AGENT_JOIN_TOKEN` | _(required)_ | Join token issued by the control plane. |
