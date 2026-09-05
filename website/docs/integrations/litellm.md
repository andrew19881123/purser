# LiteLLM integration

[LiteLLM](https://github.com/BerriAI/litellm) can proxy to Purser for both
chat completions and embedding inference using the `openai/` provider prefix.
This lets you use Purser-served models wherever an OpenAI-compatible SDK or
LiteLLM router is already wired up.

---

## Chat completions

```python
import litellm

response = litellm.completion(
    model="openai/my-llm",          # "openai/<model_id>" routes via the openai provider
    messages=[{"role": "user", "content": "Hello!"}],
    api_base="http://gateway:8080",  # Purser gateway address
    api_key="psk_...",               # API key minted by the control plane
)
print(response.choices[0].message.content)
```

---

## Embeddings

```python
import litellm

response = litellm.embedding(
    model="openai/my-embed-model",   # must match a registered embedding deployment
    input=["text to embed", "another chunk"],
    api_base="http://gateway:8080",
    api_key="psk_...",
)

for item in response.data:
    print(item["index"], len(item["embedding"]))  # index and vector length
```

LiteLLM forwards the request to `POST /v1/embeddings` and returns the upstream's
response unchanged, so the `response.data[i].embedding` list contains the raw
float32 values from the serving host.

---

## LiteLLM proxy config (YAML)

To expose Purser as a named model through the LiteLLM proxy server:

```yaml
model_list:
  - model_name: my-llm
    litellm_params:
      model: openai/my-llm
      api_base: http://gateway:8080
      api_key: psk_...

  - model_name: my-embed-model
    litellm_params:
      model: openai/my-embed-model
      api_base: http://gateway:8080
      api_key: psk_...
```

Start the proxy:

```bash
litellm --config litellm_config.yaml --port 4000
```

Clients can now call `http://localhost:4000/v1/embeddings` with
`"model": "my-embed-model"` and LiteLLM will route to Purser transparently.

---

## Environment variables

If you prefer environment-based configuration:

```bash
export OPENAI_API_BASE="http://gateway:8080"
export OPENAI_API_KEY="psk_..."
```

Then use the `openai/<model_id>` model string as shown above.

---

## Notes

- The gateway's `/v1/embeddings` endpoint forwards the request body verbatim to
  the upstream host — all parameters supported by the serving engine
  (`encoding_format`, `dimensions`, etc.) are passed through.
- Register embedding models in the control plane with `model_type: "embedding"`
  as a convention so the catalog can label them correctly (informational only;
  the gateway routes both LLM and embedding models identically).
