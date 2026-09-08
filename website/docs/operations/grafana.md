# Grafana Dashboards

Purser ships a ready-to-use Grafana provisioning bundle under `deploy/grafana/`. It
includes four pre-built dashboards, a Prometheus datasource definition, and
PrometheusRule alerts. All dashboards source metrics from the endpoints described in
[Observability](observability.md).

---

## Quick start: Helm (kube-prometheus-stack)

If you deploy Purser with the `purser-control-plane` Helm chart alongside
**kube-prometheus-stack**, enable the Grafana sidecar provisioning flag:

```bash
helm upgrade purser-cp oci://ghcr.io/andrew19881123/charts/purser-control-plane \
  --set grafana.provisioning.enabled=true
```

This creates a ConfigMap named `<release>-purser-control-plane-grafana-dashboards`
labelled `grafana_dashboard: "1"`. The kube-prometheus-stack Grafana sidecar
watches for that label, copies the JSON files into Grafana, and groups them
inside a **Purser** folder automatically.

### Prerequisites

The kube-prometheus-stack Grafana sidecar must be enabled in your values:

```yaml
# kube-prometheus-stack values
grafana:
  sidecar:
    dashboards:
      enabled: true
      label: grafana_dashboard
      labelValue: "1"
      folderAnnotation: grafana_folder
      provider:
        foldersFromFilesStructure: true
```

### Alert rules

Apply the bundled PrometheusRule to your cluster:

```bash
kubectl apply -f deploy/grafana/alerts/purser-rules.yaml
```

The rule file targets the `prometheus: kube-prometheus` label used by the default
kube-prometheus-stack Prometheus selector. Adjust the label if your Prometheus
uses a different selector.

---

## Quick start: standalone Grafana

If you run Grafana without Kubernetes, use the file-based provisioning bundle
directly.

### 1. Configure the Prometheus datasource

Copy `deploy/grafana/provisioning/datasources/prometheus.yaml` to your Grafana
provisioning directory (typically `/etc/grafana/provisioning/datasources/`).
Set `PROMETHEUS_URL` in the Grafana environment to point at your Prometheus instance:

```bash
export PROMETHEUS_URL=http://prometheus.internal:9090
```

The datasource is named **Purser-Prometheus** and is set as the default.

### 2. Configure the dashboard folder

Copy `deploy/grafana/provisioning/dashboards/purser.yaml` to
`/etc/grafana/provisioning/dashboards/`. This tells Grafana to watch
`/var/lib/grafana/dashboards/purser/` for JSON files and group them in a
**Purser** folder.

### 3. Copy the dashboard JSON files

```bash
mkdir -p /var/lib/grafana/dashboards/purser
cp deploy/grafana/dashboards/*.json /var/lib/grafana/dashboards/purser/
```

### 4. Restart Grafana

```bash
systemctl restart grafana-server
# or: docker restart grafana
```

Grafana auto-discovers the four JSON files on startup and again every 30 seconds.

---

## Dashboards

### Gateway Overview (`purser-gateway-overview`)

Covers the inference request pipeline end-to-end.

| Panel | Description |
|---|---|
| TTFT p50 / p99 | Time-to-first-token percentiles split by model. Primary SLO indicator. |
| TBT p99 | Inter-token latency (between successive SSE chunks) at the 99th percentile. |
| Request Rate by Model | Stacked request throughput (req/s) per model. |
| Error Rate by Type | Per-`error_type` error rate: `auth_failure`, `quota_exceeded`, `timeout_upstream`, etc. |
| Active Streams (stat) | Current count of concurrent SSE streaming connections. |

### Node Hardware (`purser-node-hardware`)

Per-node GPU and engine health. Refreshes every 15 s.

| Panel | Description |
|---|---|
| Decode Throughput | Auto-regressive token generation rate (tok/s) per node. |
| VRAM Used | VRAM consumed by the engine (GiB) per node. |
| Queue Depth | In-engine request queue depth per node. Threshold line at 50. |
| Inference Port Alive | Table view: green ALIVE / red DOWN per node. |
| GPU Utilization | SM utilisation (0–1) per node. **Shows 0.0 until NVML integration.** |

### Token Economics (`purser-token-economics`)

Token throughput and chargeback telemetry.

| Panel | Description |
|---|---|
| Input Token Rate | Prompt tokens consumed per second, by model. |
| Output Token Rate | Completion tokens generated per second, by model. |
| Throughput p50 | Median token generation throughput per request histogram. |
| Fleet Decode tok/s | Sum of all node decode throughputs — fleet-wide capacity indicator. |
| Active Deployments (stat) | Number of deployments currently in `ACTIVE` state. |

### Compliance & Audit (`purser-compliance-audit`)

Policy enforcement and fleet health for security and audit teams.

| Panel | Description |
|---|---|
| Policy Denial Rate | Rate of `quota_exceeded` errors by model and tenant. |
| Auth Failure Rate | Rate of `auth_failure` errors by model and tenant. |
| Fleet Readiness Ratio | `purser_nodes_ready / purser_nodes_total` (0–1). |
| CP Version (stat) | Current Control Plane version from `purser_cp_info`. |

---

## Datasource configuration

All four dashboards use a `$datasource` template variable. On first import, Grafana
binds it to the default datasource, which is **Purser-Prometheus** when provisioned
via the bundle. To point a dashboard at a different Prometheus instance, change the
variable in the dashboard's **Settings → Variables** panel.

The datasource is configured with a 15-second scrape interval alignment
(`timeInterval: "15s"`) matching the default scrape interval.

---

## Alert rules reference

The PrometheusRule at `deploy/grafana/alerts/purser-rules.yaml` defines five SLO alerts:

| Alert | Condition | Window | Severity |
|---|---|---|---|
| `PurserHighTTFTp99` | TTFT p99 > 2 s | 5 min | warning |
| `PurserHighTBTp99` | TBT p99 > 500 ms | 2 min | warning |
| `PurserNodeEngineDown` | `purser_node_inference_port_alive == 0` | 1 min | critical |
| `PurserHighErrorRate` | errors/requests > 5 % | 2 min | critical |
| `PurserQueueSaturated` | queue_depth > 50 | 1 min | warning |

Each alert includes a `runbook_url` pointing to this documentation site.

---

## Metric reference

For the full list of metrics with label schemas and example PromQL queries, see
[Observability](observability.md).
