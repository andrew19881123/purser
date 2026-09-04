# Purser

> **The Kubernetes of LLM layers** — a zero-config orchestrator for distributed
> LLM inference on your own hardware.

[![License: MIT](https://img.shields.io/badge/license-MIT-blue)](LICENSE)
[![CI](https://github.com/andrew19881123/purser/actions/workflows/ci.yml/badge.svg)](https://github.com/andrew19881123/purser/actions/workflows/ci.yml)
[![Status: alpha](https://img.shields.io/badge/status-alpha-orange)](PROJECT_STATUS.md)

## What is Purser

Purser turns a fleet of heterogeneous machines on a trusted LAN into a single,
OpenAI-compatible inference endpoint. It splits a model **by layer** (pipeline
parallelism) across your nodes, plans the optimal split for your specific
hardware, and drives **existing** inference engines (llama.cpp, and more) through
an Engine Adapter — it does **not** reimplement inference.

If you know Kubernetes, the mental model is familiar:

| Kubernetes | Purser | Role |
|---|---|---|
| Control plane | **Control Plane** | Registry, scheduling, reconciliation, PKI |
| Scheduler | **Planner** | Computes the optimal per-layer split for your fleet |
| kubelet | **Agent** | Per-node daemon that runs and supervises workloads |
| Container runtime | **Engine** (via Adapter) | The thing that actually executes inference |
| Ingress | **API Gateway** | Single OpenAI-compatible front door |

## Why / when to use it

- **Zero-config** — turn on the machines, deploy a model, get a private
  ChatGPT-style API. The Planner figures out the split; you don't hand-tune it.
- **Pipeline parallelism over LAN** — only activations (~KB per token) cross the
  network between stages, so commodity **10GbE** is plenty. No NVLink or
  InfiniBand required.
- **Private / on-prem** — no data leaves your network by design; air-gap
  friendly.
- **Bring your own engine** — the Engine Adapter abstracts the backend, so the
  orchestrator never hard-codes model or engine specifics.

## Try it in 2 minutes

No GPU required — the end-to-end demo runs entirely on a **built-in mock engine**.

```bash
make setup                 # install the project-local toolchain into .toolchain/ (idempotent)
make build                 # build the Rust workspace (agent + gateway) and Go modules
source ./env.sh            # put cargo / go / buf on PATH
( cd go/controlplane && go build -o ../../bin/control-plane . )   # stage the control-plane binary
bash tools/e2e_full.sh     # the full vertical: enroll -> deploy -> real streaming chat
```

`tools/e2e_full.sh` boots **real binaries** — the control plane, the gateway, and
one agent node — and walks the entire zero-config path:

1. **Start** the control plane + API gateway.
2. **Mint** a single-use join token and **enroll** the agent over gRPC; the node
   reports a real hardware profile and reaches `READY`.
3. **Seed** a model (`llama-8b`) and trigger a deploy.
4. The **Planner** computes a layer-split plan; the **Orchestrator** issues
   `StartEngine`; the mock engine comes up and the deployment goes `ACTIVE`.
5. Routes **sync** from the control plane to the gateway, so `GET /v1/models`
   now lists the model.
6. A **real OpenAI chat completion** streams back over SSE, token by token.

The "money shot" at the end is an actual streaming chat through the gateway —
illustrative (abbreviated) output:

```text
--- non-stream ---
{"id":"chatcmpl-...","object":"chat.completion","model":"llama-8b",
 "choices":[{"index":0,"message":{"role":"assistant","content":"..."},"finish_reason":"stop"}]}

--- streaming (SSE) ---
data: {"object":"chat.completion.chunk","model":"llama-8b","choices":[{"delta":{"role":"assistant"}}]}
data: {"object":"chat.completion.chunk","model":"llama-8b","choices":[{"delta":{"content":"Hello"}}]}
data: {"object":"chat.completion.chunk","model":"llama-8b","choices":[{"delta":{"content":" there"}}]}
...
data: {"object":"chat.completion.chunk","model":"llama-8b","choices":[{"delta":{},"finish_reason":"stop"}]}
data: [DONE]
```

Want to see the **split** in action? Run:

```bash
bash tools/e2e_multinode.sh   # a model too big for one node, split across TWO
```

It enrolls two agents (each with its own advertised address), picks a model size
that does **not** fit on one node but **does** fit across two, deploys it, and
asserts the real properties of a pipeline: a plan with **two assignments
(HOST + WORKER)** and a `pipeline_order` of length 2, the **worker started before
the host** on distinct addresses, a deployment that reaches `ACTIVE`, and a real
chat (non-stream + SSE) proxied to the pipeline host.

## Deployment

Purser ships as **two kinds of workload**, matching its two planes. The **Agent**
is a native **host package** on your fleet nodes — it needs the node's
GPUs/accelerators and supervises an inference engine worker that is *not*
sandboxed, so it runs on the host and **not** in Kubernetes. The **Control Plane,
API Gateway, and UI** are **container images** deployed with **Helm on
Kubernetes**.

### Kubernetes (Helm) — Control Plane, Gateway, UI

The **Helm chart** lives in [`deploy/helm/purser`](deploy/helm/purser) and the
**Dockerfiles** in [`deploy/docker`](deploy/docker). There is **no public image
registry yet** (one is planned), so for now **build the images and push them to
your own container registry**, then install the chart pointed at those images:

```bash
# 1. Build the three images (run from the repo root — the build context is the repo root):
docker build -f deploy/docker/control-plane.Dockerfile -t <registry>/purser-control-plane:0.1.0 .
docker build -f deploy/docker/gateway.Dockerfile       -t <registry>/purser-gateway:0.1.0 .
docker build -f deploy/docker/ui.Dockerfile            -t <registry>/purser-ui:0.1.0 .

# 2. Push them to your registry:
docker push <registry>/purser-control-plane:0.1.0
docker push <registry>/purser-gateway:0.1.0
docker push <registry>/purser-ui:0.1.0

# 3. Install the chart, pointing each component at your images:
helm install purser deploy/helm/purser \
  --set image.controlPlane.repository=<registry>/purser-control-plane --set image.controlPlane.tag=0.1.0 \
  --set image.gateway.repository=<registry>/purser-gateway           --set image.gateway.tag=0.1.0 \
  --set image.ui.repository=<registry>/purser-ui                     --set image.ui.tag=0.1.0 \
  --set controlPlane.service.type=LoadBalancer   # so out-of-cluster LAN Agents can reach it
```

Keep `replicaCount: 1`: the SQLite **Registry** and internal **PKI** are
single-writer. **Multi-replica HA** on Kubernetes requires the **Raft-replicated
Registry**, an enterprise (source-available, key-gated) feature.

### Linux fleet (native `.deb` / `.rpm` packages) — the Agent

Build the Agent's native packages, then install with your distro's package
manager:

```bash
make package-agent   # produces dist/purser-agent_0.1.0_amd64.deb + dist/purser-agent-0.1.0-1.x86_64.rpm

sudo apt install ./purser-agent_0.1.0_amd64.deb        # Debian / Ubuntu
sudo yum install ./purser-agent-0.1.0-1.x86_64.rpm     # RHEL / Fedora / openSUSE
```

The package installs the `purser-agent` **systemd** service and a config file at
`/etc/purser/agent.env` — set `PURSER_JOIN_TOKEN` and the control-plane address
there, then `sudo systemctl enable --now purser-agent`. At **fleet scale**,
publish the `.deb` / `.rpm` to an internal **apt / yum** repository and roll them
out with **MDM / Ansible / Intune**.

### macOS / Windows — the Agent

Install the Agent as a **launchd** daemon (macOS) or a **Windows service** — see
[`packaging/`](packaging/README.md).

---

For the full **Kubernetes** guide (image build internals, chart values,
networking to the LAN fleet) see [`deploy/README.md`](deploy/README.md); for host
install and the **enterprise deployment model**, see
[`packaging/README.md`](packaging/README.md).

## Architecture

Purser has two clearly separated planes: a low-volume **control plane** (gRPC +
mTLS) and a high-volume **data plane** (engine-to-engine activations across the
trusted subnet).

```
                         OpenAI-compatible HTTP  (/v1/chat/completions, SSE)
  ┌────────────┐              │
  │  Clients   │──────────────┘
  │ (OpenAI    │
  │  SDK/curl) │
  └──────┬─────┘
         │
         ▼
┌─────────────────────────── Control-plane side ───────────────────────────┐
│                                                                          │
│   ┌──────────────┐        ┌──────────────────────────────────────────┐   │
│   │ API Gateway  │◀──────▶│              Control Plane               │   │
│   │  OpenAI /v1  │ routes │   Planner · Orchestrator · Reconciler    │   │
│   │ auth, quota  │  sync  │ Registry (SQLite) · internal PKI · REST  │   │
│   └──────────────┘        └──────────────────────────────────────────┘   │
│                                                │                         │
└────────────────────────────────────────────────┼─────────────────────────┘
                                                  │  gRPC + mTLS — control plane
                                                  │  (low volume: enroll, plan,
                                                  ▼   StartEngine, heartbeats)
                  ┌──────────────────────────────┴────────┐
                  │                                       │
   ┌──────────────┬──────────────┐         ┌──────────────┬──────────────┐
   │        Node / Agent         │         │        Node / Agent         │
   │  probe · linkbench          │         │  probe · linkbench          │
   │  supervisor · model cache   │         │  supervisor · model cache   │
   │  Engine Adapter ─▶ engine   │         │  Engine Adapter ─▶ engine   │
   └─────────────────────────────┘         └─────────────────────────────┘
                  ▲                                       ▲
                  └───────────────────────────────────────┘
      DATA PLANE: engine↔engine activations (~KB/token) over the
      trusted subnet — only activations cross the net (the pipeline)
```

**Request flow.** Client → API Gateway `/v1/chat/completions` → the gateway
routes to the pipeline **host** → tokens stream back to the client over SSE. When
the model is split, the host coordinates the downstream worker stage(s); only
activations cross the network.

**Deploy flow.** `enroll` (agent joins over gRPC, becomes `READY`) → **Planner**
checks fit and produces a layer-split **plan** → **Orchestrator** calls
`StartEngine` on the **worker(s) first, then the host** → the deployment reaches
`ACTIVE` → routes **sync** to the Gateway so clients can hit the model.

## Using it for real

The demos run everything on one host; a real cluster is the same components
spread across machines.

- **Add nodes.** Install the `purser-agent` on each machine as a managed service
  — native **`.deb` / `.rpm`** packages (via **apt / yum**), or the systemd /
  launchd / Windows service units — and point it at the control plane with a join
  token. See [Deployment](#deployment) for the commands; the service unit files
  and environment templates are in [`packaging/`](packaging/README.md).
- **Deploy a model.** Register it and deploy via the control plane REST API:

  ```bash
  curl -X POST http://<control-plane>:8080/api/v1/models        -d @model.json
  curl -X POST http://<control-plane>:8080/api/v1/models/<id>/deploy -d '{}'
  ```

- **Point any OpenAI client at the Gateway.** Use the gateway as your base URL
  and one of its API keys as the bearer token:

  ```python
  from openai import OpenAI
  client = OpenAI(base_url="http://<gateway-host>:<port>/v1",
                  api_key="<your-api-key>")   # -> Authorization: Bearer <api-key>
  client.chat.completions.create(model="<id>",
      messages=[{"role": "user", "content": "Hello"}], stream=True)
  ```

Binaries and default ports:

| Component     | Binary            | Language | Key ports (default)                     |
|---------------|-------------------|----------|-----------------------------------------|
| Control plane | `control-plane`   | Go       | REST `:8080`, gRPC `:9443`              |
| Agent         | `purser-agent`    | Rust     | AgentService `:50151`, inference `:8000`|
| API gateway   | `purser-gateway`  | Rust     | OpenAI-compatible HTTP (you choose)     |

See [`packaging/README.md`](packaging/README.md) for service install, security
notes (Purser assumes a **trusted LAN** — never expose the engine to the public
internet), and the full environment-variable reference, and
[PROJECT_STATUS.md](PROJECT_STATUS.md) for the API surface. For the fleet-scale
picture — agents as native host packages, control plane/gateway/UI as containers
+ Helm on Kubernetes — see the **Enterprise deployment model** in
[`packaging/README.md`](packaging/README.md).

## Project layout & development

| Component | Language | Path |
|---|---|---|
| API contracts (source of truth) | Protobuf | `proto/purser/v1` |
| Agent | Rust | `rust/crates/agent` |
| Engine Adapter | Rust | `rust/crates/engine-adapter`, `rust/crates/adapter-llamacpp` |
| API Gateway | Rust | `rust/crates/gateway` |
| Planner | Go | `go/planner` |
| Control Plane | Go | `go/controlplane` |
| Dashboard | TypeScript / React | `ui/` |

Generated protobuf bindings live in `rust/crates/purser-proto` and `go/gen`.

The whole toolchain is **project-local** in `.toolchain/` (Rust, Go, buf) —
nothing is installed globally:

```bash
make setup       # install the toolchain into .toolchain/ (idempotent)
source ./env.sh  # put cargo / go / buf on PATH for ad-hoc commands
make gen         # regenerate types from the .proto contracts (buf)
make build       # build the Rust workspace + every Go module
make test        # run the Rust + Go test suites
make lint        # clippy (-D warnings) + go vet
make fmt         # rustfmt + go fmt
```

Contributions are welcome — see [CONTRIBUTING.md](CONTRIBUTING.md). Security
issues: please follow [SECURITY.md](SECURITY.md) rather than opening a public
issue.

## License (open-core)

Purser follows the open-core model popularized by **LiteLLM**:

- **Core — MIT.** Everything **outside** the `enterprise/` directory is free and
  open source under the [MIT License](LICENSE): the full single-cluster stack
  (Agent, Engine Adapter, Planner, Control Plane, Gateway, Dashboard).
- **`enterprise/` — source-available, key-gated.** The code is public
  (compilable, inspectable, usable for development and evaluation), but
  production use requires a valid license activated at runtime via
  `PURSER_LICENSE_KEY` (fully offline ed25519 verification, no phone-home).

See [LICENSING.md](LICENSING.md) for details.

## Status & roadmap

**Alpha — v0.1.0** (first Community Edition release). The complete zero-config
vertical — *enroll → deploy → chat* — is implemented and demonstrated end-to-end,
**single-node and split across a multi-node pipeline**, driven by the built-in
**mock engine**. CI is green across all four jobs (proto, Rust, Go, UI).

Being honest about the edges: the full flow is proven with the **mock engine**.
The **live llama.cpp** path (adapter implemented and unit-tested) and validation
on **real GPU hardware** are still work in progress, as are the enterprise
capabilities behind the license gate.

For the detailed, honest state and the prioritized backlog, see
[PROJECT_STATUS.md](PROJECT_STATUS.md); for release history, see
[CHANGELOG.md](CHANGELOG.md).
