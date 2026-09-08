# Observability

Purser exposes three complementary observability surfaces:

1. **Gateway `/metrics`** — Prometheus scrape of inference request metrics from each gateway instance.
2. **Agent `/metrics`** — Prometheus scrape of per-node hardware and engine metrics from each agent.
3. **Control Plane `/metrics`** — Prometheus scrape of cluster health, node status, and deployment counts from the CP.
4. **OpenTelemetry traces** — Distributed traces for every inference request, enriched with the [GenAI semantic conventions](https://opentelemetry.io/docs/specs/semconv/gen-ai/).

All Prometheus endpoints are unauthenticated. Restrict access using network policy or a firewall — do not expose them to the public internet.

---

## Gateway metrics

The gateway serves `GET /metrics` on its main HTTP port (default `8080`). All request metrics carry `model` and `tenant` labels.

### Prometheus scrape configuration

```yaml
scrape_configs:
  - job_name: purser-gateway
    static_configs:
      - targets:
          - gateway.internal:8080
    metrics_path: /metrics
    scrape_interval: 15s
```

### Core request metrics

| Metric | Type | Description |
|---|---|---|
| `purser_gateway_requests_total{model,tenant,status}` | Counter | Total inference requests broken down by HTTP status. |
| `purser_gateway_request_duration_seconds{model,tenant}` | Histogram | End-to-end request latency. |
| `purser_gateway_tokens_input_total{model,tenant}` | Counter | Prompt tokens consumed. |
| `purser_gateway_tokens_output_total{model,tenant}` | Counter | Completion tokens generated. |
| `purser_gateway_tokens_per_second{model,tenant}` | Histogram | Token generation throughput. |

### LLM-specific metrics

| Metric | Type | Description |
|---|---|---|
| `purser_gateway_time_to_first_token_seconds{model,tenant}` | Histogram | TTFT — time from request dispatch to first SSE token. Use p50/p99 as primary SLO indicators. |
| `purser_gateway_inter_token_latency_seconds{model,tenant}` | Histogram | TBT — wall-clock time between successive SSE chunks. |
| `purser_gateway_active_streams{model,tenant}` | Gauge | Concurrent SSE streaming connections. |
| `purser_gateway_errors_total{model,tenant,error_type}` | Counter | Typed error count. `error_type` ∈ `timeout_upstream`, `node_unavailable`, `auth_failure`, `quota_exceeded`, `bad_request`, `rate_limited`. |
| `purser_gateway_queue_wait_seconds{model}` | Histogram | Time spent waiting for a per-model semaphore slot. |
| `purser_gateway_model_queue_depth{model}` | Gauge | In-flight requests holding a per-model semaphore permit. |

### Example Grafana queries (gateway)

```promql
# p99 TTFT over the last 5 minutes
histogram_quantile(0.99,
  rate(purser_gateway_time_to_first_token_seconds_bucket[5m])
)

# p99 inter-token latency (TBT)
histogram_quantile(0.99,
  rate(purser_gateway_inter_token_latency_seconds_bucket[5m])
)

# 5xx error rate
rate(purser_gateway_errors_total{error_type!="auth_failure"}[5m])

# Output tokens per second (fleet throughput)
sum(rate(purser_gateway_tokens_output_total[5m]))
```

---

## Agent metrics

Each agent exposes `GET /metrics` on port `9091` (configurable via `PURSER_AGENT_METRICS_PORT`). Metrics are updated every `PURSER_HEALTH_INTERVAL_SECS` seconds (default 5 s).

### Prometheus scrape configuration

```yaml
scrape_configs:
  - job_name: purser-agents
    static_configs:
      - targets:
          - agent-host-1:9091
          - agent-host-2:9091
    metrics_path: /metrics
```

### Agent metrics reference

| Metric | Type | Description |
|---|---|---|
| `purser_node_decode_tokens_per_second{node_id}` | Gauge | Decode (auto-regressive) throughput in tokens/s. |
| `purser_node_prefill_tokens_per_second{node_id}` | Gauge | Prefill (prompt-processing) throughput in tokens/s. |
| `purser_node_vram_used_gb{node_id}` | Gauge | VRAM currently consumed by the engine in GiB. |
| `purser_node_queue_depth{node_id}` | Gauge | Inference requests in the engine queue. |
| `purser_node_inference_port_alive{node_id}` | Gauge | `1.0` = engine serving; `0.0` = engine down. |
| `purser_node_kv_cache_usage_ratio{node_id}` | Gauge | KV-cache occupancy (0–1). **Stub `0.0`** until NVML wired. |
| `purser_node_gpu_utilization{node_id}` | Gauge | GPU SM utilization (0–1). **Stub `0.0`** until NVML wired. |

!!! note "GPU stubs"
    `purser_node_gpu_utilization` and `purser_node_kv_cache_usage_ratio` always report `0.0` until
    NVML integration on real GPU hardware is implemented. Do not alert on these gauges yet.

---

## Control Plane metrics

The CP exposes `GET /metrics` on its HTTP port (default `8080`). No authentication required.

### Prometheus scrape configuration

```yaml
scrape_configs:
  - job_name: purser-control-plane
    static_configs:
      - targets:
          - cp.internal:8080
    metrics_path: /metrics
    scrape_interval: 15s
```

### CP metrics reference

| Metric | Type | Labels | Description |
|---|---|---|---|
| `purser_cp_info` | Gauge | `version` | Always 1. Confirms endpoint is live; carries build version. |
| `purser_deployments_active` | Gauge | — | Deployments in `ACTIVE` state. |
| `purser_nodes_ready` | Gauge | — | Nodes in `READY` or `RUNNING` state. |
| `purser_nodes_total` | Gauge | — | Total registered nodes. |
| `purser_node_cpu_utilization` | Gauge | `node_id` | CPU utilisation % from node heartbeat. |
| `purser_node_gpu_utilization` | Gauge | `node_id` | GPU utilisation % from node heartbeat. |
| `purser_node_mem_bandwidth_utilization` | Gauge | `node_id` | Memory-bandwidth utilisation %. |
| `purser_node_tokens_per_second` | Gauge | `node_id` | Tokens/s currently processed by the node. |
| `purser_node_inference_port_alive` | Gauge | `node_id` | `1` = inference port responding, `0` otherwise. |

### Example PromQL queries (CP)

```promql
# Fleet readiness ratio
purser_nodes_ready / purser_nodes_total

# Dead inference ports
purser_node_inference_port_alive == 0

# Aggregate fleet throughput
sum(purser_node_tokens_per_second)

# Control-plane version
purser_cp_info
```

---

## OTLP traces (OpenTelemetry)

### Enabling OTLP export

**Control plane:**
```bash
export OTEL_EXPORTER_OTLP_ENDPOINT=http://collector.internal:4318
export OTEL_SERVICE_NAME=purser-control-plane
```

**Gateway:**
```bash
export OTEL_EXPORTER_OTLP_ENDPOINT=http://collector.internal:4317  # gRPC
export OTEL_SERVICE_NAME=purser-gateway
```

When unset, OTLP export is a zero-overhead no-op.

### GenAI span attributes on inference traces

Every inference request produces a span named `purser.gateway.inference` with [OpenTelemetry GenAI semantic conventions](https://opentelemetry.io/docs/specs/semconv/gen-ai/):

| Attribute | Value | When set |
|---|---|---|
| `gen_ai.system` | `"purser"` | Always |
| `gen_ai.operation.name` | `"chat"` or `"completion"` | Always |
| `gen_ai.request.model` | model ID string | After request body parse |
| `gen_ai.usage.input_tokens` | estimated prompt token count | Before upstream call |
| `gen_ai.usage.output_tokens` | actual completion token count | After response consumed |
| `gen_ai.response.finish_reasons` | `"stop"` or `"error"` | After response consumed |

The legacy `model.id` attribute is preserved for backward compatibility.

### Querying traces in Grafana Explore (Tempo)

```
{ resource.service.name = "purser-gateway" }
  | select(gen_ai.request.model, gen_ai.usage.output_tokens, gen_ai.response.finish_reasons)
```

---

## Alerting examples

```yaml
groups:
  - name: purser-slo
    rules:
      - alert: PurserHighTTFTp99
        expr: |
          histogram_quantile(0.99,
            rate(purser_gateway_time_to_first_token_seconds_bucket[5m])
          ) > 2.0
        for: 5m
        labels: { severity: warning }

      - alert: PurserHighTBTp99
        expr: |
          histogram_quantile(0.99,
            rate(purser_gateway_inter_token_latency_seconds_bucket[5m])
          ) > 0.5
        for: 2m
        labels: { severity: warning }

      - alert: PurserNodeEngineDown
        expr: purser_node_inference_port_alive == 0
        for: 1m
        labels: { severity: critical }

      - alert: PurserHighErrorRate
        expr: |
          rate(purser_gateway_errors_total[5m]) /
          rate(purser_gateway_requests_total[5m]) > 0.05
        for: 2m
        labels: { severity: critical }
```

---

## Environment variables

| Variable | Component | Default | Description |
|---|---|---|---|
| `PURSER_AGENT_METRICS_PORT` | Agent | `9091` | Port for agent Prometheus `/metrics` endpoint. |
| `PURSER_HEALTH_INTERVAL_SECS` | Agent | `5` | Cadence (s) for updating Prometheus gauges from engine. |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | CP + Gateway | unset | OTLP collector endpoint; unset disables push. |
| `OTEL_SERVICE_NAME` | CP + Gateway | component default | Override the `service.name` span resource attribute. |
