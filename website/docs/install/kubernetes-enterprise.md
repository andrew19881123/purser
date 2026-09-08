# Kubernetes Enterprise Deployment (Separate CP + DP)

This guide covers an **enterprise-grade** Purser deployment where the Control Plane
and Data Planes run independently — on separate clusters, namespaces, or cloud regions.

!!! tip "Quick-start alternative"
    If you just want everything on one cluster for evaluation, use the all-in-one
    [`purser`](kubernetes.md) chart instead. This guide is for production
    environments requiring independent scaling or multi-region deployments.

## Architecture

```mermaid
graph TD
    subgraph "Control Plane cluster (purser-control-plane chart)"
        CP["Control Plane Pod\n(registry · PKI · scheduler · REST API)"]
        PVC[("PVC /data\nSQLite or PostgreSQL\n+ PKI CA key/cert")]
        SVC_CP["Service: LoadBalancer\nHTTP :8080 / gRPC :9443"]
        CP --- PVC
        SVC_CP --> CP
    end

    subgraph "Data Plane A — prod cluster (purser-dataplane chart)"
        GW_A["Gateway Pod\n(OpenAI-compatible API)"]
        SVC_A["Service: LoadBalancer\nHTTP :8080"]
        SVC_A --> GW_A
    end

    subgraph "Data Plane B — dev cluster (purser-dataplane chart)"
        GW_B["Gateway Pod\n(OpenAI-compatible API)"]
        SVC_B["Service: LoadBalancer\nHTTP :8080"]
        SVC_B --> GW_B
    end

    GW_A -- "join token (HTTPS)" --> SVC_CP
    GW_B -- "join token (HTTPS)" --> SVC_CP

    subgraph "GPU fleet A (host packages)"
        A1["Agent Node 1"]
        A2["Agent Node 2"]
    end
    subgraph "GPU fleet B (host packages)"
        A3["Agent Node 3"]
    end

    A1 -- "gRPC :9443 (enroll/heartbeat)" --> SVC_CP
    A2 -- "gRPC :9443 (enroll/heartbeat)" --> SVC_CP
    A3 -- "gRPC :9443 (enroll/heartbeat)" --> SVC_CP

    Client["OpenAI SDK client"] -->|inference| SVC_A
    Client2["Dev client"] -->|inference| SVC_B
```

Key properties of this topology:

- The Control Plane is deployed **once** — it holds the registry, PKI CA, and scheduler.
- Each Data Plane cluster runs a **Gateway pod** that connects back to the CP using a join token.
- Agents (GPU node daemons) are installed as `.deb`/`.rpm` packages and enroll with the CP over gRPC/mTLS.
- The Control Plane Service must be reachable by both Agents (gRPC `:9443`) and Data Plane Gateways (HTTP `:8080`).

## Prerequisites

- Kubernetes 1.24+ (both CP and DP clusters)
- Helm v3.8+ (OCI chart support)
- A default `StorageClass` in the CP cluster (for the PKI/registry PVC)
- Network connectivity: DP clusters → CP REST API, all Agent nodes → CP gRPC `:9443`

## Step 1 — Deploy the Control Plane

### 1a. SQLite (single-node, default)

```bash
helm upgrade --install purser-cp \
  oci://ghcr.io/andrew19881123/charts/purser-control-plane \
  --version 0.5.0 \
  --namespace purser --create-namespace \
  --set service.type=LoadBalancer
```

!!! warning "Single-writer constraint"
    SQLite is single-writer. Keep `replicaCount=1`. For HA, use PostgreSQL (see below).

### 1b. PostgreSQL (HA enterprise)

```bash
helm upgrade --install purser-cp \
  oci://ghcr.io/andrew19881123/charts/purser-control-plane \
  --version 0.5.0 \
  --namespace purser --create-namespace \
  --set service.type=LoadBalancer \
  --set database.driver=postgres \
  --set database.url="postgres://purser:secret@postgres.internal:5432/purser?sslmode=require" \
  --set replicaCount=2
```

Using an existing secret for the DB URL (recommended for production):

```bash
kubectl create secret generic purser-cp-db \
  --namespace purser \
  --from-literal=db-url="postgres://purser:secret@postgres.internal:5432/purser?sslmode=require"

helm upgrade --install purser-cp \
  oci://ghcr.io/andrew19881123/charts/purser-control-plane \
  --version 0.5.0 \
  --namespace purser --create-namespace \
  --set service.type=LoadBalancer \
  --set database.driver=postgres \
  --set database.urlSecret=purser-cp-db
```

### 1c. Verify the Control Plane

```bash
# Wait for the pod to be ready
kubectl -n purser rollout status deployment/purser-cp-purser-control-plane

# Get the external IP / hostname
kubectl -n purser get svc purser-cp-purser-control-plane

# Health check
CP_URL=http://<EXTERNAL-IP>:8080
curl -sS "$CP_URL/api/v1/cluster/health" | jq .
```

## Step 2 — Register Data Planes

For each Data Plane cluster, register an entry in the Control Plane and obtain a join token.

```bash
CP_URL=http://<EXTERNAL-IP>:8080

# Register a Data Plane (replace name and region)
DP_ID=$(curl -sS -X POST "$CP_URL/api/v1/data-planes" \
  -H "Content-Type: application/json" \
  -d '{"name":"prod","region":"us-east-1"}' | jq -r .id)

echo "Data Plane ID: $DP_ID"

# Mint a join token for that Data Plane
DP_TOKEN=$(curl -sS -X POST "$CP_URL/api/v1/data-planes/$DP_ID/join-token" | jq -r .token)

echo "Join token: $DP_TOKEN"
```

Repeat this step for each Data Plane (prod, dev, etc.).

## Step 3 — Deploy Data Planes

Deploy the `purser-dataplane` chart in each DP cluster using the ID and token from Step 2.

### Production Data Plane

```bash
helm upgrade --install dp-prod \
  oci://ghcr.io/andrew19881123/charts/purser-dataplane \
  --version 0.5.0 \
  --namespace purser --create-namespace \
  --set controlPlane.url="$CP_URL" \
  --set controlPlane.dataplaneId="$DP_PROD_ID" \
  --set controlPlane.joinToken="$DP_PROD_TOKEN" \
  --set apiKeys="sk-prod-key-1,sk-prod-key-2"
```

### Dev Data Plane (mock engine, no GPU required)

```bash
helm upgrade --install dp-dev \
  oci://ghcr.io/andrew19881123/charts/purser-dataplane \
  --version 0.5.0 \
  --namespace purser-dev --create-namespace \
  --set controlPlane.url="$CP_URL" \
  --set controlPlane.dataplaneId="$DP_DEV_ID" \
  --set controlPlane.joinToken="$DP_DEV_TOKEN" \
  --set agent.engineBackend=mock \
  --set apiKeys="sk-dev-key"
```

### Using secrets for credentials (production hardening)

Avoid passing tokens and API keys via `--set`. Use Kubernetes Secrets instead:

```bash
# Create the secret in the DP cluster
kubectl create secret generic purser-dp-prod-creds \
  --namespace purser \
  --from-literal=join-token="$DP_PROD_TOKEN" \
  --from-literal=api-keys="sk-prod-key-1,sk-prod-key-2"

helm upgrade --install dp-prod \
  oci://ghcr.io/andrew19881123/charts/purser-dataplane \
  --version 0.5.0 \
  --namespace purser --create-namespace \
  --set controlPlane.url="$CP_URL" \
  --set controlPlane.dataplaneId="$DP_PROD_ID" \
  --set controlPlane.joinTokenSecret=purser-dp-prod-creds \
  --set apiKeysSecret=purser-dp-prod-creds
```

## Step 4 — Enroll Agents

Agents run as host packages on your GPU nodes and enroll with the Control Plane over gRPC/mTLS.
See the [Linux Agent install guide](linux-agent.md) for full details.

Quick reference for a single node:

```bash
# Install the agent package (Debian/Ubuntu)
curl -L "$CP_URL/api/v1/enrollment-bundle?os=linux&arch=amd64" -o purser-agent.deb
sudo dpkg -i purser-agent.deb

# Mint a node join token and enroll
NODE_TOKEN=$(curl -sS -X POST "$CP_URL/api/v1/join-token" | jq -r .token)
sudo purser-agent enroll \
  --control-plane "$CP_URL" \
  --grpc-addr "$(kubectl -n purser get svc purser-cp-purser-control-plane \
      -o jsonpath='{.status.loadBalancer.ingress[0].ip}'):9443" \
  --token "$NODE_TOKEN"
```

## Step 5 — Verify the full stack

```bash
# Control Plane: confirm Data Planes are registered and Agents are enrolled
curl -sS "$CP_URL/api/v1/data-planes" | jq '.[].status'
curl -sS "$CP_URL/api/v1/nodes" | jq '.[].status'

# Data Plane: inference endpoint ready
DP_URL=http://$(kubectl -n purser get svc dp-prod-purser-dataplane \
    -o jsonpath='{.status.loadBalancer.ingress[0].ip}'):8080

curl -sS "$DP_URL/healthz"
curl -sS "$DP_URL/v1/models" \
  -H "Authorization: Bearer sk-prod-key-1" | jq .
```

## Multi-DP example: `values-prod.yaml` + `values-dev.yaml`

Managing configuration as files is more maintainable than long `--set` chains.

```yaml
# values-prod.yaml
controlPlane:
  url: "https://purser-cp.corp.internal:8080"
  dataplaneId: "dp-prod-us-east-1"
  joinTokenSecret: "purser-dp-prod-creds"

service:
  type: LoadBalancer

agent:
  engineBackend: llamacpp

apiKeysSecret: "purser-dp-prod-creds"

gateway:
  resources:
    requests:
      cpu: 500m
      memory: 512Mi
    limits:
      cpu: "2"
      memory: 2Gi

networkPolicy:
  enabled: true
```

```yaml
# values-dev.yaml
controlPlane:
  url: "https://purser-cp.corp.internal:8080"
  dataplaneId: "dp-dev"
  joinToken: "dp_dev_token_here"   # ok for dev; use a Secret in prod

service:
  type: ClusterIP   # dev: in-cluster only

agent:
  engineBackend: mock

apiKeys: "sk-dev-internal"
```

Deploy with:

```bash
helm upgrade --install dp-prod \
  oci://ghcr.io/andrew19881123/charts/purser-dataplane \
  --version 0.5.0 --namespace purser --create-namespace \
  -f values-prod.yaml

helm upgrade --install dp-dev \
  oci://ghcr.io/andrew19881123/charts/purser-dataplane \
  --version 0.5.0 --namespace purser-dev --create-namespace \
  -f values-dev.yaml
```

## Upgrading

Upgrade the Control Plane first, then Data Planes:

```bash
# 1. Upgrade CP (rolling update with Recreate strategy)
helm upgrade purser-cp \
  oci://ghcr.io/andrew19881123/charts/purser-control-plane \
  --version 0.5.1 --reuse-values

# 2. Upgrade each DP
helm upgrade dp-prod \
  oci://ghcr.io/andrew19881123/charts/purser-dataplane \
  --version 0.5.1 --reuse-values
```

## Uninstalling

```bash
# Remove a Data Plane
helm -n purser uninstall dp-prod

# Remove the Control Plane (WARNING: deletes the PVC with registry + PKI CA!)
# Back up /data first — see Operations > Backup & Restore.
helm -n purser uninstall purser-cp
kubectl -n purser delete pvc purser-cp-purser-control-plane-data
```

## See also

- [Kubernetes Quick-start (all-in-one chart)](kubernetes.md)
- [Linux Agent (.deb/.rpm) install](linux-agent.md)
- [Enrollment Bundle](enrollment-bundle.md)
- [HA Control Plane (Raft)](../enterprise/ha-control-plane.md)
- [Backup & Restore](../operations/backup-restore.md)
