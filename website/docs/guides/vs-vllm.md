# Purser vs vLLM

vLLM and Purser both run large models across multiple GPUs, but they lean on
different kinds of parallelism, and the difference is really a difference in
**what crosses the wire**.

This page explains that trade-off. It does not claim Purser is faster — on
hardware vLLM is designed for, it is not.

!!! warning "Alpha, and unmeasured"
    Purser's GPU inference path is not yet validated on real hardware and this
    project publishes no throughput numbers. Everything below is an argument
    about *architecture*, derived from the planner's own cost model — not a
    measurement. See [Benchmarks](../benchmarks/index.md).

---

## The two kinds of parallelism

### Tensor parallelism — split each layer

Tensor parallelism (TP) splits the weight matrices *within* every layer across
GPUs. Each GPU computes a slice of each layer, and the slices must be recombined
before the layer's output is complete. In the standard Megatron-style scheme that
means roughly **two collective operations per transformer layer, per token** —
an all-reduce after attention and another after the MLP.

For an 80-layer model that is on the order of 160 collectives to produce a single
token. Each one is a synchronisation point: every GPU waits for the slowest
participant. This is why TP is normally deployed inside one chassis over NVLink,
or across nodes over InfiniBand — the interconnect is on the critical path of
every layer.

### Pipeline parallelism — split the layer list

Pipeline parallelism (PP) gives each node a **contiguous range of layers**. Node
one runs layers 0–39, node two runs 40–79. A token's hidden state is computed
through the first range, handed to the next node, and continues.

The wire carries only the hidden-state activation at each stage boundary — one
tensor, once per stage boundary, per token. There is no per-layer collective and
no synchronisation inside a stage.

Purser implements pipeline parallelism only. Layers are assigned as contiguous
ranges and a single layer is never split across machines.

---

## Why 10GbE is enough for pipeline parallelism

The size of what crosses the wire is not a marketing claim — it is a term in the
planner's cost function. Purser models the per-token, per-hop payload as the
hidden state in FP16:

```
activation bytes = hidden_size × 2
```

So the payload scales with the model's **hidden size**, not with its parameter
count and not with its layer count:

| Hidden size | Typical model class | Bytes per token, per hop |
|---|---|---|
| 4096 | 7B–8B dense | 8 KB |
| 5120 | 13B dense | 10 KB |
| 8192 | 70B dense | 16 KB |

A 70B model split across two nodes moves about 16 KB per token across one hop.
That is why a commodity switch is not the bottleneck: the payload is kilobytes,
and it crosses the network once per stage boundary rather than 160 times per
token.

Compare the alternative: the model **weights** never cross the network at all.
Each node loads only its own layer range from local storage.

!!! note "This is the cost model, not a measurement"
    The table above is arithmetic on the formula the planner uses to *estimate*
    communication cost. It tells you the order of magnitude of the transfer; it
    does not tell you the throughput you will observe. Only a validated
    benchmark can do that, and Purser has not published one.

---

## The honest cost of pipeline parallelism

Pipeline parallelism buys capacity, not speed. Three consequences worth knowing
before you choose it:

**A single request does not go faster.** With one request in flight, only one
stage is active at a time while the others idle — the classic pipeline bubble.
Splitting a model across two nodes does not halve its latency; it adds a network
hop to every token.

**Throughput depends on keeping the pipeline full.** PP reaches good utilisation
when many requests are in flight, so stage two works on one request while stage
one starts the next. For a single-user setup, most of the fleet is idle most of
the time.

**Every hop adds latency.** The planner charges each stage boundary the link's
round-trip time plus the activation transfer, and picks the split that minimises
the slowest stage. It will also **decline to split** when an extra hop does not
pay for itself, preferring a single-node plan.

Tensor parallelism has the opposite profile: it genuinely reduces per-token
latency by putting more compute on each layer — provided the interconnect can
keep up.

---

## What each project asks of you

| | **Purser** | **vLLM** |
|---|---|---|
| Parallelism | Pipeline (contiguous layer ranges) | Tensor and pipeline |
| Who decides the split | The planner, automatically | You declare the parallel sizes |
| Heterogeneous GPUs | A first-class input | Generally assumes uniform GPUs |
| Interconnect assumed | Commodity Ethernet | Benefits substantially from NVLink / InfiniBand for TP |
| Scope | Fleet orchestrator driving an engine | Inference engine and server |
| Maturity | Alpha, GPU path unvalidated | Widely deployed in production |

The genuine differentiator is not the parallelism strategy — vLLM supports
pipeline parallelism too, and can span nodes. It is **who computes the split**.
vLLM asks you for the parallel sizes and works best when every GPU is the same.
Purser takes a fleet whose nodes differ in VRAM and memory bandwidth and solves
for the split itself.

---

## How Purser plans the split

Two distinct steps are involved, and it is worth keeping them apart.

**Choosing the cut points.** A throughput-aware dynamic program walks the layer
chain and minimises the **bottleneck stage**: of all the ways to cut the layers
into contiguous ranges and assign them to nodes in a given order, it finds the one
whose slowest stage is fastest. Per stage it charges:

- **Compute** — the bytes of active weights the stage must stream, divided by
  that node's memory bandwidth
- **Communication** — the incoming link's round-trip time plus the activation
  transfer above
- **Infeasible** — a stage whose weights, KV-cache share, and per-node overhead
  exceed the node's usable memory is excluded outright, which prunes infeasible
  splits rather than scoring them

**Choosing the node order.** Which machine is stage one, stage two, and so on is a
separate problem: the minimum-cost path visiting every node over the
activation-transfer edge costs. Purser solves this exactly for small fleets and
falls back to a heuristic beyond ten nodes.

Memory is a hard constraint; bandwidth drives the balance. That distinction has
real consequences on mismatched consumer cards, and is covered in
[Consumer GPU Setup](consumer-gpu-setup.md).

!!! note "Prior art"
    Neither step is a Purser invention, and the source says so. The layer-split
    dynamic program follows **PipeEdge**
    ([arXiv:2110.14895](https://arxiv.org/abs/2110.14895)), and treating memory as
    a hard constraint that prunes the search follows the "water-filling" approach
    of **Parallax** ([arXiv:2509.26182](https://arxiv.org/abs/2509.26182)). The
    node-ordering step is textbook Held-Karp. What Purser contributes is the
    engineering around them: running this planning automatically, over a fleet of
    machines that differ from each other, with no hand-tuning from the operator.

!!! warning "The planner does not measure your network"
    The cost model has terms for per-link round-trip time and bandwidth, but
    Purser does not currently populate that link matrix from live measurements
    outside the
    [what-if planner](../operations/what-if-planner.md). When a link is unknown
    the communication term is treated as zero, so in practice the split is
    driven by memory limits and memory bandwidth. Do not assume Purser is tuning
    the plan to your switch.

---

## Choose vLLM when

- You have GPUs in one chassis with NVLink, or nodes on InfiniBand — TP will use
  that hardware and Purser will not.
- Your GPUs are identical, so there is little for a heterogeneous planner to
  solve.
- You need maximum throughput per GPU today, with mature continuous batching and
  paged KV-cache management.
- You need a production-proven serving stack. Purser is alpha.

## Consider Purser when

- Your machines are **different from each other** — mixed VRAM, mixed
  generations — and you do not want to hand-tune a split for each combination.
- Your interconnect is ordinary Ethernet and you have no NVLink or InfiniBand.
- You want the fleet to behave like one endpoint, with node enrolment, mTLS,
  RBAC, and an audit trail, rather than a serving process you configure per host.

---

## They may end up complementary

Purser does not implement inference; it drives an engine through an Engine
Adapter, currently llama.cpp. Using a tensor-parallel engine such as vLLM as a
Purser backend — TP inside each node, PP between nodes — is a plausible
direction and appears in the project backlog, but it is **not implemented
today**. Do not plan around it.

---

## See also

- [Purser vs Ollama](vs-ollama.md)
- [Architecture](../getting-started/architecture.md)
- [Consumer GPU Setup](consumer-gpu-setup.md)
- [Benchmarking the Planner](../development/benchmarking.md) — the calibratable
  constants behind the cost model
- [What-if Planner](../operations/what-if-planner.md)
