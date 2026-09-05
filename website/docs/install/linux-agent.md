# Installing the Purser Agent on Linux

The Purser fleet agent (`purser-agent`) is a lightweight daemon that runs on
every node. It exposes `AgentService` over gRPC toward the control plane,
reports hardware metrics, and manages the local model-weight cache.

## Prerequisites

- Linux x86-64 or aarch64, kernel 5.10+
- A mounted cache volume with enough space for the model weights you intend to
  serve (typically 20–100 GB per model)

---

## Model weight fetching

The agent caches model artifacts locally so an engine never has to pull weights
at inference time. Two fetcher implementations are available.

### FileMirrorFetcher (default)

The default build ships with `FileMirrorFetcher`. It resolves artifact URLs as:

| URL form | Resolved path |
|---|---|
| `file:///abs/path` or `file://relative` | The path after `file://` |
| Absolute path (starts with `/`) | Used directly |
| Relative path | Joined onto the configured `mirror_root` |

This is the recommended fetcher for production deployments: the mirror is a
local directory, NFS share, or mounted object store populated out-of-band. No
HTTP stack is compiled in, keeping the binary small and the dependency surface
minimal.

### HttpFetcher (optional `http-fetch` feature)

For nodes that must pull weights directly from an internet origin, the agent
can be compiled with the `http-fetch` Cargo feature:

```sh
cargo build -p purser-agent --features http-fetch --release
```

This links in `reqwest` (with TLS) and enables `HttpFetcher`, which:

- Streams the response body directly to a `.tmp` staging file, then atomically
  renames it into the content-addressed blob store — a partial download is never
  admitted to the cache.
- Retries on transient failures: **5xx server errors** and **network/timeout
  errors** are retried up to `PURSER_MODEL_FETCH_MAX_RETRIES` times (default
  3). Permanent errors (4xx) fail immediately.
- Uses a 30-second per-request timeout and identifies itself with the
  `User-Agent: purser-agent/model-cache` header.

`HttpFetcher` is wired automatically when the feature is enabled; no additional
configuration is needed beyond the retry knob below.

> **Note:** Checksum verification (SHA-256) is always performed by the cache
> after a fetch, regardless of which fetcher is used. A weight file whose digest
> does not match the expected value is rejected and removed.

---

## Environment variables

| Variable | Default | Description |
|---|---|---|
| `PURSER_MODEL_FETCH_MAX_RETRIES` | `3` | Maximum retry count for `HttpFetcher` on transient errors. `0` means one attempt with no retries. |

See [Environment Variables](../configuration/env-vars.md) for the full list.
