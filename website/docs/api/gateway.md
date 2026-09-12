# Gateway API Reference (`/v1` OpenAI-compatible)

The Purser API Gateway exposes an OpenAI-compatible inference API. Any OpenAI SDK or tool that supports a custom `base_url` works with Purser without modification.

**Base URL:** `http://<gateway-host>:<port>`

The gateway serves **plaintext HTTP**. TLS is terminated upstream at the ingress / load balancer, consistent with Purser's trusted-LAN model.

---

## Authentication

All inference endpoints require a bearer token:

```http
Authorization: Bearer sk-<your-api-key>
```

API keys are created via the Control Plane (`POST /api/v1/apikeys`) and stored as gateway API keys (`PURSER_GATEWAY_API_KEYS`).

**Dev mode:** If `PURSER_GATEWAY_API_KEYS` is not set, the gateway accepts any non-empty bearer token and maps all requests to the `default` tenant. Never leave this unset in production.

**Auth errors:**

| Status | When |
|---|---|
| `401` | Missing `Authorization` header, empty token, or missing `Bearer ` prefix |
| `401` | Token not recognized (when keys are configured) |

---

## How inference requests are handled

The three inference endpoints — `/v1/chat/completions`, `/v1/completions` and `/v1/embeddings` — share one code path, so the following applies identically to all of them.

**Your request body is forwarded verbatim.** The gateway parses only two fields out of it: `model`, to resolve the deployment host from the routing table, and `stream`, to decide how to relay the response. Every other field — `temperature`, `max_tokens`, `tools`, `input`, `encoding_format`, anything an engine supports — is passed through untouched. The gateway neither validates nor rewrites them.

**`stream` is read the same way on every endpoint.** `stream: true` pipes the upstream's SSE bytes to you chunk by chunk; `stream: false` (the default) buffers the full JSON body. This decision is made from the field alone, not from which endpoint you called.

**Response shapes come from the engine, not from Purser.** Because the gateway proxies rather than synthesises, what you get back for a given endpoint is whatever the engine behind that deployment returns — and whether the engine serves that endpoint at all is the engine's business. The examples below show the shapes the bundled mock engine produces; a different engine may differ, and an engine that does not implement an endpoint will fail at the upstream rather than at the gateway.

**Request bodies are capped at 4 MB** on the three POST endpoints. The cap is enforced by the HTTP framework before the handler runs, so an oversized request is rejected without a Purser error envelope. `GET /v1/models` has no body and is not affected.

**Authentication, quota, and rate limiting** apply to all three identically — see [Authentication](#authentication) and [Rate limiting and backpressure](#rate-limiting-and-backpressure).

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

## Text Completions

### `POST /v1/completions`

The legacy (non-chat) text-completion endpoint, for clients written against the older OpenAI Completions API. Authenticated, quota-checked, and proxied exactly as chat completions are; `stream: true` is honoured the same way.

**Request:**

```json
{
  "model": "llama-8b",
  "prompt": "Write a haiku about GPUs",
  "max_tokens": 64,
  "stream": false
}
```

Only `model` and `stream` are interpreted by the gateway. `prompt`, `max_tokens` and any other field are forwarded to the engine unchanged.

**Response `200`:** the engine's `text_completion` object, relayed verbatim. With `stream: true`, `text/event-stream` chunks terminated by `data: [DONE]`, as for chat completions.

**Error responses:** identical to [`POST /v1/chat/completions`](#post-v1chatcompletions) — same authentication, routing, backpressure, and upstream failure mapping.

!!! note "Prefer chat completions for new work"
    OpenAI treats the Completions API as legacy, and so should you for new integrations. This endpoint exists so that existing clients keep working when they are pointed at Purser.

---

## Embeddings

### `POST /v1/embeddings`

Returns embedding vectors for one or more inputs, from a deployed embedding model.

**Request:**

```json
{
  "model": "purser/mock-embed",
  "input": "the quick brown fox",
  "encoding_format": "float"
}
```

`input` accepts a string or an array of strings. As everywhere on this plane, `input` and `encoding_format` are forwarded to the engine rather than interpreted by the gateway.

**Response `200`** — the shape the bundled mock engine returns:

```json
{
  "object": "list",
  "data": [
    {"object": "embedding", "embedding": [0.0123, -0.0456, "..."], "index": 0}
  ],
  "model": "purser/mock-embed",
  "usage": {"prompt_tokens": 5, "total_tokens": 5}
}
```

The mock engine emits a 128-dimension vector normalised to unit length, with values drawn at random on each run — the norm is the only property you can assert on. It is useful for wiring up a client, and carries no semantic meaning whatsoever. A real embedding model returns its own dimensionality.

**Error responses:** identical to [`POST /v1/chat/completions`](#post-v1chatcompletions).

!!! warning "`stream` is not special-cased for embeddings"
    The gateway makes its streaming decision from the `stream` field alone, on every inference endpoint. Embedding responses are buffered in practice because clients do not set `stream: true` and engines do not stream embeddings — but the gateway does not reject or ignore the field. Sending `stream: true` here puts the gateway into SSE relay mode over a response the engine never streams; leave it unset.

**Model must be an embedding model.** `GET /v1/models` lists every actively deployed model without distinguishing embedding models from generative ones, so the routing table will happily send an embedding request to a chat model. The resulting error comes from the engine.

---

## Models

### `GET /v1/models`

Lists all models with active deployments and active routes. The route table is populated — and continuously re-pushed — by the Control Plane's route reconciler (see [Internal route sync](#the-routing-table-is-in-memory-only--and-self-healing)).

**Response `200`:**

```json
{
  "object": "list",
  "data": [
    {
      "id": "llama-8b",
      "object": "model",
      "created": 1725494400,
      "owned_by": "purser"
    }
  ]
}
```

If no models are deployed, `data` is an empty array `[]`.

Entries carry no capability or modality field, so this list does not tell you which models are embedding models and which are generative. Sending an embedding request to a chat model is therefore routed normally and fails at the engine.

---

## Internal route sync (Control Plane only)

These endpoints are for the Control Plane to push and read routing updates. They are protected by `X-Purser-Internal-Token` and should not be called by clients.

### `PUT /api/v1/routes`

Adds or updates a route (maps a model ID to a deployment host endpoint). Idempotent — re-pushing the same route is a no-op.

### `GET /api/v1/routes`

Returns the routes the Gateway holds right now:

```json
{
  "object": "list",
  "count": 1,
  "data": [
    {
      "model_id": "llama-8b",
      "endpoint": "http://10.0.0.4:8080",
      "deployment_id": "dep-9",
      "quantization": "Q4_K_M",
      "state": "active"
    }
  ]
}
```

### `DELETE /api/v1/routes/{model_id}`

Removes a route (idempotent).

### The routing table is in memory only — and self-healing

The Gateway does **not** persist its routing table. A restarted Gateway starts with
an empty table, which by itself would make every inference request fail with
`503 model not available` until an operator re-deployed a model.

The Control Plane's **route reconciler** closes that gap. It pushes the desired route
set — one route per `ACTIVE` deployment — once at startup and then every 30 seconds
(`PURSER_ROUTE_RECONCILE_INTERVAL`, in seconds), and deletes any route whose model is
no longer `ACTIVE`. A Gateway that is down when the control plane starts is not an
error: the pass fails, logs a warning, and retries a few seconds later.

The practical consequence: restarting a Gateway pod is safe. Routes are restored
within one reconcile interval, with no operator action. To confirm recovery:

```bash
curl -s -H "X-Purser-Internal-Token: $PURSER_GATEWAY_TOKEN" \
  http://<gateway>:8080/api/v1/routes
```

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
        api_key="sk-<your-api-key>"
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
      apiKey: "sk-<your-api-key>",
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
      -H "Authorization: Bearer sk-<your-api-key>" \
      -H "Content-Type: application/json" \
      -d '{
        "model": "llama-8b",
        "messages": [{"role": "user", "content": "Hello"}],
        "stream": false
      }'

    # Streaming (SSE)
    curl -N http://<gateway-host>:<port>/v1/chat/completions \
      -H "Authorization: Bearer sk-<your-api-key>" \
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

A model appears in `GET /v1/models` only when it has an active deployment and the Control Plane has pushed a route to the Gateway. If a model is not listed, check the deployment state via the Control Plane API — and note that a Gateway restart empties its in-memory table, which the route reconciler refills within 30 seconds. If a model is still missing after that, the deployment itself is not `ACTIVE`.
