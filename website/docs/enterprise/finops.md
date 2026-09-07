# FinOps API

The FinOps API (v0.5) extends Purser's chargeback capabilities with real-time burn rate forecasting, per-model adoption analytics, and SLA compliance tracking. All endpoints require the **`billing`** enterprise feature.

---

## Burn-Rate Forecast

**`GET /api/v1/billing/forecast`**

Returns the daily burn rate and projected monthly spend for every tenant that has inference activity in the current calendar-month billing period.

### Response

```json
{
  "forecast": [
    {
      "org_id":                "acme",
      "team_id":               "eng",
      "period_start":          "2026-09-01T00:00:00Z",
      "period_end":            "2026-09-30T23:59:59.999999999Z",
      "days_elapsed":          7,
      "days_in_period":        30,
      "cost_used_usd":         142.50,
      "budget_usd":            500.00,
      "burn_rate_daily_usd":   20.36,
      "projected_monthly_usd": 610.71,
      "budget_remaining_usd":  357.50,
      "days_until_exhaustion": 17.56,
      "quota_utilization_pct": 28.50
    }
  ]
}
```

### Field reference

| Field | Type | Description |
|---|---|---|
| `org_id` | string | Org portion of the tenant ID (`<orgId>/<teamSlug>` convention). |
| `team_id` | string | Team portion; empty for standalone tenants. |
| `period_start` | RFC3339 | First second of the current billing month (UTC). |
| `period_end` | RFC3339 | Last nanosecond of the current billing month (UTC). |
| `days_elapsed` | int | Days since the start of the billing period (minimum 1). |
| `days_in_period` | int | Total days in the current calendar month (28–31). |
| `cost_used_usd` | float | Accumulated inference cost for the period based on `model_pricing`. |
| `budget_usd` | float \| null | Monthly cost budget from the tenant's quota config; omitted if not set. |
| `burn_rate_daily_usd` | float | `cost_used_usd / days_elapsed`. |
| `projected_monthly_usd` | float | `burn_rate_daily_usd × days_in_period`. |
| `budget_remaining_usd` | float \| null | `budget_usd − cost_used_usd`; omitted if no budget. |
| `days_until_exhaustion` | float \| null | `budget_remaining_usd / burn_rate_daily_usd`; omitted if no budget or burn rate is zero. |
| `quota_utilization_pct` | float \| null | `(cost_used_usd / budget_usd) × 100`; omitted if no budget. |

### Notes

- Tenants with zero inference activity in the current period are **not** included.
- When no pricing is configured for a model, cost contribution is zero (the report still returns activity metrics).
- Budget fields are only present when a `monthly_cost_budget > 0` is configured via `UpsertTenantQuota`.

### Example

```bash
curl -H "Authorization: Bearer $TOKEN" \
  https://cp.example.com/api/v1/billing/forecast
```

---

## Model Adoption Time-Series

**`GET /api/v1/billing/models/adoption`**

Returns request counts and output token volumes grouped by model, bucketed over time. Up to the top 10 models by total request count are returned.

### Query parameters

| Parameter | Default | Description |
|---|---|---|
| `window` | `daily` | Bucket granularity: `daily` or `weekly`. |
| `days` | `30` | Look-back window in days. Range: 1–90. |

### Response

```json
{
  "window": "daily",
  "days":   30,
  "series": [
    {
      "model_id": "llama3-8b",
      "buckets": [
        {"date": "2026-09-01", "requests": 1420, "tokens_out": 284000},
        {"date": "2026-09-02", "requests": 1680, "tokens_out": 336000}
      ]
    },
    {
      "model_id": "mistral-7b",
      "buckets": [
        {"date": "2026-09-01", "requests":  830, "tokens_out": 145000}
      ]
    }
  ]
}
```

### Field reference

| Field | Type | Description |
|---|---|---|
| `window` | string | `daily` or `weekly`. |
| `days` | int | The look-back window used for the query. |
| `series[].model_id` | string | Model identifier. |
| `series[].buckets[].date` | string | `YYYY-MM-DD` for daily; `YYYY-WNN` for weekly. |
| `series[].buckets[].requests` | int | Request count in this bucket. |
| `series[].buckets[].tokens_out` | int | Sum of completion tokens in this bucket. |

### Examples

```bash
# Daily breakdown, last 14 days
curl -H "Authorization: Bearer $TOKEN" \
  "https://cp.example.com/api/v1/billing/models/adoption?window=daily&days=14"

# Weekly breakdown, last 90 days
curl -H "Authorization: Bearer $TOKEN" \
  "https://cp.example.com/api/v1/billing/models/adoption?window=weekly&days=90"
```

---

## SLA Compliance in the Billing Report

**`GET /api/v1/billing/report?sla_threshold_ms=<N>`**

When the `sla_threshold_ms` query parameter is set, the standard billing report response is enriched with a `sla_stats` array containing the fraction of requests that completed within the specified latency threshold.

### Extended response

```json
{
  "period_start": "2026-08-08T00:00:00Z",
  "period_end":   "2026-09-08T00:00:00Z",
  "total_requests": 4300,
  "total_tokens": 86000,
  "tenants": [...],
  "sla_stats": [
    {
      "tenant_id":           "acme/eng",
      "sla_compliance_rate": 0.987,
      "sla_threshold_ms":    2000
    },
    {
      "tenant_id":           "acme/fin",
      "sla_compliance_rate": 0.943,
      "sla_threshold_ms":    2000
    }
  ]
}
```

### SLA stat fields

| Field | Type | Description |
|---|---|---|
| `tenant_id` | string | Tenant identifier (same as in `tenants[]`). |
| `sla_compliance_rate` | float | Fraction of requests with `latency_ms < sla_threshold_ms` (0.0–1.0). |
| `sla_threshold_ms` | float | The threshold supplied in the request. |

### Notes

- Only requests where `latency_ms > 0` contribute to the compliance calculation.  Requests recorded before latency tracking was enabled are excluded from both numerator and denominator.
- If no qualifying rows exist (all latencies zero), `sla_stats` is an empty array.
- The `sla_stats` field is **omitted entirely** from the response when `sla_threshold_ms` is not specified (backward-compatible).
- The threshold applies globally; per-org SLOs can be enforced by calling the endpoint with each org's agreed threshold and comparing against the reported rate.

### Setting per-org SLOs

Configure an SLA target per org in your monitoring pipeline by calling the endpoint with the org's agreed threshold and alerting when `sla_compliance_rate` falls below the target:

```bash
# Check SLA for org acme against a 1500ms P99 target
curl -H "Authorization: Bearer $TOKEN" \
  "https://cp.example.com/api/v1/billing/report?tenant_id=acme%2Feng&sla_threshold_ms=1500" \
  | jq '.sla_stats[0].sla_compliance_rate'
```

### Example

```bash
# Full report with SLA compliance at 2 second threshold
curl -H "Authorization: Bearer $TOKEN" \
  "https://cp.example.com/api/v1/billing/report?sla_threshold_ms=2000"
```

---

## Requirements

- Enterprise license with the **`billing`** feature.
- Model pricing must be configured via `PUT /api/v1/billing/pricing` for accurate cost figures in the forecast.
- Tenant quotas/budgets must be configured via `PUT /api/v1/billing/quotas/{tenantId}` for budget fields to appear in the forecast.
