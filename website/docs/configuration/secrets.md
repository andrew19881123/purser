# Secret Management (Vault / ESO)

Purser has several secrets that need to be protected in production:

- `PURSER_LICENSE_KEY` — Enterprise license key
- `PURSER_GATEWAY_TOKEN` / `PURSER_GATEWAY_INTERNAL_TOKEN` — Control Plane ↔ Gateway internal shared secret
- `PURSER_GATEWAY_API_KEYS` — client bearer tokens for the inference endpoint
- `PURSER_JOIN_TOKEN` — one-time agent enrollment tokens (short-lived, auto-expiring)
- OIDC client secret (`PURSER_OIDC_CLIENT_SECRET`) — when OIDC is enabled

Three approaches are supported, in increasing order of operational complexity:

---

## Path 1: Default (env vars / Helm `--set`)

Suitable for development and small deployments where you control the Helm release directly.

The Helm chart generates and manages the gateway internal token automatically (stored in a Kubernetes Secret named `<release>-gateway` and injected into both the Control Plane and Gateway pods via `secretKeyRef`).

Provide other secrets at install time:

```bash
helm install purser oci://ghcr.io/andrew19881123/charts/purser --version 0.3.0 \
  --set gateway.apiKeys="psk_key1:team-a,psk_key2:team-b" \
  --set license.key="<enterprise-license-key>"
```

!!! warning "Secrets in Helm --set"
    Values passed via `--set` end up in the Helm release history, which is stored in Kubernetes Secrets but may be readable to anyone with `helm list --all-namespaces`. Use `--set-string` or a values file with `--values` and restrict access to the values file.

Recommended for production: supply secrets from a values file with restricted permissions:

```bash
# secrets.yaml (permissions: 600, never commit to git)
gateway:
  apiKeys: "psk_key1:team-a,psk_key2:team-b"
license:
  key: "<enterprise-license-key>"
```

```bash
helm install purser oci://ghcr.io/andrew19881123/charts/purser --version 0.3.0 \
  --values secrets.yaml
```

---

## Path 2: External Secrets Operator (ESO) + HashiCorp Vault

Recommended for production environments where secrets are managed centrally.

!!! note "No ESO template in the chart"
    The Purser Helm chart does not currently ship a built-in `ExternalSecret` template. The ESO integration below is a manual step — you create the `ExternalSecret` resources yourself and then reference the resulting Kubernetes Secrets from `controlPlane.extraEnv` / `gateway.extraEnv`.

### Step 1: Install ESO

```bash
helm repo add external-secrets https://charts.external-secrets.io
helm install external-secrets external-secrets/external-secrets \
  --namespace external-secrets --create-namespace
```

### Step 2: Create a ClusterSecretStore for Vault

```yaml
# vault-store.yaml
apiVersion: external-secrets.io/v1beta1
kind: ClusterSecretStore
metadata:
  name: vault-backend
spec:
  provider:
    vault:
      server: "https://vault.example.com"
      path: "secret"
      version: "v2"
      auth:
        kubernetes:
          mountPath: "kubernetes"
          role: "purser"
          serviceAccountRef:
            name: "purser-vault-auth"
            namespace: "default"
```

### Step 3: Store secrets in Vault

```bash
vault kv put secret/purser/gateway \
  internal_token="<shared-secret>" \
  api_keys="psk_key1:team-a,psk_key2:team-b"

vault kv put secret/purser/license \
  key="<enterprise-license-key>"
```

### Step 4: Create ExternalSecret resources

```yaml
# purser-gateway-external-secret.yaml
apiVersion: external-secrets.io/v1beta1
kind: ExternalSecret
metadata:
  name: purser-gateway-secrets
  namespace: default
spec:
  refreshInterval: 1h
  secretStoreRef:
    name: vault-backend
    kind: ClusterSecretStore
  target:
    name: purser-gateway-vault
    creationPolicy: Owner
  data:
    - secretKey: internal-token
      remoteRef:
        key: secret/purser/gateway
        property: internal_token
    - secretKey: api-keys
      remoteRef:
        key: secret/purser/gateway
        property: api_keys
---
apiVersion: external-secrets.io/v1beta1
kind: ExternalSecret
metadata:
  name: purser-license-secret
  namespace: default
spec:
  refreshInterval: 24h
  secretStoreRef:
    name: vault-backend
    kind: ClusterSecretStore
  target:
    name: purser-license-vault
    creationPolicy: Owner
  data:
    - secretKey: license-key
      remoteRef:
        key: secret/purser/license
        property: key
```

```bash
kubectl apply -f purser-gateway-external-secret.yaml
```

### Step 5: Reference ESO secrets in Helm values

```yaml
# values.yaml
gateway:
  internalToken: ""   # leave empty — we inject from the ESO secret below
  apiKeys: ""         # same
  extraEnv:
    - name: PURSER_GATEWAY_INTERNAL_TOKEN
      valueFrom:
        secretKeyRef:
          name: purser-gateway-vault
          key: internal-token
    - name: PURSER_GATEWAY_API_KEYS
      valueFrom:
        secretKeyRef:
          name: purser-gateway-vault
          key: api-keys

controlPlane:
  extraEnv:
    - name: PURSER_GATEWAY_TOKEN
      valueFrom:
        secretKeyRef:
          name: purser-gateway-vault
          key: internal-token
    - name: PURSER_LICENSE_KEY
      valueFrom:
        secretKeyRef:
          name: purser-license-vault
          key: license-key
```

---

## Path 3: Azure Key Vault (ESO with Azure provider)

Same pattern as HashiCorp Vault above, using the ESO Azure Key Vault provider.

### ClusterSecretStore for Azure Key Vault

```yaml
apiVersion: external-secrets.io/v1beta1
kind: ClusterSecretStore
metadata:
  name: azure-keyvault
spec:
  provider:
    azurekv:
      tenantId: "<azure-tenant-id>"
      vaultUrl: "https://purser-secrets.vault.azure.net"
      authType: WorkloadIdentity
```

### ExternalSecret

```yaml
apiVersion: external-secrets.io/v1beta1
kind: ExternalSecret
metadata:
  name: purser-azure-secrets
  namespace: default
spec:
  refreshInterval: 1h
  secretStoreRef:
    name: azure-keyvault
    kind: ClusterSecretStore
  target:
    name: purser-azure-vault
    creationPolicy: Owner
  data:
    - secretKey: gateway-internal-token
      remoteRef:
        key: purser-gateway-internal-token
    - secretKey: gateway-api-keys
      remoteRef:
        key: purser-gateway-api-keys
    - secretKey: license-key
      remoteRef:
        key: purser-license-key
```

Then reference `purser-azure-vault` in `controlPlane.extraEnv` / `gateway.extraEnv` as in Path 2.

---

## Agent join tokens

Agent join tokens are short-lived (default 1 hour TTL) and single-use — they are generated at enrollment time and are not long-lived secrets:

```bash
# Mint a join token
curl -sS -X POST http://<control-plane>:8080/api/v1/join-token

# Mint with custom TTL (5 minutes for automated enrollment)
curl -sS -X POST http://<control-plane>:8080/api/v1/join-token \
  -H "Content-Type: application/json" \
  -d '{"ttl_seconds": 300}'
```

The returned token is used once by the agent's `PURSER_JOIN_TOKEN` env var during enrollment, then expires. You can automate token distribution through your secrets manager as a short-lived secret with automatic rotation.
