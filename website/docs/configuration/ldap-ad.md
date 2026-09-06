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

## Group mapping syntax

`PURSER_LDAP_GROUP_MAPPINGS` is a comma-separated list of `LDAP-Group-Name=purser-role` pairs:

```
PURSER_LDAP_GROUP_MAPPINGS=Purser-Admins=admin,Purser-Viewers=viewer,Purser-Inference=inference
```

Purser roles:

| Role | Description |
|---|---|
| `admin` | Full control — manage nodes, models, policies |
| `viewer` | Read-only access to fleet status and metrics |
| `inference` | Submit inference requests only |

If a user belongs to multiple mapped groups, the highest-privilege role wins
(`admin` > `viewer` > `inference`). A user with no matching group mapping
authenticates successfully but has no role assigned; access depends on policy defaults.

---

## Microsoft Active Directory

A typical AD deployment uses `ldaps://` on port 636 with service account search and
recursive group membership (using the `LDAP_MATCHING_RULE_IN_CHAIN` OID).

```bash
# Connection
PURSER_LDAP_URL=ldaps://ad.example.com:636
PURSER_LDAP_TLS_CA_FILE=/etc/ssl/certs/corp-ca.pem   # if using a private CA

# Service account
PURSER_LDAP_BIND_DN=CN=purser-svc,OU=Service Accounts,DC=example,DC=com
PURSER_LDAP_BIND_PASSWORD=<service-account-password>

# User search
PURSER_LDAP_USER_BASE_DN=OU=Users,DC=example,DC=com
PURSER_LDAP_USER_FILTER=(&(objectClass=user)(sAMAccountName=%s))

# Group search (recursive membership via AD OID)
PURSER_LDAP_GROUP_BASE_DN=OU=Groups,DC=example,DC=com
PURSER_LDAP_GROUP_FILTER=(&(objectClass=group)(member:1.2.840.113556.1.4.1941:=%s))
PURSER_LDAP_GROUP_ATTRIBUTE=cn

# Role mapping
PURSER_LDAP_GROUP_MAPPINGS=Purser-Admins=admin,Purser-Viewers=viewer

# Cache
PURSER_LDAP_CACHE_TTL_SECONDS=300
```

> **Tip:** Use `sAMAccountName` as `PURSER_LDAP_GROUP_ATTRIBUTE` if your group CNs
> contain spaces or long display names — `sAMAccountName` is always a single token.

---

## OpenLDAP

For a standard OpenLDAP deployment, user objects typically use `uid` as the login
attribute rather than `sAMAccountName`, and groups use `memberUid` or `member`.

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
PURSER_LDAP_USER_FILTER=(&(objectClass=posixAccount)(uid=%s))

# Group search (groupOfNames)
PURSER_LDAP_GROUP_BASE_DN=ou=groups,dc=example,dc=com
PURSER_LDAP_GROUP_FILTER=(&(objectClass=groupOfNames)(member=%s))
PURSER_LDAP_GROUP_ATTRIBUTE=cn

# Role mapping
PURSER_LDAP_GROUP_MAPPINGS=purser-admins=admin,purser-viewers=viewer

# Cache
PURSER_LDAP_CACHE_TTL_SECONDS=300
```

---

## Cache TTL and why it matters

Every `Authenticate` call that misses the cache opens a TCP connection to the LDAP
server, performs two binds (service account + user), and one or two searches. At
scale, this adds measurable latency and load to your directory server.

The default TTL is **5 minutes** (`300` seconds). During that window, a successful
credential pair is served from an in-memory map without hitting LDAP again.

**Trade-offs:**

- A longer TTL reduces directory load but means a revoked account may continue to
  authenticate for up to TTL seconds after revocation.
- A TTL of `0` disables the cache entirely — every request hits LDAP. Use only in
  environments where immediate revocation is critical and latency is not a concern.
- For most enterprise deployments, 5–15 minutes is a reasonable balance.

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
