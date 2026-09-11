# Benchmarks

**Purser publishes no inference throughput numbers.** Not because they are
unflattering — because they do not exist yet in a form worth publishing.

This page explains why, and defines exactly what a Purser benchmark will contain
when there is one. It is a contract you can hold us to.

---

## Why there are no numbers yet

Three things have to be true before a throughput figure means anything, and none
of them is true today:

**The GPU path is not validated.** The llama.cpp engine adapter is registered but
has not been validated against real GPU hardware. Additionally, the released agent
binaries are currently built without the `llamacpp` feature, so they register only
the deterministic `mock` engine — timings taken against that measure Purser's
orchestration overhead, not inference.

**The planner's performance model is uncalibrated.** The planner's cost constants
— sustained memory-bandwidth fraction, prefill-to-decode ratio, speculative
acceptance rate, SSD read bandwidth — are documented in the source as
first-order estimates awaiting calibration against real hardware. Calibrating
them *is* the benchmarking work; publishing numbers produced from uncalibrated
constants would be circular.

**A number without its conditions is noise.** Tokens per second is meaningless
without the model, quantisation, context length, concurrency, and interconnect
that produced it. Most published LLM throughput figures omit at least one.

!!! warning "The planner's estimates are not benchmarks"
    When you deploy, the planner reports an estimated throughput **range**. That
    is a feasibility signal computed from the uncalibrated constants above, with a
    deliberate uncertainty band. Do not quote it as a measurement, and do not
    expect your hardware to match it.

---

## Not to be confused with planner benchmarks

Purser does have a real, running benchmark suite — but it measures the
**planner**, not inference. It answers "how long does `Plan()` take on a
100-node fleet", in CPU microseconds, and it runs in CI on every push.

That work is documented in
[Benchmarking the Planner](../development/benchmarking.md), which is also where
the calibratable constants and the procedure for tuning them to your hardware
live. This page is about the thing that does not exist yet: end-to-end
**inference** measurements.

---

## What a published benchmark will always include

Every performance figure this project publishes will be accompanied by all of the
following. If any item is missing, treat the number as unpublished.

### Hardware

- CPU model and core count; total RAM and its speed
- Every GPU: model, VRAM, and driver version
- NIC link speed per node, and the switch between them — the number that makes or
  breaks a pipeline-parallel claim
- Storage class for model weights and any KV-cache spill

### Software

- Purser version **and** commit SHA
- Inference engine and its exact commit — for llama.cpp, the upstream SHA, since
  its performance moves week to week
- Model identifier, the exact quantisation file, and that file's SHA256
- OS and kernel version; CUDA or ROCm version

### Workload

- Prompt and completion lengths, and their distribution if not fixed
- Context window configured
- Concurrency level — one request at a time and 32 in flight are different
  experiments, and for pipeline parallelism the difference is the whole story
- Whether speculative decoding, prefix caching, or KV offload were enabled

### Method

- Number of **warmup** runs, discarded, stated explicitly
- Number of **measured** runs
- **p50, p95, and p99** — never a bare mean, and never a single run
- **Time to first token** reported separately from inter-token latency and from
  aggregate tokens per second, because pipeline parallelism affects them
  differently
- The measurement date

### Reproduction

- A script committed to this repository, and the exact command line invoked
- The raw output, committed alongside the summary

---

## What this project will not publish

- **No estimated, extrapolated, or illustrative numbers.** No "up to", no figure
  derived from a model rather than a measurement, and no example table of
  plausible-looking values.
- **No competitor numbers we did not measure ourselves** on the same hardware, in
  the same run, with the configuration disclosed. Comparisons against figures
  lifted from someone else's blog post are not comparisons.
- **No best-of-N runs.** The distribution gets published, including its tail.
- **No benchmark against the mock engine presented as inference performance.**

---

## Measuring your own fleet

Nothing stops you from benchmarking, and if you do, your numbers are more relevant
to your hardware than ours will be. Two suggestions:

- Establish a **single-node baseline first** with the same engine, model, and
  quantisation outside Purser. Without it you cannot separate the cost of the
  split from the cost of the engine.
- Vary **concurrency deliberately**. Pipeline parallelism idles most of the fleet
  at concurrency 1 and only fills up as requests overlap; a single-request
  measurement will look bad for reasons that have nothing to do with your network.

If you publish results, please include the disclosure list above — and consider
opening an issue with them. Real numbers from real fleets are the fastest route
to calibrating the planner.

---

## See also

- [Benchmarking the Planner](../development/benchmarking.md) — the planner
  micro-benchmarks that do exist, and the calibratable constants
- [Purser vs vLLM](../guides/vs-vllm.md) — the architectural argument, explicitly
  not a performance claim
- [Consumer GPU Setup](../guides/consumer-gpu-setup.md)
- [Status](../index.md#status)
