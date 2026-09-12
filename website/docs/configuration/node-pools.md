# Node Pools

Node pools let you partition your GPU fleet into named groups and control which teams can schedule deployments onto which nodes. Without a pool, every team sees every node — exactly the v0.3 behaviour. Pools add enforcement at the planning layer: the planner only considers nodes the requesting team is allowed to use.

---

## Concepts

| Term | Meaning |
|---|---|
| **Node Pool** | A named, policy-governed group of GPU nodes |
| **Exclusive pool** | Only the owner team (or org) can schedule on its nodes |
| **Shared pool** | Multiple teams can use the pool, each governed by a per-team quota |
| **PoolTeamQuota** | Per-team limits on a shared pool: max deployments, max GPU nodes, and a priority score |

A node can belong to at most one pool at a time. Nodes that belong to no pool are available to all teams (backward-compatible default).

### How the deploy flow uses pools

When a deployment request arrives with an API key whose `tenant` field matches a team ID, the control plane:

1. Calls `GetAllowedNodeIDs(teamID)` — returns node IDs from the team's exclusive pool plus nodes in any shared pool where the team has a quota.
2. Passes that list as `Constraints.AllowedNodeIDs` to the DP-layer planner.
3. The planner filters its fleet snapshot to only those nodes before solving the placement.

If `GetAllowedNodeIDs` returns an empty slice (the team has no pool assigned), the planner uses the full fleet — preserving backward compatibility with deployments made before pools were configured.

---

## REST API

All endpoints are under `/api/v1/platform/pools`. Admin or org_admin role required for mutations.

### Pool management

```
POST   /api/v1/platform/pools
GET    /api/v1/platform/pools
GET    /api/v1/platform/pools/{id}
PUT    /api/v1/platform/pools/{id}
DELETE /api/v1/platform/pools/{id}
```

#### Create a pool

```bash
curl -X POST https://purser.example.com/api/v1/platform/pools \
  -H "Authorization: Bearer $ADMIN_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "name": "ML-GPU-Lab",
    "description": "Dedicated A100 cluster for the ML team",
    "owner_type": "team",
    "owner_id": "team-uuid-here",
    "policy": "exclusive"
  }'
```

**Response 201:**

```json
{
  "id": "a3f1b2c4",
  "name": "ML-GPU-Lab",
  "description": "Dedicated A100 cluster for the ML team",
  "owner_type": "team",
  "owner_id": "team-uuid-here",
  "policy": "exclusive",
  "created_at": "2026-09-05T10:00:00Z",
  "updated_at": "2026-09-05T10:00:00Z"
}
```

| Field | Required | Values |
|---|---|---|
| `name` | yes | Any non-empty string |
| `owner_type` | no | `platform` (default), `org`, `team` |
| `owner_id` | no | org or team UUID when owner_type is org/team |
| `policy` | no | `exclusive` (default) or `shared` |

#### Update a pool

`PUT` accepts any subset of `name`, `description`, and `policy`; omitted fields are
left unchanged. `policy` must be `shared` or `exclusive`.

```bash
curl -X PUT https://purser.example.com/api/v1/platform/pools/$POOL_ID \
  -H "Authorization: Bearer $ADMIN_KEY" \
  -H "Content-Type: application/json" \
  -d '{"name": "ML-GPU-Lab-2", "policy": "shared"}'
```

Returns **200** with the updated pool JSON (same shape as create).

#### Delete a pool

Returns **409 Conflict** if any nodes are still assigned. Remove all nodes first.

```bash
curl -X DELETE https://purser.example.com/api/v1/platform/pools/$POOL_ID \
  -H "Authorization: Bearer $ADMIN_KEY"
```

---

### Node assignment

Assigning a node to a pool restricts which teams may use it. Each node can belong to at most one pool.

```
POST   /api/v1/platform/pools/{id}/nodes
GET    /api/v1/platform/pools/{id}/nodes
DELETE /api/v1/platform/pools/{id}/nodes/{nodeId}
```

#### Assign a node

```bash
curl -X POST https://purser.example.com/api/v1/platform/pools/$POOL_ID/nodes \
  -H "Authorization: Bearer $ADMIN_KEY" \
  -H "Content-Type: application/json" \
  -d '{"node_id": "gpu-node-01"}'
```

**Response 201:**

```json
{
  "pool_id": "a3f1b2c4",
  "node_id": "gpu-node-01",
  "message": "node assigned to pool"
}
```

Returns **409 Conflict** if the node is already in a different pool. Remove it from its current pool before reassigning.

#### List nodes in a pool

```bash
curl https://purser.example.com/api/v1/platform/pools/$POOL_ID/nodes \
  -H "Authorization: Bearer $ADMIN_KEY"
```

**Response 200:**

```json
{
  "pool_id": "a3f1b2c4",
  "node_ids": ["gpu-node-01", "gpu-node-02"]
}
```

#### Remove a node from a pool

```bash
curl -X DELETE \
  https://purser.example.com/api/v1/platform/pools/$POOL_ID/nodes/gpu-node-01 \
  -H "Authorization: Bearer $ADMIN_KEY"
```

Returns **204 No Content** on success.

---

### Shared pool quotas

Shared pools allow multiple teams to use the same hardware, governed by per-team quotas.

```
PUT    /api/v1/platform/pools/{id}/quotas/{teamId}
GET    /api/v1/platform/pools/{id}/quotas
DELETE /api/v1/platform/pools/{id}/quotas/{teamId}
```

#### Upsert a quota

```bash
curl -X PUT \
  https://purser.example.com/api/v1/platform/pools/$POOL_ID/quotas/$TEAM_ID \
  -H "Authorization: Bearer $ADMIN_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "max_deployments": 3,
    "max_gpu_nodes": 6,
    "priority": 10
  }'
```

**Response 200:**

```json
{
  "pool_id": "a3f1b2c4",
  "team_id": "team-uuid",
  "max_deployments": 3,
  "max_gpu_nodes": 6,
  "priority": 10,
  "created_at": "2026-09-05T10:00:00Z",
  "updated_at": "2026-09-05T10:00:00Z"
}
```

| Field | Meaning |
|---|---|
| `max_deployments` | Maximum concurrent active deployments this team may run on the pool (0 = unlimited) |
| `max_gpu_nodes` | Maximum GPU nodes this team may occupy in the pool (0 = unlimited) |
| `priority` | Lower value = higher precedence when the pool is contested (default 100) |

---

## purser.yaml example

```yaml
# purser.yaml — declarative config-as-code

platform:
  pools:
    - name: ml-gpu-lab
      description: "Dedicated A100 cluster"
      owner_type: team
      owner_id: team-ml-research
      policy: exclusive
      nodes:
        - gpu-node-01
        - gpu-node-02
        - gpu-node-03

    - name: shared-inference
      description: "Shared inference pool with per-team limits"
      owner_type: platform
      policy: shared
      nodes:
        - inf-node-01
        - inf-node-02
      quotas:
        - team_id: team-nlp
          max_deployments: 2
          max_gpu_nodes: 4
          priority: 10
        - team_id: team-cv
          max_deployments: 1
          max_gpu_nodes: 2
          priority: 20
```

---

## How the deploy flow respects pools

When a team deploys a model, the control plane automatically enforces pool membership:

1. The request carries an API key with `tenant = <team-id>`.
2. `handleDeployModel` calls `GetAllowedNodeIDs(tenant)` before invoking the planner.
3. The registry returns the union of:
   - All nodes in the team's own **exclusive** pool.
   - All nodes in **shared** pools where the team has a quota record.
4. Those node IDs are passed to the planner as `Constraints.AllowedNodeIDs`.
5. The planner only considers those nodes when computing the layer-split placement.

**Backward compatibility:** teams with no pool assigned receive an empty list from `GetAllowedNodeIDs`. The server treats an empty list as "no restriction" and passes `Constraints{}` (all nodes) to the planner — exactly the v0.3 behaviour.

```
Team A (exclusive pool: node-1, node-2)
  → deploys model → planner sees only node-1, node-2

Team B (no pool)
  → deploys model → planner sees all READY nodes (backward compat)

Team C (shared pool: node-3, node-4; quota: max_deployments=2)
  → deploys model → planner sees node-3, node-4
```

---

## API reference summary

| Method | Path | Description |
|---|---|---|
| `POST` | `/api/v1/platform/pools` | Create pool |
| `GET` | `/api/v1/platform/pools` | List all pools |
| `GET` | `/api/v1/platform/pools/{id}` | Get pool (includes `node_ids`) |
| `PUT` | `/api/v1/platform/pools/{id}` | Update name/description/policy |
| `DELETE` | `/api/v1/platform/pools/{id}` | Delete pool (409 if nodes assigned) |
| `POST` | `/api/v1/platform/pools/{id}/nodes` | Assign node to pool |
| `GET` | `/api/v1/platform/pools/{id}/nodes` | List nodes in pool |
| `DELETE` | `/api/v1/platform/pools/{id}/nodes/{nodeId}` | Remove node from pool |
| `PUT` | `/api/v1/platform/pools/{id}/quotas/{teamId}` | Upsert team quota (shared pools) |
| `GET` | `/api/v1/platform/pools/{id}/quotas` | List team quotas |
| `DELETE` | `/api/v1/platform/pools/{id}/quotas/{teamId}` | Delete team quota |

---

## Node Pools page (UI)

The **Node Pools** page in the operator dashboard lists every pool with its owner,
policy, and node count. Per row:

- **Assign Node** expands an inline panel to add/remove nodes and (for shared pools)
  view team quotas.
- **Edit** opens a modal to change the pool name, description, or policy
  (`PUT /pools/{id}`).
- **Delete** uses an arm→confirm interaction: the first click arms the button (it
  turns red and shows *Delete {name}?*), the second confirms. The control plane
  returns **409** if the pool still has assigned nodes — remove them first.

---

## Related pages

- [Platform model](platform-model.md) — organizations, teams, custom roles
- [RBAC](rbac.md) — API key roles and tenant scoping
- [purser.yaml reference](purser-yaml.md) — config-as-code
