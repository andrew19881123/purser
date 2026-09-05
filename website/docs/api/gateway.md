# Gateway API Reference (`/v1` OpenAI-compatible)

The Purser API Gateway exposes an OpenAI-compatible inference API. Any OpenAI SDK or tool that supports a custom `base_url` works with Purser without modification.

**Base URL:** `http://<gateway-host>:<port>`

The gateway serves **plaintext HTTP**. TLS is terminated upstream at the ingress / load balancer, consistent with Purser's trusted-LAN model.

---

## Authentication

All inference endpoints require a bearer token:

```http
Authorization: Bearer psk_<your-api-key>
```

API keys are created via the Control Plane (`POST /api/v1/apikeys`) and stored as gateway API keys (`PURSER_GATEWAY_API_KEYS`).

**Dev mode:** If `PURSER_GATEWAY_API_KEYS` is not set, the gateway accepts any non-empty bearer token and maps all requests to the `default` tenant. Never leave this unset in production.

**Auth errors:**

| Status | When |
|---|---|
| `401` | Missing `Authorization` header, empty token, or missing `Bearer ` prefix |
| `401` | Token not recognized (when keys are configured) |

---

## Chat Completions

### `POST /v1/chat/completions`

Creates a model response for the given conversation. Supports both buffered and streaming (SSE) responses. This is a drop-in replacement for the OpenAI Chat Completions API.

**Request:**

```json
{
  "model": "llama-8b",
  "messages": [
    {"role": "system", "content": "You are a helpful assistant."},
    {"role": "user", "content": "Hello"}
  ],
  "stream": false
}
```

`stream: true` enables Server-Sent Events (SSE) streaming.

**Buffered response `200`:**

```json
{
  "id": "chatcmpl-abc123",
  "object": "chat.completion",
  "model": "llama-8b",
  "choices": [
    {
      "index": 0,
      "message": {
        "role": "assistant",
        "content": "Hello! How can I help you today?"
      },
      "finish_reason": "stop"
    }
  ]
}
```

**Streaming response `200`:**

Content type: `text/event-stream`

```
data: {"id":"chatcmpl-abc123","object":"chat.completion.chunk","model":"llama-8b","choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":null}]}

data: {"id":"chatcmpl-abc123","object":"chat.completion.chunk","model":"llama-8b","choices":[{"index":0,"delta":{"content":"Hello"},"finish_reason":null}]}

data: {"id":"chatcmpl-abc123","object":"chat.completion.chunk","model":"llama-8b","choices":[{"index":0,"delta":{"content":"!"},"finish_reason":null}]}

data: {"id":"chatcmpl-abc123","object":"chat.completion.chunk","model":"llama-8b","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}

data: [DONE]
```

**Error responses:**

| Status | Error code | When |
|---|---|---|
| `401` | `Unauthorized` | Missing or invalid bearer token |
| `404` | `model_not_found` | Model not deployed or not in the routing table |
| `429` | `rate_limited` | Token rate limit, per-key concurrency, or global backpressure exceeded. Includes `Retry-After` header and `X-Queue-Position` header. |
| `503` | `upstream_unavailable` | The deployment host is unreachable (connect timeout or refused connection) |
| `504` | `upstream_timeout` | The deployment host did not respond within the time-to-first-byte timeout |

**429 response example:**

```json
{
  "error": {
    "message": "Token rate limit exceeded (60000 tokens/min for this key).",
    "type": "rate_limit_error"
  }
}
```

With headers:
```
Retry-After: 2
X-Queue-Position: 0
```

---

## Models

### `GET /v1/models`

Lists all models with active deployments and active routes (populated by the Control Plane via route sync).

**Response `200`:**

```json
{
  "object": "list",
  "data": [
    {
      "id": "llama-8b",
      "object": "model",
      "created": 1725494400
    }
  ]
}
```

If no models are deployed, `data` is an empty array `[]`.

---

## Internal route sync (Control Plane only)

These endpoints are for the Control Plane's orchestrator to push routing updates. They are protected by `X-Purser-Internal-Token` and should not be called by clients.

### `PUT /api/v1/routes`

Adds or updates a route (maps a model ID to a deployment host endpoint).

### `DELETE /api/v1/routes`

Removes a route.

---

## Rate limiting and backpressure

The gateway enforces three independent limits (all configurable via env vars):

1. **Token rate** (`PURSER_GATEWAY_TOKENS_PER_MIN`, default 60,000) — per-key token bucket. Prompt tokens charged up front; completion tokens charged as they stream.
2. **Per-key concurrency** (`PURSER_GATEWAY_MAX_CONCURRENT`, default 32) — max simultaneous in-flight requests per API key.
3. **Global backpressure** (`PURSER_GATEWAY_MAX_INFLIGHT`, default 512) — global ceiling across all keys.

Any limit exceeded returns `429` with a `Retry-After` header. Set any limit to `0` to disable it.

---

## OpenAI SDK usage

=== "Python"

    ```python
    from openai import OpenAI

    client = OpenAI(
        base_url="http://<gateway-host>:<port>/v1",
        api_key="psk_<your-api-key>"
    )

    # Non-streaming
    response = client.chat.completions.create(
        model="llama-8b",
        messages=[{"role": "user", "content": "Hello"}]
    )
    print(response.choices[0].message.content)

    # Streaming
    with client.chat.completions.create(
        model="llama-8b",
        messages=[{"role": "user", "content": "Hello"}],
        stream=True
    ) as stream:
        for text in stream.text_stream:
            print(text, end="", flush=True)
    ```

=== "Node.js"

    ```javascript
    import OpenAI from "openai";

    const client = new OpenAI({
      baseURL: "http://<gateway-host>:<port>/v1",
      apiKey: "psk_<your-api-key>",
    });

    const stream = await client.chat.completions.create({
      model: "llama-8b",
      messages: [{ role: "user", content: "Hello" }],
      stream: true,
    });

    for await (const chunk of stream) {
      process.stdout.write(chunk.choices[0]?.delta?.content || "");
    }
    ```

=== "curl"

    ```bash
    # Non-streaming
    curl -sS http://<gateway-host>:<port>/v1/chat/completions \
      -H "Authorization: Bearer psk_<your-api-key>" \
      -H "Content-Type: application/json" \
      -d '{
        "model": "llama-8b",
        "messages": [{"role": "user", "content": "Hello"}],
        "stream": false
      }'

    # Streaming (SSE)
    curl -N http://<gateway-host>:<port>/v1/chat/completions \
      -H "Authorization: Bearer psk_<your-api-key>" \
      -H "Content-Type: application/json" \
      -d '{
        "model": "llama-8b",
        "messages": [{"role": "user", "content": "Hello"}],
        "stream": true
      }'
    ```

---

## Model health via Control Plane

To check if a specific model is deployed and routed, use the Control Plane:

```bash
# Check all deployments
curl -s http://<control-plane>:8080/api/v1/deployments

# Check cluster health
curl -s http://<control-plane>:8080/api/v1/cluster/health
```

A model appears in `GET /v1/models` only when it has an active deployment and the Control Plane has pushed a route to the Gateway. If a model is not listed, check the deployment state via the Control Plane API.
