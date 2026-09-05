# OIDC Configuration (EntraID / Okta / Keycloak)

!!! note "OIDC is available in the community edition"
    As of v0.2, OIDC authentication is **implemented** and available without an enterprise license. Set `PURSER_OIDC_ISSUER` and `PURSER_OIDC_CLIENT_ID` to enable it. Machine-to-machine access (API keys, the internal gateway token) also continues to work regardless of OIDC state.

---

## What OIDC protects

When configured, OIDC protects:

- The **Admin UI** (Dashboard)
- The **Control Plane REST API** (`/api/v1`) — human operator access

What is **exempt** from OIDC:

- **Gateway API keys** — machine-to-machine inference traffic uses bearer tokens (`PURSER_GATEWAY_API_KEYS`), which bypass OIDC
- **Internal gateway token** — Control Plane → Gateway route sync (`PURSER_GATEWAY_INTERNAL_TOKEN`) is a separate shared secret, unaffected by OIDC
- **Agent gRPC** — Agent enrollment and heartbeat use mTLS certificates issued by the internal PKI, not OIDC

---

## Environment variables

| Variable | Description |
|---|---|
| `PURSER_OIDC_ISSUER` | OIDC issuer URL (the provider's discovery document root). Examples: `https://login.microsoftonline.com/<tenant-id>/v2.0`, `https://<tenant>.okta.com`, `https://keycloak.example.com/realms/<realm>` |
| `PURSER_OIDC_CLIENT_ID` | OAuth2 application (client) ID registered with the provider. |
| `PURSER_OIDC_CLIENT_SECRET` | OAuth2 client secret (for confidential clients). |
| `PURSER_LICENSE_KEY` | Enterprise license key — OIDC requires a valid license with the SSO/OIDC feature entitlement. |

---

## Microsoft EntraID (Azure AD)

### 1. Register an application in Entra ID

1. In the Azure portal, go to **Azure Active Directory → App registrations → New registration**.
2. Set:
   - Name: `Purser`
   - Redirect URI: `https://purser.example.com/auth/callback` (your Purser hostname)
3. After creation, note the **Application (client) ID** and **Directory (tenant) ID**.
4. Under **Certificates & secrets**, create a client secret and note the value.
5. Under **API permissions**, add `openid`, `profile`, `email`.

### 2. Configure Purser

```bash
PURSER_OIDC_ISSUER=https://login.microsoftonline.com/<tenant-id>/v2.0
PURSER_OIDC_CLIENT_ID=<application-client-id>
PURSER_OIDC_CLIENT_SECRET=<client-secret-value>
```

### 3. Helm values.yaml snippet

```yaml
controlPlane:
  extraEnv:
    - name: PURSER_OIDC_ISSUER
      value: "https://login.microsoftonline.com/<tenant-id>/v2.0"
    - name: PURSER_OIDC_CLIENT_ID
      value: "<application-client-id>"
    - name: PURSER_OIDC_CLIENT_SECRET
      valueFrom:
        secretKeyRef:
          name: purser-oidc
          key: client-secret
```

Create the secret:

```bash
kubectl create secret generic purser-oidc \
  --from-literal=client-secret=<client-secret-value>
```

---

## Okta

### 1. Create an OIDC application in Okta

1. In the Okta Admin Console, go to **Applications → Create App Integration**.
2. Select **OIDC - OpenID Connect** and **Web Application**.
3. Set:
   - App name: `Purser`
   - Sign-in redirect URIs: `https://purser.example.com/auth/callback`
4. Note the **Client ID** and **Client secret**.
5. Set the **Okta domain** (e.g. `your-tenant.okta.com`).

### 2. Configure Purser

```bash
PURSER_OIDC_ISSUER=https://your-tenant.okta.com
PURSER_OIDC_CLIENT_ID=<client-id>
PURSER_OIDC_CLIENT_SECRET=<client-secret>
```

### 3. Helm values.yaml snippet

```yaml
controlPlane:
  extraEnv:
    - name: PURSER_OIDC_ISSUER
      value: "https://your-tenant.okta.com"
    - name: PURSER_OIDC_CLIENT_ID
      value: "<client-id>"
    - name: PURSER_OIDC_CLIENT_SECRET
      valueFrom:
        secretKeyRef:
          name: purser-oidc
          key: client-secret
```

---

## Keycloak

### 1. Create a client in Keycloak

1. In the Keycloak Admin Console, go to your realm → **Clients → Create**.
2. Set:
   - Client ID: `purser`
   - Client Protocol: `openid-connect`
   - Access Type: `confidential`
   - Valid Redirect URIs: `https://purser.example.com/auth/callback`
3. Save, then go to the **Credentials** tab and note the **Secret**.

### 2. Configure Purser

```bash
PURSER_OIDC_ISSUER=https://keycloak.example.com/realms/<realm>
PURSER_OIDC_CLIENT_ID=purser
PURSER_OIDC_CLIENT_SECRET=<keycloak-client-secret>
```

### 3. Helm values.yaml snippet

```yaml
controlPlane:
  extraEnv:
    - name: PURSER_OIDC_ISSUER
      value: "https://keycloak.example.com/realms/<realm>"
    - name: PURSER_OIDC_CLIENT_ID
      value: "purser"
    - name: PURSER_OIDC_CLIENT_SECRET
      valueFrom:
        secretKeyRef:
          name: purser-oidc
          key: client-secret
```

---

## Service account API keys (bypassing OIDC)

For machine-to-machine access (CI pipelines, LiteLLM, automated scripts), use a **Gateway API key** rather than OIDC. API keys are configured on the Gateway and bypass the OIDC flow entirely:

1. Create an API key via the Control Plane REST API:

    ```bash
    curl -sS -X POST http://<control-plane>:8080/api/v1/apikeys \
      -H "Content-Type: application/json" \
      -d '{"name": "ci-service-account", "tenant": "default"}'
    ```

2. Use the returned `key` value as a bearer token for Gateway requests:

    ```bash
    Authorization: Bearer psk_<your-api-key>
    ```

This key is validated by the Gateway without going through OIDC. It is independent of the OIDC configuration on the Control Plane.
