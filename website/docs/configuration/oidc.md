# OIDC Configuration (EntraID / Okta / Keycloak)

!!! note "OIDC is available in the community edition"
    As of v0.3, OIDC authentication includes a full **Authorization Code Flow + PKCE** browser SSO endpoint. Set `PURSER_OIDC_ISSUER`, `PURSER_OIDC_CLIENT_ID`, and `PURSER_OIDC_REDIRECT_URI` to enable it. Machine-to-machine access (API keys, the internal gateway token) also continues to work regardless of OIDC state.

---

## What OIDC protects

When configured, OIDC protects:

- The **Admin UI** (Dashboard)
- The **Control Plane REST API** (`/api/v1`) — human operator access

What is **exempt** from OIDC:

- **`GET /auth/login` and `GET /auth/callback`** — the login flow endpoints themselves are unauthenticated
- **Gateway API keys** — machine-to-machine inference traffic uses bearer tokens, which bypass OIDC
- **Internal gateway token** — Control Plane → Gateway route sync is a separate shared secret, unaffected by OIDC
- **Agent gRPC** — Agent enrollment and heartbeat use mTLS certificates issued by the internal PKI, not OIDC

---

## Environment variables

| Variable | Description |
|---|---|
| `PURSER_OIDC_ISSUER` | OIDC issuer URL (the provider's discovery document root). Examples: `https://login.microsoftonline.com/<tenant-id>/v2.0`, `https://<tenant>.okta.com`, `https://keycloak.example.com/realms/<realm>` |
| `PURSER_OIDC_CLIENT_ID` | OAuth2 application (client) ID registered with the provider. |
| `PURSER_OIDC_CLIENT_SECRET` | OAuth2 client secret for confidential clients. Optional — leave unset for public PKCE-only clients. |
| `PURSER_OIDC_REDIRECT_URI` | Full callback URL registered with the IdP, e.g. `https://purser.example.com/auth/callback`. **Required** to enable the Authorization Code Flow + PKCE browser SSO endpoints (`GET /auth/login`, `GET /auth/callback`). |
| `PURSER_SESSION_SECRET` | 64-character hex-encoded 32-byte HMAC key used to sign session cookies. When unset, an ephemeral random key is generated at startup (sessions expire on process restart). **Set this for persistent sessions** across restarts or rolling deployments. Generate with: `openssl rand -hex 32` |
| `PURSER_OIDC_GROUP_MAPPINGS` | JSON object mapping OIDC group/role claim values to Purser roles. See [Group claim mapping](#group-claim-mapping) below. |

---

## Authorization Code Flow setup (browser SSO)

As of v0.3, Purser implements the **OAuth 2.0 Authorization Code Flow with PKCE** (RFC 7636) server-side. The browser never handles the code_verifier — all PKCE state is managed by the control plane.

### How it works

1. Browser hits a protected page → oidcMiddleware detects no valid session → redirects to `GET /auth/login`.
2. `GET /auth/login` generates a cryptographic `state` (32 random bytes, hex) and PKCE `code_verifier` (32 random bytes, base64url), computes `code_challenge = base64url(SHA256(verifier))`, stores `state→verifier` for 10 minutes, and redirects the browser to the IdP.
3. User authenticates at the IdP; IdP redirects to `GET /auth/callback?code=…&state=…`.
4. `GET /auth/callback` validates the `state`, exchanges the code for tokens at the IdP's token endpoint (attaching `code_verifier`), verifies the returned ID token, and sets an HttpOnly session cookie (`purser_session`, 8h TTL, HMAC-SHA256 signed).
5. Browser is redirected to `/`. Subsequent API and UI requests are authenticated via the session cookie.

### Session cookie properties

| Property | Value |
|---|---|
| Name | `purser_session` |
| HttpOnly | Yes |
| Secure | Yes (when served over HTTPS) |
| SameSite | Lax |
| TTL | 8 hours |
| Signature | HMAC-SHA256, key from `PURSER_SESSION_SECRET` |

---

## Microsoft EntraID (Azure AD) — Authorization Code Flow

### 1. Register an application in Entra ID

1. In the Azure portal, go to **Azure Active Directory → App registrations → New registration**.
2. Set:
   - Name: `Purser`
   - Redirect URI (Web): `https://purser.example.com/auth/callback`
3. After creation, note the **Application (client) ID** and **Directory (tenant) ID**.
4. Under **Certificates & secrets**, create a client secret and note the value.
5. Under **API permissions**, add `openid`, `profile`, `email` (delegated, Microsoft Graph).

### 2. Configure Purser

```bash
PURSER_OIDC_ISSUER=https://login.microsoftonline.com/<tenant-id>/v2.0
PURSER_OIDC_CLIENT_ID=<application-client-id>
PURSER_OIDC_CLIENT_SECRET=<client-secret-value>
PURSER_OIDC_REDIRECT_URI=https://purser.example.com/auth/callback
PURSER_SESSION_SECRET=$(openssl rand -hex 32)
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
    - name: PURSER_OIDC_REDIRECT_URI
      value: "https://purser.example.com/auth/callback"
    - name: PURSER_SESSION_SECRET
      valueFrom:
        secretKeyRef:
          name: purser-oidc
          key: session-secret
```

Create the secret:

```bash
kubectl create secret generic purser-oidc \
  --from-literal=client-secret=<client-secret-value> \
  --from-literal=session-secret=$(openssl rand -hex 32)
```

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
PURSER_OIDC_REDIRECT_URI=https://purser.example.com/auth/callback
PURSER_SESSION_SECRET=$(openssl rand -hex 32)
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
    - name: PURSER_OIDC_REDIRECT_URI
      value: "https://purser.example.com/auth/callback"
    - name: PURSER_SESSION_SECRET
      valueFrom:
        secretKeyRef:
          name: purser-oidc
          key: session-secret
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
PURSER_OIDC_REDIRECT_URI=https://purser.example.com/auth/callback
PURSER_SESSION_SECRET=$(openssl rand -hex 32)
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
    - name: PURSER_OIDC_REDIRECT_URI
      value: "https://purser.example.com/auth/callback"
    - name: PURSER_SESSION_SECRET
      valueFrom:
        secretKeyRef:
          name: purser-oidc
          key: session-secret
```

---

---

## Group claim mapping

When your IdP assigns users to groups (or grants them app roles), Purser can
automatically derive an RBAC role from the token's `groups` or `roles` claim —
no API key required.

### How it works

1. The token is verified by the IdP as usual.
2. `oidcMiddleware` extracts the `groups` **and** `roles` arrays from the token.
3. Each value is looked up in the `PURSER_OIDC_GROUP_MAPPINGS` dictionary.
4. If one or more matches are found, the highest-privilege mapping wins:
   `admin > inference > viewer`.
5. The resolved role is injected into the request context. `rbacMiddleware`
   enforces it exactly like an API-key role — no additional API key lookup.
6. If no mapping matches, the request falls through to the API-key RBAC path
   (the user is OIDC-authenticated but has no automatic RBAC assignment).

### Configuration

Set `PURSER_OIDC_GROUP_MAPPINGS` to a JSON object:

```bash
PURSER_OIDC_GROUP_MAPPINGS='{"purser-admins":"admin","purser-viewers":"viewer"}'
```

The keys are the exact string values from the token's `groups` or `roles` claim.
The values are Purser roles: `admin`, `viewer`, or `inference`.

### Provider examples

=== "Microsoft EntraID"

    EntraID populates the `groups` claim with Object IDs (GUIDs) by default, or
    with group names if the `groupMembershipClaims` manifest setting is
    `"SecurityGroup"` and the group names are included. The `roles` claim carries
    app-role assignment values. Use whichever your IdP emits:

    ```bash
    # Using app roles (recommended — human-readable, stable)
    PURSER_OIDC_GROUP_MAPPINGS='{"Purser.Admin":"admin","Purser.Viewer":"viewer"}'

    # Using group Object IDs
    PURSER_OIDC_GROUP_MAPPINGS='{"aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee":"admin"}'
    ```

=== "Okta"

    Okta populates the `groups` claim with group names when the OIDC app's
    **Group claims filter** is configured:

    ```bash
    PURSER_OIDC_GROUP_MAPPINGS='{"Purser-Admins":"admin","Purser-Viewers":"viewer"}'
    ```

=== "Keycloak"

    Keycloak populates `groups` with group paths (e.g. `/purser/admins`) or
    `roles` with realm/client role names depending on your mapper config:

    ```bash
    PURSER_OIDC_GROUP_MAPPINGS='{"purser-admins":"admin","purser-viewers":"viewer"}'
    ```

### Tenant scoping

When the OIDC token carries a `tid` (EntraID) or `tenant_id` claim, Purser
stores it in the request context. Viewer-role tokens with a tenant claim receive
scoped list responses:

- `GET /api/v1/deployments` — returns only deployments whose `Detail.tenant`
  field matches the token's tenant.

This is the foundational isolation layer; models and API keys will be scoped
in follow-up releases.

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
