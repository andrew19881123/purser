# GDPR Compliance

Purser Enterprise includes built-in support for the **GDPR Right to Erasure** (Article 17) and a tamper-evident **erasure audit trail** that satisfies GDPR Article 30 (records of processing activities).

## Prerequisites

- Enterprise license with the `gdpr` feature enabled.
- Admin API key (or OIDC admin role).

---

## Right to Erasure — POST /api/v1/gdpr/erasure

When a data subject requests deletion of their personal data under **GDPR Art.17**, operators invoke this endpoint with the SHA-256 hex hash of the subject's API key. Purser pseudonymises all matching records in `inference_audit_log` — it does **not** hard-delete rows, so the tamper-evident hash chain (AI Act Art.12 compliance) remains intact.

### What gets pseudonymised

| Column | Before | After |
|---|---|---|
| `api_key_hash` | `"a1b2c3d4…"` (64-char hex) | `"ERASED-20260906"` |
| `client_ip_prefix` | `"192.168.1.0/24"` | `"0.0.0.0/0"` |
| `tenant_id` | `"acme-corp"` | `"ERASED"` |

`model_id`, `timestamp`, token counts, latency, and `finish_reason` are **not** pseudonymised — these are aggregate metrics, not personal data.

### What is NOT erased

- The `inference_audit_log` rows themselves (hard-delete would break the hash chain).
- The `audit_log` tamper-evident log (each operation creates an entry; these are immutable by design).
- The `gdpr_erasure_log` record (see below).

### Example

```bash
# 1. Compute the SHA-256 hex hash of the API key to erase.
SUBJECT_HASH=$(echo -n "psk_the_plaintext_key" | sha256sum | awk '{print $1}')

# 2. Submit the erasure request.
curl -X POST https://purser.example.com/api/v1/gdpr/erasure \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "subject_type":       "api_key",
    "subject_identifier": "'"$SUBJECT_HASH"'",
    "reason":             "Data subject exercised GDPR Art.17 right to erasure"
  }'
```

Response:

```json
{
  "erased_events":  142,
  "erasure_type":   "inference_audit",
  "completed_at":   "2026-09-06T14:00:00Z",
  "subject_prefix": "a1b2c3d4..."
}
```

---

## Erasure Audit Trail — gdpr_erasure_log

Every successful erasure is recorded in the **immutable** `gdpr_erasure_log` table. This table is:

- **Append-only**: rows can never be updated or deleted.
- **PII-free**: only the SHA-256 hash of the subject identifier is stored, never the plaintext key.
- **Compliance-relevant**: records who performed the erasure, when, and how many events were scrubbed — satisfying GDPR Art.30 records of processing.

### List past erasures

```bash
curl https://purser.example.com/api/v1/gdpr/erasure-log \
  -H "Authorization: Bearer $ADMIN_TOKEN"
```

> **Note**: Full pagination and filtering will be added in v0.4. The endpoint currently returns an empty list placeholder.

### Schema

| Column | Type | Description |
|---|---|---|
| `id` | INTEGER | Auto-increment primary key |
| `subject_hash` | TEXT | SHA-256 hex of the erased subject identifier |
| `erased_at` | TEXT | RFC3339 UTC timestamp |
| `erased_by` | TEXT | Actor identity (`oidc:<sub>`, `apikey:<fingerprint>`, or `system`) |
| `reason` | TEXT | Free-text reason supplied by the operator |
| `events_erased` | INTEGER | Number of `inference_audit_log` rows pseudonymised |
| `erasure_type` | TEXT | Always `"inference_audit"` in v0.3 |

---

## Audit Log Actor Identity

Starting in **v0.3**, every entry in the tamper-evident `audit_log` uses a granular actor identity instead of the hardcoded placeholder `"api"`:

| Scenario | Actor format |
|---|---|
| OIDC authenticated request | `oidc:<sub-claim>` |
| OIDC authenticated (sub absent) | `oidc:<email-claim>` |
| API key authenticated | `apikey:<first-8-hex-chars-of-SHA256>` |
| Background / startup tasks | `system` |

This satisfies **SOC2 CC6.2** (individual accountability) and **GDPR Art.30** (attribution of processing operations).

---

## Litigation Hold

> **Warning**: There is currently no litigation hold mechanism that prevents GDPR erasure during active legal proceedings. If your organisation requires a hold, disable operator access to `POST /api/v1/gdpr/erasure` at the API gateway level while a hold is in effect. A native hold feature is planned for v0.4.

---

## License Requirement

All GDPR endpoints require the **`gdpr`** enterprise feature in the active license:

```json
{
  "features": ["gdpr", "audit", "inference_audit"]
}
```

Without this feature the endpoints return `402 Payment Required`. Contact your Purser account representative to enable it.
