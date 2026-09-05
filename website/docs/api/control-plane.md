# Purser Control Plane API

The Purser control plane exposes a REST management API under `/api/v1`.
It is intentionally separate from the inference data path (see §10 of the
architecture doc, "superficie minima") and uses only the Go standard library
router — no third-party HTTP framework — to keep the dependency footprint
minimal for air-gapped builds.

## Machine-readable spec

The full machine-readable OpenAPI 3.0 specification is available at:

```
GET /api/v1/openapi.json
```

The spec is embedded in the server binary at compile time from
`go/controlplane/server/openapi.json` (generated from `openapi.yaml`).
It can be imported directly into any OpenAPI-compatible tool:

- **Postman**: File → Import → Link, paste `http://<host>/api/v1/openapi.json`
- **Insomnia**: Application → Import/Export → Import Data → From URL
- **Swagger UI / Redoc**: Point the UI at `http://<host>/api/v1/openapi.json`
- **openapi-generator / oapi-codegen**: pass the URL or local path as input

## Base URL

```
http://<control-plane-host>:<port>/api/v1
```

Default port: `8080` (configured via `PURSER_ADDR`).

## Authentication

Authentication is **optional** and controlled by the `PURSER_OIDC_ISSUER`
environment variable. When set, every request must carry a valid OIDC-derived
Bearer token in the `Authorization` header:

```
Authorization: Bearer <token>
```

When `PURSER_OIDC_ISSUER` is not set the server accepts unauthenticated requests.

## Enterprise endpoints

Endpoints under `/api/v1/enterprise/` are open-core: the code is public but the
features are gated at runtime by a valid offline-verified license key. Without
a `PURSER_LICENSE_KEY` whose payload includes the required feature they return
`402 Payment Required`.

Current enterprise features: `audit` (tamper-evident hash-chained audit log).

## Error envelope

All non-success JSON responses (except the enterprise license gate) use:

```json
{
  "error": "machine_readable_kind",
  "message": "Human-readable description."
}
```

The 402 license-gate response uses a nested `error` object:

```json
{
  "error": {
    "message": "enterprise license required",
    "feature": "audit",
    "type": "license_required"
  }
}
```

## Routes summary

| Method | Path | Description |
|--------|------|-------------|
| GET | `/api/v1/nodes` | List all enrolled nodes |
| GET | `/api/v1/nodes/{id}` | Get a node by ID |
| POST | `/api/v1/nodes/{id}/drain` | Cordon a node (mark DRAINING) |
| DELETE | `/api/v1/nodes/{id}` | Decommission a node |
| GET | `/api/v1/models` | List the model catalog |
| POST | `/api/v1/models` | Register a model |
| DELETE | `/api/v1/models/{id}` | Remove a model |
| POST | `/api/v1/models/{id}/plan` | Dry-run plan preview (no side effects) |
| POST | `/api/v1/models/{id}/deploy` | Deploy a model |
| GET | `/api/v1/plans/{id}` | Get a stored deployment plan |
| GET | `/api/v1/deployments` | List all deployments |
| DELETE | `/api/v1/deployments/{id}` | Tear down a deployment |
| GET | `/api/v1/cluster/health` | Cluster health summary |
| GET | `/api/v1/apikeys` | List gateway API keys (metadata only) |
| POST | `/api/v1/apikeys` | Mint a gateway API key |
| DELETE | `/api/v1/apikeys/{id}` | Revoke an API key |
| POST | `/api/v1/join-token` | Mint a cluster join token |
| GET | `/api/v1/metrics` | Live metrics (Server-Sent Events) |
| GET | `/api/v1/enterprise/status` | Active edition report |
| GET | `/api/v1/enterprise/audit-log` | Tamper-evident audit log (enterprise) |
| GET | `/api/v1/openapi.json` | This OpenAPI 3.0 specification |

For full request/response schemas, parameters, and error codes see the live
spec at `GET /api/v1/openapi.json`.
