# Purser

> **The Kubernetes of LLM layers** — a zero-config orchestrator for distributed
> LLM inference on your own hardware.

[![License: MIT](https://img.shields.io/badge/license-MIT-blue)](LICENSE)
[![CI](https://github.com/andrew19881123/purser/actions/workflows/ci.yml/badge.svg)](https://github.com/andrew19881123/purser/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/andrew19881123/purser)](https://github.com/andrew19881123/purser/releases/latest)
[![Status: alpha](https://img.shields.io/badge/status-alpha-orange)](PROJECT_STATUS.md)
[![Docs](https://img.shields.io/badge/docs-GitHub%20Pages-blue)](https://andrew19881123.github.io/purser/)

📖 **[Full documentation → andrew19881123.github.io/purser](https://andrew19881123.github.io/purser/)**

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

## Install

Purser ships **prebuilt artifacts** for v0.3.0 — you do **not** need to compile
anything to run it. It comes as **two kinds of workload**, matching its two
planes:

- **Control Plane, API Gateway, and UI** — **container images** on GHCR,
  deployed with **Helm on Kubernetes**.
- **Agent** — a native **host package** (`.deb` / `.rpm`) or **binary tarball**
  on each fleet node. The Agent needs the node's GPUs/accelerators and supervises
  an inference-engine worker that is *not* sandboxed, so it runs on the host and
  **not** in Kubernetes.

Everything is published: the container images live on **GHCR** and the
packages/tarballs (`+ SHA256SUMS`) are attached to the
[**latest release**](https://github.com/andrew19881123/purser/releases/latest).

### Kubernetes (Helm) — Control Plane, Gateway, UI

The chart is published as an **OCI artifact** on GHCR, so you install it with a
**single command — no clone, no build step**. The chart's default `values.yaml`
already points at the published GHCR images
(`ghcr.io/andrew19881123/purser-{control-plane,gateway,ui}:0.1.0`), which are
**public**, so Helm pulls both the chart and the images for you with **no pull
secret**:

```bash
helm install purser oci://ghcr.io/andrew19881123/charts/purser --version 0.3.0 \
  --set controlPlane.service.type=LoadBalancer   # so out-of-cluster LAN Agents can reach it
```

That's it: one command pulls the prebuilt chart from the registry and the public
images referenced by its defaults.

`--set controlPlane.service.type=LoadBalancer` (or `NodePort`) exposes the
Control Plane's gRPC RegistrationService + REST API so Agents running **outside**
the cluster can enroll. With the default `ClusterIP` the Control Plane is
reachable only inside the cluster.

> **Chart registry visibility.** The one-command install above works because the
> OCI chart package is **public**. If you host the chart in a **private** registry
> instead, authenticate first with
> `helm registry login ghcr.io -u <user> --password-stdin` (paste a token with
> `read:packages`) before `helm install`/`helm pull`.

**Alternative — from source (to customize the chart).** If you want to edit the
chart, pin it in-tree, or install without OCI, clone the repo and install from
the local path:

```bash
git clone https://github.com/andrew19881123/purser.git
cd purser
helm install purser deploy/helm/purser \
  --set controlPlane.service.type=LoadBalancer
```

> **Private registry (optional).** Only if you host the images in your **own**
> private registry do you need a pull secret — create one and reference it with
> `--set imagePullSecrets[0].name=ghcr` (see [`deploy/README.md`](deploy/README.md)).

Keep `replicaCount: 1`: the SQLite **Registry** and internal **PKI** are
single-writer. **Multi-replica HA** requires the **Raft-replicated Registry**, an
enterprise (source-available, key-gated) feature.

### Linux fleet (native `.deb` / `.rpm` packages) — the Agent

Download the package for your distro from the
[**latest release**](https://github.com/andrew19881123/purser/releases/latest)
and install it with your package manager — **no build required**:

```bash
sudo apt install ./purser-agent_0.3.0_amd64.deb        # Debian / Ubuntu
sudo yum install ./purser-agent-0.3.0-1.x86_64.rpm     # RHEL / Fedora / openSUSE
```

The package installs the `purser-agent` **systemd** service and a config file at
`/etc/purser/agent.env` — set `PURSER_JOIN_TOKEN` and the control-plane address
there, then `sudo systemctl enable --now purser-agent`. At **fleet scale**,
mirror the `.deb` / `.rpm` into an internal **apt / yum** repository and roll them
out with **MDM / Ansible / Intune**.

### Binaries (tarballs)

If you don't use the native packages, grab the prebuilt Linux binaries from the
same release — one tarball per component — and verify them against the published
`SHA256SUMS`:

```bash
# Release assets (linux-amd64):
#   purser-agent-0.3.0-linux-amd64.tar.gz
#   purser-control-plane-0.3.0-linux-amd64.tar.gz
#   purser-gateway-0.3.0-linux-amd64.tar.gz
sha256sum -c SHA256SUMS                                 # verify against the release checksums
tar -xzf purser-agent-0.3.0-linux-amd64.tar.gz
```

### macOS / Windows — the Agent

Install the Agent as a **launchd** daemon (macOS) or a **Windows service** — see
[`packaging/`](packaging/README.md).

---

For the full **Kubernetes** guide (chart values, image visibility / pull secrets,
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
  token. See [Install](#install) for the commands; the service unit files
  and environment templates are in [`packaging/`](packaging/README.md).
- **Deploy a model.** Register it and deploy via the control plane REST API:

  ```bash
  curl -X POST http://<control-plane>:8080/api/v1/models        -d @model.json
  curl -X POST http://<control-plane>:8080/api/v1/models/<id>/deploy -d '{}'
  ```

- **Point any OpenAI client at the Gateway:**

  ```python
  from openai import OpenAI
  client = OpenAI(base_url="http://<gateway-host>:<port>/v1",
                  api_key="<your-api-key>")
  client.chat.completions.create(model="<id>",
      messages=[{"role": "user", "content": "Hello"}], stream=True)
  ```

- **Point the Anthropic SDK at the Gateway** (v0.3+):

  ```python
  import anthropic
  client = anthropic.Anthropic(
      api_key="<your-api-key>",
      base_url="http://<gateway-host>:<port>",
  )
  client.messages.create(model="<id>", max_tokens=1024,
      messages=[{"role": "user", "content": "Hello"}])
  ```

  Claude Code, Cursor, and any tool using `@anthropic-ai/sdk` work the same way — just set the base URL.

> **Gateway API keys**: If `PURSER_GATEWAY_API_KEYS` is not set, the gateway
> runs in **open dev mode** and accepts any non-empty bearer token. Always set
> this env var in production deployments.

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

## Build from source & local demo

You only need this if you want to **try the zero-config flow without a cluster**
(everything runs on one host against the built-in **mock engine** — no GPU
required), or if you're **developing** Purser. This is **not** the way to install
a real deployment — for that, use the prebuilt artifacts in [Install](#install).

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

**Alpha — v0.3.0.** The zero-config vertical (*enroll → deploy → chat*) is implemented and demonstrated end-to-end. The architecture supports enterprise features but **live inference on real GPU hardware** has not yet been validated. Use for evaluation, development, and staging — not yet recommended for production.

v0.3 adds Anthropic SDK compatibility (`/v1/messages`), `purser.yaml` config-as-code + GitOps reconciler, inference audit log (AI Act Art.12), deployment approval gates (Art.14), embedded OPA policy engine, HA Raft foundation, chargeback reports, HTTP proxy + custom CA bundle, and backup/restore CLI.

CI is green across all jobs (proto, Rust, Go, UI). See [CHANGELOG.md](CHANGELOG.md) for the full history and [PROJECT_STATUS.md](PROJECT_STATUS.md) for the honest backlog — including the GPU validation gap.
