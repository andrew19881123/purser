# OpenTelemetry (Dynatrace / Grafana / Datadog)

OpenTelemetry instrumentation is **fully implemented** in the Control Plane (Go) and the Gateway (Rust). Both components export traces and metrics to any OTLP-compatible backend the moment `OTEL_EXPORTER_OTLP_ENDPOINT` is set. When that variable is absent the built-in no-op providers stay in place — zero overhead, nothing phoned home.

---

## Environment variables

Three standard OpenTelemetry SDK variables are actively read at startup:

| Variable | Default | Description |
|---|---|---|
| `OTEL_EXPORTER_OTLP_ENDPOINT` | (empty — OTEL disabled) | OTLP base URL. **Setting this variable enables all telemetry export.** The Control Plane exports over OTLP/HTTP; the Gateway exports over OTLP/gRPC. |
| `OTEL_SERVICE_NAME` | `purser-control-plane` or `purser-gateway` | `service.name` resource attribute stamped on every span and metric. Defaults differ per component; set explicitly when running multiple instances or when your backend's service map groups by this attribute. |
| `OTEL_EXPORTER_OTLP_HEADERS` | (empty) | Comma-separated `key=value` pairs appended to every OTLP request. Used for API-key or token authentication with managed backends. Examples: `Authorization=Api-Token dt0c01.xxx` (Dynatrace), `Authorization=Basic <base64>` (Grafana Cloud). |

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

The Control Plane pushes three infrastructure gauges every 30 seconds via OTLP/HTTP:

| Metric name | Unit | Description |
|---|---|---|
| `purser.deployments.active` | `{deployment}` | Number of deployments in `ACTIVE` state. |
| `purser.nodes.ready` | `{node}` | Number of nodes in `READY` or `RUNNING` state. |
| `purser.nodes.total` | `{node}` | Total number of registered nodes. |

Meter name: `purser.control-plane`.

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
- **Control-plane request latency** — P50/P95 from the `purser.control-plane` trace span durations
- **Inference latency** — P50/P95/P99 from `purser.gateway.inference` span durations grouped by `model.id`
