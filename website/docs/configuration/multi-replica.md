# Multi-Replica Deployments

Multi-replica planning deploys the same model on multiple disjoint subsets of your fleet simultaneously.
Each replica is a complete, independent inference pipeline.
The gateway load-balances requests across all healthy replicas, increasing throughput and resilience.

## Why replicas?

| Single replica | Multi-replica |
|---|---|
| One pipeline, one failure domain | N pipelines, N failure domains |
| Throughput limited to one chain | Throughput scales with replica count |
| Planned failover requires a spare pool | Every replica is already live |

Replicas are the primary horizontal scaling primitive in Purser.
They complement [failover plans](../architecture/failover.md), which kick in when a *node* dies within a replica.

## How it works

`PlanReplicaSet` partitions the fleet into *N* equal-ish groups and plans each independently:

1. **Sort** nodes by available memory descending (best nodes first).
2. **Estimate k_min** — run a trial plan on the top-half of the fleet to find the minimum pipeline depth (number of nodes needed for one replica).
3. **Guard** — return an error if `fleet_size < k_min × replica_count`.
4. **Partition** — divide the sorted fleet into *N* equal slices (last slice absorbs remainder nodes).
5. **Plan in parallel** — each slice is planned concurrently; failures are surfaced per-replica.
6. **Return** a `ReplicaSet` with all *N* `DeploymentPlan` entries and the chosen routing policy.

Node groups are non-overlapping by construction, so replicas never share a node.

## Configuration

### Via `purser.yaml`

```yaml
model: acme/llama-70b
replicas: 2
routing: round_robin   # round_robin | least_loaded
```

### Via the planner API

```go
rs, err := plan.PlanReplicaSet(ctx, nodes, links, model, constraints, replicaCount)
```

Or use the `Constraints.ReplicaCount` field when driving `Plan()` through the control plane:

```go
c := plan.Constraints{ReplicaCount: 2}
```

Values of `0` or `1` are equivalent to a single-replica (standard) deployment.

## Routing policies

| Policy | Description |
|---|---|
| `round_robin` (default) | Distribute requests evenly across all healthy replicas. |
| `least_loaded` | Route each request to the replica with the fewest active in-flight requests. Requires live metrics feedback from the gateway. |

## Minimum viable fleet

The planner rejects a replica count it cannot satisfy:

```
planner: fleet has 6 nodes but 2 replica(s) × 4 nodes/replica required (k_min=4)
```

To find the minimum fleet size for your model:

```bash
purser plan --model acme/llama-70b --dry-run | grep k_min
```

## Known limitations (v0.3 MVP)

- **Greedy partition only.** Nodes are split by capacity rank; the first replica always gets the best nodes. A round-robin interleaving for balanced replicas is planned for v0.4.
- **No live rebalancing.** Replicas are planned at deploy time. Adding nodes to a running deployment requires a re-plan and rolling restart.
- **Homogeneous quantization.** All replicas use the same quantization chosen by phase A. Per-replica quantization override is not yet supported.
- **Links are shared.** All partitions receive the full link topology. Links that cross partition boundaries are unused but not filtered out; this has no correctness impact.

## See also

- [Failover plans](../architecture/failover.md) — per-node failover within a single replica
- [Fleet configuration](../configuration/purser-yaml.md) — full `purser.yaml` reference
- [Planner internals](../architecture/planner.md) — phases A–F design overview
