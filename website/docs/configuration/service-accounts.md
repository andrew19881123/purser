# Service Accounts

Service accounts give CI/CD pipelines and automation scripts a secure, non-interactive way to authenticate with the Purser control plane.

Instead of sharing long-lived API keys, a service account issues a **short-lived JWT (15 minutes)** via the OAuth2 `client_credentials` grant. The secret is only transmitted once — during the token exchange — and never again for subsequent API calls.

---

## API keys vs service accounts

| Property | API key | Service account |
|---|---|---|
| Credential type | Static bearer token | `client_id` + `client_secret` |
| Token TTL | Long-lived (never expires by default) | 15 minutes |
| Secret travels per request | **Yes** | No — only at token exchange |
| Best for | Simple scripts, manual tooling | CI/CD, scheduled jobs, GitOps |
| Rotation | Manual `POST …/rotate` | Each token request is fresh |

---

## Creating a service account

```bash
curl -X POST https://purser.example.com/api/v1/service-accounts \
  -H "Authorization: Bearer <admin-key>" \
  -H "Content-Type: application/json" \
  -d '{
    "name": "github-actions",
    "role": "inference",
    "tenant": "ml-team"
  }'
```

**Response** (save `client_secret` — shown only once):

```json
{
  "id": "sa-4a7f2c91e830b5d6",
  "client_id": "sa_2e9f1a3b",
  "client_secret": "y3rXvlO9…",
  "role": "inference",
  "tenant": "ml-team",
  "message": "Copy the client_secret now — it is shown only once."
}
```

### Available roles

| Role | Access |
|---|---|
| `inference` | Gateway inference endpoints only; cannot call `/api/v1` management surface |
| `viewer` | Read-only access to all `/api/v1` endpoints |
| `admin` | Full management access |

Default role is `inference` if not specified.

### Optional fields

| Field | Description |
|---|---|
| `scopes` | JSON array of fine-grained permission strings (informational) |
| `expires_at` | RFC3339 timestamp after which the account is automatically disabled |

---

## Obtaining a token (`client_credentials` grant)

```bash
TOKEN=$(curl -s -X POST https://purser.example.com/auth/token \
  -d "grant_type=client_credentials" \
  -d "client_id=sa_2e9f1a3b" \
  -d "client_secret=y3rXvlO9…" \
  | jq -r .access_token)
```

**Response:**

```json
{
  "access_token": "eyJzdWIiOiJz…",
  "token_type": "Bearer",
  "expires_in": 900
}
```

The token is valid for **900 seconds (15 minutes)**. Refresh it before it expires.

---

## Using the token

Pass the token as a standard `Authorization: Bearer` header:

```bash
curl https://purser.example.com/api/v1/nodes \
  -H "Authorization: Bearer $TOKEN"
```

---

## Examples

### curl (manual test)

```bash
# 1. Get token
TOKEN=$(curl -s -X POST https://purser.example.com/auth/token \
  -d "grant_type=client_credentials&client_id=sa_2e9f1a3b&client_secret=y3rXvlO9…" \
  | jq -r .access_token)

# 2. Use token
curl -H "Authorization: Bearer $TOKEN" \
  https://purser.example.com/api/v1/models
```

### Python

```python
import requests

def get_purser_token(base_url, client_id, client_secret):
    resp = requests.post(
        f"{base_url}/auth/token",
        data={
            "grant_type": "client_credentials",
            "client_id": client_id,
            "client_secret": client_secret,
        },
    )
    resp.raise_for_status()
    return resp.json()["access_token"]

token = get_purser_token(
    "https://purser.example.com",
    "sa_2e9f1a3b",
    "y3rXvlO9…",
)
headers = {"Authorization": f"Bearer {token}"}
models = requests.get("https://purser.example.com/api/v1/models", headers=headers).json()
```

### GitHub Actions workflow

```yaml
name: Deploy model

on:
  push:
    branches: [main]

jobs:
  deploy:
    runs-on: ubuntu-latest
    steps:
      - name: Get Purser token
        id: auth
        run: |
          TOKEN=$(curl -s -X POST "${{ vars.PURSER_URL }}/auth/token" \
            -d "grant_type=client_credentials" \
            -d "client_id=${{ vars.PURSER_CLIENT_ID }}" \
            -d "client_secret=${{ secrets.PURSER_CLIENT_SECRET }}" \
            | jq -r .access_token)
          echo "token=$TOKEN" >> $GITHUB_OUTPUT

      - name: List nodes
        run: |
          curl -H "Authorization: Bearer ${{ steps.auth.outputs.token }}" \
            "${{ vars.PURSER_URL }}/api/v1/nodes"
```

Store `PURSER_CLIENT_SECRET` as a **GitHub Actions secret**, `PURSER_CLIENT_ID` and `PURSER_URL` as variables.

---

## Listing service accounts

```bash
curl -H "Authorization: Bearer <admin-key>" \
  https://purser.example.com/api/v1/service-accounts
```

Response includes all accounts. The `client_secret` is **never** returned here.

---

## Revoking a service account

```bash
curl -X DELETE \
  -H "Authorization: Bearer <admin-key>" \
  https://purser.example.com/api/v1/service-accounts/sa-4a7f2c91e830b5d6
```

Revocation is immediate and soft: the account is disabled (`enabled=0`). Any tokens already issued will expire naturally at their 15-minute TTL.

---

## Security notes

!!! warning "Keep `client_secret` private"
    The `client_secret` is shown **only once** at creation time. Store it in a secrets manager (HashiCorp Vault, AWS Secrets Manager, GitHub Actions secrets, etc.). If you lose it, create a new service account and revoke the old one.

!!! tip "Token TTL is 15 minutes"
    Tokens expire automatically. This limits the blast radius of a leaked token compared to a long-lived API key. Re-acquire a token before the previous one expires.

!!! info "No DB lookup per request"
    Service account JWTs are HMAC-SHA256 signed and verified entirely in memory. There is no database round-trip on each authenticated request — only at token issuance.

### Environment variable

Set `PURSER_SESSION_SECRET` to a stable 32-byte hex key so tokens survive control-plane restarts:

```bash
export PURSER_SESSION_SECRET=$(openssl rand -hex 32)
```

Without it, an ephemeral key is auto-generated at startup, and all tokens are invalidated when the process restarts.
