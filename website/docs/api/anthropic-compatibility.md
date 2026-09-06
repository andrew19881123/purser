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
- An array containing `tool_use`, `tool_result`, and `text` blocks (see [Tool use](#tool-use--function-calling) below)

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

## Tool use / Function calling

The gateway fully supports Anthropic-style tool use. Define tools in the request, receive `tool_use` blocks in the response, and pass `tool_result` blocks back in the next turn.

```python
import anthropic

client = anthropic.Anthropic(
    api_key="sk-mykey",
    base_url="http://purser-gateway:8080",
)

tools = [
    {
        "name": "get_weather",
        "description": "Get the current weather for a location.",
        "input_schema": {
            "type": "object",
            "properties": {
                "location": {"type": "string", "description": "City name"},
            },
            "required": ["location"],
        },
    }
]

# First turn: model may call a tool
response = client.messages.create(
    model="qwen3-moe-235b",
    max_tokens=1024,
    tools=tools,
    messages=[{"role": "user", "content": "What's the weather in Paris?"}],
)

# response.stop_reason == "tool_use" when the model called a tool
tool_use_block = next(b for b in response.content if b.type == "tool_use")
tool_result = call_my_weather_api(tool_use_block.input["location"])

# Second turn: pass the tool result back
final = client.messages.create(
    model="qwen3-moe-235b",
    max_tokens=1024,
    tools=tools,
    messages=[
        {"role": "user", "content": "What's the weather in Paris?"},
        {"role": "assistant", "content": response.content},
        {
            "role": "user",
            "content": [
                {
                    "type": "tool_result",
                    "tool_use_id": tool_use_block.id,
                    "content": tool_result,
                }
            ],
        },
    ],
)
print(final.content[0].text)
```

### Tool choice

Pass `tool_choice` to control which tool (if any) the model calls:

```python
# Force the model to call any available tool
tool_choice={"type": "any"}

# Force a specific tool
tool_choice={"type": "tool", "name": "get_weather"}

# Let the model decide (default)
tool_choice={"type": "auto"}
```

### Streaming with tool use

Tool use works transparently with `stream=True`. The gateway emits the standard Anthropic SSE sequence:

```
content_block_start  (type="tool_use", index=1, id="...", name="...")
content_block_delta  (type="input_json_delta", partial_json="...")
content_block_stop
message_delta        (stop_reason="tool_use")
message_stop
```

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
| Tool use (`tools[]`, `tool_use`/`tool_result` blocks) | **Supported** (v0.3+) |
| `tool_choice` (`auto` / `any` / `tool`) | **Supported** (v0.3+) |
| Tool use streaming (`input_json_delta` SSE) | **Supported** (v0.3+) |
| `stop_sequences`, `top_p`, `top_k` | **Supported** (v0.3+) |
| Image input (`{"type":"image",...}`) | Accepted (parsed, not forwarded) — coming in a future release |
| Extended thinking (`thinking` content blocks) | Coming in a future release |
| Batches API (`/v1/messages/batches`) | Not supported |

## How it works internally

The gateway translates requests at the edge; the inference backend always receives OpenAI-format JSON:

1. The `system` field becomes the first `{"role":"system","content":"..."}` message.
2. `text` content block arrays are concatenated into a plain string.
3. Assistant messages with `tool_use` blocks are translated to OpenAI `tool_calls` arrays.
4. User messages with `tool_result` blocks are exploded: each block becomes a separate `{"role":"tool","tool_call_id":"...","content":"..."}` message.
5. `tools[]` definitions are translated to OpenAI `tools[].function` objects (JSON Schema passthrough).
6. `tool_choice`, `stop_sequences`, `top_p`, and `top_k` are forwarded to the upstream.
7. On the response path:
   - Non-streaming: `finish_reason: "tool_calls"` → `stop_reason: "tool_use"`; `tool_calls` array → Anthropic `tool_use` content blocks.
   - Streaming: `delta.tool_calls` fragments are emitted as `content_block_start` + `content_block_delta(input_json_delta)` SSE events, with one numbered block per tool call.

Because the translation is stateless and happens in the gateway process, there is no added latency beyond a few microseconds of JSON parsing per request.
