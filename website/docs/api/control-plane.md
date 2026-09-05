# Control Plane API Reference (`/api/v1`)

The Control Plane exposes a management REST API under `/api/v1`. This is separate from the inference data path — it is for fleet operators, not for inference clients.

**Base URL:** `http://<control-plane-host>:8080`

All responses are `application/json`. All request bodies that carry a payload use `application/json`.

---
## Authentication

Every non-public endpoint requires an API key supplied as a Bearer token:

```http
Authorization: Bearer psk_<base64>
```

Keys are minted via `POST /api/v1/apikeys` (see below). The plaintext secret
is returned **once** at creation time; only its SHA-256 hash is persisted.

### RBAC

Each key carries a **role** that limits what it may do. See
[RBAC configuration](../configuration/rbac.md) for the full role reference.
In brief:

| Role | Behaviour |
|------|-----------|
| `admin` | Full access to all management endpoints |
| `viewer` | `GET` requests only; any mutating request returns `403` |
| `inference` | Rejected on all `/api/v1/*` endpoints with `403`; for use on the gateway `/v1/` surface only |

Requests without a token, or with an unrecognised token, are passed through
to the handler; handlers enforce authentication independently where required.

### Public endpoints (no token required)

The following endpoints are always accessible regardless of the key role:

- `GET /api/v1/cluster/health`

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

### `POST /api/v1/nodes/{id}/restart`

Tears down all active deployments on the node and lets the reconciler re-provision them on remaining available nodes. The node process itself is **not** rebooted — only the engine processes are stopped and re-scheduled.

This is an asynchronous operation: the response is `202 Accepted` and actual re-provisioning happens in the background as the reconciler notices the stopped deployments.

**Response `202`:**

```json
{
  "node_id": "node-abc123",
  "deployments": ["dep-abc123"],
  "message": "deployments torn down; re-provisioning proceeds in the background"
}
```

**Response `404`:** Node not found.

**Response `409`:** Node has no active deployments to restart.

**Response `501`:** Orchestrator not configured.

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

### `POST /api/v1/models/import`

Imports a model from an external source into the catalog. Dispatches by the `source` field.

**Request body:**

```json
{
  "source":   "huggingface",
  "repo":     "meta-llama/Llama-3.1-8B-Instruct",
  "revision": "main",
  "filename_pattern": "*.Q4_K_M.gguf"
}
```

Supported `source` values and their required fields:

| `source` | Required fields | Optional fields |
|---|---|---|
| `huggingface` | `repo` | `revision`, `filename_pattern` |
| `s3` / `gcs` / `azure` | `uri` | `name`, `family`, `size_gb` |
| `sagemaker` | — | `model_group`, `version` |
| `vertexai` | `model` | `vertex_version` |
| `azureml` | `name` | `workspace`, `azure_version` |

For HuggingFace imports, the `X-HF-Token` request header overrides the server's `PURSER_HF_TOKEN`.

**Response `201`:** Model registered. Body contains the created model object.

**Response `400`:** Missing required field or unknown source.

**Response `401`:** HuggingFace auth required (`hf_auth_required`).

**Response `404`:** Source repository or resource not found.

**Response `409`:** Model with this ID already exists.

See [HuggingFace Hub](../integrations/huggingface.md), [Model Sources](../configuration/model-sources.md), [SageMaker](../integrations/sagemaker.md), [Vertex AI](../integrations/vertexai.md), and [Azure ML](../integrations/azureml.md) for integration-specific details.

### `GET /api/v1/models/{id}/health`

Reports the operational health of a deployed model. Does **not** perform a live inference probe — it reads deployment state from the registry.

**Response `200`:**

```json
{
  "model_id":         "llama-8b",
  "status":           "healthy",
  "deployment_id":    "dep-abc123",
  "deployment_state": "ACTIVE",
  "node_count":       2
}
```

`status` values:

| `status` | Condition |
|---|---|
| `"healthy"` | Most recent deployment is `ACTIVE` |
| `"degraded"` | Deployment is `PROVISIONING` or `STOPPING` (transient) |
| `"unavailable"` | Deployment is `FAILED`, `STOPPED`, or no deployment exists |

**Response `404`:** Model not found in catalog.

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

### `GET /api/v1/enrollment-bundle`

Returns a pre-filled environment file (`purser-enrollment.env`) that contains the three variables a new node needs to enroll:

- `PURSER_CONTROL_PLANE_ADDR` — gRPC address of the RegistrationService
- `PURSER_JOIN_TOKEN` — a freshly minted one-time join token
- `PURSER_CLUSTER_ID` — the cluster identifier

The bundle format is a shell-sourceable env file (KEY=value per line). Save it to `/etc/purser/agent.env` on the new node and restart the agent service.

**Response `200`:** Plain-text env file (MIME: `text/plain`).

```
PURSER_CONTROL_PLANE_ADDR=http://10.0.0.1:9443
PURSER_JOIN_TOKEN=psk_...
PURSER_CLUSTER_ID=default
```

**Response `501`:** Fleet manager not configured.

See [Enrollment Bundle](../install/enrollment-bundle.md) for the full workflow.

---

## API Keys

### `POST /api/v1/apikeys`

Mints a new Gateway API key. The plaintext key is returned once; only its SHA-256 hash is persisted.

**Request body:**

```json
{
  "name": "my-key",
  "tenant": "team-a",
  "role": "viewer",
  "quota": 0
}
```

`role` is one of `"admin"` (default), `"viewer"`, or `"inference"`. `name` and `tenant` are optional labels. `quota` is reserved (0 = unlimited).

**Response `201`:**

```json
{
  "id": "key-a1b2c3d4",
  "name": "my-key",
  "tenant": "team-a",
  "role": "viewer",
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

## Live Metrics — `GET /api/v1/metrics`

Returns a **Server-Sent Events (SSE)** stream of real-time hardware metrics for
every node in the cluster. The control plane pushes one frame every **2 seconds**;
clients keep the connection open and process frames as they arrive.

### Request

```
GET /api/v1/metrics HTTP/1.1
Accept: text/event-stream
```

No query parameters. The connection is long-lived until the client closes it.

### SSE Frame Format

Each frame carries a single JSON object on the `data:` line:

```
data: {"at":"2026-09-05T12:00:00Z","aggregate_decode_tok_s":86.5,"nodes":[...]}

```

(Two newlines end each frame, per the SSE spec.)

### Top-Level Fields

| Field | Type | Description |
|---|---|---|
| `at` | string (RFC 3339) | Emission timestamp (UTC). |
| `aggregate_decode_tok_s` | number | Sum of `decode_tok_s` across all nodes currently producing tokens. |
| `nodes` | array | Per-node hardware metrics (see below). Always present; empty when no nodes are enrolled. |

### Per-Node Entry (`nodes[*]`)

| Field | Type | Description |
|---|---|---|
| `node_id` | string | Unique node identifier assigned at enrollment. |
| `state` | string | Node lifecycle state, e.g. `NODE_STATE_RUNNING`, `NODE_STATE_READY`. |
| `metrics` | object | Hardware metrics sub-object (see below). |

### Metrics Sub-Object (`nodes[*].metrics`)

All fields default to `0` when the node has not yet sent a heartbeat or when
the agent has not yet loaded a model. **Zero values are honest** — the control
plane never omits a known node, it emits zeros for nodes that have not yet
reported.

| Field | Type | Unit | Description |
|---|---|---|---|
| `prefill_tok_s` | number | tokens/s | Prefill (prompt-processing) throughput for the current request. |
| `decode_tok_s` | number | tokens/s | Decode (generation) throughput, the primary throughput signal. |
| `ram_used_gb` | number | GB | Host RAM consumed by the agent and loaded model weights. |
| `vram_used_gb` | number | GB | GPU VRAM consumed (0 for CPU-only nodes). |
| `queue_depth` | integer | requests | Number of inference requests currently queued on this node. |
| `accepted_tokens_ratio` | number | 0–1 | Speculative-decoding acceptance ratio; 0 when speculative decoding is not active. |

### Example Frame

```json
{
  "at": "2026-09-05T12:00:00Z",
  "aggregate_decode_tok_s": 90.5,
  "nodes": [
    {
      "node_id": "node-gpu-01",
      "state": "NODE_STATE_RUNNING",
      "metrics": {
        "prefill_tok_s": 680,
        "decode_tok_s": 46,
        "ram_used_gb": 22,
        "vram_used_gb": 74,
        "queue_depth": 1,
        "accepted_tokens_ratio": 0.71
      }
    },
    {
      "node_id": "node-gpu-02",
      "state": "NODE_STATE_READY",
      "metrics": {
        "prefill_tok_s": 0,
        "decode_tok_s": 0,
        "ram_used_gb": 0,
        "vram_used_gb": 0,
        "queue_depth": 0,
        "accepted_tokens_ratio": 0
      }
    }
  ]
}
```

`node-gpu-02` appears with zeros because it is enrolled and ready but has not
yet received a deployment.

### Update Cadence

Frames are emitted every **2 seconds** (configurable at server startup via
`Config.MetricsInterval`). Metrics values reflect the most recent heartbeat
received from each agent; agents heartbeat on a similar cadence so values are
typically no more than a few seconds stale.

### Data Source

Metrics come from the `EngineMetrics` field of agent heartbeats
(`purser.v1.RegistrationService/Heartbeat` gRPC stream). The control plane
caches the latest sample per node in an in-memory store (`fleet.LiveMetrics`)
and merges it with the registry node list on each SSE tick. Nodes that have
enrolled but not yet started heartbeating are included with zero metrics.

### Client Example (JavaScript)

```javascript
const source = new EventSource('/api/v1/metrics');
source.onmessage = (ev) => {
  const snap = JSON.parse(ev.data);
  console.log('aggregate tok/s:', snap.aggregate_decode_tok_s);
  for (const node of snap.nodes) {
    console.log(node.node_id, node.metrics.decode_tok_s);
  }
};
```

---

## Usage Accounting

These endpoints are used by the gateway to record token usage and by operators to query chargeback data. See [Usage Accounting](../enterprise/usage-accounting.md) for the full guide.

### `POST /api/v1/usage`

Internal endpoint called by the gateway after each inference request. Operators do not call this directly.

**Authentication:** `X-Purser-Internal-Token: <token>` (required when `PURSER_INTERNAL_TOKEN` is set).

**Request body:**

```json
{"api_key_id": "key-abc123", "model_id": "llama-3-8b", "input_tokens": 42, "output_tokens": 128}
```

**Response `200`:** `{"ok": true}`

### `GET /api/v1/apikeys/{id}/usage`

Returns aggregate token usage for a single API key.

**Response `200`:**

```json
{"api_key_id": "key-abc123", "total_requests": 17, "input_tokens": 8432, "output_tokens": 21050}
```

### `GET /api/v1/usage/summary`

Returns usage grouped by tenant. Accepts optional `?since=<RFC3339>` query parameter.

**Response `200`:**

```json
{"tenants": [{"tenant": "acme", "total_requests": 1042, "input_tokens": 412000, "output_tokens": 980000}]}
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

```json
{
  "error": {
    "message": "enterprise license required",
    "feature": "audit",
    "type": "license_required"
  }
}
```

---

## OpenAPI Specification

### `GET /api/v1/openapi.json`

Serves the embedded OpenAPI 3.0 specification as JSON. The spec is compiled from
`go/controlplane/server/openapi.json` (generated from `openapi.yaml`).

- **Postman**: File → Import → Link, paste `http://<host>/api/v1/openapi.json`
- **Swagger UI / Redoc**: Point the UI at `http://<host>/api/v1/openapi.json`

---

## Routes Summary

| Method | Path | Description |
|--------|------|-------------|
| GET | `/api/v1/nodes` | List all enrolled nodes |
| GET | `/api/v1/nodes/{id}` | Get a node by ID |
| POST | `/api/v1/nodes/{id}/drain` | Cordon a node (mark DRAINING) |
| POST | `/api/v1/nodes/{id}/restart` | Restart engines on a node (async re-provision) |
| DELETE | `/api/v1/nodes/{id}` | Decommission a node |
| GET | `/api/v1/models` | List the model catalog (with fit verdicts) |
| POST | `/api/v1/models` | Register a model (protojson ModelSpec) |
| POST | `/api/v1/models/import` | Import from HuggingFace, S3/GCS/Azure, SageMaker, Vertex AI, or Azure ML |
| DELETE | `/api/v1/models/{id}` | Remove a model (guarded — refuses if deployed) |
| GET | `/api/v1/models/{id}/health` | Operational health of a deployed model |
| POST | `/api/v1/models/{id}/plan` | Dry-run plan preview (no side effects) |
| POST | `/api/v1/models/{id}/deploy` | Deploy a model |
| GET | `/api/v1/plans/{id}` | Get a stored deployment plan |
| GET | `/api/v1/deployments` | List all deployments |
| DELETE | `/api/v1/deployments/{id}` | Tear down a deployment |
| GET | `/api/v1/cluster/health` | Cluster health summary (public, no auth required) |
| GET | `/api/v1/apikeys` | List gateway API keys (metadata only) |
| POST | `/api/v1/apikeys` | Mint a gateway API key |
| DELETE | `/api/v1/apikeys/{id}` | Revoke an API key |
| GET | `/api/v1/apikeys/{id}/usage` | Aggregate token usage for a key |
| POST | `/api/v1/join-token` | Mint a cluster join token |
| GET | `/api/v1/enrollment-bundle` | Download pre-filled enrollment env file |
| GET | `/api/v1/metrics` | Live hardware metrics (Server-Sent Events, 2 s cadence) |
| POST | `/api/v1/usage` | Record per-request usage (internal, called by gateway) |
| GET | `/api/v1/usage/summary` | Usage grouped by tenant (optional `?since=<RFC3339>`) |
| GET | `/api/v1/enterprise/status` | Active edition report (community or enterprise) |
| GET | `/api/v1/enterprise/audit-log` | Tamper-evident audit log (enterprise, `"audit"` feature) |
| GET | `/api/v1/openapi.json` | OpenAPI 3.0 specification (public, no auth required) |

For full request/response schemas, parameters, and error codes see the live
spec at `GET /api/v1/openapi.json`.

---

## Error Format

All errors are JSON with `"error"` (machine-readable code) and `"message"`:

```json
{
  "error": "forbidden",
  "message": "this API key has viewer role (read-only)"
}
```

Common error codes:

| HTTP | `error` | Meaning |
|------|---------|---------|
| 400 | `bad_request` / `invalid_role` | Malformed input |
| 403 | `forbidden` | RBAC denial |
| 404 | `not_found` | Resource does not exist |
| 409 | `model_in_use` / `node_in_use` | Guarded delete blocked |
| 402 | `license_required` | Enterprise feature, license absent |
| 500 | various | Internal error |

