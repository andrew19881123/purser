# Service Accounts

Service accounts give CI/CD pipelines and automation scripts a secure, non-interactive way to authenticate with the Purser control plane.

Instead of sharing long-lived API keys, a service account issues a **short-lived JWT (15 minutes)** via the OAuth2 `client_credentials` grant. The secret is only transmitted once — during the token exchange — and never again for subsequent API calls.

!!! info "Service accounts are team-level credentials"
    From v0.5, service accounts belong to a **team**, not to an individual user. This aligns with LiteLLM and proxy auth patterns where a single service account provides machine-to-machine access on behalf of a team. Use `team_id` when creating a service account. The legacy `tenant` field is still accepted for backward compatibility.

---

## API keys vs service accounts

| Property | API key | Service account |
|---|---|---|
| Credential type | Static bearer token | `client_id` + `client_secret` |
| Token TTL | Long-lived (never expires by default) | 15 minutes |
| Secret travels per request | **Yes** | No — only at token exchange |
| Belongs to | Individual user / tenant | **Team** (machine-to-machine) |
| Best for | Simple scripts, manual tooling | CI/CD, scheduled jobs, GitOps, LiteLLM |
| Rotation | Manual `POST …/rotate` | Each token request is fresh |

---

## Creating a service account

```bash
curl -X POST https://purser.example.com/api/v1/service-accounts \
  -H "Authorization: Bearer <admin-key>" \
  -H "Content-Type: application/json" \
  -d '{
    "name": "github-actions",
    "team_id": "team-ml",
    "role": "inference",
    "description": "GitHub Actions CI pipeline for the ML team"
  }'
```

**Response** (save `client_secret` — shown only once):

```json
{
  "id": "sa-4a7f2c91e830b5d6",
  "client_id": "sa_2e9f1a3b",
  "client_secret": "y3rXvlO9…",
  "role": "inference",
  "team_id": "team-ml",
  "tenant": "team-ml",
  "message": "Copy the client_secret now — it is shown only once."
}
```

### Request fields

| Field | Required | Description |
|---|---|---|
| `name` | Yes | Human-readable name for the service account |
| `team_id` | **Yes (v0.5+)** | The team this SA belongs to; scopes all usage attribution to that team |
| `role` | No | `inference` (default), `viewer`, or `admin` |
| `description` | No | Optional free-text description |
| `scopes` | No | JSON array of fine-grained permission strings |
| `expires_at` | No | RFC3339 timestamp after which the account is automatically disabled |

!!! warning "Deprecated: `tenant` field"
    The `tenant` field is still accepted for backward compatibility with clients that have not migrated to `team_id`. If **both** `team_id` and `tenant` are supplied, `team_id` takes precedence. New code should always use `team_id`.

### Available roles

| Role | Access |
|---|---|
| `inference` | Gateway inference endpoints only; cannot call `/api/v1` management surface |
| `viewer` | Read-only access to all `/api/v1` endpoints |
| `admin` | Full management access |

Default role is `inference` if not specified.

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

## LiteLLM integration

Service accounts are the recommended credential for [LiteLLM](https://docs.litellm.ai/) and other OpenAI-compatible proxies. Each team creates one service account; LiteLLM exchanges it for short-lived tokens, keeping the long-lived secret out of inference traffic.

### Step 1 — Create a team service account

```bash
curl -X POST https://purser.example.com/api/v1/service-accounts \
  -H "Authorization: Bearer <admin-key>" \
  -H "Content-Type: application/json" \
  -d '{
    "name": "litellm-proxy",
    "team_id": "team-ml",
    "role": "inference",
    "description": "LiteLLM proxy credential for team-ml"
  }'
```

Save the returned `client_id` and `client_secret` in your secrets manager.

### Step 2 — Configure LiteLLM

In your `litellm_config.yaml`, use the `custom_auth` flow or set the bearer token directly from a token-refresh script:

```yaml
model_list:
  - model_name: llama3-70b
    litellm_params:
      model: openai/llama3-70b
      api_base: https://purser.example.com/v1
      api_key: os.environ/PURSER_BEARER_TOKEN

environment_variables:
  PURSER_BEARER_TOKEN: ""   # populated at runtime by token-refresh sidecar
```

### Step 3 — Token-refresh sidecar (recommended)

Because Purser tokens expire in 15 minutes, run a refresh loop alongside LiteLLM:

```bash
#!/usr/bin/env bash
# refresh-token.sh — runs as a sidecar, writes token to a shared env file
set -euo pipefail

while true; do
  TOKEN=$(curl -sf -X POST "${PURSER_URL}/auth/token" \
    -d "grant_type=client_credentials" \
    -d "client_id=${PURSER_CLIENT_ID}" \
    -d "client_secret=${PURSER_CLIENT_SECRET}" \
    | jq -r .access_token)

  # Write to the file LiteLLM reads (or export to the process environment).
  echo "PURSER_BEARER_TOKEN=${TOKEN}" > /run/secrets/purser.env
  sleep 600   # refresh every 10 minutes (token TTL is 15 min)
done
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
