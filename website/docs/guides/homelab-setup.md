# Homelab Setup (two machines, no Kubernetes)

You do not need Kubernetes to run Purser. This guide describes the Docker Compose
path for a two-machine homelab: the control plane in containers on one box, a
native agent on each machine that has a GPU.

It deliberately **does not repeat** the steps in
[Quickstart](../getting-started/quickstart.md) or
[Multi-Node Inference](../getting-started/multi-node-inference.md) — it explains
the topology, the wiring between the two, and the parts specific to a home LAN.

---

## Read this first

!!! warning "The prebuilt agent cannot run real models yet"
    The real inference backend is a **compile-time feature** of the agent
    (`--features llamacpp`), and the release workflow currently builds
    `purser-agent` **without** it. The published `.deb`, `.rpm`, and tarball
    agents therefore register only the `mock` engine. Setting
    `PURSER_ENGINE_BACKEND=llamacpp` on such a binary fails with:

    ```
    llama.cpp backend requested but binary was not compiled with --features llamacpp
    ```

    So today this guide gets you a working **fleet, planner, and OpenAI-compatible
    endpoint** returning deterministic mock output. To serve real weights you must
    build the agent yourself with `--features llamacpp` and provide llama.cpp
    binaries. Purser is alpha and its GPU path is not yet validated — see
    [Status](../index.md#status).

That is worth knowing before you spend an evening on it. Everything below is
still useful: it is the same topology real inference will use.

---

## The topology

The Compose stack contains the **control plane, gateway, UI, database, and
proxy** — it contains no agent. Agents run natively on the machines that own the
GPUs, because they supervise a local engine process and need direct access to the
hardware.

```
            ┌──────────────── Machine A (also a GPU node) ────────────────┐
            │                                                            │
 you ──────▶│  docker compose:  proxy :3000 ──▶ ui                       │
            │                   control-plane :8080 REST · :9443 gRPC    │
            │                   gateway (OpenAI /v1)                     │
            │                   postgres                                 │
            │                                                            │
            │  native:          purser-agent  :50151 · engine :8000      │
            └──────────────────────────┬─────────────────────────────────┘
                                       │  LAN (gRPC + mTLS, activations)
            ┌──────────────────────────┴─────────────────────────────────┐
            │  Machine B                                                 │
            │  native:          purser-agent  :50151 · engine :8000      │
            └────────────────────────────────────────────────────────────┘
```

Machine A doubles as a GPU node here, which is the normal homelab arrangement.
Nothing stops you from putting the control plane on a fanless mini-PC instead —
it does not need a GPU.

---

## Prerequisites

- Two machines on the same LAN, reachable by IP, with static addresses or DHCP
  reservations
- Docker and the Compose plugin on machine A
- A GPU on each machine you want to serve layers
- Enough **host RAM** on each node — the planner uses
  `min(available RAM, VRAM)` as a node's usable memory, so a 24 GB card behind
  16 GB of free RAM is planned as a 16 GB node

---

## Step 1 — Control plane on machine A

Bring up the Compose stack following
[Quickstart](../getting-started/quickstart.md). The important homelab-specific
change is that the control plane must be reachable from machine B, not only from
localhost — note machine A's LAN address (for example `192.168.1.10`) and use it
everywhere below.

Confirm it answers on the LAN address, not just `127.0.0.1`:

```bash
curl -s http://192.168.1.10:8080/api/v1/cluster/health
```

If that works from machine B, the wiring is right.

!!! warning "The demo stack is not hardened"
    The Compose stack ships demo credentials and a demo gateway API key, and the
    gateway serves plaintext HTTP by design — TLS belongs on an ingress in front.
    Purser assumes a **trusted LAN**. Do not port-forward any of these ports to
    the internet.

---

## Step 2 — An agent on each GPU machine

Install the agent natively on **both** machines, following
[Linux Agent (.deb/.rpm)](../install/linux-agent.md), or
[macOS / Windows Agent](../install/macos-windows.md) for those platforms.

Point each agent at machine A's control plane and give it a join token. The
enrolment procedure, token minting, and the environment file layout are covered
step by step in
[Multi-Node Inference](../getting-started/multi-node-inference.md) — follow that
page rather than improvising, since the join token and mTLS enrolment must match.

Two homelab notes:

- `PURSER_ENGINE_BACKEND` is an **agent-side** setting and defaults to `mock`.
  Setting it on the control plane does nothing.
- Choosing `llamacpp` additionally requires `PURSER_LLAMACPP_BIN` to point at the
  directory holding the llama.cpp binaries, and an agent built with that feature
  (see the warning at the top).

---

## Step 3 — Confirm both nodes joined

```bash
curl -s http://192.168.1.10:8080/api/v1/nodes \
  -H 'Authorization: Bearer <admin-key>' | jq '.nodes[].profile.nodeId'
```

Or open the dashboard at `http://192.168.1.10:3000` and look at the Fleet page.

A node must reach `READY` before the planner will consider it. An agent that
enrols but never becomes ready is almost always a connectivity problem in one
direction — see the [FAQ](../faq.md).

---

## Step 4 — Register a model and deploy

Follow [Deploy Your First Model](../getting-started/deploy-model.md), then
[Multi-Node Inference](../getting-started/multi-node-inference.md) for the split
across two nodes.

The one thing worth doing first is a **dry run**, which tells you whether your
two machines can hold the model before you attempt a deployment:

```bash
curl -sS -X POST http://192.168.1.10:8080/api/v1/models/<model-id>/plan \
  -H 'Authorization: Bearer <admin-key>' | jq
```

It returns `200` with `"feasible": false` and a reason when the model does not
fit, which is much easier to read than a failed deploy. For how the planner
handles two different cards, see [Consumer GPU Setup](consumer-gpu-setup.md).

---

## Ports to open on the LAN

| Port | Component | Direction |
|---|---|---|
| `3000` | Compose proxy → UI and gateway | your browser → machine A |
| `8080` | Control plane REST | agents and clients → machine A |
| `9443` | Control plane gRPC (mTLS) | agents → machine A |
| `50151` | Agent service | control plane → each agent |
| `8000` | Engine inference port | gateway and peer agents → each agent |

Home routers and distribution firewalls are the usual culprit when enrolment
hangs: the control plane must reach each agent on `50151`, and each agent must
reach the control plane on `9443`. It is a **two-way** requirement, which trips
up setups where one machine is on Wi-Fi with client isolation enabled.

---

## Where to go next

- [Consumer GPU Setup](consumer-gpu-setup.md) — mismatched cards, and why VRAM is
  not what balances the split
- [Multi-Node Inference](../getting-started/multi-node-inference.md) — the full
  enrolment and deploy walkthrough
- [Migrating from the OpenAI API](openai-migration.md) — point your existing
  apps at the gateway
- [FAQ](../faq.md) — including what the mock engine is and what phones home
- [purser.yaml](../configuration/purser-yaml.md) — declare the fleet
  declaratively once it stops changing
