# Purser vs Ollama

Both projects let you run open-weight models on your own hardware, and both drive
llama.cpp-derived inference rather than reimplementing it. They solve different
problems, and for most people **the honest answer is Ollama**.

Use this page to work out which side of the line you are on.

---

## The short version

| | **Purser** | **Ollama** |
|---|---|---|
| Scope | Orchestrator for a **fleet** of machines | Model runner for **one** machine |
| Splits one model across several machines | Yes — automatic layer split | No |
| Validated on real GPUs today | **Not yet** | Yes |
| Setup effort | Control plane + an agent per node | One binary |
| Model library | You register models yourself | `ollama pull <name>` |
| Fleet identity, RBAC, audit trail | Yes | Not in scope |

!!! warning "Purser is alpha and GPU inference is unvalidated"
    Purser's llama.cpp adapter is registered but has **not** been validated
    against real GPU hardware, and this project publishes no throughput numbers.
    See [Status](../index.md#status) and [Benchmarks](../benchmarks/index.md).
    Ollama runs on real GPUs today. If you need something working this
    afternoon, that difference matters more than any architectural argument
    below.

---

## Choose Ollama when

- **The model fits on one machine.** This covers the large majority of local
  inference. Nothing in Purser's design makes the single-host case faster —
  Purser would simply add a control plane in front of the same class of engine.
- **You want it working now.** See the warning above.
- **You are one person on one desktop or laptop.** Purser's value is fleet
  coordination. With no fleet, there is nothing to coordinate.
- **You want a curated model library.** `ollama pull` resolves a name to
  weights. Purser expects you to register the model *and its architecture*
  yourself — layer count, hidden size, KV-head geometry, and the quantisations
  available. See [Model Catalog](../configuration/models.md).

---

## Consider Purser when

### The model does not fit on any single machine you own

This is the case Purser exists for. Purser splits a model **by layer** across
nodes (pipeline parallelism) and computes the split for your specific hardware
instead of asking you to hand-tune it. Two machines with a 24 GB card each are
planned against a combined budget, less a per-node reservation for the OS and
runtime.

Ollama runs a model on one host; it has no facility for splitting a single model
across several machines. So a 70B model at Q4 is out of reach for a collection
of 24 GB cards, however many of them you own.

See [Multi-Node Inference](../getting-started/multi-node-inference.md) for the
setup, and [Consumer GPU Setup](consumer-gpu-setup.md) for how the planner
treats nodes with different VRAM.

### You have several machines and want one endpoint

Purser presents the whole fleet as a single OpenAI-compatible endpoint through
its [API Gateway](../api/gateway.md), and keeps a registry of nodes,
deployments, and routes. Adding a node means enrolling an agent, not
reconfiguring clients.

### You need what an orchestrator brings

Purser ships fleet-level controls that a single-host runner has no reason to
have:

- Internal PKI with mTLS between control plane and agents
  ([PKI Operations](../operations/pki-operations.md))
- API keys with RBAC roles ([RBAC](../configuration/rbac.md)) and OIDC SSO
  ([OIDC](../configuration/oidc.md))
- A tamper-evident, hash-chained audit log
  ([Audit Log](../enterprise/audit-log.md) — enterprise)
- Multi-tenant organisations, teams, and node pools
  ([Platform Model](../configuration/platform-model.md))

If you are running inference for a team rather than for yourself, this is the
part a single-host runner deliberately leaves out.

---

## They are not really competitors

Purser does **not** reimplement inference. It drives existing engines through an
Engine Adapter and treats the engine as a replaceable backend — the same
llama.cpp lineage Ollama builds on. The comparison is closer to *Docker vs
Kubernetes* than to two rival engines: one runs a workload on a host, the other
schedules workloads across hosts.

A reasonable setup is both: Ollama on the workstation for interactive work,
Purser for the models too large to fit there.

---

## What Purser does not do

Stated plainly, so you can rule it out quickly:

- **No tensor parallelism.** Layers are assigned as contiguous ranges to nodes;
  a single layer is never split across two machines. For the trade-off this
  implies, see [Purser vs vLLM](vs-vllm.md).
- **No lower latency for a single request.** Splitting a model across machines
  adds a network hop at every pipeline stage boundary. Pipeline parallelism is a
  way to run a model that otherwise would not fit — not a way to make a model
  that already fits go faster.
- **No curated model registry.** You supply the model architecture.
- **No published performance numbers.** See
  [Benchmarks](../benchmarks/index.md) for what a published benchmark will
  contain, and why none exists yet.

---

## See also

- [Purser vs vLLM](vs-vllm.md) — pipeline vs tensor parallelism
- [Multi-Node Inference](../getting-started/multi-node-inference.md)
- [Consumer GPU Setup](consumer-gpu-setup.md)
- [FAQ](../faq.md)
