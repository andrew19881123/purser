# Migrating from the OpenAI API

Purser's [API Gateway](../api/gateway.md) speaks the OpenAI wire protocol, so
moving an existing application off `api.openai.com` is a **base URL and an API
key** — no client library change, no request rewriting.

This page covers the one-line change, what has parity and what does not, and the
pitfalls that actually bite.

---

## The one-line change

=== "Python"

    ```python
    from openai import OpenAI

    client = OpenAI(
        base_url="http://<gateway-host>:<port>/v1",
        api_key="psk_<your-api-key>",
    )
    ```

=== "Node.js"

    ```javascript
    import OpenAI from "openai";

    const client = new OpenAI({
      baseURL: "http://<gateway-host>:<port>/v1",
      apiKey: "psk_<your-api-key>",
    });
    ```

=== "Environment only"

    ```bash
    # Most OpenAI SDKs read these, so no code change at all
    export OPENAI_BASE_URL="http://<gateway-host>:<port>/v1"
    export OPENAI_API_KEY="psk_<your-api-key>"
    ```

A complete, runnable version of this — minting the gateway key first, then
calling it — is in the
[Python SDK guide](../integrations/python-sdk.md#call-the-openai-compatible-inference-endpoint).
For more request/response detail and the full error table, see the
[Gateway API reference](../api/gateway.md).

### Before and after

```diff
 from openai import OpenAI

-client = OpenAI(
-    api_key=os.environ["OPENAI_API_KEY"],
-)
+client = OpenAI(
+    base_url="http://gateway.internal:8081/v1",
+    api_key=os.environ["PURSER_API_KEY"],
+)

 response = client.chat.completions.create(
-    model="gpt-4o-mini",
+    model="llama-8b",
     messages=[{"role": "user", "content": "Hello"}],
 )
```

The only other change is the **model name**: it must match a model you have
deployed in Purser, not an OpenAI model id.

---

## Feature parity

The gateway is a routing and policy layer, not a reimplementation. It reads only
the `model` and `stream` fields to decide where to send a request and whether to
stream the response; **every other field is forwarded verbatim** to the engine
behind the deployment.

That has an important consequence: for request *parameters*, parity is a property
of the engine you are running, not of Purser. For *endpoints*, parity is a
property of the gateway, and this is the list:

| Endpoint | Status |
|---|---|
| `POST /v1/chat/completions` | Supported, buffered and SSE streaming |
| `POST /v1/completions` | Supported (legacy completions) |
| `POST /v1/embeddings` | Supported, always buffered — no streaming |
| `GET /v1/models` | Supported, with the caveat below |
| `POST /v1/messages` | Supported — Anthropic-format, see [Anthropic Compatibility](../api/anthropic-compatibility.md) |
| `GET /v1/models/{id}` | Not implemented |
| `/v1/images/*`, `/v1/audio/*` | Not implemented |
| `/v1/files`, `/v1/fine_tuning/*` | Not implemented |
| `/v1/assistants/*`, `/v1/batches` | Not implemented |
| `/v1/moderations` | Not implemented |

!!! note "Parameters pass through to the engine"
    Fields like `temperature`, `top_p`, `max_tokens`, `stop`, `tools`, and
    `response_format` are relayed to the engine untouched. Whether they take
    effect depends on that engine's own support — the gateway neither implements
    nor validates them. If a parameter is silently ignored, check the engine
    behind the deployment, not the gateway.

### `GET /v1/models` lists routes, not your catalog

A model appears in `GET /v1/models` only once it has an **active deployment** and
the control plane has pushed a route to the gateway. A model registered in the
catalog but not deployed will not be listed, and calling it returns `404`.

To see everything registered, ask the control plane instead:

```bash
curl -s http://<control-plane>:8080/api/v1/models \
  -H 'Authorization: Bearer <admin-key>'
```

---

## Authentication

Purser uses the same bearer-token header, with its own key format:

```http
Authorization: Bearer psk_<your-api-key>
```

Keys are minted on the control plane (`POST /api/v1/apikeys`) and presented to
the gateway via `PURSER_GATEWAY_API_KEYS`. See
[API Key Lifecycle](../configuration/api-keys.md).

!!! warning "Dev mode accepts any token"
    If `PURSER_GATEWAY_API_KEYS` is unset, the gateway accepts **any** non-empty
    bearer token and attributes every request to the `default` tenant. That is
    convenient for a first test and unacceptable in production — set the variable
    before exposing the gateway to anything.

---

## Common pitfalls

**The gateway serves plaintext HTTP.** TLS is terminated upstream at your ingress
or load balancer, consistent with Purser's trusted-LAN model. An SDK that insists
on `https://` needs the ingress in front, not a gateway flag.

**Requests are capped at 4 MB.** The inference endpoints enforce a 4 MB body
limit. Long conversation histories and large embedding batches are the usual way
to hit it — split the batch rather than raising expectations.

**`404 model_not_found` usually means "not deployed".** The model name must match
a deployment with an active route, not a catalog entry. See the section above.

**Rate limits are on by default, and they are per key.** Three independent limits
apply, all returning `429` with a `Retry-After` header:

| Limit | Variable | Default |
|---|---|---|
| Tokens per minute, per key | `PURSER_GATEWAY_TOKENS_PER_MIN` | 60,000 |
| Concurrent requests, per key | `PURSER_GATEWAY_MAX_CONCURRENT` | 32 |
| Global in-flight ceiling | `PURSER_GATEWAY_MAX_INFLIGHT` | 512 |

Set any of them to `0` to disable. Client code that never expected a `429` from a
self-hosted endpoint should learn to honour `Retry-After`.

**`503` and `504` are Purser-specific signals.** `503 upstream_unavailable` means
the deployment host is unreachable; `504 upstream_timeout` means it did not
produce a first byte in time. Neither is an OpenAI error code your existing
retry logic will recognise — treat both as retryable.

**Embeddings never stream.** Passing `stream: true` to `/v1/embeddings` will not
produce SSE; the response is always buffered.

**Response metadata is synthesised.** Fields such as `created` and `owned_by` in
`GET /v1/models` are generated by the gateway. Do not build logic on them.

---

## Framework configuration

Anything that accepts an OpenAI-compatible base URL works unchanged. Two common
cases:

### LangChain

```python
from langchain_openai import ChatOpenAI

llm = ChatOpenAI(
    model="llama-8b",                              # your deployed model id
    base_url="http://<gateway-host>:<port>/v1",
    api_key="psk_<your-api-key>",
)

print(llm.invoke("Explain pipeline parallelism in one sentence.").content)
```

`ChatOpenAI` streams over the same SSE path as the raw SDK, so `llm.stream(...)`
works as well.

### LiteLLM

Purser needs no special provider plugin — point LiteLLM at the gateway with the
`openai/` prefix. See the [LiteLLM integration guide](../integrations/litellm.md)
for a full `config.yaml`.

---

## Verify the migration

```bash
# 1. The gateway is up and your key is accepted
curl -s http://<gateway-host>:<port>/v1/models \
  -H 'Authorization: Bearer psk_<your-api-key>'

# 2. A real completion round-trip
curl -sS http://<gateway-host>:<port>/v1/chat/completions \
  -H 'Authorization: Bearer psk_<your-api-key>' \
  -H 'Content-Type: application/json' \
  -d '{"model":"llama-8b","messages":[{"role":"user","content":"Hello"}]}'
```

If step 1 returns an empty `data` array, no model is deployed and routed yet —
that is a deployment problem, not a migration problem. See the
[FAQ](../faq.md).

---

## See also

- [Gateway API Reference](../api/gateway.md)
- [Anthropic Compatibility](../api/anthropic-compatibility.md)
- [Python SDK](../integrations/python-sdk.md) · [TypeScript SDK](../integrations/typescript-sdk.md)
- [LiteLLM Integration](../integrations/litellm.md)
- [API Key Lifecycle](../configuration/api-keys.md)
