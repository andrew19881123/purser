# API Key Lifecycle

Purser uses short-lived, rotatable API keys for both the management API and the
inference gateway. This page covers how to create, rotate, and expire keys, and
how to use the access-log to spot zombie keys that haven't been used in a long
time.

## Key format (v0.5+)

Starting in v0.5, new keys use the `sk-` prefix format, matching the OpenAI and
Anthropic convention:

```
sk-a3f8bc12de456789abcdef0123456789abcdef01
```

The format is `sk-` followed by exactly 40 lowercase hexadecimal characters
(20 random bytes). The plaintext key is never stored — only its SHA-256 hash
is persisted in the database.

### Legacy `psk_` format

Keys created before v0.5 used the `psk_` prefix. These keys **continue to work
without any changes** — the control plane and gateway accept both formats. The
legacy format will be accepted through at least v0.6.

## Creating a key

### Via the Dashboard

1. Open the **Settings** page and select the **API Keys** tab.
2. Click **New Key**.
3. Enter a name, select the tenant (team), role, and optionally an expiry date.
4. Click **Create** — the plaintext key is shown once. Copy it before closing the dialog.

### Via the API

```bash
curl -s -X POST https://purser.example.com/api/v1/apikeys \
  -H 'Content-Type: application/json' \
  -d '{"name":"ci-runner","tenant":"eng","role":"inference","quota":50000}'
```

```json
{
  "id": "key-a1b2c3d4",
  "name": "ci-runner",
  "tenant": "eng",
  "role": "inference",
  "key": "sk-a3f8bc12de456789abcdef0123456789abcdef01"
}
```

> **The plaintext key is shown exactly once. Copy it before closing the response.**

The `role` field controls what the key may do:

| Role | Permissions |
|------|-------------|
| `admin` | Full control-plane access |
| `viewer` | Read-only `GET` access on `/api/v1` |
| `inference` | Gateway `/v1` endpoints only (no CP management surface) |

## Setting an expiry

To create a key that expires automatically, set `expires_at` in the request body:

```bash
curl -s -X POST https://purser.example.com/api/v1/apikeys \
  -H 'Content-Type: application/json' \
  -d '{
    "name":"short-lived",
    "tenant":"eng",
    "role":"inference",
    "expires_at":"2026-12-31T23:59:59Z"
  }'
```

Once `expires_at` has passed, `GetAPIKeyByHash` returns `ErrNotFound` — the key
is treated as non-existent for authentication purposes without requiring an
explicit revocation step. Expired keys remain in the database for audit
purposes.

> **Background watcher:** the control plane scans for keys expiring within the
> next 14 days every 6 hours and emits an `apikey.expiry_warning` audit event
> for each one. You can subscribe to audit events or query the audit log to
> build alerting on top of this.

## Rotating a key

Key rotation atomically creates a successor key and disables the predecessor in
a single transaction. Use this whenever you suspect a key has been compromised,
or as part of a regular secret-rotation schedule.

```bash
curl -s -X POST https://purser.example.com/api/v1/apikeys/key-a1b2c3d4/rotate
```

```json
{
  "old_id": "key-a1b2c3d4",
  "new_id": "key-e5f6a7b8",
  "key":    "sk-e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0c1d2e3f4",
  "role":   "inference",
  "tenant": "eng",
  "message": "Copy the key now — it is shown only once."
}
```

The new key inherits the `role`, `tenant`, `quota`, `scopes`, and `expires_at`
from the old key. The `predecessor_id` field on the new key records the ID of
the key it replaced, giving you an auditable rotation chain.

**Error cases:**

| Status | `error` code | Meaning |
|--------|-------------|---------|
| `404` | `not_found` | The key ID does not exist |
| `409` | `key_already_revoked` | The key is already disabled; rotation is a no-op |

## The `last_used_at` field and zombie keys

Purser records the last time a key was used for a successful request. The
timestamp is updated at most once per 5 minutes per key to bound write
amplification on the auth hot-path.

Use `last_used_at` to identify **zombie keys** — credentials that were issued
but have never (or rarely) been used:

```bash
# List all keys and look for ones with a null or old last_used_at
curl -s https://purser.example.com/api/v1/apikeys | jq '
  .apikeys[]
  | select(.last_used_at == null or
           (.last_used_at | fromdateiso8601) < (now - 30*86400))
  | {id, name, tenant, last_used_at}
'
```

Zombie keys are a security risk. Rotate or revoke them with:

```bash
# Revoke (permanent)
curl -X DELETE https://purser.example.com/api/v1/apikeys/key-a1b2c3d4

# Rotate (creates a new key you may choose not to distribute)
curl -X POST https://purser.example.com/api/v1/apikeys/key-a1b2c3d4/rotate
```

## `created_by` audit field

Since v0.5, every key carries a `created_by` field that captures the identity
of the actor who created it:

- OIDC users: `oidc:<subject>` or `oidc:<email>`
- API key holders: `apikey:<8-char fingerprint>[@tenant]`
- Unauthenticated bootstrap: `system`

`created_by` appears in `GET /api/v1/apikeys` responses. It is `null` / omitted
for keys created before v0.5.

## Access log

The control plane records every authenticated request made with an API key.
Each entry stores the HTTP method, path, `/24` client IP prefix (GDPR
data-minimisation — the full IP is never persisted), User-Agent, and HTTP
status code.

### Unified access-log endpoint (v0.5+)

```bash
# All keys
curl -s 'https://purser.example.com/api/v1/logs/access?limit=50'

# Filter by a specific key
curl -s 'https://purser.example.com/api/v1/logs/access?api_key_id=key-a1b2c3d4&limit=20'
```

Query parameters:

| Parameter | Default | Description |
|-----------|---------|-------------|
| `api_key_id` | (none) | Filter by key ID |
| `limit` | 100 | Max entries (max 1000) |

```json
{
  "entries": [
    {
      "id": 4812,
      "api_key_id": "key-a1b2c3d4",
      "method": "POST",
      "path": "/v1/chat/completions",
      "ip_prefix": "10.0.1.0/24",
      "user_agent": "python-httpx/0.27.2",
      "status_code": 200,
      "request_at": "2026-09-01T14:23:07Z"
    }
  ],
  "count": 1
}
```

### Legacy per-key endpoint (deprecated)

The old endpoint `GET /api/v1/apikeys/{id}/access-log` now issues a permanent
`301 Moved Permanently` redirect to `GET /api/v1/logs/access?api_key_id={id}`.
Update integrations before v0.6 ships.

## Dashboard: API Key Usage

The **Settings → API Keys** page in the operator dashboard gives a live view of
every key alongside its token consumption.

### Key table columns

| Column | What it shows |
|--------|---------------|
| **Name** | Human-readable label set at creation |
| **Team** | The tenant/team that owns the key |
| **Key** | Obfuscated prefix (full secret never displayed again) |
| **Role** | `admin` / `viewer` / `inference` — colour-coded badge |
| **Usage** | Request quota meter: `usedThisMonth / monthlyQuota` |
| **Tokens** | Aggregate token counts from the usage endpoint (lazy-loaded) |
| **Last used** | Relative time ("5 m ago", "never") |
| **Status** | `active` (green) / `revoked` (gray) |
| **Actions** | Revoke button (non-destructive confirm dialog) |

### Quota progress bar

The **Usage** column renders a colour-coded meter based on the fraction of
`usedThisMonth / monthlyQuota`:

| Colour | Threshold |
|--------|-----------|
| Green (ok) | < 70 % |
| Yellow (warning) | 70 – 90 % |
| Red (danger) | > 90 % |

Keys with `monthlyQuota = null` (unlimited) show **"Unlimited"** in place of the bar.

### Last-used indicator

The **Last used** column displays a compact relative timestamp ("3h ago",
"2d ago"). A key that has never been used shows **"never"** — these are
*zombie keys* and are a security risk. See the
[Zombie keys](#the-last_used_at-field-and-zombie-keys) section below for how
to find and clean them up programmatically.

### Token usage (lazy-loaded)

The **Tokens** column fires a per-key `GET /api/v1/apikeys/{id}/usage` request
after the key list loads. While the response is in-flight, a spinner is shown.
The final value renders as `<input_tokens> in / <output_tokens> out` using a
compact notation (e.g. `1.2K in / 567 out`).

## Summary of lifecycle API

| Endpoint | Description |
|----------|-------------|
| `POST /api/v1/apikeys` | Create a new key |
| `GET  /api/v1/apikeys` | List all keys (no plaintext, no hashes) |
| `DELETE /api/v1/apikeys/{id}` | Permanently delete a key |
| `POST /api/v1/apikeys/{id}/rotate` | Atomic rotate: new key + disable old |
| `GET  /api/v1/logs/access` | Unified access-log (all keys or filtered) |
| `GET  /api/v1/apikeys/{id}/access-log` | **Deprecated** — 301 redirect to above |
| `GET  /api/v1/apikeys/{id}/usage` | Aggregate token usage for a key |

---

## See also

- [RBAC Permissions](permissions.md) — permission strings and built-in roles that govern what a key can do
- [Platform Users & Custom Roles](../api/platform-users.md) — fine-grained role management for org members
- [Service Accounts](../configuration/service-accounts.md) — machine-to-machine authentication without user credentials
- [Chargeback reports](../enterprise/chargeback.md) — per-key token usage in billing reports
