# LDAP / Active Directory Authentication

Purser supports LDAP and Active Directory (AD) as an authentication backend via the
`ldapauth` package in the control-plane. When enabled, operators can authenticate
with their corporate AD/LDAP credentials instead of — or in addition to — OIDC or
API keys.

LDAP authentication is enabled by setting `PURSER_LDAP_URL`. If the variable is not
set, the LDAP connector is disabled and startup is unaffected.

---

## Environment variables

| Variable | Required | Default | Description |
|---|---|---|---|
| `PURSER_LDAP_URL` | yes (to enable) | — | LDAP server URL. Supports `ldap://` (plain or StartTLS) and `ldaps://` (TLS). Example: `ldaps://ad.example.com:636` |
| `PURSER_LDAP_BIND_DN` | yes | — | Distinguished name of the service account used to search the directory. Example: `cn=purser-svc,ou=service-accounts,dc=example,dc=com` |
| `PURSER_LDAP_BIND_PASSWORD` | yes | — | Password for the service account. |
| `PURSER_LDAP_USER_BASE_DN` | yes | — | Base DN for user searches. Example: `ou=users,dc=example,dc=com` |
| `PURSER_LDAP_USER_FILTER` | no | `(&(objectClass=user)(sAMAccountName=%s))` | LDAP filter template to locate a user. `%s` is replaced with the sanitized username. |
| `PURSER_LDAP_GROUP_BASE_DN` | no | — | Base DN for group searches. If empty, group lookup is skipped and all authenticated users get the empty role (login succeeds but access depends on policy). |
| `PURSER_LDAP_GROUP_FILTER` | no | `(&(objectClass=group)(member:1.2.840.113556.1.4.1941:=%s))` | LDAP filter template to find groups by member DN. `%s` is replaced with the user's full DN. The default uses the AD recursive membership OID. |
| `PURSER_LDAP_GROUP_ATTRIBUTE` | no | `cn` | Attribute used to extract the group name. Use `sAMAccountName` for AD if the CN contains spaces or display names. |
| `PURSER_LDAP_GROUP_MAPPINGS` | no | — | Comma-separated `GroupName=role` pairs. Maps LDAP group names to Purser roles (`admin`, `viewer`, `inference`). Example: `Purser-Admins=admin,Purser-Viewers=viewer` |
| `PURSER_LDAP_CACHE_TTL_SECONDS` | no | `300` (5 min) | How long to cache a successful authentication. Set to `0` to disable caching (not recommended in production). |
| `PURSER_LDAP_TLS_CA_FILE` | no | — | Path to a PEM file with additional trusted CA certificates. If empty, the system certificate pool is used. Required when using a private CA. |
| `PURSER_LDAP_STARTTLS` | no | `0` | Set to `1` to upgrade a plain `ldap://` connection to TLS using StartTLS before binding. |
| `PURSER_LDAP_INSECURE_SKIP_VERIFY` | no | `0` | Set to `1` to disable TLS certificate verification. **Never use in production.** |

---

## Group → role mapping

Purser resolves a **Purser role** for each authenticated user by matching their LDAP
group memberships against a configured mapping. The highest-privilege matching role
wins.

| Role | Description |
|---|---|
| `admin` | Full control — manage nodes, models, policies |
| `viewer` | Read-only access to fleet status and metrics |
| `inference` | Submit inference requests only |

### Option A — environment variable (flat, name-based)

`PURSER_LDAP_GROUP_MAPPINGS` is a comma-separated list of `LDAP-Group-Name=purser-role`
pairs. The group name is matched against the `PURSER_LDAP_GROUP_ATTRIBUTE` value (e.g.
the CN):

```
PURSER_LDAP_GROUP_MAPPINGS=Purser-Admins=admin,Purser-Viewers=viewer,Purser-Inference=inference
```

### Option B — purser.yaml (DN-based, recommended for production)

Add a `ldap.group_mappings` block to `purser.yaml`. The `dn` field is the full
distinguished name of the group, making the mapping unambiguous even when groups in
different OUs share the same CN:

```yaml
ldap:
  group_mappings:
    - dn: "CN=purser-admins,OU=groups,DC=acme,DC=com"
      role: admin
    - dn: "CN=ml-engineers,OU=groups,DC=acme,DC=com"
      role: inference
    - dn: "CN=viewers,OU=groups,DC=acme,DC=com"
      role: viewer
  default_role: ""          # "" = deny access if no group matches (default)
  group_cache_ttl_s: 300    # cache TTL in seconds (default 5 min)
```

When both options are configured, DN-based mappings (Option B) take precedence for any
group that has a DN match. Name-based mappings (Option A) remain active and are checked
after DN lookups.

### Default role

When a user authenticates successfully but none of their groups match any mapping, the
`ldap.default_role` controls the outcome:

| `default_role` value | Behaviour |
|---|---|
| `""` (empty, default) | Access denied — `ErrNoGroupMapping` is returned; login form shows a 403 error |
| `"viewer"` | User is granted viewer access |
| `"inference"` | User is granted inference-only access |
| `"admin"` | User is granted admin access (use with caution) |

---

## Nested group support

### Active Directory

The default `PURSER_LDAP_GROUP_FILTER` uses the AD `LDAP_MATCHING_RULE_IN_CHAIN` OID
(`:1.2.840.113556.1.4.1941:`) which resolves transitive group membership server-side.
No additional configuration is required for AD nested groups.

```bash
# AD recursive membership — already the default:
PURSER_LDAP_GROUP_FILTER='(&(objectClass=group)(member:1.2.840.113556.1.4.1941:=%s))'
```

### OpenLDAP (and other servers)

For servers that do not support the AD chain filter, enable client-side recursive
lookup with `ldap.nested_groups: true`:

```yaml
ldap:
  nested_groups: true      # Purser walks parent groups up to 5 levels deep
  group_mappings:
    - dn: "cn=all-staff,ou=groups,dc=example,dc=com"
      role: viewer
    - dn: "cn=engineering,ou=groups,dc=example,dc=com"
      role: inference
```

With `nested_groups: true`, after the initial group search Purser also searches for
groups that contain each found group as a member, recursing up to **5 levels**. This
covers nested group trees such as:

```
alice → cn=backend-team → cn=engineering → cn=all-staff
```

Nested group lookup uses `(member=<groupDN>)` searches and is subject to the same
`PURSER_LDAP_GROUP_BASE_DN` scope. Deep hierarchies may add latency; consider enabling
caching to offset this.

> **Note:** Enabling `nested_groups: true` on an AD server is harmless but redundant —
> the AD chain filter already handles transitivity server-side.

---

## Group cache TTL

Every `Authenticate` call that misses the cache opens a TCP connection to the LDAP
server, performs two binds (service account + user), and one or two searches. At
scale, this adds measurable latency and load to your directory server.

The default TTL is **5 minutes** (`300` seconds). During that window, a successful
credential pair is served from an in-memory map without hitting LDAP again.

**Configure via environment variable:**
```bash
PURSER_LDAP_CACHE_TTL_SECONDS=300
```

**Configure via purser.yaml:**
```yaml
ldap:
  group_cache_ttl_s: 300   # 0 = use env var default; -1 = disable caching
```

**Trade-offs:**

- A longer TTL reduces directory load but means a revoked account may continue to
  authenticate for up to TTL seconds after revocation.
- A TTL of `0` disables the cache entirely — every request hits LDAP. Use only in
  environments where immediate revocation is critical and latency is not a concern.
- For most enterprise deployments, 5–15 minutes is a reasonable balance.

---

## Diagnostic endpoint: POST /api/v1/ldap/test

The `POST /api/v1/ldap/test` endpoint lets operators test LDAP connectivity and group
resolution for a given user without requiring them to log in. It is useful when
debugging group mapping configuration.

**Request:**
```http
POST /api/v1/ldap/test
Content-Type: application/json
Authorization: Bearer <admin-api-key>

{"username": "alice", "password": "secret"}
```

**Successful response (200 OK):**
```json
{
  "authenticated": true,
  "user_dn":       "CN=alice,OU=users,DC=acme,DC=com",
  "groups":        ["CN=ml-engineers,OU=groups,DC=acme,DC=com", "CN=all-staff,OU=groups,DC=acme,DC=com"],
  "mapped_role":   "inference",
  "cache_hit":     false
}
```

**No group mapping (403 Forbidden):**
```json
{
  "authenticated": true,
  "denied":        true,
  "mapped_role":   "",
  "cache_hit":     false,
  "message":       "user authenticated but no group mapping grants access"
}
```

**Error responses:**

| HTTP Status | Error code | Meaning |
|---|---|---|
| 404 | `not_configured` | LDAP not configured on this server |
| 400 | `bad_request` | Missing username or password |
| 401 | `invalid_credentials` | Wrong username or password |
| 403 | `denied` body field | Credentials valid but no matching group |
| 503 | `ldap_unavailable` | LDAP server unreachable |

The `cache_hit` field indicates whether the result was served from the in-memory cache.
Use `cache_hit: false` results to verify the actual live group membership; `true`
results may reflect state from up to `group_cache_ttl_s` seconds ago.

> **Security note:** This endpoint requires an `admin` API key or equivalent OIDC
> admin role. Viewer and inference keys receive 403. In community mode (no API keys
> configured), access is unrestricted — deploy behind a firewall or enable API keys
> before exposing the management API.

---

## Microsoft Active Directory — full example

```yaml
# purser.yaml
ldap:
  group_mappings:
    - dn: "CN=Purser-Admins,OU=Security Groups,DC=corp,DC=example,DC=com"
      role: admin
    - dn: "CN=ML-Engineers,OU=Security Groups,DC=corp,DC=example,DC=com"
      role: inference
    - dn: "CN=All-Staff,OU=Security Groups,DC=corp,DC=example,DC=com"
      role: viewer
  default_role: ""        # deny if no group matches
  group_cache_ttl_s: 300  # 5 minutes
  nested_groups: false    # AD chain filter already handles nesting
```

```bash
# Connection
PURSER_LDAP_URL=ldaps://ad.corp.example.com:636
PURSER_LDAP_TLS_CA_FILE=/etc/ssl/certs/corp-ca.pem

# Service account
PURSER_LDAP_BIND_DN='CN=purser-svc,OU=Service Accounts,DC=corp,DC=example,DC=com'
PURSER_LDAP_BIND_PASSWORD=<service-account-password>

# User search
PURSER_LDAP_USER_BASE_DN='OU=Users,DC=corp,DC=example,DC=com'
PURSER_LDAP_USER_FILTER='(&(objectClass=user)(sAMAccountName=%s))'

# Group search (recursive membership via AD OID — default, handles nesting)
PURSER_LDAP_GROUP_BASE_DN='OU=Security Groups,DC=corp,DC=example,DC=com'
PURSER_LDAP_GROUP_FILTER='(&(objectClass=group)(member:1.2.840.113556.1.4.1941:=%s))'
PURSER_LDAP_GROUP_ATTRIBUTE=cn
```

> **Tip:** Use `sAMAccountName` as `PURSER_LDAP_GROUP_ATTRIBUTE` if your group CNs
> contain spaces or long display names — `sAMAccountName` is always a single token.

---

## OpenLDAP — full example

```yaml
# purser.yaml
ldap:
  group_mappings:
    - dn: "cn=purser-admins,ou=groups,dc=example,dc=com"
      role: admin
    - dn: "cn=engineers,ou=groups,dc=example,dc=com"
      role: inference
    - dn: "cn=all-staff,ou=groups,dc=example,dc=com"
      role: viewer
  default_role: "viewer"  # grant viewer to any authenticated user with no specific group
  group_cache_ttl_s: 300
  nested_groups: true     # follow parent groups up to 5 levels (memberOf overlay required)
```

```bash
# Connection (StartTLS on standard port 389)
PURSER_LDAP_URL=ldap://ldap.example.com:389
PURSER_LDAP_STARTTLS=1
PURSER_LDAP_TLS_CA_FILE=/etc/ssl/certs/corp-ca.pem

# Service account
PURSER_LDAP_BIND_DN=cn=purser-svc,ou=service-accounts,dc=example,dc=com
PURSER_LDAP_BIND_PASSWORD=<service-account-password>

# User search (POSIX accounts)
PURSER_LDAP_USER_BASE_DN=ou=people,dc=example,dc=com
PURSER_LDAP_USER_FILTER='(&(objectClass=posixAccount)(uid=%s))'

# Group search (groupOfNames — simple member filter, no AD chain OID)
PURSER_LDAP_GROUP_BASE_DN=ou=groups,dc=example,dc=com
PURSER_LDAP_GROUP_FILTER='(&(objectClass=groupOfNames)(member=%s))'
PURSER_LDAP_GROUP_ATTRIBUTE=cn
```

> **OpenLDAP nested groups:** The recursive parent-group walk (`nested_groups: true`)
> requires the `memberOf` overlay to be loaded on the OpenLDAP server, or it must be
> possible to search `(member=<groupDN>)` against the group subtree. Without it,
> only direct memberships are resolved.

---

## Login flow

When LDAP is enabled, these endpoints are registered on the control-plane management API:

| Method | Path | Description |
|--------|------|-------------|
| `GET`  | `/auth/ldap-login` | Serves a minimal HTML login form (username + password). |
| `POST` | `/auth/ldap-login` | Submits credentials; issues a session cookie on success. |
| `POST` | `/api/v1/ldap/test` | Diagnostic endpoint — tests connectivity + group resolution. Admin only. |

Both `/auth/ldap-login` endpoints are **public** — they bypass OIDC middleware and
RBAC checks because they are the authentication mechanism itself. When `PURSER_LDAP_URL`
is **not** set, `GET /auth/ldap-login` redirects to `/auth/login` (the OIDC SSO path)
and `POST /auth/ldap-login` returns `404 Not Configured`.

### How LDAP and OIDC coexist

LDAP and OIDC can be active simultaneously on the same server. Each has its own
login entry-point:

- **OIDC SSO:** `GET /auth/login` → IdP redirect → `GET /auth/callback`
- **LDAP form login:** `GET /auth/ldap-login` → POST to `/auth/ldap-login`

Once authenticated via either path, the user receives an identical **`purser_session`
cookie** (HMAC-SHA256 signed). The session middleware is completely agnostic to the
original auth method — it validates the cookie signature and checks the session row
in the `oidc_sessions` table.

### Session persistence and auth_method

Successful LDAP logins create a row in the `oidc_sessions` table with
`auth_method = 'ldap'`. The other fields behave identically to OIDC sessions:

| Field | Value |
|-------|-------|
| `sub` | User's full LDAP DN, e.g. `cn=alice,ou=users,dc=example,dc=com` |
| `email` | `mail` or `userPrincipalName` attribute from the LDAP entry |
| `idp_issuer` | `"ldap"` (constant) |
| `auth_method` | `"ldap"` |
| `expires_at` | `now + 8 hours` (same as OIDC sessions) |

### Session duration

LDAP sessions last **8 hours**, the same as OIDC sessions. This is controlled by the
`sessionTTL` constant in the server and cannot currently be configured per-method.

Sessions can be revoked explicitly via `GET /auth/logout` (clears the cookie and
marks the DB row as revoked) or via the admin force-logout API. Revocation propagates
to all cluster nodes because validation checks the distributed `oidc_sessions` table.

---

## Fallback behaviour when LDAP is unreachable

If the LDAP server is temporarily unavailable (network partition, maintenance window):

- **New authentications fail** with `ldap: server unreachable`. The caller receives an
  appropriate HTTP 503 or 401 response depending on the API endpoint.
- **Existing sessions remain valid.** Session tokens issued before the outage continue
  to work until their own expiry. LDAP unreachability does not invalidate in-flight
  tokens.
- **Cached credentials remain valid.** If a credential pair was cached before the
  outage, it continues to be served until the cache entry expires.

This means a short LDAP outage is typically transparent to users who are already
logged in, and only new login attempts fail.

---

## Security recommendations

### TLS is mandatory in production

LDAP binds transmit credentials. Always use one of:

- **`ldaps://` (recommended):** TLS from the first byte. Use port 636.
- **`ldap://` + `PURSER_LDAP_STARTTLS=1`:** Upgrades to TLS before any bind.
  Use only when `ldaps://` is not supported by the server.

Plain `ldap://` without StartTLS sends passwords in cleartext. Never use in
production.

### `PURSER_LDAP_INSECURE_SKIP_VERIFY`

This variable disables certificate validation and is intended **only** for local
development against a self-signed test directory. Setting it in any environment where
real credentials are used exposes your deployment to man-in-the-middle attacks.

For non-public CAs, use `PURSER_LDAP_TLS_CA_FILE` to add your root certificate
instead of disabling verification.

### Service account permissions

The service account (`PURSER_LDAP_BIND_DN`) needs only read access to the user and
group subtrees. Follow the principle of least privilege:

- Grant `Read` on `userPrincipalName`, `mail`, `sAMAccountName`, `memberOf`.
- Deny write permissions entirely.
- Use a dedicated account that cannot be used for interactive login.

### LDAP injection prevention

All user-supplied input (username, user DN) is sanitized with `ldap.EscapeFilter`
before being inserted into search filter strings. This prevents filter injection
attacks where a malicious username like `admin)(|(objectClass=*)` could otherwise
broaden the search scope.
