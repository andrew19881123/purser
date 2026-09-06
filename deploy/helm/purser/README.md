# Purser Helm Chart

Kubernetes chart for the [Purser](https://github.com/andrew19881123/purser) AI-gateway
platform. Deploys three components (Control Plane, Gateway, UI) plus optional
cert-manager integration, External Secrets Operator support, and Kubernetes-native
security hardening.

## Quick install

```bash
helm install purser oci://ghcr.io/andrew19881123/helm/purser \
  --namespace purser --create-namespace
```

## Parameters (selection)

| Key | Default | Description |
|-----|---------|-------------|
| `replicaCount` | `1` | Pod replicas (keep at 1; HA is enterprise) |
| `image.pullPolicy` | `IfNotPresent` | Image pull policy |
| `networkPolicy.enabled` | `false` | Create NetworkPolicy resources |
| `containerSecurityContext.*` | see below | Applied to every container |
| `podSecurityContext.*` | see below | Applied at the pod level |
| `tls.certManager.enabled` | `false` | Use cert-manager for mTLS cert |
| `externalSecrets.enabled` | `false` | Use External Secrets Operator |

Full parameter list: [`values.yaml`](values.yaml).

---

## Security

### Container security context

By default every container runs with:

```yaml
containerSecurityContext:
  readOnlyRootFilesystem: true
  allowPrivilegeEscalation: false
  runAsNonRoot: true
  runAsUser: 65532
  runAsGroup: 65532
  capabilities:
    drop:
      - ALL
  seccompProfile:
    type: RuntimeDefault
```

To relax a field chart-wide (e.g. during initial testing):

```bash
helm upgrade purser oci://... \
  --set containerSecurityContext.readOnlyRootFilesystem=false
```

#### nginx / UI emptyDir volumes

`readOnlyRootFilesystem: true` prevents nginx from writing its PID file and cache.
The UI template automatically mounts three `emptyDir` volumes when
`containerSecurityContext.readOnlyRootFilesystem=true` (the default):

| Volume | Mount | Purpose |
|--------|-------|---------|
| `nginx-cache` | `/var/cache/nginx` | Proxy and fastcgi cache |
| `nginx-run` | `/var/run` | PID file |
| `nginx-tmp` | `/tmp` | Temp upload buffer |

These volumes are added **automatically** — no manual configuration needed.

### Pod security context

```yaml
podSecurityContext:
  fsGroup: 65532
  runAsNonRoot: true
  seccompProfile:
    type: RuntimeDefault
```

`fsGroup: 65532` ensures the Control Plane's persistent volume (SQLite + PKI) is
group-writable by the non-root UID the distroless image runs as.

### NetworkPolicy (opt-in)

Enable with:

```bash
helm upgrade purser oci://... --set networkPolicy.enabled=true
```

Three `NetworkPolicy` resources are created:

| Resource | Ingress | Egress |
|----------|---------|--------|
| `*-control-plane` | Port 8080 from gateway pods; port 9443 from 0.0.0.0/0 (agents) | Unrestricted |
| `*-gateway` | Port 8080 from 0.0.0.0/0 | Unrestricted |
| `*-ui` | Port 8080 from 0.0.0.0/0 | Unrestricted |

The control-plane policy restricts REST-API ingress to gateway pods only; gRPC
enrollment (port 9443) is open to `0.0.0.0/0` because Purser Agents run as host
packages outside Kubernetes and reach the cluster via NodePort/LoadBalancer.

### Hardened production values

```yaml
# values-hardened.yaml
networkPolicy:
  enabled: true

containerSecurityContext:
  readOnlyRootFilesystem: true
  allowPrivilegeEscalation: false
  runAsNonRoot: true
  runAsUser: 65532
  runAsGroup: 65532
  capabilities:
    drop:
      - ALL
  seccompProfile:
    type: RuntimeDefault

podSecurityContext:
  fsGroup: 65532
  runAsNonRoot: true
  seccompProfile:
    type: RuntimeDefault

controlPlane:
  service:
    type: LoadBalancer   # expose gRPC to out-of-cluster agents

tls:
  certManager:
    enabled: true
    issuerRef:
      name: internal-ca
      kind: ClusterIssuer

externalSecrets:
  enabled: true
  secretStoreRef:
    name: vault-cluster-store
    kind: ClusterSecretStore
  remoteRefs:
    joinToken: "secret/data/purser/join-token"
    internalToken: "secret/data/purser/internal-token"
```

Apply with:

```bash
helm upgrade --install purser oci://ghcr.io/andrew19881123/helm/purser \
  --namespace purser --create-namespace \
  -f values-hardened.yaml
```
