# Audit Log Dashboard

Purser provides two complementary audit views in the operator dashboard, accessible from the sidebar under **Audit Log**:

| Tab | Description |
|---|---|
| **Inference Audit** | Tamper-evident per-request log of every inference call — model, tenant, token counts, latency, and cryptographic hash chain. |
| **Access Log** | Gateway HTTP access log — endpoint, method, IP prefix, user agent, and status code. |

Both tabs are always visible. The **Chain Integrity Panel** sits above the tabs and is persistent — it reports the cryptographic health of the inference hash chain at a glance.

---

## Chain Integrity Panel

This panel is Purser's unique differentiator. No other AI inference platform provides cryptographic hash-chain verification directly in the operator dashboard.

```
┌─ Chain Integrity ─────────────────────────────────────────────────────────┐
│  [Chain verified ✓]  Block count: 1,420  Last verified: Sep 8 2026 14:00  │
│                                                               [Verify Now] │
└───────────────────────────────────────────────────────────────────────────┘
```

### What it shows

| Field | Description |
|---|---|
| **Chain verified** (green badge) | All hash links are intact — no tampering detected. |
| **Chain broken at seq N** (orange badge) | A hash mismatch, broken link, or sequence gap was found at entry `N`. Compliance alert: investigate immediately. |
| **Block count** | Number of chained rows in the log. |
| **Last verified** | Timestamp of the last successful verification run. |

### Verify Now button

Click **Verify Now** to trigger a fresh `GET /api/v1/inference-audit/verify` call. The badge and metadata update immediately with the latest result.

The page calls the verify endpoint automatically on mount. Auto-refresh on window focus is disabled to prevent unnecessary load on long-running deployments with large chains.

---

## Inference Audit Tab

The default tab shows the per-request inference log, paginated and filterable.

### Columns

| Column | Description |
|---|---|
| **#** | Sequential entry number (`seq`). |
| **Model** | Model ID that served the request. |
| **Tenant** | Tenant identifier from the API key record. |
| **API Key** | API key ID that issued the request. |
| **Latency** | End-to-end latency in milliseconds. |
| **Tokens (in/out)** | Input / output token counts. |
| **Status** | `ok` (green) or `error` (red). |
| **Time** | Wall-clock timestamp in the browser's local timezone. |

### Filters

| Filter | Behaviour |
|---|---|
| **Model** | Dropdown populated with model IDs from the current page. |
| **Tenant** | Dropdown populated with tenants from the current page. |
| **Since / Until** | Date range inputs — narrow results to a specific window. |

All filter changes reset the page offset to 0.

### Pagination

**Prev** / **Next** buttons advance through the log 50 events at a time. The current position (`from–to / total`) is shown between the two buttons.

### CSV Export

Click **Export CSV** to download the current page's events as a comma-separated file. The CSV contains: `seq`, `model_id`, `tenant`, `api_key_id`, `input_tokens`, `output_tokens`, `latency_ms`, `status`, `created_at`.

The export is client-side (no server round-trip). To export the full log, use the API directly with a large `limit`.

---

## Access Log Tab

The Access Log tab shows the gateway's HTTP access log — useful for security review and debugging authentication failures.

### Columns

| Column | Description |
|---|---|
| **API Key** | API key ID associated with the request. |
| **Method** | HTTP method (`POST`, `GET`, etc.). |
| **Path** | Request path, e.g. `/v1/chat/completions`. |
| **IP Prefix** | CIDR `/24` prefix of the client IP — full IP is never stored. |
| **User Agent** | HTTP `User-Agent` header value. |
| **Status** | HTTP response code, colour-coded: green for 2xx, orange for 4xx, red for 5xx. |
| **Time** | Request timestamp in the browser's local timezone. |

### Filter

Enter any part of an API key ID in the filter input to narrow the results. Leave blank to show all entries.

### Refresh

Click **Refresh** to reload the access log. Entries are fetched on tab mount; they do not auto-refresh.

---

## Audit Chain — how it works

Every new inference event extends a SHA-256 hash chain stored in the `inference_audit_log` table:

```
Hash = SHA-256( rawBytes(PrevHash) || CanonicalBytes(event) )
```

Where:
- `PrevHash` for the genesis entry is `0000…0000` (64 hex zeros).
- `PrevHash` for each subsequent entry is the `Hash` of the previous entry.
- `CanonicalBytes(event)` is a deterministic, length-prefixed encoding of the covered fields (see `inference-audit.md` for the full spec).

This means changing any historical entry — its content, order, or membership — invalidates every hash that follows, making tampering immediately detectable.

### What "Chain broken at seq N" means

The verify endpoint re-walks every chained row and recomputes each hash. If it finds a discrepancy at row `N`, it returns `verified: false, broken_at_seq: N`. This indicates one of:

- Content of row `N` was modified after the fact.
- Row `N` (or a row before it) was deleted, inserted, or reordered.
- The `hash` column was overwritten with a different value.

Investigate row `N` and the entries immediately before it in the database. Correlate with your operational logs to determine whether this was a legitimate administrative action or an integrity breach.

---

## API reference

### List inference events

```
GET /api/v1/inference-audit
Authorization: Bearer <admin-or-viewer-key>
```

**Query parameters** (all optional):

| Parameter | Description |
|---|---|
| `limit` | Page size (default 50, max 1000) |
| `offset` | Number of events to skip |
| `model_id` | Filter by model |
| `tenant` | Filter by tenant |
| `since` | ISO-8601 inclusive lower bound on `created_at` |
| `until` | ISO-8601 inclusive upper bound on `created_at` |

**Response** (`200 OK`):

```json
{
  "events": [
    {
      "seq": 1,
      "model_id": "llama3-8b",
      "model_revision": "main",
      "model_quantization": "Q4_K_M",
      "tenant": "acme",
      "api_key_id": "key-abc123",
      "node_id": "node-1",
      "inference_engine": "llamacpp",
      "input_tokens": 512,
      "output_tokens": 128,
      "latency_ms": 1240,
      "status": "ok",
      "created_at": "2026-09-08T14:23:07Z",
      "hash": "abc123...",
      "prev_hash": "def456..."
    }
  ],
  "total": 1420
}
```

Returns `402 Payment Required` without an `inference_audit` enterprise feature license.

---

### Verify the hash chain

```
GET /api/v1/inference-audit/verify
Authorization: Bearer <admin-or-viewer-key>
```

**Response** (`200 OK` — even when the chain is broken):

```json
{
  "verified": true,
  "block_count": 1420,
  "last_verified_at": "2026-09-08T14:00:00Z",
  "broken_at_seq": null
}
```

| Field | Type | Description |
|---|---|---|
| `verified` | boolean | `true` when every entry is consistent. |
| `block_count` | integer | Number of chained rows examined. |
| `last_verified_at` | string | ISO-8601 timestamp of this verification run. |
| `broken_at_seq` | integer or null | `seq` of the first broken entry; `null` when `verified=true`. |

Returns `402 Payment Required` without a valid license.

---

### List access log entries

```
GET /api/v1/logs/access
Authorization: Bearer <admin-or-viewer-key>
```

**Query parameters** (all optional):

| Parameter | Description |
|---|---|
| `limit` | Page size (default 50) |
| `api_key_id` | Filter by API key ID |

**Response** (`200 OK`):

```json
{
  "entries": [
    {
      "id": 4812,
      "api_key_id": "key-abc123",
      "method": "POST",
      "path": "/v1/chat/completions",
      "ip_prefix": "10.0.1.0/24",
      "user_agent": "python-httpx/0.27.2",
      "status_code": 200,
      "request_at": "2026-09-08T14:23:07Z"
    }
  ],
  "count": 1
}
```

---

## Enabling the audit features

Both the inference audit log and access log require an enterprise license key with the `inference_audit` feature. Set `PURSER_LICENSE_KEY`:

```bash
# Environment variable
export PURSER_LICENSE_KEY=<your-enterprise-key>

# Helm
helm upgrade purser oci://ghcr.io/andrew19881123/charts/purser --version 0.5.0 \
  --set license.key="<your-enterprise-key>"
```

Verify the feature is active:

```bash
curl -s http://<control-plane>:8080/api/v1/enterprise/status | jq '.features'
# ["inference_audit", ...]
```

---

## SIEM export

To forward inference events to Splunk, Elastic, or any SIEM, poll the API and use the `seq` field as a monotonic cursor:

```bash
#!/bin/bash
LAST_SEQ=0
CP=http://cp.internal:8080

while true; do
  # Fetch 500 events at a time, skipping already-seen rows
  RESULT=$(curl -s "$CP/api/v1/inference-audit?limit=500&offset=$LAST_SEQ")
  EVENTS=$(echo "$RESULT" | jq '.events | length')
  if [ "$EVENTS" -gt 0 ]; then
    # POST to your SIEM
    echo "$RESULT" | jq '.events[]' | siem-ingest
    LAST_SEQ=$((LAST_SEQ + EVENTS))
  fi
  sleep 60
done
```

The `hash` and `prev_hash` fields are included in the API response so your SIEM can independently verify chain integrity on ingestion.

---

## Privacy

| Field | Stored value |
|---|---|
| Prompt / completion text | **Never stored** |
| Full client IP address | **Never stored** — only the `/24` CIDR prefix |
| API key secret | **Never stored** — only the key ID prefix |

Purser records only the metadata necessary for compliance (AI Act Art. 12) and security review. Operators are responsible for meeting further data-retention and deletion obligations under applicable law.
