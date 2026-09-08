# Purser Grafana Provisioning Bundle

Ready-to-use Grafana dashboards and alert rules for Purser observability.

## Directory layout

```
deploy/grafana/
├── provisioning/
│   ├── datasources/prometheus.yaml   # Prometheus datasource auto-provisioning
│   └── dashboards/purser.yaml        # Dashboard folder provisioning config
├── dashboards/
│   ├── gateway-overview.json         # Inference latency, error rate, active streams
│   ├── node-hardware.json            # Per-node GPU/VRAM/queue/engine liveness
│   ├── token-economics.json          # Token throughput + chargeback overview
│   └── compliance-audit.json         # Policy denials, auth failures, fleet readiness
└── alerts/
    └── purser-rules.yaml             # PrometheusRule CRD with 5 SLO alert rules
```

## Quick start — standalone Grafana

1. Copy `provisioning/` into your Grafana `conf/provisioning/` directory.
2. Copy `dashboards/` to `/var/lib/grafana/dashboards/purser/`.
3. Set `PROMETHEUS_URL` in your environment to point at your Prometheus instance
   (default: `http://prometheus:9090`).
4. Restart Grafana.

## Quick start — Kubernetes (kube-prometheus-stack)

Apply the PrometheusRule:

```bash
kubectl apply -f deploy/grafana/alerts/purser-rules.yaml
```

Enable the Grafana sidecar in the Helm chart:

```bash
helm upgrade purser-cp deploy/helm/purser-control-plane \
  --set grafana.provisioning.enabled=true
```

## Dashboards

| Dashboard | UID | Description |
|---|---|---|
| Gateway Overview | `purser-gateway-overview` | TTFT p50/p99, TBT p99, request rate, error rate, active streams |
| Node Hardware | `purser-node-hardware` | Decode tok/s, VRAM, queue depth, port alive, GPU util (stub) |
| Token Economics | `purser-token-economics` | Input/output token rates, throughput p50, fleet tok/s, active deployments |
| Compliance & Audit | `purser-compliance-audit` | Quota denials, auth failures, fleet readiness, CP version |

## Metric reference

See [website/docs/operations/observability.md](../../website/docs/operations/observability.md)
for the full metric list with labels and descriptions.
