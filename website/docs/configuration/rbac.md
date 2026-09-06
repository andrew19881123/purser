# RBAC: Role-Based Access Control for API Keys

Every Purser API key carries an **RBAC role** that limits what the key may do.
A key's role is set at creation time and cannot be changed after the fact —
rotate (revoke + create) when the access level needs to change.

## The Three Roles

| Role | What it can do |
|------|----------------|
| `admin` | Full access to every control-plane management endpoint. Use for operators, CI pipelines, and the dashboard itself. |
| `viewer` | Read-only access (`GET`) to every `/api/v1/*` endpoint. Cannot create, update, or delete any resource. Use for monitoring dashboards, audit tooling, and any consumer that only needs to observe cluster state. |
| `inference` | Access to the gateway inference surface (`/v1/chat/completions`, etc.) only. Requests to the control-plane management surface (`/api/v1/*`) are rejected with `403 Forbidden`, including public cluster-health reads. Use for applications and end-users calling the model. |

### When to use each role

- **admin** — one key per human operator or trusted automation; audit every use.
- **viewer** — read-only monitoring tools, alerting pipelines, or any service
  that only needs to *observe* the cluster without being able to change it.
  A compromised viewer key cannot alter models, deployments, or other keys.
- **inference** — issue one per application or team that sends prompts. An
  inference key cannot drain a node, delete a model, or create new keys.
  It is scoped to the gateway only.

## Creating a Role-Scoped Key

### Via the dashboard

Open **Settings → API keys → Create key**. Select a role from the **Role**
dropdown before clicking **Create**. The secret is shown exactly once.

### Via the API

```http
POST /api/v1/apikeys
Content-Type: application/json

{
  "name": "my-inference-app",
  "tenant": "team-ai",
  "role": "inference",
  "quota": 500000
}
```

Omitting `"role"` defaults to `"admin"` (backward-compatible behaviour for
keys created before RBAC was introduced).

### Via the CLI

```bash
purser apikeys create --name my-viewer --role viewer
```

## Security Model

- The internal gateway ↔ control-plane channel uses a shared **internal token**
  (`PURSER_INTERNAL_TOKEN`), which bypasses RBAC entirely. This token is never
  exposed to end-users and is automatically rotated by the operator's secret
  manager. Do not share it.
- Public endpoints (`GET /api/v1/cluster/health`) bypass RBAC so they remain
  accessible for health probes regardless of the key role.
- An inference key presented to the control plane is **rejected at the RBAC
  layer**, before any handler runs. The error body contains
  `"error": "forbidden"` and a human-readable `"message"`.
- A viewer key attempting a `POST`, `PUT`, `PATCH`, or `DELETE` request also
  receives `403 Forbidden` before the request reaches any handler.

## Tenant isolation

Purser scopes list responses to the requesting principal's tenant so that
viewers in one tenant cannot enumerate resources belonging to another.

### How it works

Every request is inspected by `extractRequestTenant` before a list query runs:

| Authenticated as | Tenant scope |
|---|---|
| Admin API key | All tenants (unrestricted) |
| Viewer / inference API key | Key's own `tenant` field only |
| Viewer API key with empty `tenant` | All tenants (global viewer) |
| OIDC viewer with `tid`/`tenant_id` claim | Claim's tenant only |
| OIDC admin | All tenants (unrestricted) |
| Unauthenticated | All tenants (handler decides if auth is required) |

### Affected endpoints

| Endpoint | Admin view | Non-admin viewer view |
|---|---|---|
| `GET /api/v1/apikeys` | All enabled and disabled keys | Only enabled keys in own tenant |
| `GET /api/v1/deployments` | All deployments | Only deployments whose `detail.tenant` matches |

`GET /api/v1/nodes` and `GET /api/v1/models` are infrastructure and catalog
resources shared across all tenants — they are not scoped.

### Known limitation — deployments (v0.4 TODO)

The `deployments` table stores the tenant inside the `detail` JSON blob rather
than in a dedicated `tenant_id` column. The current filter therefore reads all
deployments into memory and discards those that do not match. A proper
`tenant_id` column with a SQL `WHERE` clause is planned for v0.4.

---

## OIDC-sourced roles

When OIDC authentication is enabled and `PURSER_OIDC_GROUP_MAPPINGS` is
configured, roles can be derived automatically from the token's `groups` or
`roles` claims — no API key is needed for human operators.

### How the resolution works

1. The OIDC token is verified as normal.
2. Purser extracts the `groups` **and** `roles` arrays from the ID token claims.
3. Each value is looked up in the `PURSER_OIDC_GROUP_MAPPINGS` JSON dictionary.
4. If one or more matches are found, the **highest-privilege** mapping wins:
   `admin > inference > viewer`.
5. The resolved role is injected into the request context and enforced by the
   same RBAC rules as API key roles (see the table above).
6. If no group/role claim matches any mapping, the request falls through to
   API-key RBAC. If no API key is presented either, the request is anonymous
   and each handler decides whether to accept or reject it.

### Example

```bash
# Map EntraID app roles to Purser roles
PURSER_OIDC_GROUP_MAPPINGS='{"Purser.Admin":"admin","Purser.Viewer":"viewer"}'
```

An operator whose token carries `"roles": ["Purser.Admin"]` gets `admin`
access to all control-plane endpoints — without creating an API key.

A read-only user whose token carries `"groups": ["purser-viewers"]` (after
mapping to `viewer`) can call any `GET /api/v1/*` endpoint but is blocked on
`POST`, `PUT`, `PATCH`, and `DELETE` with `403 Forbidden`.

See [OIDC Group Claim Mapping](oidc.md#group-claim-mapping) for full IdP
examples and tenant-scoping details.

---

## Backward Compatibility

Keys created before RBAC was introduced have `role = "admin"` in the database
(the column was added with `DEFAULT 'admin'`). No existing key loses access.

---

## Permission-based RBAC (v0.4)

Starting in v0.4, every API endpoint is mapped to a **minimum required
permission string**. The middleware resolves the caller's effective permissions
and rejects requests that lack the required string with `403 Forbidden` and a
body that includes the `"required"` field for diagnostics.

### How the middleware resolves permissions

The priority chain for each request is:

1. **OIDC session (no API key)** — the OIDC group-claim mapping is used as
   before (see [OIDC Group Claim Mapping](oidc.md#group-claim-mapping)).
2. **API key with team context** — when the key's `tenant` field matches a
   registered team, `GetEffectivePermissions` is called to resolve the full
   permission set from the team-member → custom-role chain.
3. **Legacy API key** — keys with a `role` of `admin`, `viewer`, or `inference`
   fall back to `FromLegacyRole()` for full backward compatibility.

Platform admins (`IsPlatformAdmin = true`) bypass all permission checks
unconditionally.

### Route → permission map

The table below lists every endpoint covered by the v0.4 permission engine.
Endpoints **not** in this table fall through to the legacy role switch (safe
degradation for any clients or keys not yet migrated).

| HTTP Method | Path | Required permission |
|---|---|---|
| `GET`    | `/api/v1/nodes`                                  | `team:metrics:view`     |
| `POST`   | `/api/v1/nodes/{id}/drain`                       | `team:models:deploy`    |
| `DELETE` | `/api/v1/nodes/{id}`                             | `org:pools:request`     |
| `GET`    | `/api/v1/models`                                 | `team:metrics:view`     |
| `POST`   | `/api/v1/models`                                 | `team:models:deploy`    |
| `DELETE` | `/api/v1/models/{id}`                            | `team:models:undeploy`  |
| `POST`   | `/api/v1/models/{id}/deploy`                     | `team:models:deploy`    |
| `GET`    | `/api/v1/deployments`                            | `team:metrics:view`     |
| `DELETE` | `/api/v1/deployments/{id}`                       | `team:models:undeploy`  |
| `GET`    | `/api/v1/apikeys`                                | `team:metrics:view`     |
| `POST`   | `/api/v1/apikeys`                                | `team:keys:create`      |
| `DELETE` | `/api/v1/apikeys/{id}`                           | `team:keys:revoke`      |
| `POST`   | `/api/v1/apikeys/{id}/rotate`                    | `team:keys:create`      |
| `GET`    | `/api/v1/enterprise/audit-log`                   | `team:metrics:view`     |
| `GET`    | `/api/v1/inference-audit`                        | `team:metrics:view`     |
| `GET`    | `/api/v1/approvals`                              | `team:approvals:view`   |
| `POST`   | `/api/v1/approvals/{deploymentId}/approve`       | `team:approvals:review` |
| `POST`   | `/api/v1/approvals/{deploymentId}/reject`        | `team:approvals:review` |
| `GET`    | `/api/v1/policies`                               | `team:metrics:view`     |
| `PUT`    | `/api/v1/policies/{name}`                        | `org:roles:create`      |
| `DELETE` | `/api/v1/policies/{name}`                        | `org:roles:delete`      |
| `POST`   | `/api/v1/config/apply`                           | `team:models:deploy`    |
| `POST`   | `/api/v1/config/diff`                            | `team:metrics:view`     |
| `POST`   | `/api/v1/platform/orgs`                          | `platform:orgs:create`  |
| `DELETE` | `/api/v1/platform/orgs/{id}`                     | `platform:orgs:delete`  |
| `POST`   | `/api/v1/platform/pools`                         | `platform:pools:manage` |

### Legacy role → effective permissions

For API keys that have not been migrated to team membership, the legacy role
is converted to a permission set automatically:

| Legacy role | Effective permission strings |
|---|---|
| `admin`     | All permissions (platform admin bypass) |
| `viewer`    | `team:members:view`, `team:metrics:view`, `team:approvals:view` |
| `inference` | `inference:call` |

These mappings mean existing `admin` keys remain fully operational, `viewer`
keys can still access all read endpoints that require `team:metrics:view`, and
`inference` keys are denied access to every management endpoint (as before).

### Migrating keys to custom roles

To grant fine-grained access (e.g. a developer who can deploy but not manage
billing), create a custom role and a team membership:

```bash
# Create a custom role for developers
curl -X POST /api/v1/platform/orgs/{orgId}/roles \
  -d '{"name":"developer","permissions":["team:models:deploy","team:models:undeploy","team:metrics:view","team:keys:create","inference:call"]}'

# Add the API key's ID as a team member with the role
curl -X POST /api/v1/platform/teams/{teamId}/members \
  -d '{"user_sub":"<key-id>","role_id":"<role-id>"}'
```

Once the team membership is in place the key's `tenant` field is used to
resolve the custom role on every request — no key rotation required.
