# OpenTelemetry (Dynatrace / Grafana)

!!! note "OTel instrumentation status in v0.1.1"
    OpenTelemetry is included as a Go module dependency (`go.opentelemetry.io/otel v1.44.0`) but is **not yet wired up** in the Control Plane, Gateway, or Agent source code in v0.1.1. The standard `OTEL_*` environment variables are not actively read by any component in this release.

    This page documents the planned integration and configuration patterns. When instrumentation lands, it will use the standard OpenTelemetry SDK environment variable interface documented here, so you can prepare your observability stack now.

---

## Planned signals

When OTel instrumentation is active, Purser will emit:

### Traces

- **HTTP spans** on `/api/v1` handlers in the Control Plane (node list, model create, deploy, join-token, etc.)
- **gRPC spans** on the RegistrationService (Agent enrollment and heartbeat)
- **Gateway spans** on incoming `/v1/chat/completions` requests, including upstream proxy hops

### Metrics

- `purser.deployments.active` — number of deployments in `ACTIVE` state
- `purser.nodes.ready` — number of nodes in `READY` or `RUNNING` state
- `purser.gateway.requests.inflight` — in-flight request count at the Gateway
- `purser.gateway.tokens.rate` — token throughput per tenant

### Logs

- Structured JSON logs from all components (already emitted today via `slog` / `tracing_subscriber`)
- Audit log events (see [Audit Log](../enterprise/audit-log.md)) — forwarded as structured log records when an OTLP log exporter is configured (Enterprise)

---

## Standard environment variables

Configure the OTLP exporter with standard OpenTelemetry SDK variables. These will be read when instrumentation is wired up:

| Variable | Description |
|---|---|
| `OTEL_EXPORTER_OTLP_ENDPOINT` | OTLP exporter endpoint URL. When this is set, the SDK sends spans and metrics. Examples: `https://otlp-gateway-prod-eu-west-0.grafana.net/otlp` (Grafana Cloud), `https://abc12345.live.dynatrace.com/api/v2/otlp` (Dynatrace), `http://otel-collector:4317` (self-hosted). |
| `OTEL_SERVICE_NAME` | Service name reported in spans and metrics. Suggested values: `purser-control-plane`, `purser-gateway`, `purser-agent`. |
| `OTEL_EXPORTER_OTLP_HEADERS` | Comma-separated `key=value` pairs added to OTLP requests. Used for API key / token authentication. |
| `OTEL_EXPORTER_OTLP_PROTOCOL` | Wire protocol: `grpc` (default) or `http/protobuf`. |
| `OTEL_RESOURCE_ATTRIBUTES` | Key-value pairs added to all telemetry as resource attributes. Example: `deployment.environment=prod,cluster.name=infra-01`. |

---

## Configuration examples

### Grafana Cloud (OTLP endpoint + API key)

1. In Grafana Cloud, go to **Connections → Add new connection → OpenTelemetry (OTLP)**.
2. Note the OTLP endpoint and generate a Grafana Cloud API key.

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
    - name: OTEL_RESOURCE_ATTRIBUTES
      value: "deployment.environment=prod"

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

Create the secret with the Grafana Cloud auth header:

```bash
kubectl create secret generic purser-otel \
  --from-literal=grafana-headers="Authorization=Basic $(echo -n '<instance-id>:<api-key>' | base64)"
```

### Dynatrace (DT API token header)

1. In Dynatrace, go to **Settings → Integration → Dynatrace API** and create a token with `metrics.ingest`, `logs.ingest`, and `traces.ingest` scopes.
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
    - name: OTEL_EXPORTER_OTLP_PROTOCOL
      value: "http/protobuf"
```

```bash
kubectl create secret generic purser-otel \
  --from-literal=dt-headers="Authorization=Api-Token dt0c01.XXXXXXXX"
```

### Self-hosted OpenTelemetry Collector

Deploy the OTel Collector in your cluster and point Purser at it:

```yaml
# otel-collector-config.yaml (relevant exporter section)
exporters:
  prometheusremotewrite:
    endpoint: "http://prometheus:9090/api/v1/write"
  jaeger:
    endpoint: "jaeger-collector:14250"

service:
  pipelines:
    traces:
      receivers: [otlp]
      exporters: [jaeger]
    metrics:
      receivers: [otlp]
      exporters: [prometheusremotewrite]
```

```yaml
# Helm values.yaml
controlPlane:
  extraEnv:
    - name: OTEL_EXPORTER_OTLP_ENDPOINT
      value: "http://otel-collector:4317"
    - name: OTEL_SERVICE_NAME
      value: "purser-control-plane"
```

---

## Sample Grafana dashboard

When metrics are flowing, a useful dashboard includes:

- **Active deployments** — `purser_deployments_active` gauge
- **Ready nodes** — `purser_nodes_ready` gauge
- **Gateway inflight requests** — `purser_gateway_requests_inflight` gauge
- **Token throughput** — `rate(purser_gateway_tokens_total[5m])` by tenant
- **Request latency** — P50/P95/P99 from `purser_gateway_request_duration_seconds` histogram
- **Error rate** — `rate(purser_gateway_requests_total{status=~"5.."}[5m])`

Import the dashboard JSON from [`deploy/dashboards/purser.json`](https://github.com/andrew19881123/purser/tree/main/deploy/dashboards) when it ships.

---

## Audit log → SIEM export

When the Enterprise audit log is enabled (`PURSER_LICENSE_KEY` with `"audit"` feature), audit events can be forwarded to Splunk or Elastic via the OTel Collector's log pipeline:

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

See [Enterprise: Audit Log](../enterprise/audit-log.md) for details on the audit log format.
