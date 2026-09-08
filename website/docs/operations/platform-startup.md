# Control Plane Startup Sequence (v0.4)

When the Purser control plane starts, it performs a deterministic sequence of
initialisation steps before accepting API requests. Understanding this sequence
helps operators diagnose startup failures and configure health checks correctly.

---

## Startup sequence

### 1. Schema migration

The control plane calls `reg.Migrate()` immediately after opening the SQLite
registry. Migration runs every start and is idempotent — it uses
`CREATE TABLE IF NOT EXISTS` and `ALTER TABLE ... ADD COLUMN IF NOT EXISTS`
semantics, so it is safe on an already-up-to-date database.

v0.4 adds the following tables (among others):

| Table | Purpose |
|-------|---------|
| `organizations` | Top-level multi-tenant orgs |
| `teams` | Teams within an org |
| `platform_users` | Platform-level user identities |
| `org_members` / `team_members` | Org and team membership with roles |
| `custom_roles` | Per-org and system-level RBAC roles |
| `node_pools` | Named GPU node groups with access policies |
| `pool_team_quotas` | Pool allocation quotas per team |

If migration fails the process exits immediately with a non-zero status — this
is intentional so a corrupt or incompatible database does not silently accept
requests.

### 2. System role seeding

After migration, the control plane calls `reg.SeedSystemRoles()`. This inserts
the six built-in platform roles into `custom_roles` using `ON CONFLICT(id) DO
NOTHING`, so it is fully idempotent and safe to run on every restart.

The six built-in roles and their canonical permission sets are defined in
`go/controlplane/permissions/engine.go` (`permissions.SystemRoles()`) — a
single source of truth shared by both the seeding path and the runtime
permission engine.

| Role ID | Display name | Scope |
|---------|-------------|-------|
| `platform_admin` | Platform Administrator | Full platform access |
| `org_admin` | Organization Administrator | Full org access |
| `team_admin` | Team Administrator | Full team access |
| `developer` | Developer | Deploy + inference |
| `viewer` | Viewer | Read-only |
| `inference_only` | Inference Only | Inference API only |

Seeding failure is **non-fatal**: if the roles already exist (or the registry is
temporarily read-only) the control plane logs a warning and continues. Roles are
never overwritten — if you need to update a built-in role's permissions, restart
with an empty database or delete the role row and let seeding re-create it.

### 3. GitOps config watcher (optional)

When `PURSER_CONFIG` is set to a `purser.yaml` path, the control plane applies
the declared cluster config at startup and then starts a background watcher that
re-applies it every `PURSER_CONFIG_INTERVAL` seconds (default 30 s). The watcher
is described in more detail in the [purser.yaml reference](../configuration/purser-yaml.md).

---

## Platform status endpoint

```
GET /api/v1/platform/status
```

Returns a JSON summary of the current platform state. Useful for admin
dashboards and smoke-testing a fresh deployment.

**Authentication:** requires a valid admin API key or OIDC admin token.

**Response (200 OK):**

```json
{
  "platform_version": "v0.4",
  "organizations": 3,
  "teams": 7,
  "node_pools": 2,
  "platform_users": 12,
  "system_roles_seeded": true,
  "features": {
    "organizations": true,
    "team_pools":    true,
    "custom_roles":  true,
    "ldap_auth":     false,
    "rbac_v2":       true
  }
}
```

| Field | Description |
|-------|-------------|
| `platform_version` | Purser platform version |
| `organizations` | Total number of organisations |
| `teams` | Total number of teams across all orgs |
| `node_pools` | Total number of node pools |
| `platform_users` | Total number of platform users |
| `system_roles_seeded` | Always `true` after v0.4 startup |
| `features` | Map of enabled feature flags |

---

## Platform health endpoint (liveness probe)

```
GET /api/v1/platform/health
```

A lightweight liveness probe suitable for Kubernetes `livenessProbe` and
`readinessProbe` configuration. This endpoint does **not** require
authentication — it is in the public bypass list so probes run without
credentials.

**Healthy (200 OK):**

```json
{"status": "ok", "version": "v0.4"}
```

**Unhealthy — database unreachable (503 Service Unavailable):**

```json
{"status": "unhealthy", "error": "sql: database is closed"}
```

### Kubernetes configuration example

```yaml
livenessProbe:
  httpGet:
    path: /api/v1/platform/health
    port: 8080
  initialDelaySeconds: 5
  periodSeconds: 10
  failureThreshold: 3

readinessProbe:
  httpGet:
    path: /api/v1/platform/health
    port: 8080
  initialDelaySeconds: 3
  periodSeconds: 5
```

The probe pings the SQLite backing store on every call, so a 503 response
reliably indicates a database connectivity problem rather than a transient
Go runtime issue.

---

## Audit trail team context

Starting with v0.4, team-scoped API keys include the team identifier in every
audit log entry. The `actor` field now follows the format:

```
apikey:<8-char-sha256-fingerprint>@<team-slug>
```

For example: `apikey:abc12345@team-ml`

This makes audit entries immediately identifiable without having to look up the
key's metadata. OIDC tokens continue to use `oidc:<sub>` or `oidc:<email>`
format.
