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
┌──────────────────────────────────────────────────────────┐
│                   Control Plane (CP)                     │
│  Registry · PKI · Auth · Policy · Planner · REST API     │
│                                                          │
│   POST /platform/dataplanes  ←  operator registers DP   │
│   GET  /platform/dataplanes/{id}/config  ←  DP pulls     │
│   POST /platform/dataplanes/{id}/heartbeat  ←  DP pushes │
└──────────────┬───────────────────────────────────────────┘
               │  join_token (dp_…) + HTTPS
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

Data Planes pull their routing table, auth bundle, and active policies from the CP
on startup and at configurable intervals. Heartbeats flow from DP to CP so the CP
can mark a DP `degraded` or `offline` when it goes silent.

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

The CP compiles a configuration bundle — routing table (model → node assignments),
auth bundle (API key hashes + quotas), and active Rego policy bundle — and makes it
available for the DP to pull.

### DP pulls config

```
GET /api/v1/platform/dataplanes/{id}/config
Authorization: Bearer <join_token>
```

**Response (200 OK):**

```json
{
  "routing_table":  { "llama3-8b": { "nodes": ["node-1", "node-2"] } },
  "auth_bundle":    { "sha256:abc…": { "quota": 10000, "role": "inference" } },
  "policy_bundle":  ["allow_inference"],
  "generated_at":   "2026-09-07T12:05:00Z"
}
```

### Operator pushes config

An operator can push a snapshot directly (for testing or emergency overrides):

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
| `PUT` | `/api/v1/platform/dataplanes/{id}/config` | admin | Push config snapshot |
| `POST` | `/api/v1/platform/dataplanes/{id}/nodes/{nodeId}` | admin | Assign node to DP |
| `DELETE` | `/api/v1/platform/dataplanes/{id}/nodes/{nodeId}` | admin | Unassign node |
| `GET` | `/api/v1/platform/dataplanes/{id}/nodes` | admin | List nodes in DP |

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
