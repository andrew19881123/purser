# Consumer GPU Setup (mismatched cards, no NVLink)

Home and lab fleets are rarely uniform: a 24 GB card in the desktop, a 12 GB card
in the older box, maybe an 8 GB card in a spare machine. This page explains how
Purser's planner treats that fleet, and — more usefully — **which mismatch
actually matters**.

The short answer surprises people: *VRAM decides whether a plan exists; memory
bandwidth decides how the layers are shared out.*

!!! warning "Alpha, and unmeasured on GPUs"
    Purser's llama.cpp adapter has not been validated against real GPU hardware,
    and the planner's performance constants are explicitly uncalibrated. This
    page describes what the planner *computes*; it does not promise what your
    hardware will deliver. See [Benchmarks](../benchmarks/index.md).

---

## No NVLink required — and none expected

Purser has no concept of NVLink, PCIe topology, or intra-node GPU interconnect.
Its planner models only **node-to-node** links, and it splits models into
contiguous layer ranges whose stage boundaries carry a single activation tensor
per token. Absent NVLink is not a degraded mode for Purser; it is the only mode
it models.

One consequence worth knowing: when a node reports several GPUs, the planner
**sums their VRAM into one flat number**. Two 12 GB cards in one machine look
identical to a single 24 GB card. If you want the planner to treat them as
separate pipeline stages, run one agent per GPU — see
[single-host multi-GPU](../getting-started/multi-node-inference.md#single-host-multi-gpu).

---

## What the planner actually reads from each node

The agent reports a hardware profile; the planner consumes a small set of fields:

| Input | What it does |
|---|---|
| `VRAMGB` | Feeds the per-node memory ceiling |
| `RAMAvailableGB` | Host RAM net of the OS and other workloads |
| `UnifiedMemory` | Apple Silicon and similar — VRAM and RAM are one pool |
| `MemBandwidthGBs` | **The only speed input.** Proxy for decode throughput |
| `DiskFreeGB`, `KVSSDOffload` | Optional KV-cache spill to SSD, when the engine supports it |
| `PrefixCachingFactor` | Expected KV-cache hit fraction, when the engine reports it |

Note what is *absent*: there is no FLOPS, no SM count, no GPU model name. The
planner does not know a 4090 from a 3060 except through the memory bandwidth
number reported for it.

### How usable memory is derived

```
unified memory   → RAMAvailableGB
has VRAM         → min(RAMAvailableGB, VRAMGB)
CPU-only         → RAMAvailableGB
```

The `min()` matters on consumer boxes: a machine with a 24 GB card but only 16 GB
of *available* system RAM is planned as a 16 GB node. Skimping on host RAM
silently shrinks your GPU.

---

## The two mismatches, and why only one rebalances the split

### VRAM mismatch sets a ceiling

Every pipeline stage must satisfy:

```
weights share + KV-cache share + 2 GB overhead  ≤  usable memory × 0.90
```

Two reservations are applied. A flat **2 GB per node** for the OS and inference
runtime, and a **10% headroom** on top. A stage that violates this is excluded
from consideration outright, so an 8 GB node simply cannot be assigned more
layers than fit inside roughly 5 GB of weights-plus-KV. That is the ceiling.

### Bandwidth mismatch sets the balance

The planner minimises the **slowest stage** — a dynamic program over cut points
that follows the PipeEdge line of work, described with its prior art in
[Purser vs vLLM](vs-vllm.md#how-purser-plans-the-split). A stage's compute cost is
modelled as the bytes of active weights it must stream divided by that node's
memory bandwidth. Minimising the maximum therefore equalises
`layers ÷ bandwidth` — which means:

> Layers are distributed in proportion to **memory bandwidth**, not in proportion
> to VRAM.

This is the counter-intuitive part. The two tables below are **worked
illustrations of the objective**, not measurements — the memory and bandwidth
columns are inputs you supply, and the layer column is what minimising the
slowest stage implies for them.

First, two nodes with identical memory bandwidth but very different capacity:

| Node | Usable memory | Bandwidth (input) | Layers implied |
|---|---|---|---|
| A | 24 GB | 900 GB/s | ~half |
| B | 12 GB | 900 GB/s | ~half, until its ceiling binds |

The 24 GB card does **not** attract twice the layers just for being larger. It
only takes on more once node B's memory ceiling forces the split to move. Node A
ends up with spare VRAM, and that is the planner behaving as designed — its
objective is stage time, not VRAM utilisation.

Now vary the bandwidth instead, keeping the ratio easy to follow:

| Node | Usable memory | Bandwidth (input) | Layers implied |
|---|---|---|---|
| A | 24 GB | 900 GB/s | ~3× node B's share |
| B | 12 GB | 300 GB/s | ~1× |

Here the split does shift, because equalising `layers ÷ bandwidth` across stages
means a 3:1 bandwidth ratio produces roughly a 3:1 layer split. In practice the
bigger card usually also has the wider memory bus, so the two effects point the
same way — but the driver is bandwidth.

!!! tip "Report memory bandwidth accurately"
    Because it is the only speed signal the planner has, a wrong
    `MemBandwidthGBs` produces a badly balanced pipeline even when every stage
    fits. Take the figure from the card's specification.

---

## Practical consequences for a mixed fleet

**A small node can make a plan infeasible even when the totals look fine.**
Aggregate VRAM may exceed the model size while no *contiguous* split satisfies
every stage's ceiling. This surfaces as a distinct planner error —
`no feasible contiguous partition ... (memory/headroom)` — and, unhelpfully, with
a reported deficit of 0 GB, because the shortfall is per-stage, not aggregate. If
you see a 0 GB deficit, the fix is usually to exclude the smallest node rather
than to add memory.

**Excluding your weakest node often helps twice.** It removes the binding ceiling
and it removes a hop. The planner already prefers fewer nodes when an extra hop
does not pay for itself, and it will choose a single-node plan over a split when
that is better.

**Splitting can be stricter than not splitting.** The 10% headroom is applied to
multi-node stages but not to a single-node plan. The same model can therefore be
accepted on one 24 GB card and rejected across two — this is deliberate, not a
bug.

**Layer weights are assumed uniform.** Each layer is costed as
`model size ÷ layer count`. Embedding tables and the LM head are not modelled
separately, so the first and last stages of a real model are somewhat heavier
than the planner believes. The 2 GB per-node reservation is what absorbs that.

**Quantisation is chosen for you.** The planner picks the highest-quality
quantisation that fits your fleet, and you can pin one explicitly if you would
rather trade quality for headroom.

---

## Check before you commit hardware

Two read-only tools answer "will this fit?" without deploying anything.

**Dry-run the plan for your current fleet.** This returns feasibility, the split,
and the reason if infeasible — and it returns `200` with `"feasible": false`
rather than an error, so it is safe to poll:

```bash
curl -sS -X POST http://<control-plane>:8080/api/v1/models/<model-id>/plan \
  -H 'Authorization: Bearer <admin-key>' | jq
```

**Model hardware you do not own yet** with the
[what-if planner](../operations/what-if-planner.md) — add hypothetical nodes and
see whether the model becomes feasible before you buy the card.

The plan explanation names the chosen quantisation, the bottleneck stage, and
each node's `need X GB of Y GB useful`, which is the fastest way to see which
node is binding.

---

## Setting expectations on throughput

The planner reports an estimated throughput **range**, not a measurement. That
range is produced from constants the project documents as requiring calibration
against real hardware — sustained bandwidth fraction, prefill-to-decode ratio,
speculative acceptance rate — and it carries a deliberate uncertainty band.
Treat the planner's estimate as a feasibility signal, not a performance promise,
and see [Benchmarking the Planner](../development/benchmarking.md) if you want to
calibrate those constants for your own hardware.

---

## See also

- [Multi-Node Inference](../getting-started/multi-node-inference.md) — the setup steps
- [Homelab Setup](homelab-setup.md) — two machines, Docker Compose, no Kubernetes
- [Purser vs vLLM](vs-vllm.md) — why the interconnect requirement is low
- [What-if Planner](../operations/what-if-planner.md)
- [Benchmarks](../benchmarks/index.md)
