# Platform Users, Custom Roles & Permissions API

_Available from Purser v0.4_

This page documents the platform-identity layer: how clients discover the current
user's identity and permissions, how operators manage custom roles, and how the
permission catalogue works.

---

## Quick-start: what every client should do at login

When your application signs in, call `GET /api/v1/platform/users/me` immediately.
This single endpoint tells you:

- **who** the caller is (`actor` string)
- **which orgs** they belong to, with their role in each org
- **which teams** they belong to

```http
GET /api/v1/platform/users/me
Authorization: Bearer <token>
```

```json
{
  "actor": "oidc:alice@example.com",
  "orgs":  [{"org_id": "org-1", "user_sub": "oidc:alice@example.com", "role": "org_admin", "joined_at": "2026-09-01T10:00:00Z"}],
  "teams": [{"team_id": "team-1", "user_sub": "oidc:alice@example.com", "role_id": "role-dev", "joined_at": "2026-09-01T10:00:00Z"}],
  "note":  "full user profile requires OIDC/LDAP integration (planned for a future release)"
}
```

> **Note:** A full user profile (display name, avatar) requires OIDC/LDAP integration
> (planned for a future release). The current response exposes the stable `actor` string and
> membership data.

---

## Users

### `GET /api/v1/platform/users`

List all platform users. Returns the set of users who are members of the default org.

**Auth:** `platform_admin` (API key with role `admin`) required.

**Response 200**

```json
{
  "users": [
    {"user_sub": "oidc:alice@example.com", "org_id": "default", "role": "org_admin"},
    {"user_sub": "apikey:a1b2c3d4",        "org_id": "default", "role": "member"}
  ]
}
```

---

### `GET /api/v1/platform/users/me`

Return the calling actor's identity and memberships. Works with any valid credential
(API key, OIDC token, or service-account JWT). This is the **recommended first call**
after authentication.

**Auth:** Any authenticated request.

**Response 200** — see [Quick-start](#quick-start-what-every-client-should-do-at-login).

---

### `GET /api/v1/platform/users/{id}`

Return the profile and memberships of a specific user by their `user_sub` string.

**Auth:** `platform_admin` or the same user (`actor == id`).

**Response 200**

```json
{
  "user_sub": "oidc:alice@example.com",
  "orgs":     [...],
  "teams":    [...]
}
```

**Response 403** — access denied (not admin and not the same user).

---

## Custom Roles

Custom roles let `org_admin` users define named permission sets that can be assigned
to team members. Each role is scoped to an org and carries a list of
[permission strings](#permissions).

### Built-in (system) roles

System roles are seeded by the control plane at startup. They cannot be modified
or deleted. Use `GET /api/v1/platform/orgs/{orgId}/roles` to discover them; they
are returned with `"is_system": true`.

---

### `POST /api/v1/platform/orgs/{orgId}/roles`

Create a new custom role in an org.

**Auth:** `platform_admin` (API key `admin` role).

**Request body**

```json
{
  "name":        "ml_engineer",
  "description": "Full access to model deployment and metrics within a team",
  "permissions": [
    "team:models:deploy",
    "team:models:view",
    "team:metrics:view",
    "inference:call"
  ]
}
```

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `name` | string | yes | Unique name within the org |
| `description` | string | no | Human-readable description |
| `permissions` | string[] | no | List of permission keys (see [Permissions](#permissions)) |

**Response 201** — the created `CustomRole` object:

```json
{
  "id":          "role-a1b2c3d4",
  "org_id":      "org-1",
  "name":        "ml_engineer",
  "description": "Full access to model deployment and metrics within a team",
  "permissions": ["team:models:deploy", "team:models:view", "team:metrics:view", "inference:call"],
  "is_system":   false,
  "created_at":  "2026-09-06T12:00:00Z",
  "updated_at":  "2026-09-06T12:00:00Z"
}
```

**Response 409** — a role with that name already exists in this org.

---

### `GET /api/v1/platform/orgs/{orgId}/roles`

List all roles (system and custom) for an org, ordered by `is_system DESC, name`.

**Auth:** Any authenticated request.

**Response 200**

```json
{
  "roles": [
    {"id": "role-builtin-admin", "name": "admin", "is_system": true, ...},
    {"id": "role-a1b2c3d4",     "name": "ml_engineer", "is_system": false, ...}
  ]
}
```

---

### `GET /api/v1/platform/orgs/{orgId}/roles/{id}`

Get a single custom role by ID.

**Response 200** — `CustomRole` object.  
**Response 404** — role not found.

---

### `PUT /api/v1/platform/orgs/{orgId}/roles/{id}`

Replace the mutable fields of a custom role. System roles cannot be modified.

**Auth:** `platform_admin`.

**Request body** — same shape as `POST` (all fields optional; omitted fields retain
their current value).

**Response 200** — the updated `CustomRole`.  
**Response 404** — role not found.  
**Response 409** — `"is_system": true` (system roles cannot be modified) or
a name conflict with an existing role.

---

### `DELETE /api/v1/platform/orgs/{orgId}/roles/{id}`

Delete a custom role.

**Auth:** `platform_admin`.

**Response 204** — deleted.  
**Response 404** — role not found.  
**Response 409** — role is a system role, or is currently assigned to one or more
team members. Unassign all members before deleting.

---

## Permissions

### `GET /api/v1/platform/permissions`

Return the complete catalogue of fine-grained permission strings recognised by
Purser, with descriptions and scope tags.

**Auth:** Any authenticated request.

**Response 200**

```json
{
  "permissions": [
    {"key": "team:models:deploy",   "description": "Deploy models to the team's node pool", "scope": "team"},
    {"key": "team:metrics:view",    "description": "Read live metrics for the team's resources", "scope": "team"},
    {"key": "inference:call",       "description": "Call the inference API (/v1/...)",          "scope": "inference"},
    ...
  ]
}
```

### Permission scopes

| Scope | Description |
|-------|-------------|
| `platform` | Platform-wide actions (user management, org management) |
| `org` | Per-org actions (member management, role management, billing) |
| `team` | Per-team actions (model deploy, node management, API keys) |
| `inference` | Inference gateway calls |

### Full permission list

| Key | Scope | Description |
|-----|-------|-------------|
| `platform:users:view` | platform | List and read platform users |
| `platform:users:manage` | platform | Create and deactivate platform users |
| `platform:orgs:view` | platform | List and read organisations |
| `platform:orgs:manage` | platform | Create and manage organisations |
| `platform:audit:view` | platform | Read the platform-level audit log |
| `org:members:view` | org | List org members |
| `org:members:manage` | org | Add and remove org members |
| `org:roles:view` | org | List custom roles in an org |
| `org:roles:manage` | org | Create, edit, and delete custom roles |
| `org:teams:view` | org | List teams within an org |
| `org:teams:manage` | org | Create and manage teams |
| `org:billing:view` | org | View billing and quota information |
| `org:billing:manage` | org | Adjust billing limits and quotas |
| `org:audit:view` | org | Read the org-level audit log |
| `org:policy:view` | org | Read OPA/Rego policies |
| `org:policy:manage` | org | Create and edit policies |
| `team:models:view` | team | List models registered to the team |
| `team:models:deploy` | team | Deploy models to the team's node pool |
| `team:models:delete` | team | Remove model deployments |
| `team:nodes:view` | team | List nodes registered to the team |
| `team:nodes:manage` | team | Enroll and drain nodes |
| `team:metrics:view` | team | Read live metrics |
| `team:audit:view` | team | Read the team-level audit log |
| `team:apikeys:view` | team | List API keys scoped to the team |
| `team:apikeys:manage` | team | Create and revoke API keys |
| `team:config:view` | team | Read the team's desired-state config |
| `team:config:apply` | team | Apply config-as-code |
| `team:approval:vote` | team | Cast an approval vote for team deployments |
| `inference:call` | inference | Call the inference API (/v1/...) |
| `inference:stream` | inference | Stream inference responses |
| `inference:audit:view` | inference | Read inference audit logs |
| `inference:usage:view` | inference | Read per-request token usage |
| `inference:quota:manage` | inference | Adjust inference quotas |

---

## Effective Permissions

### `GET /api/v1/platform/teams/{teamId}/my-permissions`

Return the effective permission set the calling actor has within a specific team.
The permissions are resolved from the custom role assigned to the caller in that
team's `team_members` record.

If the caller is not a member of the team, an empty permission list is returned
(not 404 — so clients can always query without pre-checking membership).

**Auth:** Any authenticated request.

**Response 200**

```json
{
  "team_id":     "team-1",
  "user_sub":    "oidc:alice@example.com",
  "role_id":     "role-a1b2c3d4",
  "role_name":   "ml_engineer",
  "permissions": ["team:models:deploy", "team:models:view", "team:metrics:view", "inference:call"]
}
```

---

## Data types

### `CustomRole`

```json
{
  "id":          "role-a1b2c3d4",
  "org_id":      "org-1",
  "name":        "ml_engineer",
  "description": "...",
  "permissions": ["team:models:deploy", "..."],
  "is_system":   false,
  "created_at":  "2026-09-06T12:00:00Z",
  "updated_at":  "2026-09-06T12:00:00Z"
}
```

### `EffectivePermissions`

```json
{
  "team_id":     "team-1",
  "user_sub":    "oidc:alice@example.com",
  "role_id":     "role-a1b2c3d4",
  "role_name":   "ml_engineer",
  "permissions": ["..."]
}
```

---

## Roadmap

| Version | Feature |
|---------|---------|
| v0.4 (now) | Custom role CRUD, permissions catalogue, effective-permissions resolution |
| Planned | Full user profiles backed by OIDC/LDAP (`GET /users` returns rich records with email, name, avatar) |
| Planned | `POST /api/v1/platform/orgs` — explicit org creation API |
| Planned | Team member assignment via REST (`POST /platform/teams/{id}/members`) |

---

## See also

- [Organizations & Teams API](platform-orgs.md) — create orgs, teams, and memberships
- [RBAC Permissions](../configuration/permissions.md) — full permission string catalogue and built-in roles
- [OIDC SSO configuration](../configuration/oidc.md) — configure OIDC/LDAP identity providers for user login
- [Service accounts](../configuration/service-accounts.md) — non-interactive machine identities
