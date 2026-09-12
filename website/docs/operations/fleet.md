# Fleet Management

The fleet API lets operators inspect, cordon, and decommission the GPU nodes enrolled in a Purser
cluster. All endpoints are authenticated and available under `/api/v1`.

---

## Node lifecycle

```
ENROLLED ──► READY ──► RUNNING
                │
                ▼
            DRAINING
                │
                ▼
         DECOMMISSIONED
```

| State | Description |
|---|---|
| `NODE_STATE_READY` | Node is enrolled and available for new deployments. |
| `NODE_STATE_RUNNING` | Node is actively serving at least one deployment. |
| `NODE_STATE_DRAINING` | Node has been cordoned; new deployments will not be scheduled here. Existing deployments are not migrated automatically. |
| `NODE_STATE_DECOMMISSIONED` | Node has been removed from the active fleet. Certificates are revoked. The record is retained in the registry. |

---

## Endpoints

### `GET /api/v1/nodes`

List all nodes registered in the cluster, in all lifecycle states.

**Auth:** Any authenticated request.

**Response 200**

```json
{
  "nodes": [
    {
      "id": "node-abc123",
      "hostname": "gpu-box-1.internal",
      "os": "linux",
      "arch": "amd64",
      "ram_gb": 64.0,
      "vram_gb": 24.0,
      "state": "NODE_STATE_READY",
      "advertised_agent_addr": "gpu-box-1.internal:9090",
      "last_seen": "2026-09-12T10:00:00Z",
      "created_at": "2026-08-01T09:00:00Z",
      "updated_at": "2026-09-12T10:00:00Z"
    }
  ]
}
```

---

### `GET /api/v1/nodes/{id}`

Return a single node by ID.

**Auth:** Any authenticated request.

**Response 200** — the `Node` object (see above).

**Response 404** — node not found.

---

### `GET /api/v1/fleet/capacity`

Return aggregated resource totals and headroom across all `READY` and `RUNNING` nodes, plus a list of catalog models that can fit on current free capacity.

**Auth:** Any authenticated request (viewer-accessible; no special role required).

**Response 200**

```json
{
  "ready_nodes": 3,
  "vram_total_gb": 72.0,
  "vram_used_gb": 24.0,
  "vram_headroom_gb": 48.0,
  "ram_total_gb": 192.0,
  "ram_headroom_gb": 128.0,
  "mem_bandwidth_total_gbs": 1200.0,
  "mem_bandwidth_headroom_gbs": 800.0,
  "bottleneck": "vram",
  "can_fit_models": ["llama-3-8b", "mistral-7b"]
}
```

| Field | Description |
|---|---|
| `ready_nodes` | Count of nodes in `NODE_STATE_READY` or `NODE_STATE_RUNNING`. |
| `vram_total_gb` | Sum of VRAM across all ready nodes. |
| `vram_used_gb` | VRAM attributed to nodes that host at least one active deployment. |
| `vram_headroom_gb` | `vram_total_gb − vram_used_gb`. |
| `ram_total_gb` | Sum of system RAM across ready nodes (from hardware profile if available). |
| `mem_bandwidth_total_gbs` | Sum of memory-bandwidth (GB/s) across ready nodes. |
| `bottleneck` | The most-constrained resource by headroom ratio: `vram`, `ram`, `bandwidth`, or `none`. |
| `can_fit_models` | Model IDs from the catalog that the planner can deploy given remaining headroom. Empty when no planner is configured. |

---

### `POST /api/v1/nodes/{id}/drain`

Cordon a node: marks it `NODE_STATE_DRAINING` so the planner stops scheduling new deployments there. **Existing deployments on the node are not migrated or rebalanced** — only new scheduling is blocked.

**Auth:** Any authenticated request.

**Response 200**

```json
{
  "node_id": "node-abc123",
  "state": "NODE_STATE_DRAINING",
  "message": "node cordoned (unschedulable); existing deployments are not migrated or rebalanced"
}
```

**Response 404** — node not found.

!!! note "Draining is not migration"
    This endpoint cordons the node only. Active deployments on the drained node continue to run.
    To fully vacate a node, undeploy any active models first, then drain.

---

### `POST /api/v1/nodes/{id}/restart`

Tear down all active deployments on the node and allow the reconciler to re-provision them on remaining available nodes. **The agent process on the node is not rebooted.**

**Auth:** Any authenticated request.

**Response 202 Accepted** — restart is asynchronous; re-provisioning happens in the background.

```json
{
  "node_id": "node-abc123",
  "deployments": ["dep-xyz", "dep-abc"],
  "message": "deployments torn down; re-provisioning proceeds in the background"
}
```

**Response 404** — node not found.

**Response 409** — node has no active deployments to restart.

---

### `DELETE /api/v1/nodes/{id}`

Decommission a node: transitions it to `NODE_STATE_DECOMMISSIONED` and revokes its mTLS certificate. The node record is retained in the registry.

**Auth:** Any authenticated request.

**Guarded operation:** the request is rejected if any non-terminal deployment still occupies the node.

**Response 204** — decommissioned successfully.

**Response 404** — node not found.

**Response 409** — node is still hosting active deployments.

```json
{
  "error": "node_in_use",
  "message": "node still hosts one or more active deployments; tear them down or migrate them first",
  "deployments": ["dep-xyz"]
}
```

---

## Common operator workflows

### Graceful node removal

1. `POST /api/v1/nodes/{id}/drain` — stop new deployments being scheduled.
2. Undeploy any active models via `DELETE /api/v1/deployments/{id}` for each blocking deployment.
3. `DELETE /api/v1/nodes/{id}` — decommission once no active deployments remain.

### Rolling node restart

1. For each node you want to cycle: `POST /api/v1/nodes/{id}/restart`.
2. Monitor reconciler progress at `GET /api/v1/reconciler/status`.
3. Poll `GET /api/v1/fleet/capacity` to confirm capacity is restored before moving to the next node.

---

## See also

- [Reconciler](reconciler.md) — background loop that watches for stopped deployments and re-provisions them.
- [What-if Planner](what-if-planner.md) — model hardware ROI simulations against current fleet capacity.
- [Observability](observability.md) — Prometheus metrics and the live SSE metrics stream.
