# Certificate Management (cert-manager)

!!! note "cert-manager support status"
    As of v0.1.1, the Purser Helm chart ships a standard Kubernetes `Ingress` resource with support for `cert-manager.io/cluster-issuer` annotations. A dedicated `tls.certManager.enabled` values key is not yet present in the chart — TLS certificates are managed via Ingress TLS annotations.

    This page documents how to use cert-manager with the Ingress networking model and what gRPC port considerations apply.

---

## When to use cert-manager

Use cert-manager when:

- You want **automatic TLS certificate provisioning** for the Purser Dashboard and API Gateway public endpoints
- You're using the **Ingress networking model** (single hostname for all components)
- You have cert-manager already installed in your cluster

cert-manager manages:
- TLS for the **Ingress** (HTTPS for Dashboard + REST API + Gateway `/v1`)

cert-manager does **not** manage:
- The **internal PKI** used between the Control Plane and Agents — this is a self-signed CA managed by Purser itself (`PURSER_PKI_DIR`)
- Agent mTLS certificates — issued by the internal PKI at enrollment time

---

## Prerequisites

Install cert-manager if not already present:

```bash
helm repo add jetstack https://charts.jetstack.io
helm install cert-manager jetstack/cert-manager \
  --namespace cert-manager --create-namespace \
  --set installCRDs=true
```

---

## Let's Encrypt issuer

Create a `ClusterIssuer` for Let's Encrypt:

```yaml
# letsencrypt-prod.yaml
apiVersion: cert-manager.io/v1
kind: ClusterIssuer
metadata:
  name: letsencrypt-prod
spec:
  acme:
    server: https://acme-v02.api.letsencrypt.org/directory
    email: ops@example.com
    privateKeySecretRef:
      name: letsencrypt-prod-account-key
    solvers:
      - http01:
          ingress:
            class: nginx
```

```bash
kubectl apply -f letsencrypt-prod.yaml
```

Install Purser with TLS:

```bash
helm install purser oci://ghcr.io/andrew19881123/charts/purser --version 0.1.1 \
  --set ingress.enabled=true \
  --set ingress.host=purser.example.com \
  --set ingress.className=nginx \
  --set "ingress.annotations.cert-manager\.io/cluster-issuer=letsencrypt-prod" \
  --set ingress.tls[0].secretName=purser-tls \
  --set ingress.tls[0].hosts[0]=purser.example.com
```

This produces an Ingress with the cert-manager annotation. cert-manager detects it, issues a certificate via Let's Encrypt HTTP-01, and stores it in the `purser-tls` Secret.

---

## Internal CA issuer

For air-gapped or on-prem environments without public ACME:

```yaml
# internal-ca-issuer.yaml
apiVersion: cert-manager.io/v1
kind: ClusterIssuer
metadata:
  name: internal-ca
spec:
  ca:
    secretName: internal-ca-tls  # your CA cert/key in this Secret
```

```bash
helm install purser oci://ghcr.io/andrew19881123/charts/purser --version 0.1.1 \
  --set ingress.enabled=true \
  --set ingress.host=purser.internal.example.com \
  --set ingress.className=nginx \
  --set "ingress.annotations.cert-manager\.io/cluster-issuer=internal-ca" \
  --set ingress.tls[0].secretName=purser-internal-tls \
  --set ingress.tls[0].hosts[0]=purser.internal.example.com
```

---

## DNS SANs the certificate covers

The Ingress TLS certificate covers the single hostname configured in `ingress.host`:

- `purser.example.com` — the dashboard, REST API (`/api/*`), and gateway (`/v1/*`)

It does **not** cover:
- Agent enrollment (`<control-plane-host>:9443`) — this is gRPC/mTLS with Purser's internal PKI, not a public-facing endpoint

---

## gRPC port (9443) and TLS

The RegistrationService gRPC (Agent enrollment, heartbeat) on port **9443** is mTLS-protected by Purser's **internal PKI** — not by cert-manager. The internal PKI:

- Is initialized at Control Plane startup
- Generates a self-signed CA on first start, persisted to `PURSER_PKI_DIR`
- Issues client certificates to enrolling Agents during the `Join` RPC

This CA is internal and not intended for public HTTPS use. Exposing gRPC to the LAN for Agent enrollment (via `LoadBalancer` or `NodePort`) uses this internal CA, not cert-manager.

!!! warning "Do not route gRPC through HTTP/1.1 Ingress"
    cert-manager + nginx Ingress provides TLS for HTTP/1.1 traffic only. gRPC requires HTTP/2. The Control Plane's gRPC port (9443) must be exposed directly as a Service (LoadBalancer or NodePort) — do not route it through the HTTP Ingress.

---

## Values.yaml reference for TLS via Ingress

```yaml
ingress:
  enabled: true
  className: "nginx"
  annotations:
    cert-manager.io/cluster-issuer: "letsencrypt-prod"
    # nginx-specific annotations if needed:
    # nginx.ingress.kubernetes.io/proxy-body-size: "0"
    # nginx.ingress.kubernetes.io/proxy-read-timeout: "3600"
  host: "purser.example.com"
  tls:
    - secretName: purser-tls
      hosts:
        - purser.example.com
```
