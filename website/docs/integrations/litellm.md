# LiteLLM Integration

[LiteLLM](https://docs.litellm.ai/) is a unified LLM proxy that supports hundreds of providers via a common OpenAI-compatible interface. Purser's API Gateway is OpenAI-compatible, so LiteLLM can use Purser as a backend with no special plugin.

---

## LiteLLM config.yaml

Create or extend your LiteLLM configuration file:

```yaml
# litellm_config.yaml
model_list:
  - model_name: llama-8b
    litellm_params:
      model: openai/llama-8b
      api_base: http://<purser-gateway-host>:<port>/v1
      api_key: <PURSER_GATEWAY_API_KEY>

  # Multiple models from the same Purser cluster
  - model_name: llama-70b
    litellm_params:
      model: openai/llama-70b
      api_base: http://<purser-gateway-host>:<port>/v1
      api_key: <PURSER_GATEWAY_API_KEY>

general_settings:
  master_key: "sk-litellm-master-key"
```

Start LiteLLM:

```bash
litellm --config litellm_config.yaml --port 4000
```

Test through LiteLLM:

```bash
curl -sS http://localhost:4000/v1/chat/completions \
  -H "Authorization: Bearer sk-litellm-master-key" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "llama-8b",
    "messages": [{"role": "user", "content": "Hello from LiteLLM"}]
  }'
```

---

## Getting a Purser API key

Create a Gateway API key via the Control Plane:

```bash
curl -sS -X POST http://<control-plane>:8080/api/v1/apikeys \
  -H "Content-Type: application/json" \
  -d '{"name": "litellm-proxy", "tenant": "litellm"}'
```

The `key` in the response (`psk_...`) is your `PURSER_GATEWAY_API_KEY`. Store it securely — it is only returned once.

!!! warning "API keys are returned once"
    The plaintext key is returned only at creation time. Only the SHA-256 hash is persisted. If you lose the key, create a new one with `POST /api/v1/apikeys` and revoke the old one with `DELETE /api/v1/apikeys/{id}`.

---

## Health check configuration

LiteLLM performs health checks before routing traffic. To enable proper health checking:

```yaml
model_list:
  - model_name: llama-8b
    litellm_params:
      model: openai/llama-8b
      api_base: http://<purser-gateway-host>:<port>/v1
      api_key: <PURSER_GATEWAY_API_KEY>
    model_info:
      # LiteLLM health check uses GET /v1/models — Purser supports this
      mode: "chat"
```

The Purser Gateway's `GET /v1/models` endpoint returns all models with active deployments. LiteLLM can use this to confirm the model is available before routing requests.

---

## Error code mapping

LiteLLM interprets standard HTTP status codes. Purser returns:

| Purser status | LiteLLM interpretation | When |
|---|---|---|
| `200` / `201` | Success | Normal completion or streaming |
| `401` | Authentication error | Missing or invalid `Authorization: Bearer` header |
| `429` | Rate limit exceeded — LiteLLM retries with exponential backoff | Token rate, per-key concurrency, or global backpressure limit hit |
| `503` | Model unavailable — LiteLLM marks the backend unhealthy and may fall back | The deployment host is unreachable (gateway connect timeout) |
| `504` | Timeout — LiteLLM may retry | Gateway time-to-first-byte timeout exceeded |

LiteLLM's automatic retry-on-429 works with Purser's `Retry-After` header (set by `PURSER_GATEWAY_RETRY_AFTER_SECS`, default 2 seconds).

Configure LiteLLM retry behavior:

```yaml
general_settings:
  num_retries: 3
  request_timeout: 120  # seconds; set higher for large models / slow networks
```

---

## Using OIDC + LiteLLM together

When OIDC is enabled on the Control Plane (Enterprise), human operator access to `/api/v1` requires an OIDC session. LiteLLM's requests to the **Gateway** (`/v1/chat/completions`) bypass OIDC — they authenticate with a **Gateway API key** (bearer token), not with OIDC.

The correct setup:

1. Create a dedicated Gateway API key for LiteLLM (service account key):

    ```bash
    curl -sS -X POST http://<control-plane>:8080/api/v1/apikeys \
      -H "Content-Type: application/json" \
      -d '{"name": "litellm-svc", "tenant": "litellm"}'
    ```

2. Use the returned `psk_...` key as the `api_key` in LiteLLM's config. This key is validated by the Gateway, not by the Control Plane OIDC layer.

3. Restrict LiteLLM's access to only the Gateway endpoint — it does not need access to the Control Plane REST API (`/api/v1`).

---

## OpenAI Python SDK through LiteLLM

```python
from openai import OpenAI

# Connect to LiteLLM (which proxies to Purser)
client = OpenAI(
    base_url="http://localhost:4000/v1",
    api_key="sk-litellm-master-key"
)

# Non-streaming
response = client.chat.completions.create(
    model="llama-8b",
    messages=[{"role": "user", "content": "Explain pipeline parallelism"}]
)
print(response.choices[0].message.content)

# Streaming
stream = client.chat.completions.create(
    model="llama-8b",
    messages=[{"role": "user", "content": "Hello"}],
    stream=True
)
for chunk in stream:
    if chunk.choices[0].delta.content:
        print(chunk.choices[0].delta.content, end="", flush=True)
```

---

## Direct connection (bypassing LiteLLM)

If you don't need LiteLLM's multi-provider routing, connect directly to Purser's Gateway:

```python
from openai import OpenAI

client = OpenAI(
    base_url="http://<purser-gateway-host>:<port>/v1",
    api_key="psk_<your-gateway-api-key>"
)

response = client.chat.completions.create(
    model="llama-8b",
    messages=[{"role": "user", "content": "Hello"}],
    stream=True
)
```

The Purser Gateway is a drop-in replacement for the OpenAI API base URL — all standard OpenAI SDK options work.
