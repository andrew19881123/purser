# purser-dataplane Helm Chart

Kubernetes chart for a [Purser](https://github.com/andrew19881123/purser) Data Plane.
Deploys the **API Gateway** — the OpenAI-compatible inference endpoint that proxies
requests to the GPU Agent fleet in your cluster.

A Data Plane connects to an existing Purser Control Plane (deployed with
[`purser-control-plane`](../purser-control-plane/README.md) or the all-in-one
[`purser`](../purser/README.md) chart) using a join token.

> **Note on Agents:** Purser Agents (the GPU node daemons) are installed as
> native host packages (`.deb`/`.rpm`) on your GPU nodes — not as pods.
> See [Linux Agent install](https://andrew19881123.github.io/purser/install/linux-agent/).

## Quick install

```bash
helm upgrade --install dp-prod \
  oci://ghcr.io/andrew19881123/charts/purser-dataplane \
  --version 0.5.0 \
  --namespace purser --create-namespace \
  --set controlPlane.url=https://purser-cp.internal:8080 \
  --set controlPlane.dataplaneId=dp-abc123 \
  --set controlPlane.joinToken=dp_mytoken \
  --set apiKeys="sk-prod-key-here"
```

## Minimum required configuration

All three `controlPlane.*` fields are required:

| Parameter | Description |
|-----------|-------------|
| `controlPlane.url` | Control Plane base URL (e.g. `https://purser-cp.internal:8080`) |
| `controlPlane.dataplaneId` | Data Plane ID registered in the Control Plane |
| `controlPlane.joinToken` | Join token (`dp_...`) from the Control Plane |

**Production:** always set `apiKeys` to prevent open dev mode.

## Parameters (selection)

| Key | Default | Description |
|-----|---------|-------------|
| `replicaCount` | `1` | Gateway pod replicas |
| `image.repository` | `ghcr.io/andrew19881123/purser-gateway` | Container image |
| `image.tag` | `""` (chart appVersion) | Image tag |
| `service.type` | `LoadBalancer` | Service type for inference endpoint |
| `service.port` | `8080` | Inference port |
| `controlPlane.url` | `""` | **REQUIRED** Control Plane URL |
| `controlPlane.dataplaneId` | `""` | **REQUIRED** Data Plane ID |
| `controlPlane.joinToken` | `""` | Join token (or use `joinTokenSecret`) |
| `controlPlane.joinTokenSecret` | `""` | K8s Secret name with key `join-token` |
| `agent.engineBackend` | `llamacpp` | `llamacpp` (production) or `mock` (demo) |
| `apiKeys` | `""` | Comma-separated API keys; empty = open dev mode |
| `apiKeysSecret` | `""` | K8s Secret name with key `api-keys` |
| `networkPolicy.enabled` | `false` | Create NetworkPolicy resources |

Full parameter list: [`values.yaml`](values.yaml).

## Using existing secrets

Avoid passing secrets via `--set` in production. Create the secret manually:

```bash
kubectl create secret generic purser-dp-secrets \
  --namespace purser \
  --from-literal=join-token=dp_mytoken \
  --from-literal=api-keys=sk-prod-key

helm upgrade --install dp-prod \
  oci://ghcr.io/andrew19881123/charts/purser-dataplane \
  --namespace purser \
  --set controlPlane.url=https://purser-cp.internal:8080 \
  --set controlPlane.dataplaneId=dp-abc123 \
  --set controlPlane.joinTokenSecret=purser-dp-secrets \
  --set apiKeysSecret=purser-dp-secrets
```

## Documentation

See [Enterprise Kubernetes Deployment](https://andrew19881123.github.io/purser/install/kubernetes-enterprise/)
for the full guide including multi-DP setups.
