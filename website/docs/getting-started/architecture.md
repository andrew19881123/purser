# Architecture

Purser is composed of three long-running processes that collaborate over gRPC and HTTP:

| Process | Language | Role |
|---|---|---|
| **control-plane** | Go | Fleet registry, PKI, REST management API (`/api/v1`), orchestrator |
| **purser-gateway** | Rust | OpenAI-compatible API gateway; proxies chat requests to the active inference host |
| **purser-agent** | Rust | Runs on each inference node; enrolls with the control-plane, starts/stops engine processes on demand |

## Request flow

```
client → gateway (/v1/chat/completions)
       → [route table synced by control-plane]
       → inference host (purser-agent → llama.cpp / vllm)
```

For multi-node deployments the control-plane plans a pipeline (HOST + WORKER roles), orchestrates the start order (workers first), and registers the HOST's inference address with the gateway.

## E2E test coverage

The full request flow above is exercised by the automated E2E test suite:

- **`tools/e2e_full.sh`** — single-node: enroll one agent, seed a model, deploy it, drive
  a real chat (non-streaming and SSE) through the gateway.  This is the primary
  integration gate and runs in CI on every push to a `release/*` branch.

- **`tools/e2e_multinode.sh`** — multi-node: enroll two agents, find a model size that
  spans both nodes (requires exactly 2 nodes), verify HOST/WORKER pipeline ordering,
  and confirm the chat is proxied through the pipeline HOST.

Both scripts exit 0 on pass and non-zero on any assertion failure, making them
suitable as unattended CI steps.
