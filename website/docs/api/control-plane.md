# Control Plane API

The Purser control-plane exposes a REST management surface at `/api/v1/`.
It is deliberately separate from the inference data path (`/v1/`).

## Authentication

Every non-public endpoint requires an API key supplied as a Bearer token:

```http
Authorization: Bearer psk_<base64>
```

Keys are minted via `POST /api/v1/apikeys` (see below). The plaintext secret
is returned **once** at creation time; only its SHA-256 hash is persisted.

### RBAC

Each key carries a **role** that limits what it may do. See
[RBAC configuration](../configuration/rbac.md) for the full role reference.
In brief:

| Role | Behaviour |
|------|-----------|
| `admin` | Full access to all management endpoints |
| `viewer` | `GET` requests only; any mutating request returns `403` |
| `inference` | Rejected on all `/api/v1/*` endpoints with `403`; for use on the gateway `/v1/` surface only |

Requests without a token, or with an unrecognised token, are passed through
to the handler; handlers enforce authentication independently where required.

### Public endpoints (no token required)

The following endpoints are always accessible regardless of the key role:

- `GET /api/v1/cluster/health`
- `GET /api/v1/openapi.json`

## Endpoints

### Nodes

| Method | Path | Description |
|--------|------|-------------|
| `GET` | `/api/v1/nodes` | List all enrolled nodes |
| `GET` | `/api/v1/nodes/{id}` | Get a single node |
| `POST` | `/api/v1/nodes/{id}/drain` | Cordon a node (marks DRAINING) |
| `DELETE` | `/api/v1/nodes/{id}` | Decommission a node |

### Models

| Method | Path | Description |
|--------|------|-------------|
| `GET` | `/api/v1/models` | List the model catalog (with fit verdicts) |
| `POST` | `/api/v1/models` | Register a model (protojson ModelSpec body) |
| `DELETE` | `/api/v1/models/{id}` | Remove a model (guards active deployments) |
| `POST` | `/api/v1/models/{id}/plan` | Dry-run plan (no deploy) |
| `POST` | `/api/v1/models/{id}/deploy` | Deploy a model |

### Deployments

| Method | Path | Description |
|--------|------|-------------|
| `GET` | `/api/v1/deployments` | List all deployments |
| `DELETE` | `/api/v1/deployments/{id}` | Tear down a deployment |

### Plans

| Method | Path | Description |
|--------|------|-------------|
| `GET` | `/api/v1/plans/{id}` | Get a stored deployment plan |

### API Keys

| Method | Path | Description |
|--------|------|-------------|
| `GET` | `/api/v1/apikeys` | List API keys (metadata only; no secrets) |
| `POST` | `/api/v1/apikeys` | Create an API key |
| `DELETE` | `/api/v1/apikeys/{id}` | Revoke an API key |

**Create key request body:**

```json
{
  "name": "my-key",
  "tenant": "team-a",
  "role": "inference",
  "quota": 100000
}
```

`role` defaults to `"admin"` if omitted. `quota` is a monthly request cap
(0 = unlimited).

**Create key response (201 Created):**

```json
{
  "id": "key-a1b2c3d4",
  "name": "my-key",
  "tenant": "team-a",
  "role": "inference",
  "key": "psk_…"
}
```

The `key` field contains the full plaintext secret. **Copy it now** — it is
never returned again.

### Cluster & Enrollment

| Method | Path | Description |
|--------|------|-------------|
| `GET` | `/api/v1/cluster/health` | Cluster health summary (public) |
| `POST` | `/api/v1/join-token` | Mint a node enrollment token |
| `GET` | `/api/v1/metrics` | Live cluster metrics (SSE stream) |

### Enterprise

| Method | Path | Description |
|--------|------|-------------|
| `GET` | `/api/v1/enterprise/status` | License edition and features |
| `GET` | `/api/v1/enterprise/audit-log` | Tamper-evident audit log (requires license) |

## Error Format

All errors are JSON with `"error"` (machine-readable code) and `"message"`:

```json
{
  "error": "forbidden",
  "message": "this API key has viewer role (read-only)"
}
```

Common error codes:

| HTTP | `error` | Meaning |
|------|---------|---------|
| 400 | `bad_request` / `invalid_role` | Malformed input |
| 403 | `forbidden` | RBAC denial |
| 404 | `not_found` | Resource does not exist |
| 409 | `model_in_use` / `node_in_use` | Guarded delete blocked |
| 402 | `license_required` | Enterprise feature, license absent |
| 500 | various | Internal error |
