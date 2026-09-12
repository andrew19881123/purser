# Data Planes

A **Data Plane** (DP) is a named inference cluster governed by the Purser Control
Plane (CP). Each Data Plane consists of one or more GPU Agent nodes and one Gateway.
The Control Plane never handles user inference traffic directly — it is a management
and orchestration layer only.

This separation mirrors the CP/DP architecture of API management platforms such as
IBM API Connect: the Control Plane holds configuration and policy; every Data Plane
independently enforces it.

---

## Architecture

```
┌──────────────────────────────────────────────────────────────┐
│                   Control Plane (CP)                         │
│  Registry · PKI · Auth · Policy · Planner · REST API         │
│                                                              │
│   POST /platform/dataplanes  ←  operator registers DP       │
│   PUT  /platform/dataplanes/{id}/config/refresh ← immediate │
│   ──── config snapshot push (every 30 s, background) ────►  │
│   POST /platform/dataplanes/{id}/heartbeat  ←  DP health    │
└──────────────┬───────────────────────────────────────────────┘
               │  join_token (dp_…) + HTTPS
               │  config snapshot pushed every 30 s
               │
      ┌────────▼─────────┐         ┌───────────────────────┐
      │  Data Plane A    │         │  Data Plane B          │
      │  (production)    │         │  (staging)             │
      │                  │         │                        │
      │  Gateway         │         │  Gateway               │
      │  Agent  Agent    │         │  Agent                 │
      │  GPU01  GPU02    │         │  GPU03                 │
      └──────────────────┘         └───────────────────────┘
         ↑ user inference traffic       ↑ user inference traffic
```

The CP pushes a config snapshot (routing table, auth bundle, active policies) to
every active Data Plane every 30 seconds. The DP can operate autonomously using the
stored snapshot if the CP is temporarily unreachable. Heartbeats flow DP → CP so
the CP can mark a DP `degraded` or `offline` when it goes silent.

---

## Concepts

| Term | Meaning |
|---|---|
| **Data Plane** | A named inference cluster (1+ agents + 1 gateway) |
| **Join Token** | A one-time `dp_…` secret returned on creation; presented by the DP to authenticate config-pull and heartbeat calls |
| **Config Snapshot** | The routing table, auth bundle, and policy bundle the CP pushes to a DP |
| **Heartbeat** | Periodic health report from a DP gateway to the CP |
| **Tier** | Free-form label (`production`, `staging`, `development`, custom) |
| **Status** | `registering` → `active` → `degraded` → `offline` |

### Node assignment

Every fleet node can be assigned to exactly one Data Plane. Nodes with no assignment
belong to the default (unpartitioned) pool and are available for deployments that
do not specify a DP. Assignment is changed at any time via the REST API.

---

## Registering a Data Plane

```bash
curl -s -X POST https://cp.example.com/api/v1/platform/dataplanes \
  -H "Authorization: Bearer <admin-key>" \
  -H "Content-Type: application/json" \
  -d '{
    "name":        "prod-us-east",
    "tier":        "production",
    "gateway_url": "https://ai-prod.acme.com",
    "description": "US-East production GPU cluster"
  }'
```

**Response (201 Created):**

```json
{
  "dataplane": {
    "id":          "dp-a1b2c3d4e5f6",
    "name":        "prod-us-east",
    "tier":        "production",
    "gateway_url": "https://ai-prod.acme.com",
    "status":      "registering",
    "created_at":  "2026-09-07T12:00:00Z",
    "updated_at":  "2026-09-07T12:00:00Z"
  },
  "join_token": "dp_4a7f…"
}
```

> **The `join_token` is returned exactly once.** Store it in a secret manager
> (e.g. HashiCorp Vault, Kubernetes Secret) immediately. It cannot be retrieved
> again — rotate it by deleting and re-creating the Data Plane.

---

## Heartbeat

The DP gateway calls this endpoint periodically (default: every 30 s) to report
its health. The CP updates `status` and `last_heartbeat` in the registry.

```
POST /api/v1/platform/dataplanes/{id}/heartbeat
Authorization: Bearer <join_token>

{
  "status":        "active",
  "node_count":    3,
  "active_models": ["llama3-8b", "mistral-7b"]
}
```

**Response: 204 No Content**

If no heartbeat is received within the configured TTL (default 5 min) the CP sets
the DP status to `degraded`. After a second missed TTL window it sets it to
`offline`.

---

## Config Snapshot

The CP compiles a **Config Snapshot** for each Data Plane — a self-contained bundle
the DP gateway uses to operate independently if the CP is temporarily unreachable.

### Contents

| Field | Description |
|---|---|
| `routing_table` | Active deployments whose host/engine nodes are assigned to this DP. Key: `model_id`. Value: `{deployment_id, endpoint, state, quantization}`. |
| `auth_bundle` | Enabled API key hashes with their `role`, `tenant`, and `quota`. The gateway authenticates inference requests locally without calling the CP. |
| `policy_bundle` | Names of enabled OPA/Rego policies the gateway should enforce. |
| `generated_at` | RFC3339 timestamp of when the snapshot was built. |

### Automatic push (background)

The CP runs a background loop that rebuilds and stores the config snapshot for every
non-offline Data Plane **every 30 seconds**.  This means the DP always has a
reasonably fresh snapshot available via the pull endpoint without any polling overhead
on the DP side.

```
CP background loop (every 30 s)
  for each active/degraded DP:
    1. buildDataPlaneSnapshot(dpID)
       a. nodes assigned to this DP → filter routing_table
       b. enabled API keys         → auth_bundle
       c. enabled OPA policies     → policy_bundle
    2. store snapshot in registry (UPDATE dataplanes SET config_snapshot=…)
```

### Flow diagram

```
  Control Plane                         Registry          Data Plane
  ─────────────                         ────────          ──────────
  startConfigSnapshotPusher (every 30s)
    │
    ├─ buildDataPlaneSnapshot(dpID)
    │   ├─ ListNodesByDataPlane  ──────►  SQLite
    │   ├─ ListDeployments       ──────►  SQLite
    │   ├─ ListAPIKeys           ──────►  SQLite
    │   └─ ListPolicies          ──────►  SQLite
    │
    └─ UpdateDataPlaneConfigSnapshot ──► SQLite (config_snapshot column)
                                                        │
                                             GET /config ◄── DP gateway
                                             (pull on startup or interval)
```

### Immediate refresh

After a model deployment or policy change you can force an immediate snapshot rebuild
without waiting for the next 30-second tick:

```bash
curl -X POST https://cp.example.com/api/v1/platform/dataplanes/dp-a1b2c3d4e5f6/config/refresh \
  -H "Authorization: Bearer <admin-key>"
```

**Response (200 OK):**

```json
{
  "message":      "config snapshot refreshed",
  "dataplane_id": "dp-a1b2c3d4e5f6",
  "refreshed_at": "2026-09-07T12:05:30Z"
}
```

**When to call `/config/refresh`:**

- After deploying a new model to a DP (`POST /models/{id}/deploy`)
- After enabling or disabling an OPA policy (`PUT /policies/{name}`)
- After creating or revoking an API key that this DP should immediately see
- For emergency snapshot resets (e.g. after a config rollback)

### DP pulls config

The DP gateway pulls the latest snapshot on startup and can refresh it on demand:

```
GET /api/v1/platform/dataplanes/{id}/config
Authorization: Bearer <join_token>
```

**Response (200 OK):**

```json
{
  "routing_table": {
    "llama3-8b": {
      "deployment_id": "dep-abc123",
      "state":         "DEPLOYMENT_STATE_ACTIVE",
      "endpoint":      "http://10.0.0.5:8080",
      "quantization":  "Q4_K_M"
    }
  },
  "auth_bundle": {
    "aabbccdd…": { "role": "inference", "tenant": "team-alpha", "quota": 10000 }
  },
  "policy_bundle":  ["allow_inference", "rate_limit_by_tenant"],
  "generated_at":   "2026-09-07T12:05:00Z"
}
```

### Operator pushes config (manual override)

An operator can push a snapshot directly for testing or emergency overrides.
The background pusher will overwrite it on the next 30-second tick unless stopped.

```
PUT /api/v1/platform/dataplanes/{id}/config
Authorization: Bearer <admin-key>

{
  "routing_table":  { … },
  "auth_bundle":    { … },
  "policy_bundle":  ["allow_inference"]
}
```

---

## Node Assignment

Assign a fleet node to a Data Plane so the CP planner targets it for deployments
on that DP:

```bash
# Assign node-1 to DP prod-us-east
curl -X POST https://cp.example.com/api/v1/platform/dataplanes/dp-a1b2c3d4e5f6/nodes/node-1 \
  -H "Authorization: Bearer <admin-key>"

# List nodes in a DP
curl https://cp.example.com/api/v1/platform/dataplanes/dp-a1b2c3d4e5f6/nodes \
  -H "Authorization: Bearer <admin-key>"

# Unassign node-1 (back to default pool)
curl -X DELETE https://cp.example.com/api/v1/platform/dataplanes/dp-a1b2c3d4e5f6/nodes/node-1 \
  -H "Authorization: Bearer <admin-key>"
```

---

## REST API Reference

| Method | Path | Auth | Description |
|---|---|---|---|
| `POST` | `/api/v1/platform/dataplanes` | admin | Register DP, returns one-time join token |
| `GET` | `/api/v1/platform/dataplanes` | admin | List all DPs |
| `GET` | `/api/v1/platform/dataplanes/{id}` | admin | Get DP by ID |
| `PUT` | `/api/v1/platform/dataplanes/{id}` | admin | Update name/tier/gateway_url/status |
| `DELETE` | `/api/v1/platform/dataplanes/{id}` | admin | Delete DP |
| `POST` | `/api/v1/platform/dataplanes/{id}/heartbeat` | join_token | DP health report |
| `GET` | `/api/v1/platform/dataplanes/{id}/config` | join_token or admin | Pull config snapshot |
| `PUT` | `/api/v1/platform/dataplanes/{id}/config` | admin | Push config snapshot (manual override) |
| `POST` | `/api/v1/platform/dataplanes/{id}/config/refresh` | admin | Trigger immediate snapshot rebuild |
| `POST` | `/api/v1/platform/dataplanes/{id}/nodes/{nodeId}` | admin | Assign node to DP |
| `DELETE` | `/api/v1/platform/dataplanes/{id}/nodes/{nodeId}` | admin | Unassign node |
| `GET` | `/api/v1/platform/dataplanes/{id}/nodes` | admin | List nodes in DP |

---

## Data Planes page (UI)

The **Data Planes** page in the operator dashboard lists every registered DP. Click
a row to expand its detail panel, which now exposes the full lifecycle:

- **Edit** opens a modal to change the name, tier, gateway URL, or description
  (`PUT /platform/dataplanes/{id}`).
- **Delete** uses an arm→confirm interaction — the first click arms the button (it
  turns red and reads *Delete {name}?*), the second confirms
  (`DELETE /platform/dataplanes/{id}`). A hint reminds the operator that assigned
  nodes are released.
- **Assigned nodes** lists the DP's nodes (`GET …/{id}/nodes`); an input assigns a
  node by id (`POST …/{id}/nodes/{nodeId}`) and each node chip has an arm→confirm
  unassign action (`DELETE …/{id}/nodes/{nodeId}`).
- **Refresh config** (existing) triggers an immediate snapshot rebuild.

---

## Status lifecycle

```
registering  →  active  →  degraded  →  offline
                  ↑____________|
                  (heartbeat received)
```

| Status | Meaning |
|---|---|
| `registering` | DP was created but has not yet sent a heartbeat |
| `active` | Heartbeating normally |
| `degraded` | Missed ≥ 1 heartbeat TTL window |
| `offline` | Missed ≥ 2 heartbeat TTL windows; treated as unavailable by the planner |

---

## Database schema (reference)

The `dataplanes` table is defined in `go/controlplane/registry/schema.sql`.
The `nodes.dataplane_id` column is added additively at startup by
`SQLiteRegistry.Migrate` so existing databases are upgraded non-destructively.

```sql
CREATE TABLE IF NOT EXISTS dataplanes (
    id              TEXT    PRIMARY KEY,
    name            TEXT    NOT NULL,
    tier            TEXT    NOT NULL DEFAULT 'production',
    gateway_url     TEXT    NOT NULL DEFAULT '',
    status          TEXT    NOT NULL DEFAULT 'active',
    join_token_hash TEXT    NOT NULL DEFAULT '',
    config_snapshot TEXT    NOT NULL DEFAULT '{}',
    last_heartbeat  TEXT,
    created_at      TEXT    NOT NULL,
    updated_at      TEXT    NOT NULL
);
-- nodes.dataplane_id added by ensureColumn at startup (NULL = default/unassigned)
```
