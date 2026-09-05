# Usage Accounting

Purser records per-request token usage for every inference request that passes through the gateway. Usage data is stored in the control-plane SQLite database (`usage_log` table) and is queryable by API key or by tenant, making it the foundation for chargeback, billing, and quota auditing.

## What is tracked

For every successful inference request the gateway records:

| Field | Description |
|---|---|
| `api_key_id` | The gateway API key that authenticated the request |
| `model_id` | The model that served the request |
| `input_tokens` | Estimated input tokens (messages content, whitespace-split words ÷ 4) |
| `output_tokens` | Output tokens — SSE delta frames counted for streaming, `usage.completion_tokens` for buffered responses |
| `request_at` | RFC3339 timestamp (UTC) |

Token counts are **best-effort estimates**, not exact tokenizations. They are sufficient for chargeback and cost allocation and are consistent with the quota/rate-limiting counts the gateway already uses.

## Configuration

### Control Plane

Set `PURSER_INTERNAL_TOKEN` (or `--internal-token`) to a shared secret that the gateway must present:

```bash
PURSER_INTERNAL_TOKEN=<shared-secret> ./purser-controlplane
```

If unset, `POST /api/v1/usage` is open to any caller (suitable for single-node dev deployments).

### Gateway

Set `PURSER_CONTROL_PLANE_URL` to the control plane's base URL. If unset, usage recording is skipped (backward-compatible):

```bash
PURSER_CONTROL_PLANE_URL=http://control-plane:8080 \
PURSER_GATEWAY_INTERNAL_TOKEN=<shared-secret> \
  ./purser-gateway
```

The gateway reuses its existing `PURSER_GATEWAY_INTERNAL_TOKEN` as the `X-Purser-Internal-Token` credential when posting to the control plane.

## Endpoints

### POST /api/v1/usage

Internal endpoint called by the gateway after each inference request completes. Operators should not call this directly; the gateway does so automatically.

**Authentication:** `X-Purser-Internal-Token: <token>` (required when `PURSER_INTERNAL_TOKEN` is set on the control plane).

**Request body:**
```json
{
  "api_key_id":    "key-abc123",
  "model_id":      "llama-3-8b",
  "input_tokens":  42,
  "output_tokens": 128
}
```

**Response (200):**
```json
{ "ok": true }
```

---

### GET /api/v1/apikeys/{id}/usage

Returns aggregate token usage for a single API key. Returns `404` if the key does not exist.

```bash
curl http://control-plane:8080/api/v1/apikeys/key-abc123/usage
```

**Response (200):**
```json
{
  "api_key_id":      "key-abc123",
  "total_requests":  17,
  "input_tokens":    8432,
  "output_tokens":   21050
}
```

---

### GET /api/v1/usage/summary

Returns token usage grouped by tenant. Accepts an optional `?since=<RFC3339>` query parameter to restrict the window.

```bash
# All-time summary
curl http://control-plane:8080/api/v1/usage/summary

# Since a specific date (for monthly chargeback)
curl "http://control-plane:8080/api/v1/usage/summary?since=2026-09-01T00:00:00Z"
```

**Response (200):**
```json
{
  "tenants": [
    {
      "tenant":         "acme",
      "total_requests": 1042,
      "input_tokens":   412000,
      "output_tokens":  980000
    },
    {
      "tenant":         "beta",
      "total_requests": 88,
      "input_tokens":   32000,
      "output_tokens":  71000
    }
  ]
}
```

Tenants with no usage in the requested window are omitted. The list is sorted alphabetically by tenant name.

## Chargeback workflow

1. Create one API key per team/cost-centre (`POST /api/v1/apikeys`).
2. At billing cycle end, call `GET /api/v1/usage/summary?since=<start-of-period>` to get totals by tenant.
3. Multiply token counts by your per-token rate to compute the charge.
4. For itemised billing, call `GET /api/v1/apikeys/{id}/usage` for each key in a tenant.

```bash
# Example: get September 2026 usage for all tenants
curl "http://control-plane:8080/api/v1/usage/summary?since=2026-09-01T00:00:00Z" \
  | jq '.tenants[] | {tenant, cost: (.input_tokens * 0.000001 + .output_tokens * 0.000002)}'
```

## Schema

```sql
CREATE TABLE IF NOT EXISTS usage_log (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    api_key_id    TEXT NOT NULL,
    model_id      TEXT NOT NULL,
    input_tokens  INTEGER NOT NULL DEFAULT 0,
    output_tokens INTEGER NOT NULL DEFAULT 0,
    request_at    TEXT NOT NULL  -- RFC3339
);
```

Indices are maintained on `api_key_id` and `request_at` for efficient per-key and time-windowed queries.
