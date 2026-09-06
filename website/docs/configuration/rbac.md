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
