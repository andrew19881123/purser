# OpenTelemetry (Dynatrace / Grafana / Datadog)

OpenTelemetry instrumentation is **fully implemented** in the Control Plane (Go) and the Gateway (Rust). Both components export traces and metrics to any OTLP-compatible backend the moment `OTEL_EXPORTER_OTLP_ENDPOINT` is set. When that variable is absent the built-in no-op providers stay in place — zero overhead, nothing phoned home.

---

## Environment variables

Standard OpenTelemetry SDK variables actively read at startup:

| Variable | Default | Description |
|---|---|---|
| `OTEL_EXPORTER_OTLP_ENDPOINT` | (empty — OTEL disabled) | OTLP base URL. **Setting this variable enables all telemetry export.** The Control Plane exports over OTLP/HTTP; the Gateway exports over OTLP/gRPC. |
| `OTEL_SERVICE_NAME` | `purser-control-plane` or `purser-gateway` | `service.name` resource attribute stamped on every span and metric. Defaults differ per component; set explicitly when running multiple instances or when your backend's service map groups by this attribute. |
| `OTEL_EXPORTER_OTLP_HEADERS` | (empty) | Comma-separated `key=value` pairs appended to every OTLP request. Used for API-key or token authentication with managed backends. Examples: `Authorization=Api-Token dt0c01.xxx` (Dynatrace), `Authorization=Basic <base64>` (Grafana Cloud). |
| `OTEL_TRACES_SAMPLER` | `always_on` | Trace sampler to use. See [Trace sampling](#trace-sampling) below for all accepted values. |
| `OTEL_TRACES_SAMPLER_ARG` | (empty) | Numeric argument for ratio-based samplers (float, 0.0–1.0). Required for `traceidratio` and `parentbased_traceidratio`; defaults to `1.0` when unset or unparseable. |

---

---

## Trace sampling

The Control Plane reads the standard `OTEL_TRACES_SAMPLER` variable (case-sensitive). Five values are supported:

| `OTEL_TRACES_SAMPLER` | Behaviour |
|---|---|
| `always_on` (default) | Every request is sampled. Good for development; noisy at scale. |
| `always_off` | No traces are exported. Useful to confirm zero overhead without removing the endpoint. |
| `traceidratio` | Probabilistic sampling based on the trace ID. Set `OTEL_TRACES_SAMPLER_ARG` to a float between `0.0` and `1.0` (e.g. `0.1` = 10%). Defaults to `1.0` when the arg is absent or unparseable. |
| `parentbased_traceidratio` | Like `traceidratio` but respects the sampling decision of the parent span (e.g. from an upstream service). Recommended for multi-service setups. |
| `parentbased_always_off` | Defers to the parent span's sampling decision; samples nothing that does not have a sampled parent. |

### Quick examples

```bash
# Sample 10% of traces
OTEL_TRACES_SAMPLER=traceidratio
OTEL_TRACES_SAMPLER_ARG=0.1

# Respect upstream sampling decision at 5%
OTEL_TRACES_SAMPLER=parentbased_traceidratio
OTEL_TRACES_SAMPLER_ARG=0.05

# Disable tracing entirely
OTEL_TRACES_SAMPLER=always_off
```

```yaml
# Helm values.yaml
controlPlane:
  extraEnv:
    - name: OTEL_TRACES_SAMPLER
      value: "parentbased_traceidratio"
    - name: OTEL_TRACES_SAMPLER_ARG
      value: "0.1"
```

---

## What Purser emits

### Traces

**Control Plane** — the `otelMiddleware` in `server.go` wraps every `/api/v1` handler with a server-side HTTP span. Each span carries:

- `http.method` — the HTTP verb
- `http.path` — the request path
- `http.status_code` — the response status

Tracer name: `purser.control-plane`. Exporter: OTLP/HTTP.

**Gateway** — every `/v1/chat/completions` and `/v1/completions` request runs inside a `purser.gateway.inference` span with:

- `http.route` — `/v1/chat/completions` or `/v1/completions`
- `model.id` — the requested model identifier (filled in once the request body is parsed)

Tracer name: `purser.gateway`. Exporter: OTLP/gRPC.

### Metrics

The Control Plane pushes metrics every 30 seconds via OTLP/HTTP.

#### Infrastructure gauges (Meter: `purser.control-plane`)

| Metric name | Unit | Description |
|---|---|---|
| `purser.deployments.active` | `{deployment}` | Number of deployments in `ACTIVE` state. |
| `purser.nodes.ready` | `{node}` | Number of nodes in `READY` or `RUNNING` state. |
| `purser.nodes.total` | `{node}` | Total number of registered nodes. |

#### Per-node hardware metrics (Meter: `purser.control-plane`)

These metrics are emitted once per node that has sent at least one heartbeat. Each data point carries a `node_id` attribute. Nodes that have not yet reported are omitted (no zero-fill) so graphs show only live nodes.

| Metric name | Type | Unit | Description |
|---|---|---|---|
| `purser.node.cpu_utilization` | Float64Gauge | `%` | CPU utilisation percentage as reported by the node agent (0–100). |
| `purser.node.gpu_utilization` | Float64Gauge | `%` | GPU utilisation percentage (0–100; 0 when no GPU is present). |
| `purser.node.mem_bandwidth_utilization` | Float64Gauge | `%` | Memory-bandwidth utilisation as a fraction of peak measured bandwidth (0–100). |
| `purser.node.tokens_per_second` | Float64Gauge | `{token}/s` | Current token throughput estimate; 0 when the node is not serving. |
| `purser.node.inference_port_alive` | Int64Gauge | `{bool}` | `1` if the node's inference HTTP port is responding, `0` otherwise. |

These values come from the `NodeMetrics` extension of the agent heartbeat introduced in v0.3. Agents running an older version will leave these gauges at 0. Only the `NodeMetricsGetter` path (wired when the fleet registration server is live) populates these gauges.

#### Reconciler metrics (Meter: `purser.reconciler`)

The self-healing reconciler loop emits counters, gauges, and a histogram to help operators understand control-plane activity and approval backlogs.

| Metric name | Type | Unit | Description |
|---|---|---|---|
| `purser.reconciler.events_detected` | Int64Counter | `{event}` | Reconciler events dispatched (past hysteresis threshold), labelled by `type` (`engine_down`, `node_down`, `new_node`, `orphan_deployment`). |
| `purser.reconciler.events_acted` | Int64Counter | `{event}` | Events where the reconciler actually took a corrective action, labelled by `type`. |
| `purser.reconciler.events_pending_approval` | Int64Gauge | `{event}` | Events currently waiting for operator approval, labelled by `type`. A non-zero value means the operator needs to review and approve a proposed action. |
| `purser.reconciler.loop_duration_ms` | Float64Histogram | `ms` | Wall-clock duration of each `Reconcile()` pass. Use P95/P99 to detect registry contention or slow reconcile loops. |

### Audit log bridge

Every committed audit event is emitted as a span event named `purser.audit` on the active HTTP request span. This correlates audit records with the originating request in your trace backend — no separate log pipeline required.

Each `purser.audit` span event carries these attributes:

| Attribute | Description |
|---|---|
| `audit.seq` | Monotonic sequence number (tamper-evident chain position). |
| `audit.actor` | Who performed the action (e.g. `api`, an OIDC subject). |
| `audit.action` | Action name (e.g. `model.created`, `join_token.minted`). |
| `audit.target` | Resource affected (e.g. a model ID, node ID, cluster ID). |

When `OTEL_EXPORTER_OTLP_ENDPOINT` is unset, `span.IsRecording()` returns `false` and the span event call is a complete no-op.

---

## Configuration examples

### Dynatrace

1. In Dynatrace, go to **Settings → Integration → Dynatrace API** and create a token with `metrics.ingest` and `traces.ingest` scopes.
2. Note your environment ID (e.g. `abc12345`).

```yaml
# Helm values.yaml
controlPlane:
  extraEnv:
    - name: OTEL_EXPORTER_OTLP_ENDPOINT
      value: "https://abc12345.live.dynatrace.com/api/v2/otlp"
    - name: OTEL_EXPORTER_OTLP_HEADERS
      valueFrom:
        secretKeyRef:
          name: purser-otel
          key: dt-headers
    - name: OTEL_SERVICE_NAME
      value: "purser-control-plane"

gateway:
  extraEnv:
    - name: OTEL_EXPORTER_OTLP_ENDPOINT
      value: "https://abc12345.live.dynatrace.com/api/v2/otlp"
    - name: OTEL_EXPORTER_OTLP_HEADERS
      valueFrom:
        secretKeyRef:
          name: purser-otel
          key: dt-headers
    - name: OTEL_SERVICE_NAME
      value: "purser-gateway"
```

```bash
kubectl create secret generic purser-otel \
  --from-literal=dt-headers="Authorization=Api-Token dt0c01.XXXXXXXX"
```

### Grafana Tempo + Prometheus

1. In Grafana Cloud, go to **Connections → Add new connection → OpenTelemetry (OTLP)**.
2. Note the OTLP endpoint and generate an API key.

```yaml
# Helm values.yaml
controlPlane:
  extraEnv:
    - name: OTEL_EXPORTER_OTLP_ENDPOINT
      value: "https://otlp-gateway-prod-eu-west-0.grafana.net/otlp"
    - name: OTEL_EXPORTER_OTLP_HEADERS
      valueFrom:
        secretKeyRef:
          name: purser-otel
          key: grafana-headers
    - name: OTEL_SERVICE_NAME
      value: "purser-control-plane"

gateway:
  extraEnv:
    - name: OTEL_EXPORTER_OTLP_ENDPOINT
      value: "https://otlp-gateway-prod-eu-west-0.grafana.net/otlp"
    - name: OTEL_EXPORTER_OTLP_HEADERS
      valueFrom:
        secretKeyRef:
          name: purser-otel
          key: grafana-headers
    - name: OTEL_SERVICE_NAME
      value: "purser-gateway"
```

```bash
# Grafana Cloud uses HTTP Basic auth: instance-id:api-key encoded as base64
kubectl create secret generic purser-otel \
  --from-literal=grafana-headers="Authorization=Basic $(echo -n '<instance-id>:<api-key>' | base64)"
```

### Datadog

Point Purser at the Datadog Agent's OTLP intake (enabled by default in Agent v6.32+/v7.32+):

```yaml
# Helm values.yaml
controlPlane:
  extraEnv:
    - name: OTEL_EXPORTER_OTLP_ENDPOINT
      value: "http://datadog-agent.monitoring:4318"
    - name: OTEL_SERVICE_NAME
      value: "purser-control-plane"

gateway:
  extraEnv:
    - name: OTEL_EXPORTER_OTLP_ENDPOINT
      # Gateway uses gRPC (port 4317); control-plane uses HTTP (port 4318)
      value: "http://datadog-agent.monitoring:4317"
    - name: OTEL_SERVICE_NAME
      value: "purser-gateway"
```

No `OTEL_EXPORTER_OTLP_HEADERS` needed when the Datadog Agent runs in-cluster — authentication is handled by the Agent using the `DD_API_KEY` set on the Agent pod.

### Self-hosted OpenTelemetry Collector

Deploy the OTel Collector in your cluster and point Purser at it:

```yaml
# otel-collector-config.yaml (relevant sections)
receivers:
  otlp:
    protocols:
      http:           # control-plane → port 4318
        endpoint: "0.0.0.0:4318"
      grpc:           # gateway → port 4317
        endpoint: "0.0.0.0:4317"

exporters:
  prometheus:
    endpoint: "0.0.0.0:8889"
  otlp/tempo:
    endpoint: "tempo:4317"
    tls:
      insecure: true

service:
  pipelines:
    traces:
      receivers: [otlp]
      exporters: [otlp/tempo]
    metrics:
      receivers: [otlp]
      exporters: [prometheus]
```

```yaml
# Helm values.yaml
controlPlane:
  extraEnv:
    - name: OTEL_EXPORTER_OTLP_ENDPOINT
      value: "http://otel-collector:4318"
    - name: OTEL_SERVICE_NAME
      value: "purser-control-plane"

gateway:
  extraEnv:
    - name: OTEL_EXPORTER_OTLP_ENDPOINT
      value: "http://otel-collector:4317"
    - name: OTEL_SERVICE_NAME
      value: "purser-gateway"
```

---

## Audit log → SIEM export

When the Enterprise audit log is enabled (`PURSER_LICENSE_KEY` with `"audit"` feature), audit events flow into your trace backend as `purser.audit` span events on the originating HTTP request span. This means every model creation, node drain, join-token mint, and API key operation is correlated with the actor, the request trace ID, and the tamper-evident chain sequence number — without a separate log pipeline.

To forward audit events on to Splunk or Elastic, route the OTel Collector's trace pipeline through a log exporter that indexes span events:

```yaml
# otel-collector-config.yaml
exporters:
  elasticsearch:
    endpoints: ["https://elastic.example.com:9200"]
    index: "purser-audit"
  splunk_hec:
    endpoint: "https://splunk.example.com:8088/services/collector/event"
    token: "<HEC-token>"
```

See [Enterprise: Audit Log](../enterprise/audit-log.md) for the audit entry format and chain-verification API.

---

## Sample Grafana dashboard queries

When metrics are flowing, a useful dashboard includes:

- **Active deployments** — `purser_deployments_active` gauge
- **Ready nodes** — `purser_nodes_ready` gauge
- **Total nodes** — `purser_nodes_total` gauge
- **Per-node CPU utilisation** — `purser_node_cpu_utilization{node_id="…"}` (0–100 %)
- **Per-node GPU utilisation** — `purser_node_gpu_utilization{node_id="…"}` (0–100 %)
- **Token throughput per node** — `purser_node_tokens_per_second{node_id="…"}`
- **Inference port alive** — `purser_node_inference_port_alive{node_id="…"}` (1 = up, 0 = down)
- **Reconciler event rate** — `rate(purser_reconciler_events_detected_total[5m])` by `type`
- **Reconciler approval backlog** — `purser_reconciler_events_pending_approval` by `type`
- **Reconciler loop P95 latency** — P95 of `purser_reconciler_loop_duration_ms` histogram
- **Control-plane request latency** — P50/P95 from the `purser.control-plane` trace span durations
- **Inference latency** — P50/P95/P99 from `purser.gateway.inference` span durations grouped by `model.id`
