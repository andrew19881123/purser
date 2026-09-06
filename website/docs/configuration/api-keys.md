# API Key Lifecycle

Purser uses short-lived, rotatable API keys for both the management API and the
inference gateway. This page covers how to create, rotate, and expire keys, and
how to use the access-log to spot zombie keys that haven't been used in a long
time.

## Creating a key

```bash
curl -s -X POST http://localhost:8080/api/v1/apikeys \
  -H 'Content-Type: application/json' \
  -d '{"name":"ci-runner","tenant":"eng","role":"inference","quota":50000}'
```

```json
{
  "id": "key-a1b2c3d4",
  "name": "ci-runner",
  "tenant": "eng",
  "role": "inference",
  "key": "psk_AbCd…"
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
curl -s -X POST http://localhost:8080/api/v1/apikeys \
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
curl -s -X POST http://localhost:8080/api/v1/apikeys/key-a1b2c3d4/rotate
```

```json
{
  "old_id": "key-a1b2c3d4",
  "new_id": "key-e5f6a7b8",
  "key":    "psk_XyZw…",
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
curl -s http://localhost:8080/api/v1/apikeys | jq '
  .apikeys[]
  | select(.last_used_at == null or
           (.last_used_at | fromdateiso8601) < (now - 30*86400))
  | {id, name, tenant, last_used_at}
'
```

Zombie keys are a security risk. Rotate or revoke them with:

```bash
# Revoke (permanent)
curl -X DELETE http://localhost:8080/api/v1/apikeys/key-a1b2c3d4

# Rotate (creates a new key you may choose not to distribute)
curl -X POST http://localhost:8080/api/v1/apikeys/key-a1b2c3d4/rotate
```

## Access log

The control plane records every authenticated request made with an API key.
Each entry stores the HTTP method, path, `/24` client IP prefix (GDPR
data-minimisation — the full IP is never persisted), User-Agent, and HTTP
status code.

```bash
curl -s 'http://localhost:8080/api/v1/apikeys/key-a1b2c3d4/access-log?limit=20'
```

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
  ]
}
```

The `limit` query parameter defaults to 50 and is capped at 1000.

## Summary of lifecycle API

| Endpoint | Description |
|----------|-------------|
| `POST /api/v1/apikeys` | Create a new key |
| `GET  /api/v1/apikeys` | List all keys (no plaintext, no hashes) |
| `DELETE /api/v1/apikeys/{id}` | Permanently delete a key |
| `POST /api/v1/apikeys/{id}/rotate` | Atomic rotate: new key + disable old |
| `GET  /api/v1/apikeys/{id}/access-log` | Per-key request audit trail |
| `GET  /api/v1/apikeys/{id}/usage` | Aggregate token usage for a key |
