# Environment Variables

This page documents the environment variables that configure Purser components at startup.

## Control Plane

| Variable | Default | Description |
|---|---|---|
| `PURSER_PLANNER_ORDERING_THRESHOLD` | `10` | Fleet size at or below which the planner uses the exact Held-Karp algorithm to find the minimum-cost pipeline ordering. Above this threshold the planner switches to the nearest-neighbour + 2-opt heuristic. Held-Karp has O(2^N·N²) complexity and is feasible up to ~12 nodes; raise this value only on planners with abundant memory and CPU. |

### Notes

- `PURSER_PLANNER_ORDERING_THRESHOLD` is read **once at startup**. Changing it while the control plane is running has no effect; restart the process to apply a new value.
- Setting the threshold above `12` is not recommended unless you have measured the planning latency on your hardware: Held-Karp doubles in cost for every extra node.
