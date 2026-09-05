# Tamper-Evident Audit Log

The Purser audit log records every administrative event with a cryptographic hash chain. Any tampering — content mutation, reordering, insertion, or deletion — is detectable by re-verifying the chain.

This is an **Enterprise feature** gated on `PURSER_LICENSE_KEY` with the `"audit"` feature entitlement. Without a valid license, `GET /api/v1/enterprise/audit-log` returns `402 Payment Required`.

---

## What gets logged

The audit log records:

| Event | `action` field | When |
|---|---|---|
| Model registered | `model.created` | `POST /api/v1/models` succeeds |
| Model deleted | `model.deleted` | `DELETE /api/v1/models/{id}` succeeds |
| API key created | `apikey.created` | `POST /api/v1/apikeys` succeeds |
| API key revoked | `apikey.deleted` | `DELETE /api/v1/apikeys/{id}` succeeds |
| Join token minted | `join_token.minted` | `POST /api/v1/join-token` succeeds |
| Node drain | `fleet.node.draining` | `POST /api/v1/nodes/{id}/drain` succeeds |
| Node decommission | `fleet.node.decommissioned` | `DELETE /api/v1/nodes/{id}` succeeds |

All events carry:
- `actor` — the API client identity (currently `"api"`)
- `action` — the verb
- `target` — the affected resource ID
- `details` — optional structured context (key→value map)

---

## How the hash chain works

Every audit entry is immutably chained with SHA-256:

```
Hash = SHA-256( rawBytes(PrevHash) || CanonicalBytes(content) )
```

Where:
- `PrevHash` for the first entry is `0000...0000` (64 hex zeros — `GenesisPrevHash`)
- `PrevHash` for each subsequent entry is the `Hash` of the previous entry
- `CanonicalBytes(content)` is a deterministic length-prefixed encoding of `Seq`, `TimeUnixNano`, `Actor`, `Action`, `Target`, and `Details` (with map keys sorted). It deliberately excludes `PrevHash` and `Hash`.
- All hashes are hex-encoded SHA-256

This means changing any historical entry — its content, order, or membership — invalidates every hash that follows.

### What the chain catches

- Content mutation (changing any field)
- Entry reordering (sequential `Seq` numbers must be contiguous)
- Deletion (gap in `Seq` sequence)
- Insertion (downstream hashes become invalid)
- Broken `PrevHash` links

### What it cannot catch alone

Rewriting the **most recent** entry and recomputing its hash produces an internally consistent chain. To close this gap, publish the head `Hash` to an external trusted anchor (append-only log, HSM, or Merkle tree) and compare it against the last entry's `Hash` on each retrieval.

---

## Entry fields

```json
{
  "seq": 42,
  "time_unix_nano": 1725494400000000000,
  "actor": "api",
  "action": "model.created",
  "target": "llama-8b",
  "details": {},
  "prev_hash": "a3f8...",
  "hash": "9c21..."
}
```

| Field | Type | Description |
|---|---|---|
| `seq` | `uint64` | 1-based position in the chain. Genesis = 1. Must be contiguous. |
| `time_unix_nano` | `int64` | Wall-clock time as Unix nanoseconds. |
| `actor` | `string` | Who performed the action. |
| `action` | `string` | Verb (e.g. `model.created`, `apikey.deleted`). |
| `target` | `string` | Affected resource ID. |
| `details` | `object` | Optional structured context. Keys sorted in canonical encoding. |
| `prev_hash` | `string` | Hex SHA-256 of the preceding entry's `hash` (or `GenesisPrevHash` for seq=1). |
| `hash` | `string` | Hex SHA-256 of `rawBytes(PrevHash) || CanonicalBytes(content)`. |

---

## Reading the audit log

```bash
GET /api/v1/enterprise/audit-log
```

Query parameters:
- `limit` — number of entries to return (default 100, override with `?limit=N`)

Returns entries in ascending `seq` order (oldest first). Requires a valid Enterprise license with the `"audit"` feature.

### Response shape

```json
{
  "feature": "audit",
  "licensee": "Acme Corp",
  "entries": [
    {
      "seq": 1,
      "time_unix_nano": 1725494400000000000,
      "actor": "api",
      "action": "join_token.minted",
      "target": "default",
      "details": null,
      "prev_hash": "0000000000000000000000000000000000000000000000000000000000000000",
      "hash": "a3f8..."
    },
    {
      "seq": 2,
      "time_unix_nano": 1725494401000000000,
      "actor": "api",
      "action": "model.created",
      "target": "llama-8b",
      "details": null,
      "prev_hash": "a3f8...",
      "hash": "9c21..."
    }
  ],
  "chain": {
    "verified": true,
    "length": 2
  }
}
```

When the chain is intact, `chain.verified` is `true`.

When tampering is detected, `chain.verified` is `false` and a `break` object identifies the first failing entry:

```json
{
  "chain": {
    "verified": false,
    "length": 5,
    "break": {
      "index": 2,
      "seq": 3,
      "kind": "hash",
      "msg": "stored hash 'abc...' does not match recomputed hash 'def...'"
    }
  }
}
```

`kind` values:
- `"seq"` — `Seq` is not the expected contiguous value (reorder, delete, or insert)
- `"link"` — `PrevHash` does not equal the previous entry's `Hash` (broken chain link)
- `"hash"` — stored `Hash` does not match the recomputed value (content tampering)

A failed verification is **never** a 500 error — it is a 200 with `verified: false`.

---

## Enabling the audit log

Set `PURSER_LICENSE_KEY` to a key with the `"audit"` feature entitlement:

```bash
# Environment variable
export PURSER_LICENSE_KEY=<your-enterprise-key>

# Helm
helm upgrade purser oci://ghcr.io/andrew19881123/charts/purser --version 0.1.1 \
  --set license.key="<your-enterprise-key>"
```

Verify the feature is active:

```bash
curl -s http://<control-plane>:8080/api/v1/enterprise/status
# "features": ["audit", ...]
```

---

## OTEL export for SIEM (Splunk / Elastic)

When OTel log export is available (see [OpenTelemetry](../configuration/otel.md)), audit events are forwarded as structured log records with the `purser.audit` instrumentation scope. Each record includes the full entry fields.

For direct SIEM integration without OTel, poll `GET /api/v1/enterprise/audit-log?limit=1000` and forward to Splunk HEC or Elastic Bulk API. The `seq` field provides a reliable monotonic cursor — store the last seen `seq` and request `?limit=N` on subsequent polls.

Example Splunk HEC forwarder (shell):

```bash
#!/bin/bash
LAST_SEQ=0
CP=http://cp.internal:8080

while true; do
  RESULT=$(curl -s "$CP/api/v1/enterprise/audit-log?limit=500")
  # Filter entries with seq > LAST_SEQ and POST to Splunk HEC
  # ... (parse with jq, POST to Splunk)
  sleep 60
done
```

---

## Integrity verification (standalone)

To verify the audit chain independently of the API (e.g. from a compliance script):

```bash
# Fetch all entries
curl -s "http://<cp>:8080/api/v1/enterprise/audit-log?limit=10000" > audit.json

# Check the chain.verified field
python3 -c "import json, sys; d=json.load(open('audit.json')); print('OK' if d['chain']['verified'] else f'TAMPERED: {d[\"chain\"][\"break\"]}')"
```

The server-side verification re-runs the full chain on every request. You can also re-implement the chain rule independently:

```
Hash = hex(SHA-256(hex_decode(PrevHash) || CanonicalBytes(content)))
```

where `CanonicalBytes` is the length-prefixed encoding described in `go/controlplane/audit/audit.go`.
