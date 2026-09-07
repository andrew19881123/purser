# Platform UI — Organizations, Teams & Node Pools

Available since **v0.4**.

The Purser operator dashboard now includes three new sections under the **Operate**
sidebar group that cover the v0.4 platform model:

| Page | URL | Description |
|---|---|---|
| Organizations | `/platform/orgs` | Create and manage top-level organizations |
| Team | `/platform/orgs/{orgId}/teams/{teamId}` | Manage members, pool, and permissions |
| Node Pools | `/platform/pools` | Create pools, assign nodes, set quotas |

---

## Workflow: from org creation to inference

```
1. Create an organization      →  /platform/orgs
2. Create a team inside it     →  /platform/orgs/{orgId}/teams
3. Invite members to the team  →  /platform/orgs/{orgId}/teams/{teamId}
4. Create a node pool          →  /platform/pools
5. Assign GPU nodes to pool    →  /platform/pools  (expand row)
6. Set per-team quota          →  /platform/pools  (shared pools only)
7. Team members deploy models  →  /deployments
```

---

## Organizations page

The **Organizations** page (`/platform/orgs`) is the entry point for the platform
hierarchy. Each organization is an isolated namespace for teams, API keys, and
billing.

**Actions available to platform admins:**

- **Create Organization** — choose a name and slug (auto-derived from the name).
  An optional description helps operators identify each org.
- **View Teams** — navigate to the team listing for the org.
- **Delete** — permanently removes the org after a two-click confirmation.

> **Note**: deleting an organization removes all of its teams and revokes their
> API keys. This action cannot be undone.

---

## Team page

The **Team** page shows three cards:

### 1. Members

A table of all users that belong to the team, their role, and when they joined.
Platform admins and org admins can:

- **Invite Member** — enter a user ID (email or internal ID) and role ID.
- **Remove** — click the trash icon and confirm to remove a member.

### 2. Node Pool

Shows which node pool has been assigned to this team. Displays the pool name and its
policy badge (`Exclusive` or `Shared`). A link navigates to `/platform/pools` for
pool management.

If no pool has been assigned yet, a placeholder guides the admin to the Node Pools
page.

### 3. My Permissions

Calls `GET /platform/teams/{id}/my-permissions` and displays the permissions the
current authenticated user has within the team. An **Org Admin** badge appears when
the user holds org-wide admin rights.

---

## Node Pools page

The **Node Pools** page (`/platform/pools`) is where platform admins configure the
compute allocation layer.

### Policy badges

| Badge | Meaning |
|---|---|
| **Exclusive** (green) | Only one team can use this pool at a time |
| **Shared** (blue) | Multiple teams share the pool according to quotas |

### Creating a pool

Click **Create Pool** and fill in:

- **Name** — human-readable label (e.g. `h100-pool-a`).
- **Description** — optional note.
- **Owner Type** — `platform`, `org`, or `team`.
- **Owner ID** — the platform, org slug, or team ID that owns this pool.
- **Policy** — `shared` or `exclusive`.

### Assigning nodes

Expand a pool row to reveal the **detail panel**:

1. Enter a node ID in the text field and click **Assign Node**.
2. Existing nodes are shown as badges; click the trash icon to remove one.

### Team quotas (shared pools only)

For shared pools the detail panel also shows the **Team Quotas** table. Quotas are
set via `PUT /platform/pools/{id}/quotas/{teamId}` and control:

| Field | Description |
|---|---|
| `max_deployments` | Maximum concurrent deployments this team can run from this pool |
| `max_gpu_nodes` | Maximum GPU nodes the team can occupy simultaneously |
| `priority` | Scheduling priority (higher = first served) |

---

## API reference

All endpoints live under `/api/v1/platform/...` and require a valid session or
`Authorization: Bearer <api-key>` header.

| Method | Path | Description |
|---|---|---|
| `GET` | `/platform/orgs` | List all organizations |
| `POST` | `/platform/orgs` | Create an organization |
| `GET` | `/platform/orgs/{id}` | Get one organization |
| `DELETE` | `/platform/orgs/{id}` | Delete an organization |
| `GET` | `/platform/orgs/{orgId}/teams` | List teams in an org |
| `POST` | `/platform/orgs/{orgId}/teams` | Create a team |
| `GET` | `/platform/teams/{id}` | Get one team |
| `DELETE` | `/platform/teams/{id}` | Delete a team |
| `GET` | `/platform/teams/{id}/members` | List team members |
| `POST` | `/platform/teams/{id}/members` | Add a member |
| `DELETE` | `/platform/teams/{id}/members/{userId}` | Remove a member |
| `GET` | `/platform/teams/{id}/my-permissions` | Get current user's effective permissions |
| `GET` | `/platform/pools` | List all node pools |
| `POST` | `/platform/pools` | Create a node pool |
| `GET` | `/platform/pools/{id}` | Get one pool |
| `GET` | `/platform/pools/{id}/nodes` | List nodes in a pool |
| `PUT` | `/platform/pools/{id}/nodes/{nodeId}` | Assign a node |
| `DELETE` | `/platform/pools/{id}/nodes/{nodeId}` | Remove a node |
| `GET` | `/platform/pools/{id}/quotas` | List team quotas (shared pools) |
| `PUT` | `/platform/pools/{id}/quotas/{teamId}` | Create or update a quota |
| `GET` | `/platform/me` | Current user's orgs and teams |

---

## RBAC roles

The platform model recognizes the following built-in roles:

| Role | Scope | Capabilities |
|---|---|---|
| `platform_admin` | Platform | Full access to all organizations, teams, and pools |
| `org_admin` | Organization | Manage teams within their org; read pools |
| `team_admin` | Team | Invite/remove members; view pool assignment |
| `member` | Team | Read-only; can deploy within quota |

Custom roles can be defined with an explicit permission list. See
[RBAC configuration](rbac.md) for details.

---

## Mock mode

When `VITE_PURSER_MOCK=1` is set, all platform endpoints return empty lists and
single-item stubs so the UI is fully navigable offline. No mock data is shipped in
production bundles (the mock is code-split behind a dynamic import).
