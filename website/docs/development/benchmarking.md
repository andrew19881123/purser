# Benchmarking the Planner

The Purser Planner (`go/planner/plan`) contains a micro-benchmark suite that
measures `Plan()` performance across fleet sizes, model classes, and edge cases.
Running the benchmarks locally is the primary way to calibrate the
[CALIBRATABLE constants](#calibratable-constants) in `plan.go`.

---

## Running benchmarks locally

```bash
# From the repo root — ensure the toolchain is on PATH first:
source ./env.sh

# Run all benchmarks once, with allocation counts:
cd go/planner
CGO_ENABLED=0 go test ./plan/... -bench=. -benchmem -run='^$'

# Run a specific benchmark (e.g. just the large fleet):
CGO_ENABLED=0 go test ./plan/... -bench=BenchmarkPlanLargeFleet -benchmem -run='^$'

# Run with higher iteration count for a stable mean (5 runs):
CGO_ENABLED=0 go test ./plan/... -bench=. -benchmem -count=5 -run='^$' > new.txt
```

The `-run='^$'` flag suppresses regular tests so only benchmarks execute.

---

## Comparing against the baseline

The file `go/planner/plan/bench_baseline.txt` records the reference run.
Use `benchstat` (part of `golang.org/x/perf`) to compare:

```bash
# Install benchstat once (uses the project Go toolchain):
go install golang.org/x/perf/cmd/benchstat@latest

# Capture a new run:
cd go/planner
CGO_ENABLED=0 go test ./plan/... -bench=. -benchmem -count=5 -run='^$' > /tmp/new.txt

# Compare:
benchstat go/planner/plan/bench_baseline.txt /tmp/new.txt
```

`benchstat` reports the geometric mean and a p-value for each benchmark. A
delta > ±10% with p < 0.05 is a signal worth investigating.

---

## Benchmark scenarios

| Benchmark | Fleet | Model | Expected path |
|---|---|---|---|
| `BenchmarkPlanSmallFleet` | 4 nodes | 7B q4 (7 GB) | Single-node plan (rule G3) |
| `BenchmarkPlanMediumFleet` | 20 nodes | 100B q4 (50 GB) | 3-node pipeline split |
| `BenchmarkPlanLargeFleet` | 100 nodes | 80B q8 (160 GB) | 8-node pipeline split |
| `BenchmarkPlanNoFit` | 4 nodes | 500B q4 (250 GB) | Early-exit PlanError (phase A) |
| `BenchmarkPlanForceNodeCount` | 4 nodes | 7B q4, `ForceNodeCount=1` | Forced single-node + validatePlanMemory |
| `BenchmarkFitAll` | 20 nodes | 10-model catalog | Full phase A–F across architectures |

**Node spec** used in all scenarios: 24 GB VRAM, 56 GB RAM available,
200 GB/s memory bandwidth (A6000-class GPU node). Links are 25 GB/s / 2 ms RTT
(NVLink/InfiniBand class).

### Why these scenarios

- **SmallFleet** is the common case: a small model served by a single node.
  It exercises the hot path (phases A and B's rule G3 short-circuit) and the
  `estimateSingleNode` performance estimate.

- **MediumFleet** and **LargeFleet** exercise the full pipeline (phases A–F):
  quantization selection, candidate subset ranking, the throughput-aware DP
  partition (phase C, `dpPartition`), Held-Karp ordering (phase D), draft
  placement (phase E), and per-node failover pre-computation (phase F).

- **NoFit** measures the cost of the fast-fail path — important for the
  control plane's planning loop, which may test many models that cannot fit.

- **ForceNodeCount** exercises the operator-constraint path plus the
  `validatePlanMemory` backstop that guards against constraint-induced
  memory overflow.

- **FitAll** simulates a full model-catalog sweep: 10 models spanning dense,
  MoE, MLA, linear-attention, and draft architectures, giving an end-to-end
  wall-clock budget for catalog re-planning on fleet topology changes.

---

## CALIBRATABLE constants

`plan.go` documents a set of named constants that are deliberately
"first-order estimates" requiring calibration against real hardware.
Benchmark results are the primary input for tightening them.

| Constant | Default | What it models | How to calibrate |
|---|---|---|---|
| `memBandwidthUtilFraction` | `0.70` | Fraction of peak memory bandwidth sustained at decode | Run a single-node decode benchmark; compare measured tok/s to `1 / (weights_GB / bw_GBs)`. |
| `expectedAcceptedTokens` | `4.5` | Speculative-decoding effective step size | Measure acceptance rate on representative traffic with the draft model enabled. |
| `perfBandFraction` | `0.30` | ±uncertainty band around the point estimate | Narrow as coefficients are pinned; start ≥ 0.20. |
| `prefillComputeMultiple` | `8.0` | Prefill throughput / decode throughput | Benchmark prefill vs decode throughput on target hardware. |
| `kvSsdOffloadFactor` | `0.50` | Usable fraction of SSD for KV offload | Measure KV-cache SSD throughput vs VRAM throughput for the engine. |
| `overheadOSRuntimeGB` | `2.0` | Per-node OS + runtime memory reservation | Measure actual RSS of the OS + engine process at idle. |
| `costW1Hops` | `10.0` | Cost-function weight for pipeline hops | Calibrate using `tc netem` link sweeps on representative hardware. |
| `costW2LinkCost` | `1.0` | Cost-function weight for per-edge network cost | Same as above. |
| `costW3Imbalance` | `5.0` | Cost-function weight for stage-time variance | Tune against multi-node decode profiling. |

The benchmark suite makes regressions in planner throughput visible; the
calibration workflow (benchmark → adjust constant → benchmark again) is
documented in design document 08 §15.

---

## CI integration

The CI workflow (`go` job, `planner` matrix) runs the benchmarks with
`-benchtime=1x` (one iteration each) on every push and PR:

```yaml
- name: benchmark
  if: matrix.module == 'planner'
  working-directory: go/planner
  run: |
    CGO_ENABLED=0 go test ./plan/... -bench=. -benchmem -benchtime=1x \
      -run='^$' 2>&1 | tee /tmp/planner-bench-results.txt
    cat /tmp/planner-bench-results.txt
```

`benchtime=1x` is a smoke check only — it confirms the benchmarks compile and
run without panicking, and prints baseline numbers in the CI log for trend
spotting. It is **not** a performance gate. Use `benchstat` locally with
`-count=5` or higher for statistically meaningful comparisons.
