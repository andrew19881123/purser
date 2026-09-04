# Purser

> **The Kubernetes of LLM layers** — a zero-config orchestrator for distributed LLM inference on your own hardware.

Purser turns a fleet of heterogeneous machines on a trusted LAN into a single,
OpenAI-compatible inference endpoint. It splits a model **by layer** (pipeline
parallelism) across nodes, plans the optimal split for your specific hardware,
and drives existing inference engines (llama.cpp, and more) — it does **not**
reimplement inference.

**Status: early / alpha.** The full zero-config vertical — *enroll → deploy →
chat* — works end-to-end with the built-in mock engine on a single node.
Multi-node pipelines and the live llama.cpp path are in progress. See
[PROJECT_STATUS.md](PROJECT_STATUS.md) for the honest, detailed state and roadmap.

## Why

- **Zero-config** — turn on the machines, click deploy, get a private
  ChatGPT-style API on your own hardware.
- **Pipeline parallelism over LAN** — only activations (~KB/token) cross the
  network, so commodity 10GbE works; no NVLink/InfiniBand required.
- **Bring your own engine** — an Engine Adapter abstracts the backend; the rest
  of the system never hard-codes model specifics.
- **Private & on-prem** — no data leaves your network by design; air-gap friendly.

## Architecture

| Component | Language | Path | Role |
|---|---|---|---|
| API contracts | protobuf | `proto/` | Shared gRPC contracts (source of truth) |
| Agent | Rust | `rust/crates/agent` | Per-node daemon: HW probe, link benchmark, engine supervisor, model cache, discovery |
| Engine Adapter | Rust | `rust/crates/engine-adapter`, `rust/crates/adapter-llamacpp` | `EngineBackend` trait: mock / llama.cpp |
| Planner | Go | `go/planner` | Optimal layer-split algorithm (dynamic programming) |
| Control Plane | Go | `go/controlplane` | Registry, orchestrator, reconciler, internal PKI, REST API |
| API Gateway | Rust | `rust/crates/gateway` | OpenAI-compatible `/v1`, SSE streaming, auth, quota |
| Dashboard | TypeScript / React | `ui/` | Fleet view, model catalog, deploy, chat playground |

Communication: gRPC + mTLS between Control Plane and Agents; REST/SSE between the
UI and the Control Plane; OpenAI-compatible HTTP between clients and the Gateway.

## Quickstart

Everything runs through a **project-local** toolchain in `.toolchain/` — nothing
is installed globally.

```bash
make setup       # install Rust, Go, buf into .toolchain/ (idempotent)
source ./env.sh  # put cargo/go/buf on PATH for ad-hoc commands
make gen         # regenerate types from the .proto contracts
make build       # build all workspaces (Rust + Go)
make test        # run the test suites
```

Run the full end-to-end demo — control plane + gateway + a mock-engine node,
covering enroll → deploy → **real streaming chat**:

```bash
bash tools/e2e_full.sh
```

## License

Purser's core is licensed under **AGPL-3.0** — see [LICENSE](LICENSE). Certain
enterprise/compliance features are offered under a separate commercial license;
see [LICENSING.md](LICENSING.md) for the open-core model.

## Contributing & security

Contributions are welcome — see [CONTRIBUTING.md](CONTRIBUTING.md). To report a
security vulnerability, please follow [SECURITY.md](SECURITY.md) (do **not** open
a public issue). Participation is governed by our
[Code of Conduct](CODE_OF_CONDUCT.md).
