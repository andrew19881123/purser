# Control Plane API Reference

# Control Plane API Reference (`/api/v1`)

The Control Plane exposes a management REST API under `/api/v1`. This is separate from the inference data path — it is for fleet operators, not for inference clients.

**Base URL:** `http://<control-plane-host>:8080`

All responses are `application/json`. All request bodies that carry a payload use `application/json`.

---

## Nodes

### `GET /api/v1/nodes`

Returns all nodes known to the registry.

**Response `200`:**

```json
{
  "nodes": [
    {
      "id": "node-abc123",
      "state": "NODE_STATE_READY",
      "hostname": "gpu-node-01",
      ...
    }
  ]
}
```

### `GET /api/v1/nodes/{id}`

Returns a single node by ID.

**Response `200`:** Node object.

**Response `404`:**

```json
{"error": "not_found", "message": "node not found"}
```

### `POST /api/v1/nodes/{id}/drain`

Cordons a node: marks it `DRAINING` so the Planner stops scheduling new deployments onto it.

!!! note "Drain is cordon-only"
    This cordons the node only. It does **not** live-migrate, rebalance, or fail over deployments already running on the node. That capability is not yet implemented.

**Response `200`:**

```json
{
  "node_id": "node-abc123",
  "state": "NODE_STATE_DRAINING",
  "message": "node cordoned (unschedulable); existing deployments are not migrated or rebalanced"
}
```

**Response `404`:** Node not found.

**Response `501`:** Fleet manager not configured.

### `DELETE /api/v1/nodes/{id}`

Soft-decommissions a node: transitions it to `DECOMMISSIONED` and revokes its mTLS certificate. The node row remains visible in `GET /api/v1/nodes` in the `DECOMMISSIONED` state.

**Guards:**
- `404` if the node is unknown
- `409` if any non-terminal deployment still occupies the node (lists the blocking deployment IDs) — tear those down first

**Response `204`:** No content. Node decommissioned.

**Response `409`:**

```json
{
  "error": "node_in_use",
  "message": "node still hosts one or more active deployments; tear them down or migrate them first",
  "deployments": ["dep-abc123"]
}
```

---

## Models

### `GET /api/v1/models`

Returns the model catalog. When a Planner is configured, each entry is annotated with a `fit` verdict (deployable vs. doesn't fit, with estimated tok/s range or deficit).

**Response `200`:**

```json
{
  "models": [
    {
      "id": "llama-8b",
      "family": "llama",
      "architecture": "transformer",
      "params_total_b": 8.0,
      "engine": "mock",
      "fit": {
        "deployable": true,
        "node_count": 1
      }
    }
  ]
}
```

### `POST /api/v1/models`

Registers a model in the catalog. Request body is a protojson-encoded `purser.v1.ModelSpec`.

**Request body:**

```json
{
  "model_id": "llama-8b",
  "family": "llama",
  "architecture": "transformer",
  "params_total_b": 8.0,
  "engine": "mock"
}
```

**Response `201`:**

```json
{"model_id": "llama-8b"}
```

**Response `400`:** Missing or invalid body / `model_id`.

**Response `409`:** Model already exists.

### `DELETE /api/v1/models/{id}`

Removes a model from the catalog.

**Guards:**
- `404` if the model is unknown
- `409` if any non-terminal deployment still references the model — tear those down first

**Response `204`:** No content. Model deleted.

**Response `409`:**

```json
{
  "error": "model_in_use",
  "message": "model is referenced by one or more active deployments; tear them down first",
  "deployments": ["dep-abc123"]
}
```

### `POST /api/v1/models/{id}/plan`

Read-only Planner dry run: computes the layer-split plan for the model against the current fleet without persisting anything or deploying.

**Response `200` (feasible):**

```json
{
  "feasible": true,
  "id": "plan-abc123",
  "model_id": "llama-8b",
  "quantization": "q4_k_m",
  "cost": 1.23,
  "plan": { ... }
}
```

**Response `200` (not feasible):**

```json
{
  "feasible": false,
  "reason": "insufficient memory: need 16GB, fleet has 8GB"
}
```

**Response `404`:** Model not found.

**Response `501`:** Planner not configured.

### `POST /api/v1/models/{id}/deploy`

Deploys a model. The plan is obtained in priority order:

1. Inline `plan` (protojson `DeploymentPlan`) in the request body
2. Stored plan referenced by `plan_id` in the request body
3. Planner produces a plan from the current fleet (the normal, plan-less path)

**Request body (optional):**

```json
{
  "plan_id": "plan-abc123"
}
```

or

```json
{
  "plan": { ... }
}
```

or empty body `{}` (uses the Planner).

**Response `202`:**

```json
{
  "deployment_id": "dep-abc123",
  "model_id": "llama-8b",
  "plan_id": "plan-abc123-1a2b"
}
```

**Response `404`:** Model not found.

**Response `422`:** Model does not fit the current fleet.

```json
{
  "error": "model_does_not_fit",
  "message": "insufficient memory...",
  "reason": "memory",
  "deficit_gb": 8.0,
  "suggestions": ["add a node with at least 16GB VRAM"]
}
```

**Response `501`:** Orchestrator not configured.

---

## Deployments

### `GET /api/v1/deployments`

Returns all deployments.

**Response `200`:**

```json
{
  "deployments": [
    {
      "id": "dep-abc123",
      "model_id": "llama-8b",
      "state": "DEPLOYMENT_STATE_ACTIVE",
      ...
    }
  ]
}
```

### `DELETE /api/v1/deployments/{id}`

Tears down a deployment.

**Response `200`:**

```json
{"deployment_id": "dep-abc123", "state": "stopping"}
```

**Response `404`:** Deployment not found.

**Response `501`:** Orchestrator not configured.

---

## Plans

### `GET /api/v1/plans/{id}`

Returns a stored deployment plan by ID.

**Response `200`:** Plan object.

**Response `404`:** Plan not found.

---

## Join Tokens

### `POST /api/v1/join-token`

Mints a single-use, expiring cluster join token. The operator hands the returned token to a machine via `PURSER_JOIN_TOKEN`; the Agent enrolls over the `RegistrationService` gRPC `Join` RPC.

**Request body (optional):**

```json
{
  "ttl_seconds": 3600
}
```

`ttl_seconds <= 0` uses the fleet default (1 hour).

**Response `201`:**

```json
{
  "token": "psk_...",
  "expires_at": "2026-09-05T01:00:00Z",
  "cluster_id": "default"
}
```

The plaintext token is returned **once** and never persisted.

**Response `501`:** Fleet manager not configured.

---

## API Keys

### `POST /api/v1/apikeys`

Mints a new Gateway API key. The plaintext key is returned once; only its SHA-256 hash is persisted.

**Request body:**

```json
{
  "name": "my-key",
  "tenant": "team-a",
  "quota": 0
}
```

`name` and `tenant` are optional labels. `quota` is reserved.

**Response `201`:**

```json
{
  "id": "key-a1b2c3d4",
  "name": "my-key",
  "tenant": "team-a",
  "key": "psk_..."
}
```

### `GET /api/v1/apikeys`

Returns all API keys. **The plaintext key and its hash are never returned** — only metadata (`id`, `name`, `tenant`, `quota`, `enabled`, `created_at`, `updated_at`).

**Response `200`:**

```json
{
  "apikeys": [
    {
      "id": "key-a1b2c3d4",
      "name": "my-key",
      "tenant": "team-a",
      "enabled": true
    }
  ]
}
```

### `DELETE /api/v1/apikeys/{id}`

Permanently revokes an API key.

**Response `204`:** No content. Key revoked.

**Response `404`:** Key not found.

---

## Cluster Health

### `GET /api/v1/cluster/health`

Reports a coarse cluster health summary (DB reachability + node counts).

**Response `200`:**

```json
{
  "status": "ok",
  "total_nodes": 3,
  "ready_nodes": 3,
  "checked_at": "2026-09-05T00:00:00Z"
}
```

`status` values:
- `"ok"` — at least one node is ready
- `"degraded"` — nodes exist but none is ready
- `"empty"` — no nodes enrolled
- `"unavailable"` — database is unreachable (response is `503`)

---

## Live Metrics (SSE)

### `GET /api/v1/metrics`

Streams live cluster metrics as Server-Sent Events. Emits an initial snapshot immediately, then one every 2 seconds (configurable) until the client disconnects.

Content type: `text/event-stream`

**SSE event format:**

```
data: {"total_nodes": 3, "by_state": {"NODE_STATE_READY": 2, "NODE_STATE_RUNNING": 1}, "at": "2026-09-05T00:00:00Z"}

data: {"total_nodes": 3, "by_state": {"NODE_STATE_READY": 3}, "at": "2026-09-05T00:00:02Z"}
```

---

## Enterprise

### `GET /api/v1/enterprise/status`

Reports the active edition. Never fails and never phones home — the verdict comes from the offline-verified key loaded at startup.

**Response `200` (community):**

```json
{
  "edition": "community",
  "licensee": "community",
  "features": []
}
```

**Response `200` (enterprise):**

```json
{
  "edition": "enterprise",
  "licensee": "Acme Corp",
  "features": ["audit", "ha"],
  "expires": "2027-09-04T00:00:00Z"
}
```

### `GET /api/v1/enterprise/audit-log`

Returns the tamper-evident audit log with chain verification. **Requires Enterprise license with the `"audit"` feature.**

Query parameters:
- `limit` — number of entries (default 100)

**Response `200`:** See [Enterprise: Audit Log](../enterprise/audit-log.md) for the full response schema.

**Response `402`:** Enterprise license required.
GET /api/v1/openapi.json
`go/controlplane/server/openapi.json` (generated from `openapi.yaml`).
- **Postman**: File → Import → Link, paste `http://<host>/api/v1/openapi.json`
- **Swagger UI / Redoc**: Point the UI at `http://<host>/api/v1/openapi.json`
```json
{
  "error": {
    "message": "enterprise license required",
    "feature": "audit",
    "type": "license_required"
  }
}
```

<<<<<<< HEAD
---

## Error format

All errors follow this format:

```json
{"error": "<error_code>", "message": "<human-readable description>"}
```

Common error codes: `not_found`, `model_in_use`, `node_in_use`, `model_exists`, `bad_request`, `bad_spec`, `bad_plan`, `no_deployer`, `no_fleet`, `no_planner`, `deploy_failed`, `teardown_failed`, `join_token_failed`, `create_apikey_failed`, `list_apikeys_failed`, `license_required`.
=======
## Routes summary

| Method | Path | Description |
|--------|------|-------------|
| GET | `/api/v1/nodes` | List all enrolled nodes |
| GET | `/api/v1/nodes/{id}` | Get a node by ID |
| POST | `/api/v1/nodes/{id}/drain` | Cordon a node (mark DRAINING) |
| DELETE | `/api/v1/nodes/{id}` | Decommission a node |
| GET | `/api/v1/models` | List the model catalog |
| POST | `/api/v1/models` | Register a model |
| DELETE | `/api/v1/models/{id}` | Remove a model |
| POST | `/api/v1/models/{id}/plan` | Dry-run plan preview (no side effects) |
| POST | `/api/v1/models/{id}/deploy` | Deploy a model |
| GET | `/api/v1/plans/{id}` | Get a stored deployment plan |
| GET | `/api/v1/deployments` | List all deployments |
| DELETE | `/api/v1/deployments/{id}` | Tear down a deployment |
| GET | `/api/v1/cluster/health` | Cluster health summary |
| GET | `/api/v1/apikeys` | List gateway API keys (metadata only) |
| POST | `/api/v1/apikeys` | Mint a gateway API key |
| DELETE | `/api/v1/apikeys/{id}` | Revoke an API key |
| POST | `/api/v1/join-token` | Mint a cluster join token |
| GET | `/api/v1/metrics` | Live metrics (Server-Sent Events) |
| GET | `/api/v1/enterprise/status` | Active edition report |
| GET | `/api/v1/enterprise/audit-log` | Tamper-evident audit log (enterprise) |
| GET | `/api/v1/openapi.json` | This OpenAPI 3.0 specification |

For full request/response schemas, parameters, and error codes see the live
spec at `GET /api/v1/openapi.json`.
>>>>>>> worktree-agent-a6e8c8db47223afb0
