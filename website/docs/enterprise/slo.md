# SLO Contracts

Purser v0.6 introduces **SLO (Service Level Objective) contracts** — per-model
latency commitments that are stored in `purser.yaml`, persisted to the control
plane, and continuously evaluated against live inference traffic. The compliance
status for each model is available through a dedicated REST endpoint and can be
wired to Prometheus alerting.

!!! note "No licence feature required"
    Although this page sits in the Enterprise section, SLO contracts are not
    gated by a licence feature in v0.6. `GET /api/v1/slo/compliance` answers for
    any admin or viewer role, with or without a `PURSER_LICENSE_KEY`, and no flag
    string enables or disables it. Do not request an SLO entitlement when
    ordering a key — there is none to grant. See the
    [feature gate reference](license.md#feature-gate-reference) for the flags that
    are enforced.

---

## What SLO contracts give you

| Without SLO contracts | With SLO contracts |
|---|---|
| Latency thresholds are baked into Prometheus `expr` strings and Grafana dashboards | Thresholds are version-controlled in `purser.yaml` alongside model deployments |
| Compliance requires ad-hoc queries against `billing/report?sla_threshold_ms=…` | `GET /api/v1/slo/compliance` returns a ready-made `met`/`breached`/`insufficient_data` verdict per model |
| Alerts fire for a single global threshold | Each model can have its own TTFT and target compliance |

---

## Configuring SLOs in purser.yaml

Add an `slo` block to your cluster config. The `"*"` key sets the global default
that applies to every model without an explicit entry.

```yaml
apiVersion: purser/v1
kind: ClusterConfig
metadata:
  name: prod-cluster
cluster:
  id: prod

# ... models, deployments, etc. ...

slo:
  models:
    # Global default: applies to any model not explicitly listed below.
    "*":
      ttft_ms: 2000            # time-to-first-token SLO in ms (default 2000)
      tbt_ms: 500              # inter-token latency SLO in ms  (default 500)
      target_compliance: 0.95  # minimum fraction of requests meeting SLO (default 0.95)

    # Stricter contract for a high-priority model.
    llama3-70b:
      ttft_ms: 1500
      target_compliance: 0.99

    # Relaxed contract for a large, slower model.
    qwen3-moe-235b:
      ttft_ms: 5000
      target_compliance: 0.90
```

### Applying the config

```bash
purser config apply purser.yaml
# or via the REST API:
curl -X POST https://cp.example.com/api/v1/config/apply \
     -H "Authorization: Bearer $TOKEN" \
     --data-binary @purser.yaml
```

SLO rows are stored in the `slo_configs` table inside the control-plane database
and are updated on every `config apply`. Re-applying the same config is idempotent.

### Field reference

| Field | Type | Default | Description |
|---|---|---|---|
| `ttft_ms` | integer | `2000` | Time-to-first-token SLO threshold in milliseconds. A request is "compliant" when `0 < latency_ms < ttft_ms`. |
| `tbt_ms` | integer | `500` | Inter-token latency SLO in milliseconds. Stored for future use; `tbt_compliance` is always `null` in v0.6 because per-token timing is not yet recorded in the inference audit log. |
| `target_compliance` | float | `0.95` | Minimum fraction (0.0–1.0) of requests that must meet the TTFT SLO for the model to be considered "met". |

---

## GET /api/v1/slo/compliance

Returns current TTFT compliance rates per model, computed over a rolling time
window from the inference audit log.

### Query parameters

| Parameter | Type | Default | Description |
|---|---|---|---|
| `model_id` | string | — | Filter results to a single model. Omit to see all models. |
| `window_hours` | integer | `24` | Lookback window in hours. Must be between 1 and 168. |

### Response

```json
{
  "window_hours": 24,
  "generated_at": "2026-09-08T21:00:00Z",
  "models": [
    {
      "model_id": "llama3-8b",
      "slo": {
        "ttft_ms": 2000,
        "tbt_ms": 500,
        "target_compliance": 0.95
      },
      "actual": {
        "ttft_compliance": 0.987,
        "tbt_compliance": null,
        "request_count": 1420,
        "period_start": "2026-09-07T21:00:00Z"
      },
      "status": "met"
    },
    {
      "model_id": "qwen3-moe-235b",
      "slo": {
        "ttft_ms": 5000,
        "tbt_ms": 500,
        "target_compliance": 0.90
      },
      "actual": {
        "ttft_compliance": 0.851,
        "tbt_compliance": null,
        "request_count": 312,
        "period_start": "2026-09-07T21:00:00Z"
      },
      "status": "breached"
    }
  ]
}
```

### Status values

| Status | Meaning |
|---|---|
| `met` | `actual.ttft_compliance >= slo.target_compliance` — the model is meeting its SLO. |
| `breached` | `actual.ttft_compliance < slo.target_compliance` — the model is failing its SLO. |
| `insufficient_data` | Fewer than 10 requests in the window — not enough data to draw a conclusion. `actual.ttft_compliance` is `null`. |

### Compliance formula

```
ttft_compliance = compliant_count / request_count
```

Where:

- `compliant_count` = requests where `0 < latency_ms < ttft_ms`
- `request_count` = all requests in the window (including those with `latency_ms == 0`,
  i.e. requests where the gateway did not record latency)

Requests with unrecorded latency (`latency_ms == 0`) are counted in `request_count`
but not in `compliant_count`, which **conservatively lowers** the compliance rate.

### Interpreting `insufficient_data`

A model shows `insufficient_data` when it has received fewer than 10 requests in
the selected time window. This is expected for:

- **Newly deployed models** — not enough traffic yet.
- **Rarely-used models** — consider widening the window (`window_hours=168` for
  a 7-day view).
- **Off-hours** — traffic naturally drops at night; use a longer window or
  redirect alerting to business-hours dashboards.

`insufficient_data` is not an error: it simply means there is not enough statistical
evidence to make a compliance verdict. A newly deployed model should not immediately
fire a `PurserSLOBreached` Prometheus alert.

---

## Prometheus alerting

Purser ships a `PurserSLOBreached` Prometheus alert rule in
`deploy/grafana/alerts/purser-rules.yaml`:

```yaml
- alert: PurserSLOBreached
  expr: |
    (
      sum(rate(purser_gateway_time_to_first_token_seconds_bucket{le="2.0"}[1h]))
      /
      sum(rate(purser_gateway_time_to_first_token_seconds_count[1h]))
    ) < 0.95
  for: 15m
  labels: { severity: critical }
  annotations:
    summary: "TTFT SLO compliance below 95% for 15 minutes"
```

This alert fires when the **global** rolling 1-hour TTFT compliance falls below 95%.
The `le="2.0"` bucket selector corresponds to the default `ttft_ms=2000` threshold.

### Adjusting the alert for custom thresholds

If you change the global default in `purser.yaml`:

```yaml
slo:
  models:
    "*":
      ttft_ms: 3000   # changed from 2000
```

Update the alert expression to match by changing `le="2.0"` to `le="3.0"`.

For **per-model** alerting, use the compliance API response directly — Prometheus
cannot easily query per-model thresholds stored in the control-plane database. A
recommended pattern is to expose the compliance endpoint as a custom Prometheus
exporter target and alert on the `status` field.

### Alert tuning

| Situation | Recommended change |
|---|---|
| Alert fires too often during ramp-up | Increase `for: 15m` to `for: 30m` |
| Per-model SLO alerting needed | Use `/api/v1/slo/compliance` as a Prometheus scrape target |
| Different threshold per environment | Use separate alert rules per cluster |

---

## Example: dashboard query

In Grafana you can use the compliance API as a data source via the JSON API plugin
or by querying Prometheus directly with the TTFT histogram:

```promql
# Fraction of requests within the 2 s TTFT SLO (rolling 5 min)
sum(rate(purser_gateway_time_to_first_token_seconds_bucket{le="2.0"}[5m]))
/
sum(rate(purser_gateway_time_to_first_token_seconds_count[5m]))
```

For per-model breakdown, use the control-plane compliance API and visualise the
`ttft_compliance` field from `GET /api/v1/slo/compliance`.

---

## Dashboard UI (v0.6)

The operator dashboard exposes a dedicated **SLO Contracts** page at `/slo` with a
real-time compliance view:

- **Summary row** — four KPI tiles: Total models, Met, Breached, Insufficient data.
  Gives an at-a-glance status before reading the full table.
- **Window selector** — toggle between 1 h, 6 h, 24 h, and 7 d lookback windows.
  Changing the window re-queries the API immediately.
- **Compliance table** — per model: TTFT target, actual TTFT compliance percentage,
  TBT target, target compliance threshold, status badge, and request count in window.
- **Breached badge** — red with a pulsing animation to draw attention to SLO
  violations without the operator having to scan the full table.

The page does not require an Enterprise license in v0.6; `GET /api/v1/slo/compliance`
answers for any authenticated role.
