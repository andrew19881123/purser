# Platform Multi-Tenant Model

Purser v0.4 introduces a first-class multi-tenant hierarchy that lets you partition
GPU nodes, models, and access keys across organizations and teams.

---

## Hierarchy

```
Platform
└── Organization  (top-level tenant, e.g. "Acme Corp")
    ├── Teams  (sub-units, e.g. "ML Research", "Inference Ops")
    │   └── Users  (with team-scoped custom roles)
    └── Node Pools  (GPU node groups with an access policy)
```

Every entity lives at a stable layer:

| Layer | Identifier | Description |
|---|---|---|
| **Platform** | N/A | The Purser installation itself |
| **Organization** | `org_id` (UUID), `slug` (URL-safe, immutable) | Top-level tenant |
| **Team** | `team_id` (UUID), `slug` (unique within org) | Sub-unit of an org |
| **User** | `user_id` (OIDC `sub` or LDAP DN) | Platform-level identity |
| **Node Pool** | `pool_id` | Named group of GPU nodes |

---

## Organizations

An organization is a top-level tenant. Its `slug` is URL-safe, set at creation
time, and immutable thereafter.

```http
POST /api/v1/orgs
{
  "id":   "org-acme",
  "name": "Acme Corp",
  "slug": "acme"
}
```

---

## Teams

A team is a sub-unit of an organization. Slugs must be unique within the org but
can be reused across organizations.

```http
POST /api/v1/orgs/acme/teams
{
  "id":     "team-ml",
  "name":   "ML Research",
  "slug":   "ml-research"
}
```

---

## Users

Platform users are identified by an OIDC `sub` claim or an LDAP DN. The
`UpsertPlatformUser` operation is called at each login so display names and
email addresses stay current without manual sync.

| Field | Description |
|---|---|
| `id` | OIDC `sub` or LDAP DN (stable, never changes) |
| `email` | Unique; updated on each login |
| `auth_method` | `oidc` \| `ldap` \| `service_account` |
| `last_seen_at` | Timestamp of most recent successful authentication |

---

## Custom Roles and Permissions

Permission strings follow the pattern `<scope>:<resource>:<action>`.

### Full permission reference

| Permission | Scope | Description |
|---|---|---|
| `platform:orgs:create` | platform | Create a new organization |
| `platform:orgs:delete` | platform | Delete an organization |
| `platform:pools:manage` | platform | Create/delete/reassign node pools |
| `platform:users:invite` | platform | Invite users to the platform |
| `org:teams:create` | org | Create a team within the org |
| `org:teams:delete` | org | Delete a team within the org |
| `org:members:invite` | org | Invite users to the organization |
| `org:members:remove` | org | Remove users from the organization |
| `org:roles:create` | org | Create custom roles |
| `org:roles:delete` | org | Delete custom roles |
| `org:pools:request` | org | Request node pool allocation |
| `team:models:deploy` | team | Deploy a model to team-accessible nodes |
| `team:models:undeploy` | team | Remove a deployment |
| `team:keys:create` | team | Create API keys scoped to the team |
| `team:keys:revoke` | team | Revoke API keys |
| `team:members:view` | team | List team members |
| `team:members:invite` | team | Invite users to the team |
| `team:members:remove` | team | Remove users from the team |
| `team:metrics:view` | team | View token usage and latency metrics |
| `team:approvals:view` | team | View pending deployment approval requests |
| `team:approvals:review` | team | Approve or reject deployment requests |
| `inference:call` | inference | Call inference endpoints (`/v1/…`) |

### Built-in system roles

The following roles are seeded by `SeedSystemRoles` at startup and cannot be
deleted (`is_system = true`).

| Role ID | Name | Permissions summary |
|---|---|---|
| `platform_admin` | Platform Admin | All platform:, org:, team:, inference: |
| `org_admin` | Org Admin | All org: + all team: + inference:call |
| `team_admin` | Team Admin | All team: + inference:call |
| `developer` | Developer | deploy, undeploy, keys:create, metrics:view, inference:call |
| `viewer` | Viewer | members:view, metrics:view |
| `inference_only` | Inference Only | inference:call only |

Custom (non-system) roles can be created per-organization:

```http
POST /api/v1/orgs/acme/roles
{
  "id":          "data-scientist",
  "name":        "Data Scientist",
  "permissions": ["team:models:deploy", "team:metrics:view", "inference:call"]
}
```

---

## Node Pools

A node pool is a named group of GPU nodes with an access policy.

### Policy types

| Policy | Description |
|---|---|
| `exclusive` | Only the pool's owner (org or team) can schedule work here |
| `shared` | Multiple teams can use the pool, governed by per-team quotas |

### Owner types

| `owner_type` | `owner_id` | Description |
|---|---|---|
| `platform` | _(empty)_ | Pool managed by platform admins |
| `org` | `org_id` | Pool allocated to a specific organization |
| `team` | `team_id` | Pool owned by a single team |

### Node membership

Each node belongs to **at most one** pool. Moving a node to a different pool
reassigns it atomically.

```http
POST /api/v1/pools/pool-gpu-tier1/nodes
{"node_id": "node-a100-01"}
```

---

## Pool Quotas (shared pools)

When a pool's policy is `shared`, per-team quotas govern how much of the pool
each team may consume.

| Field | Description |
|---|---|
| `max_deployments` | Maximum simultaneous deployments (0 = unlimited) |
| `max_gpu_nodes` | Maximum nodes a team's deployments may occupy (0 = unlimited) |
| `priority` | Lower number = higher scheduling priority when the pool is contested |

```http
PUT /api/v1/pools/pool-gpu-tier1/quotas/team-ml
{
  "max_deployments": 5,
  "max_gpu_nodes":   10,
  "priority":        50
}
```

---

## Effective Permissions

The `GetEffectivePermissions` API resolves the full permission set for a user in
a specific team context. It:

1. Determines the team's parent organization.
2. Checks whether the user is an `org_admin` (grants all org: + team: permissions
   from the built-in `org_admin` role).
3. Unions the org-admin grant with any explicit team-role permissions.

The result is used by authorization middleware to gate every API call.

```json
{
  "user_id":     "alice@example.com",
  "team_id":     "team-ml",
  "org_id":      "org-acme",
  "is_org_admin": false,
  "permissions": [
    "inference:call",
    "team:keys:create",
    "team:metrics:view",
    "team:models:deploy",
    "team:models:undeploy"
  ]
}
```

---

## Accessible Nodes for a Team

`GetAllowedNodeIDs` returns the union of:

1. All nodes in an **exclusive** pool owned by the team (`owner_type=team`, `owner_id=<team_id>`).
2. All nodes in any **shared** pool for which the team has a quota record.

Nodes that belong to no pool, or to pools the team has no access to, are never
returned. This is the enforcement boundary used by the scheduler before placing
a deployment.
