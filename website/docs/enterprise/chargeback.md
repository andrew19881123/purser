# Chargeback — Multi-Tenant Billing Reports

Purser's chargeback feature lets platform teams see exactly how much each tenant
(team, project, or cost-centre) consumes in a given period, broken down by model.
This data powers internal billing conversations, FinOps dashboards, and capacity
planning.

---

## Requirements

| Component | Requirement |
|---|---|
| Purser version | v0.3+ |
| License feature | `billing` (Enterprise plan) |
| Consumers | Admin or viewer role (read-only) |

The **quick summary** endpoint (`/billing/summary`) is **not** enterprise-gated and
is used by the Settings-page stats bar. The **full report** (`/billing/report`) and
the **ChargebackPage** in the UI require the `billing` feature in the active license.

---

## Enabling chargeback

1. Obtain a Purser Enterprise license that includes the `billing` feature.
2. Set the `PURSER_LICENSE_KEY` environment variable (or `--license-key` flag) on
   the control plane.
3. The feature is active immediately — no restart required after the key is loaded.

Verify the feature is enabled:

```bash
curl -s http://localhost:8080/api/v1/enterprise/status | jq .features
# ["billing", ...]
```

---

## Data source

Chargeback aggregates the `inference_audit_log` table, which the gateway populates
after every completed inference request. Each row carries:

- `tenant_id` — from the API key's `tenant` field
- `model_id` — the model that served the request
- `prompt_tokens` / `completion_tokens` — token counts from the model engine
- `latency_ms` — end-to-end gateway latency
- `timestamp` — RFC3339Nano, UTC

No prompt content is ever stored (GDPR Article 5 data minimisation, AI Act Art.12).

---

## API reference

### GET /api/v1/billing/report

Returns a chargeback report for a configurable time window.

**Auth:** Bearer token with `admin` or `viewer` role.  
**Enterprise gate:** `billing` feature required (returns `402` without it).

#### Query parameters

| Parameter | Type | Default | Description |
|---|---|---|---|
| `start` | RFC3339 string | now − 30 days | Window start (inclusive) |
| `end` | RFC3339 string | now | Window end (inclusive) |
| `tenant_id` | string | _(all tenants)_ | Filter to a single tenant |
| `format` | `json` \| `csv` | `json` | Response format |

#### JSON response

```json
{
  "period_start": "2026-08-07T00:00:00Z",
  "period_end":   "2026-09-06T00:00:00Z",
  "total_requests": 45230,
  "total_tokens":   18234560,
  "tenants": [
    {
      "tenant_id":          "team-eng",
      "model_id":           "qwen3-moe",
      "request_count":      12450,
      "prompt_tokens":      5678900,
      "completion_tokens":  2345670,
      "total_tokens":       8024570,
      "avg_latency_ms":     245.3,
      "period_start":       "2026-08-07T00:00:00Z",
      "period_end":         "2026-09-06T00:00:00Z"
    },
    {
      "tenant_id":          "team-fin",
      "model_id":           "llama-3.1-8b",
      "request_count":      6780,
      "prompt_tokens":      3210000,
      "completion_tokens":  1000000,
      "total_tokens":       4210000,
      "avg_latency_ms":     189.7,
      "period_start":       "2026-08-07T00:00:00Z",
      "period_end":         "2026-09-06T00:00:00Z"
    }
  ]
}
```

Rows are ordered by `total_tokens` descending (heaviest consumers first).

#### CSV response

When `format=csv` is specified:

- `Content-Type: text/csv`
- `Content-Disposition: attachment; filename="billing-report.csv"`

```
tenant_id,model_id,request_count,prompt_tokens,completion_tokens,total_tokens,avg_latency_ms
team-eng,qwen3-moe,12450,5678900,2345670,8024570,245.3
team-fin,llama-3.1-8b,6780,3210000,1000000,4210000,189.7
```

#### Examples

```bash
# Last 30 days (default)
curl -H "Authorization: Bearer $TOKEN" \
  "http://localhost:8080/api/v1/billing/report"

# Custom window, filtered to one tenant
curl -H "Authorization: Bearer $TOKEN" \
  "http://localhost:8080/api/v1/billing/report?start=2026-08-01T00:00:00Z&end=2026-09-01T00:00:00Z&tenant_id=team-eng"

# Download CSV
curl -H "Authorization: Bearer $TOKEN" \
  "http://localhost:8080/api/v1/billing/report?format=csv" \
  -o billing-report.csv
```

---

### GET /api/v1/billing/summary

Returns a lightweight summary (totals only) for the last 30 days. **Not
enterprise-gated** — used by the Settings page stats bar and the ChargebackPage
header.

**Auth:** Bearer token with any role (admin, viewer, or inference).

#### Response

```json
{
  "period_start":   "2026-08-07T00:00:00Z",
  "period_end":     "2026-09-06T00:00:00Z",
  "total_requests": 45230,
  "total_tokens":   18234560,
  "active_tenants": 4
}
```

---

## ChargebackPage (UI)

The operator dashboard includes a dedicated **Chargeback** page in the **Use**
sidebar section. It provides:

- **Period picker** — Last 7 / 30 / 90 days
- **Summary stats** — total requests, total tokens, active tenant count
- **Usage table** — per tenant+model breakdown, ordered by total tokens
- **CSV export** — one-click download of the full report

Without a valid `billing` license the page shows an *Enterprise license required*
message instead of data.

---

## Analysing the CSV with pandas

```python
import pandas as pd

df = pd.read_csv("billing-report.csv")

# Cost estimate: assume $0.001 per 1K tokens (placeholder rate)
df["cost_usd"] = df["total_tokens"] / 1000 * 0.001

# Roll up to tenant level
summary = (
    df.groupby("tenant_id")
      .agg(total_tokens=("total_tokens", "sum"),
           total_requests=("request_count", "sum"),
           estimated_cost_usd=("cost_usd", "sum"))
      .sort_values("total_tokens", ascending=False)
)
print(summary)
```

---

## FinOps integrations

### FOCUS spec mapping

The FOCUS (FinOps Open Cost and Usage Specification) v1.0 columns map as follows:

| FOCUS column | Purser chargeback field |
|---|---|
| `BillingAccountId` | `tenant_id` |
| `ResourceId` | `model_id` |
| `UsageQuantity` | `total_tokens` |
| `UsageUnit` | `"tokens"` (constant) |
| `BillingPeriodStart` | `period_start` |
| `BillingPeriodEnd` | `period_end` |
| `ListUnitPrice` | Your internal token price |
| `BilledCost` | `total_tokens × ListUnitPrice / 1000` |

### Apptio / Cloudability

Export the CSV and import it via **Apptio Cloudability → Cost & Usage → Upload**.
Map `tenant_id` to the `Business Unit` dimension.

### CloudHealth by VMware

Use the REST import endpoint or the S3 bucket ingestion pipeline. The CSV schema
is compatible with CloudHealth's custom-metric ingest format after adding a `date`
column derived from `period_start`.

### Grafana / Prometheus

For real-time token-rate dashboards, scrape the `/api/v1/billing/summary` endpoint
at a regular interval and push the metrics to a Prometheus push-gateway, or wire
up a custom Grafana data-source plugin that calls the JSON endpoint directly.

---

## Related pages

- [Inference Audit Log](inference-audit.md) — raw per-request audit trail
- [Usage Accounting](usage-accounting.md) — per-key quota tracking
- [Enterprise Overview](overview.md) — all enterprise features
