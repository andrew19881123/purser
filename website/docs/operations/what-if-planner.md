# What-if Planner

The **What-if Planner** lets operators simulate a hypothetical fleet configuration and see
the planner's output without touching the real cluster. Use it to answer hardware ROI
questions like:

> "If I add two RTX 3090 nodes, will `llama3-70b` become deployable?
> What throughput gain should I expect?"

The endpoint runs the full DP layer-split planner against a virtual fleet and returns
feasibility, estimated throughput, and an improvement delta versus the current fleet —
all without writing anything to the registry.

## Endpoint

```
POST /api/v1/planner/what-if
```

**Auth:** any authenticated role (admin or viewer) — the endpoint is read-only.

---

## Request

```json
{
  "model_id": "llama3-70b",
  "hypothetical_nodes": [
    {
      "node_id":            "hyp-node-a100-1",
      "gpu_vram_gb":        80,
      "gpu_count":          1,
      "cpu_cores":          32,
      "ram_gb":             256,
      "net_bandwidth_gbps": 10,
      "ssd_bandwidth_gbps": 3
    }
  ],
  "include_existing_nodes": true
}
```

### Request fields

| Field | Type | Required | Description |
|---|---|---|---|
| `model_id` | string | Yes | ID of the model to plan. Must exist in the catalog. |
| `hypothetical_nodes` | array | No | List of virtual nodes to add to the fleet. Can be empty — falls back to the real fleet. |
| `include_existing_nodes` | bool | No | When `true`, merge virtual nodes with the current READY fleet. Also triggers the `current_plan` comparison so you can see the before/after delta. Default `false`. |

### Hypothetical node fields

| Field | Type | Description |
|---|---|---|
| `node_id` | string | Arbitrary identifier for the virtual node (e.g. `"hyp-a100-1"`). |
| `gpu_vram_gb` | float | GPU VRAM in GB (e.g. `80` for an A100-SXM4-80GB). |
| `gpu_count` | int | Number of GPUs (informational; the planner treats the node as one unit). |
| `cpu_cores` | int | Number of CPU cores (informational). |
| `ram_gb` | float | System RAM in GB. Determines fit for the model weights + KV cache. |
| `net_bandwidth_gbps` | float | Network bandwidth in Gbps. Used for inter-node pipeline links. Default: 10 Gbps. |
| `ssd_bandwidth_gbps` | float | SSD bandwidth in Gbps. When > 0, KV-cache SSD offload is enabled (adds ~1 TB effective KV pool). |

---

## Response

```json
{
  "feasible": true,
  "plan": {
    "assignments": [
      {"node_id": "hyp-node-a100-1", "layer_start": 0,  "layer_end": 39},
      {"node_id": "existing-node-1", "layer_start": 40, "layer_end": 79}
    ],
    "estimated_decode_tok_s_min": 45.2,
    "estimated_decode_tok_s_max": 62.1,
    "pipeline_depth": 2
  },
  "current_plan": {
    "feasible": false,
    "deficit_vram_gb": 24.5
  },
  "improvement": {
    "decode_tok_s_delta_min": 45.2,
    "decode_tok_s_delta_max": 62.1,
    "feasibility_change": "infeasible_to_feasible"
  }
}
```

### Response fields

| Field | Description |
|---|---|
| `feasible` | Whether the model can be deployed on the hypothetical fleet. |
| `plan.assignments` | Layer-to-node assignment for each pipeline stage. |
| `plan.estimated_decode_tok_s_min/max` | Estimated decode throughput range (tok/s). This is a conservative range — coefficients are calibrated against real hardware benchmarks. |
| `plan.pipeline_depth` | Number of pipeline stages (nodes used). |
| `current_plan.feasible` | Whether the model is deployable **today** on the real fleet (present only when `include_existing_nodes: true`). |
| `current_plan.deficit_vram_gb` | How many GB of memory the current fleet is short (present when `current_plan.feasible: false`). |
| `improvement.decode_tok_s_delta_min/max` | Throughput improvement vs current fleet (tok/s delta). |
| `improvement.feasibility_change` | One of `infeasible_to_feasible`, `feasible_improved`, or `feasible_unchanged`. |

---

## Example: "Will two RTX 3090 nodes make llama3-70b deployable?"

**Scenario:**  
- Current fleet: two nodes with 24 GB VRAM each — not enough for a 70B model.
- Proposed hardware: two RTX 3090 cards (24 GB VRAM / 64 GB RAM each).

```bash
curl -s -X POST https://<control-plane>/api/v1/planner/what-if \
  -H "Authorization: Bearer $PURSER_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "model_id": "llama3-70b",
    "hypothetical_nodes": [
      {
        "node_id": "new-rtx3090-1",
        "gpu_vram_gb": 24,
        "gpu_count": 1,
        "cpu_cores": 16,
        "ram_gb": 64,
        "net_bandwidth_gbps": 10
      },
      {
        "node_id": "new-rtx3090-2",
        "gpu_vram_gb": 24,
        "gpu_count": 1,
        "cpu_cores": 16,
        "ram_gb": 64,
        "net_bandwidth_gbps": 10
      }
    ],
    "include_existing_nodes": true
  }'
```

**Example response (infeasible → feasible):**

```json
{
  "feasible": true,
  "plan": {
    "assignments": [
      {"node_id": "new-rtx3090-1", "layer_start":  0, "layer_end": 19},
      {"node_id": "new-rtx3090-2", "layer_start": 20, "layer_end": 39},
      {"node_id": "existing-node-1", "layer_start": 40, "layer_end": 59},
      {"node_id": "existing-node-2", "layer_start": 60, "layer_end": 79}
    ],
    "estimated_decode_tok_s_min": 8.4,
    "estimated_decode_tok_s_max": 15.7,
    "pipeline_depth": 4
  },
  "current_plan": {
    "feasible": false,
    "deficit_vram_gb": 36.0
  },
  "improvement": {
    "decode_tok_s_delta_min": 8.4,
    "decode_tok_s_delta_max": 15.7,
    "feasibility_change": "infeasible_to_feasible"
  }
}
```

The `improvement.feasibility_change: "infeasible_to_feasible"` tells you that adding
these two nodes unlocks deployment, and the `estimated_decode_tok_s_min/max` range gives
you the expected throughput envelope to compare against your SLA requirements.

---

## Using results for hardware procurement

1. **Check feasibility first** — `feasible: true` means the planner found a valid
   layer-split assignment. `feasible: false` with a non-zero `current_plan.deficit_vram_gb`
   tells you exactly how much more (V)RAM you need.

2. **Check the throughput range** — `estimated_decode_tok_s_min/max` is a conservative
   band (±30%) around the planner's point estimate. Use the minimum for SLA sizing.

3. **Check pipeline depth** — a `pipeline_depth` above 1 means the model is split across
   multiple nodes. Higher pipeline depth increases latency per token; a depth of 1
   (single-node plan) is always preferred if the hardware budget allows.

4. **Iterate** — try different node configurations until the simulated throughput meets
   your SLA, then order the hardware. No cluster changes are needed during this process.

---

## Error responses

| Status | Error code | Cause |
|---|---|---|
| 400 | `bad_request` | Missing `model_id` or malformed JSON. |
| 404 | `not_found` | `model_id` does not exist in the catalog. |
| 501 | `no_planner` | The control plane was started without a planner (dev/test mode). |
| 500 | `plan_failed` | Internal planner error (context cancelled, etc.). |

A `200 OK` with `"feasible": false` is **not** an error — it means the hypothetical fleet
still cannot hold the model. Check `current_plan.deficit_vram_gb` to see how much more
capacity is needed.
