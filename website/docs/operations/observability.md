# Observability

Purser exposes Prometheus metrics from two components: the **gateway** and
each **agent node**. Both endpoints serve the standard Prometheus text-exposition
format and are unauthenticated — expose them only inside a trusted network
(a dedicated scrape VLAN, a service-mesh sidecar, or a Prometheus pod with
network policies applied).

---

## Gateway metrics

The gateway serves `GET /metrics` on the same port as the inference API (default
`8080`). All gateway metrics carry `model` and `tenant` labels so you can slice
dashboards by model or by customer.

### Core request metrics

| Metric | Type | Description |
|---|---|---|
| `purser_gateway_requests_total{model,tenant,status}` | Counter | Total inference requests, broken down by HTTP status. |
| `purser_gateway_request_duration_seconds{model,tenant}` | Histogram | End-to-end request latency. |
| `purser_gateway_tokens_input_total{model,tenant}` | Counter | Prompt tokens consumed. |
| `purser_gateway_tokens_output_total{model,tenant}` | Counter | Completion tokens generated. |
| `purser_gateway_tokens_per_second{model,tenant}` | Histogram | Token generation throughput. |

### LLM-specific metrics

| Metric | Type | Description |
|---|---|---|
| `purser_gateway_time_to_first_token_seconds{model,tenant}` | Histogram | Time from request dispatch to first SSE token (TTFT). Use `p50`/`p99` as primary SLO indicators. |
| `purser_gateway_inter_token_latency_seconds{model,tenant}` | Histogram | Inter-token latency (TBT) — wall-clock time between successive SSE chunks. |
| `purser_gateway_active_streams{model,tenant}` | Gauge | Number of currently active SSE streaming connections. Decremented via RAII guard — a permanently non-zero value after all requests complete indicates a resource leak. |
| `purser_gateway_errors_total{model,tenant,error_type}` | Counter | Typed error count. `error_type` ∈ `timeout_upstream`, `node_unavailable`, `auth_failure`, `quota_exceeded`, `bad_request`, `rate_limited`. |
| `purser_gateway_queue_wait_seconds{model}` | Histogram | Time a request spent waiting to acquire a per-model semaphore slot. |
| `purser_gateway_model_queue_depth{model}` | Gauge | In-flight requests currently holding a per-model semaphore permit. |

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

# Request error rate
rate(purser_gateway_errors_total[5m])

# Output tokens per second
rate(purser_gateway_tokens_output_total[5m])
```

---

## Agent metrics

Each agent daemon exposes a dedicated Prometheus endpoint at
`GET http://<agent-host>:9091/metrics` (port configurable via
`PURSER_AGENT_METRICS_PORT`).

The agent scrapes `EngineMetrics` from the supervised inference engine every
`PURSER_HEALTH_INTERVAL_SECS` seconds (default: 5 s) and updates the gauges.
All agent metrics carry a `node_id` label that matches the node's identity in the
control-plane registry.

### Prometheus scrape configuration

```yaml
scrape_configs:
  - job_name: purser-agents
    # Assumes Consul service discovery or a static list of agent hosts.
    static_configs:
      - targets:
          - agent-host-1:9091
          - agent-host-2:9091
    metrics_path: /metrics
```

### Agent metrics reference

| Metric | Type | Description |
|---|---|---|
| `purser_node_decode_tokens_per_second{node_id}` | Gauge | Decode (auto-regressive generation) throughput in tokens per second as reported by the engine. |
| `purser_node_prefill_tokens_per_second{node_id}` | Gauge | Prefill (prompt-processing) throughput in tokens per second. |
| `purser_node_vram_used_gb{node_id}` | Gauge | VRAM / GPU memory currently consumed by the engine, in GiB. |
| `purser_node_queue_depth{node_id}` | Gauge | Number of inference requests currently in the engine queue. |
| `purser_node_inference_port_alive{node_id}` | Gauge | `1.0` when the engine is in the RUNNING phase (serving requests); `0.0` otherwise. |
| `purser_node_kv_cache_usage_ratio{node_id}` | Gauge | KV-cache hit ratio (0.0–1.0). **Stub: always `0.0`** — hardware KV-cache sampling is not yet implemented. |
| `purser_node_gpu_utilization{node_id}` | Gauge | GPU SM utilization ratio (0.0–1.0). **Stub: always `0.0`** — GPU utilization requires NVML on real hardware and is not yet wired. |

!!! note "GPU utilization stub"
    `purser_node_gpu_utilization` and `purser_node_kv_cache_usage_ratio` report
    `0.0` on all current deployments. These metrics are reserved for future NVML
    integration and hardware-level KV-cache sampling. Dashboard alerts should
    **not** fire on these gauges until the stubs are replaced.

### Example Grafana queries (agent)

```promql
# Decode throughput across all nodes
purser_node_decode_tokens_per_second

# Identify nodes with a dead engine (alive = 0)
purser_node_inference_port_alive == 0

# VRAM used per node
purser_node_vram_used_gb

# Average queue depth across the fleet
avg(purser_node_queue_depth)
```

---

## Inter-token latency (TBT)

The gateway records the wall-clock gap between every pair of successive SSE
chunks as `purser_gateway_inter_token_latency_seconds`. This is the Time Between
Tokens (TBT) histogram, also known as inter-token latency.

TBT is distinct from TTFT: TTFT measures how long the user waits for the **first**
token; TBT measures how smoothly tokens **flow** after that. A high p99 TBT
(while p50 is low) typically indicates GPU memory pressure or head-of-line
blocking in the engine's batch scheduler.

### Recommended dashboard panels

| Panel | PromQL |
|---|---|
| p50 TBT | `histogram_quantile(0.50, rate(purser_gateway_inter_token_latency_seconds_bucket[5m]))` |
| p99 TBT | `histogram_quantile(0.99, rate(purser_gateway_inter_token_latency_seconds_bucket[5m]))` |
| p99 TTFT | `histogram_quantile(0.99, rate(purser_gateway_time_to_first_token_seconds_bucket[5m]))` |
| Error rate | `rate(purser_gateway_errors_total[5m])` |

### Alerting example (Prometheus rules)

```yaml
groups:
  - name: purser-slo
    rules:
      - alert: HighP99TBT
        expr: |
          histogram_quantile(0.99,
            rate(purser_gateway_inter_token_latency_seconds_bucket[5m])
          ) > 0.5
        for: 2m
        labels:
          severity: warning
        annotations:
          summary: "p99 inter-token latency > 500 ms"
          description: "Model {{ $labels.model }} — p99 TBT is {{ $value | humanizeDuration }}"

      - alert: NodeEngineDown
        expr: purser_node_inference_port_alive == 0
        for: 1m
        labels:
          severity: critical
        annotations:
          summary: "Purser agent engine is down"
          description: "Node {{ $labels.node_id }} engine is not serving (alive=0)"
```

---

## Environment variables

| Variable | Component | Default | Description |
|---|---|---|---|
| `PURSER_AGENT_METRICS_PORT` | Agent | `9091` | TCP port the agent's Prometheus `/metrics` endpoint binds to. |
| `PURSER_HEALTH_INTERVAL_SECS` | Agent | `5` | Cadence (seconds) at which the agent updates its Prometheus gauges from the engine. |
