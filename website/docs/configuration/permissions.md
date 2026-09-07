# Permission Resolver

Purser v0.4 introduces a fine-grained permission system for organizations and teams.
This page documents all permission strings, the six built-in roles, how wildcard
matching works, backward compatibility with the legacy three-role model, and how
to define custom roles (see [Custom roles](#custom-roles-preview) below).

---

## Permission string format

Every action in Purser maps to a colon-separated hierarchical string:

```
<namespace>:<resource>:<action>
```

For example: `team:models:deploy`, `org:members:invite`, `platform:orgs:create`.

The top-level namespaces are:

| Namespace | Scope |
|-----------|-------|
| `platform:` | Platform-wide administration (cross-org) |
| `org:` | Organization-level management |
| `team:` | Team-level resources (models, keys, members) |
| `inference:` | Gateway inference calls |

---

## Complete permission reference

### Platform permissions

| Permission | Description |
|------------|-------------|
| `platform:orgs:create` | Create a new organization on the platform |
| `platform:orgs:delete` | Delete an existing organization (and all its teams) |
| `platform:pools:manage` | Add, edit, or remove GPU/compute pools platform-wide |
| `platform:users:invite` | Invite users to the platform before they belong to an org |

### Organization permissions

| Permission | Description |
|------------|-------------|
| `org:teams:create` | Create a new team within the organization |
| `org:teams:delete` | Delete a team and all its associated resources |
| `org:members:invite` | Invite a user to the organization |
| `org:members:remove` | Remove a member from the organization |
| `org:roles:create` | Create a custom role definition scoped to the org |
| `org:roles:delete` | Delete a custom role definition |
| `org:pools:request` | Request additional compute pool quota for the org |

### Team permissions

| Permission | Description |
|------------|-------------|
| `team:models:deploy` | Deploy a model to a team's serving pool |
| `team:models:undeploy` | Remove a deployed model from the serving pool |
| `team:keys:create` | Issue a new API key for the team |
| `team:keys:revoke` | Revoke an existing API key |
| `team:members:view` | List team members and their roles |
| `team:members:invite` | Add a member to the team |
| `team:members:remove` | Remove a member from the team |
| `team:metrics:view` | Read inference throughput, latency, and cost metrics |
| `team:approvals:view` | View pending deployment approval requests |
| `team:approvals:review` | Approve or reject deployment requests |

### Inference permissions

| Permission | Description |
|------------|-------------|
| `inference:call` | Send requests to the gateway (`/v1/chat/completions`, etc.) |

---

## Built-in roles

Six roles are seeded by the platform on first boot. They cannot be deleted.

### Role–permission matrix

| Permission | `platform_admin` | `org_admin` | `team_admin` | `developer` | `viewer` | `inference_only` |
|---|:---:|:---:|:---:|:---:|:---:|:---:|
| `platform:orgs:create` | ✓ | | | | | |
| `platform:orgs:delete` | ✓ | | | | | |
| `platform:pools:manage` | ✓ | | | | | |
| `platform:users:invite` | ✓ | | | | | |
| `org:teams:create` | ✓ | ✓ | | | | |
| `org:teams:delete` | ✓ | ✓ | | | | |
| `org:members:invite` | ✓ | ✓ | | | | |
| `org:members:remove` | ✓ | ✓ | | | | |
| `org:roles:create` | ✓ | ✓ | | | | |
| `org:roles:delete` | ✓ | ✓ | | | | |
| `org:pools:request` | ✓ | ✓ | | | | |
| `team:models:deploy` | ✓ | ✓ | ✓ | ✓ | | |
| `team:models:undeploy` | ✓ | ✓ | ✓ | ✓ | | |
| `team:keys:create` | ✓ | ✓ | ✓ | ✓ | | |
| `team:keys:revoke` | ✓ | ✓ | ✓ | | | |
| `team:members:view` | ✓ | ✓ | ✓ | | ✓ | |
| `team:members:invite` | ✓ | ✓ | ✓ | | | |
| `team:members:remove` | ✓ | ✓ | ✓ | | | |
| `team:metrics:view` | ✓ | ✓ | ✓ | ✓ | ✓ | |
| `team:approvals:view` | ✓ | ✓ | ✓ | | ✓ | |
| `team:approvals:review` | ✓ | ✓ | ✓ | | | |
| `inference:call` | ✓ | ✓ | ✓ | ✓ | | ✓ |

### Role descriptions

**`platform_admin`** — Full platform access. Intended for the platform operator team.
Should be issued sparingly and audited on every use.

**`org_admin`** — Full control over a single organization: teams, members, custom roles,
and pool quota. Cannot touch cross-org or platform-level resources.

**`team_admin`** — Full control over a single team: models, keys, members, approvals.
Cannot create or delete teams at the org level.

**`developer`** — Can deploy/undeploy models, create API keys, view metrics, and call
the inference gateway. Cannot revoke other keys or manage team membership.

**`viewer`** — Read-only observer. Can see members, metrics, and approvals but cannot
change anything.

**`inference_only`** — Can only call the inference gateway. No control-plane access
whatsoever. Use for applications and end-users that only need to send prompts.

---

## Wildcard matching

The permission resolver supports wildcard suffixes using `:*`:

```
"team:*"     matches any permission that starts with "team:"
"org:*"      matches any permission that starts with "org:"
"platform:*" matches any permission that starts with "platform:"
"inference:*" matches any permission that starts with "inference:"
```

Wildcards are anchored to the segment boundary — a `team:*` grant does **not** match
`org:teams:create`, even though `teams` appears in both strings. The comparison is:

```
Does the required permission start with the wildcard prefix ("team:")?
```

| Wildcard | Required | Match? |
|----------|----------|--------|
| `team:*` | `team:models:deploy` | Yes |
| `team:*` | `team:keys:revoke` | Yes |
| `team:*` | `org:teams:create` | No — different namespace |
| `org:*` | `org:members:invite` | Yes |
| `org:*` | `team:models:deploy` | No — different namespace |
| `inference:*` | `inference:call` | Yes |

Wildcards are evaluated in the permission resolver (`permissions.Has`) and work
identically in all contexts where permissions are checked: RBAC middleware, API
handlers, and programmatic checks via `HasAll`/`HasAny`.

---

## Backward compatibility: legacy roles

Before v0.4, Purser used three coarse API key roles:
`admin`, `viewer`, and `inference`. These are still accepted for existing keys
while teams are being migrated to the new model.

The resolver maps them to v0.4 permission sets as follows:

| Legacy role | Maps to built-in role | Effective permissions |
|-------------|----------------------|-----------------------|
| `admin` | `platform_admin` | All 21 permissions, `IsPlatformAdmin = true` |
| `viewer` | `viewer` | `team:members:view`, `team:metrics:view`, `team:approvals:view` |
| `inference` | `inference_only` | `inference:call` only |

An API key that carries the old `admin` role has the same effective access as a
`platform_admin` in the new model. A key with the old `inference` role maps to
exactly `inference:call` — no control-plane access.

New keys issued after upgrading to v0.4 should use the fine-grained roles. The
legacy three-role model will be removed in v0.6.

---

## Custom roles (preview)

!!! warning "Not yet available"
    Custom role creation via the API is in preview and subject to change.
    The schema and endpoints documented here may be updated before general availability.

Custom roles are scoped to an organization. A member with `org:roles:create` can
define a new role with any subset of the permissions they themselves hold.

### Create a custom role

```http
POST /api/v1/orgs/{org_id}/roles
Content-Type: application/json

{
  "name": "ml-engineer",
  "description": "Can deploy models and call the gateway but cannot manage keys",
  "permissions": [
    "team:models:deploy",
    "team:models:undeploy",
    "team:metrics:view",
    "inference:call"
  ]
}
```

### Assign a custom role to a team member

```http
PUT /api/v1/orgs/{org_id}/teams/{team_id}/members/{user_id}
Content-Type: application/json

{
  "role_id": "ml-engineer"
}
```

A user may hold multiple roles; the effective permission set is the union of all
assigned role permissions, deduplicated and sorted. Use `permissions.Merge()` in
Go code when combining sets programmatically.
