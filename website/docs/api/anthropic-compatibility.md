# Anthropic Messages API Compatibility

Purser Gateway exposes `POST /v1/messages`, making it compatible with the **Anthropic SDK** and any tool that uses `@anthropic-ai/sdk` (Claude Code, Cursor, Continue, and others). You can point these tools at a Purser cluster by changing only the `base_url`.

## Endpoint

```
POST /v1/messages
```

This lives on the same gateway port as the OpenAI-compatible endpoints. Both APIs coexist under `/v1`.

## Authentication

The Anthropic SDK sends credentials in the `x-api-key` header by default. Purser accepts both forms:

| Header | Example |
|---|---|
| `x-api-key` | `x-api-key: sk-mykey` |
| `Authorization: Bearer` | `Authorization: Bearer sk-mykey` |

The same API keys configured via `PURSER_GATEWAY_API_KEYS` are accepted through both headers. When no keys are configured, the gateway runs in **open dev mode** and accepts any non-empty value.

## Request format

```json
{
  "model": "qwen3-moe-235b",
  "max_tokens": 1024,
  "messages": [
    { "role": "user", "content": "Hello" },
    { "role": "assistant", "content": "Hi there!" },
    { "role": "user", "content": "What can you do?" }
  ],
  "system": "You are a helpful assistant.",
  "stream": true,
  "temperature": 0.7
}
```

The `content` field of each message may be:

- A plain string: `"content": "Hello"`
- An array of typed content blocks: `"content": [{"type": "text", "text": "Hello"}]`

Only `text` blocks are extracted; `tool_use` and `image` blocks are silently dropped in this version (see [Feature support](#feature-support) below).

## Response format — non-streaming

```json
{
  "id": "msg_a1b2c3d4",
  "type": "message",
  "role": "assistant",
  "content": [
    { "type": "text", "text": "I can answer questions and help with tasks." }
  ],
  "model": "qwen3-moe-235b",
  "stop_reason": "end_turn",
  "stop_sequence": null,
  "usage": {
    "input_tokens": 22,
    "output_tokens": 13
  }
}
```

## Response format — streaming (SSE)

The gateway emits standard Anthropic SSE events:

```
event: message_start
data: {"type":"message_start","message":{"id":"msg_a1b2c3d4","type":"message","role":"assistant","content":[],"model":"qwen3-moe-235b","stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":22,"output_tokens":0}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}

event: ping
data: {"type":"ping"}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"I can"}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":" help"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":13}}

event: message_stop
data: {"type":"message_stop"}
```

## Error format

Errors follow the Anthropic error envelope — **not** the OpenAI envelope:

```json
{
  "type": "error",
  "error": {
    "type": "authentication_error",
    "message": "Invalid API key."
  }
}
```

| HTTP status | `error.type` | Cause |
|---|---|---|
| 400 | `invalid_request_error` | Malformed or missing required fields |
| 401 | `authentication_error` | Missing or invalid API key |
| 403 | `permission_error` | Key exists but lacks permission |
| 404 | `invalid_request_error` | Model not found in routing table |
| 429 | `rate_limit_error` | Quota or concurrency limit exceeded |
| 529 | `overloaded_error` | Deployment host unavailable |
| 504 | `api_error` | Upstream response timeout |

## SDK configuration

### Python (`anthropic` SDK)

```python
import anthropic

client = anthropic.Anthropic(
    api_key="sk-mykey",          # any key accepted by the gateway
    base_url="http://purser-gateway:8080",
)

message = client.messages.create(
    model="qwen3-moe-235b",
    max_tokens=1024,
    messages=[{"role": "user", "content": "What is 2 + 2?"}],
)
print(message.content[0].text)
```

### TypeScript (`@anthropic-ai/sdk`)

```typescript
import Anthropic from "@anthropic-ai/sdk";

const client = new Anthropic({
  apiKey: "sk-mykey",
  baseURL: "http://purser-gateway:8080",
});

const message = await client.messages.create({
  model: "qwen3-moe-235b",
  max_tokens: 1024,
  messages: [{ role: "user", content: "What is 2 + 2?" }],
});
console.log(message.content[0].text);
```

### Claude Code (`claude` CLI)

Claude Code reads `ANTHROPIC_BASE_URL` and `ANTHROPIC_API_KEY`:

```bash
export ANTHROPIC_BASE_URL=http://purser-gateway:8080
export ANTHROPIC_API_KEY=sk-mykey
claude
```

You can persist these in `~/.claude.json` or your shell profile.

### Cursor

In Cursor settings, set:

- **AI Provider**: Anthropic
- **API Key**: `sk-mykey`
- **API Base URL**: `http://purser-gateway:8080`

## Feature support

| Feature | Status |
|---|---|
| Non-streaming `POST /v1/messages` | Supported |
| Streaming `POST /v1/messages` (SSE) | Supported |
| `x-api-key` authentication | Supported |
| `Authorization: Bearer` authentication | Supported |
| System prompt (`system` field) | Supported |
| Text content blocks (`{"type":"text","text":"..."}`) | Supported |
| Multi-turn conversations | Supported |
| `max_tokens`, `temperature` | Supported |
| Tool use (`tool_use`, `tool_result` blocks) | Coming in a future release |
| Image input (`{"type":"image",...}`) | Coming in a future release |
| `top_p`, `top_k`, `stop_sequences` | Not forwarded (use OpenAI endpoint for full param passthrough) |
| Batches API (`/v1/messages/batches`) | Not supported |

## How it works internally

The gateway translates requests at the edge; the inference backend always receives OpenAI-format JSON:

1. The `system` field becomes the first `{"role":"system","content":"..."}` message.
2. Content block arrays are flattened: only `text` blocks are kept; their text is concatenated.
3. The resulting `messages` array (plus `model`, `stream`, `max_tokens`, `temperature`) is forwarded as a standard `POST /v1/chat/completions` request to the deployment host.
4. On the response path: non-streaming JSON is re-shaped into the Anthropic `message` envelope; streaming chunks are translated token-by-token from OpenAI `content_block_delta` to Anthropic `content_block_delta` events.

Because the translation is stateless and happens in the gateway process, there is no added latency beyond a few microseconds of JSON parsing per request.
