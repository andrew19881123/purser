# purser-control-plane Helm Chart

Kubernetes chart for the [Purser](https://github.com/andrew19881123/purser) Control Plane.
This chart deploys **only** the Control Plane component (registry, scheduler, PKI, auth API).
It is designed for enterprise environments where the Control Plane and Data Planes are deployed
independently — potentially on different clusters or in different regions.

For a single-cluster quick-start that bundles everything in one release, use the
[`purser`](../purser/README.md) chart instead.

## Architecture

```
┌─────────────────────────────────────┐
│  Control Plane cluster              │
│  ┌───────────────────────────────┐  │
│  │  purser-control-plane chart   │  │
│  │  - Control Plane pod          │  │
│  │  - PVC (SQLite/PKI, 1 Gi)     │  │
│  │  - Service (HTTP+gRPC)        │  │
│  └───────────────────────────────┘  │
└───────────────┬─────────────────────┘
                │  join token (HTTPS)
     ┌──────────▼──────────┐   ┌─────────────────────┐
     │  Data Plane A       │   │  Data Plane B        │
     │  purser-dataplane   │   │  purser-dataplane    │
     │  (prod cluster)     │   │  (dev cluster)       │
     └─────────────────────┘   └─────────────────────┘
```

## Quick install

```bash
helm upgrade --install purser-cp \
  oci://ghcr.io/andrew19881123/charts/purser-control-plane \
  --version 0.5.0 \
  --namespace purser --create-namespace \
  --set service.type=LoadBalancer \
  --set database.driver=sqlite
```

For PostgreSQL (HA enterprise):

```bash
helm upgrade --install purser-cp \
  oci://ghcr.io/andrew19881123/charts/purser-control-plane \
  --version 0.5.0 \
  --namespace purser --create-namespace \
  --set service.type=LoadBalancer \
  --set database.driver=postgres \
  --set database.url="postgres://purser:pass@postgres.internal:5432/purser?sslmode=require"
```

## Minimum required configuration

| Parameter | Description |
|-----------|-------------|
| `service.type` | Set to `LoadBalancer` or `NodePort` so Data Planes can reach the CP |
| `database.driver` | `sqlite` (default) or `postgres` |
| `database.url` | Required when `database.driver=postgres` |

## Parameters (selection)

| Key | Default | Description |
|-----|---------|-------------|
| `replicaCount` | `1` | Pod replicas (keep at 1 without Raft HA) |
| `image.repository` | `ghcr.io/andrew19881123/purser-control-plane` | Container image |
| `image.tag` | `""` (chart appVersion) | Image tag |
| `service.type` | `ClusterIP` | Service type — set LoadBalancer/NodePort for external access |
| `service.httpPort` | `8080` | REST management API port |
| `service.grpcPort` | `9443` | gRPC RegistrationService port |
| `database.driver` | `sqlite` | `sqlite` or `postgres` |
| `database.url` | `""` | PostgreSQL connection URL (required when driver=postgres) |
| `persistence.enabled` | `true` | Persist PKI CA and SQLite registry |
| `persistence.size` | `1Gi` | PVC size |
| `controlPlane.clusterId` | `"default"` | Cluster ID echoed in join tokens |
| `license.key` | `""` | Enterprise license key |
| `networkPolicy.enabled` | `false` | Create NetworkPolicy resources |

Full parameter list: [`values.yaml`](values.yaml).

## Documentation

See [Enterprise Kubernetes Deployment](https://andrew19881123.github.io/purser/install/kubernetes-enterprise/)
for the full step-by-step guide, including multi-region DP setup.
