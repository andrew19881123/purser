# Gateway API

The Purser gateway exposes an OpenAI-compatible inference API at `/v1`. All
inference endpoints require a Bearer token (`Authorization: Bearer <key>`) issued
by the control plane.

---

## POST /v1/chat/completions

Proxies a chat completion request to the deployment host for the requested model.
Supports both streaming (`"stream": true`, Server-Sent Events) and buffered
(`"stream": false`) responses.

**Request** — OpenAI `ChatCompletionRequest` (all parameters forwarded verbatim).

**Response** — `chat.completion` JSON object, or a `text/event-stream` of
`chat.completion.chunk` frames terminated by `data: [DONE]`.

---

## POST /v1/completions

Legacy text-completion analogue of `/v1/chat/completions`. Supports the same
`stream` flag.

---

## POST /v1/embeddings

Proxies an embedding request to the deployment host for the requested model.
Embeddings are always returned as a buffered JSON response (no streaming).

### Request

```json
{
  "model": "my-embed-model",
  "input": "text to embed",
  "encoding_format": "float"
}
```

| Field             | Type             | Required | Description                                                 |
|-------------------|------------------|----------|-------------------------------------------------------------|
| `model`           | string           | yes      | ID of a registered, active embedding deployment.            |
| `input`           | string or array  | yes      | Text or list of texts to embed.                             |
| `encoding_format` | string           | no       | `"float"` (default) or `"base64"`. Forwarded to upstream.  |

### Response

```json
{
  "object": "list",
  "data": [
    {
      "object": "embedding",
      "embedding": [0.023, -0.011, ...],
      "index": 0
    }
  ],
  "model": "my-embed-model",
  "usage": {
    "prompt_tokens": 5,
    "total_tokens": 5
  }
}
```

### Error responses

| Status | Code             | Meaning                                            |
|--------|------------------|----------------------------------------------------|
| 401    | `unauthorized`   | Missing or invalid Bearer token.                   |
| 404    | `model_not_found`| No active route for the requested model.           |
| 429    | `rate_limit_error`| Per-tenant quota or global backpressure exceeded. |
| 503    | `node_unavailable`| Model route is draining or upstream is down.      |
| 504    | `timeout`        | Upstream timed out before returning a response.    |

---

## GET /v1/models

Returns all currently **active** model routes (models that are registered and
not draining). Requires no authentication.

```json
{
  "object": "list",
  "data": [
    {
      "id": "my-llm",
      "object": "model",
      "created": 1700000000,
      "owned_by": "purser"
    }
  ]
}
```

---

## Routing

The gateway resolves `model` → deployment host via a routing table kept fresh by
the control plane. The same table is used for chat, completions, and embeddings —
the type of serving is determined by the upstream host, not the gateway.
