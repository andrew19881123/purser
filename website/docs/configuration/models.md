# Model Catalog

The Model Catalog is Purser's registry of imported model weights. Before a model can
be deployed it must first be registered in the catalog. Each catalog entry stores the
model's architecture metadata and a pre-computed *fit verdict* that tells you whether
the model can run on the current fleet.

---

## Registering a model

Use the **Model Studio** page (UI) or the REST API to import a model.

### From Hugging Face

```http
POST /api/v1/models/import
Content-Type: application/json

{
  "type": "huggingface",
  "repo": "meta-llama/Llama-3.1-8B-Instruct",
  "revision": "main"
}
```

### From object storage (S3 / GCS / Azure Blob)

```http
POST /api/v1/models/import
Content-Type: application/json

{
  "type": "object_storage",
  "uri": "s3://my-models/llama-3.1-8b/llama-3.1-8b-q4_k_m.gguf",
  "name": "llama-8b",
  "family": "Llama 3.1 8B"
}
```

See [Model Sources](model-sources.md) for full S3/GCS/Azure configuration and
IAM/RBAC requirements.

### From SageMaker / Vertex AI / Azure ML

```http
POST /api/v1/models/import
Content-Type: application/json

{ "type": "sagemaker", "modelGroup": "my-llama-group", "version": "1" }
```

On success the API returns `200` with the created `ModelSpec` object and the model
appears in the catalog immediately.

---

## Deleting a model

### Via the UI

1. Open **Model Catalog** in the sidebar.
2. Locate the model card and click the **Delete** (trash) button.
3. A confirmation dialog opens. Type the model ID exactly to confirm.
4. Click **Delete permanently**.

!!! warning "Active deployments block deletion"
    If the model has any active deployments the delete request returns `409 Conflict`
    with the message *"model is referenced by one or more active deployments; tear them
    down first"*. The UI shows an inline error inside the confirmation dialog:

    > Cannot delete: model is used by an active deployment

    Navigate to **Deployments**, undeploy the affected deployment, then retry.

### Via the API

```http
DELETE /api/v1/models/{id}
```

**Response codes**

| Code | Meaning |
|------|---------|
| `204 No Content` | Model deleted successfully. |
| `404 Not Found` | No model with that ID in the catalog. |
| `409 Conflict` | Model has one or more active deployments. Undeploy them first. |

---

## Preview Deploy (fleet split planner)

Before committing a deployment you can ask Purser's DP layer-split planner to compute
an optimal assignment of model layers to your fleet nodes.

### Via the UI

On each **feasible** model card click **Preview Split**. A modal opens showing:

- **Node assignments** — which node receives which layer range and its role
  (`HOST` or `WORKER`).
- **Estimated decode throughput** — a min–max range in tok/s based on the
  fleet's measured memory bandwidth.
- **Pipeline order** — the ordered list of nodes that tokens traverse during
  inference.

If the model does not fit the current fleet the modal shows *"Cannot be deployed on
this fleet"* together with the planner's reason (e.g., insufficient VRAM).

### Via the API

```http
POST /api/v1/models/{id}/plan
```

**Response shape**

```json
{
  "feasible": true,
  "plan": {
    "planId": "plan-abc123",
    "modelId": "llama-8b",
    "quantization": "Q4_K_M",
    "assignments": [
      {
        "nodeId": "node-gpu-01",
        "role": "host",
        "layerStart": 0,
        "layerEnd": 31,
        "draft": false
      }
    ],
    "pipelineOrder": ["node-gpu-01"],
    "estimated": {
      "decodeTokSMin": 30,
      "decodeTokSMax": 50,
      "prefillTokSMin": 100,
      "prefillTokSMax": 200,
      "headroomGb": 2.1
    },
    "cost": 1.0,
    "explanation": ["Single node fits all layers with Q4_K_M quantization"]
  }
}
```

When the model does not fit the fleet the response is:

```json
{
  "feasible": false,
  "reason": "Insufficient VRAM: need 40 GB, fleet has 16 GB"
}
```

**Fields**

| Field | Description |
|-------|-------------|
| `feasible` | `true` if the planner found a valid assignment. |
| `reason` | Human-readable reason when `feasible` is `false`. |
| `plan.assignments` | Per-node layer ranges and roles. |
| `plan.pipelineOrder` | Ordered node IDs describing the token pipeline. |
| `plan.estimated.decodeTokSMin/Max` | Estimated decode throughput range (tok/s). |
| `plan.estimated.prefillTokSMin/Max` | Estimated prefill throughput range (tok/s). |
| `plan.estimated.headroomGb` | Free VRAM left after the model is loaded (GB). |
| `plan.cost` | Planner cost score (lower is better; used for tie-breaking). |
| `plan.explanation` | Ordered rationale lines from the planner. |

The plan endpoint is read-only — it does not create a deployment. To deploy, call
`POST /api/v1/deployments` with the model ID (optionally referencing the `planId`).

---

## Fit verdict

The catalog endpoint (`GET /api/v1/catalog`) attaches a pre-computed `FitVerdict` to
each model:

| `reasonKey` | Meaning |
|-------------|---------|
| `fits` | Model fits comfortably with the chosen quantization. |
| `fits_tight` | Model fits but VRAM headroom is less than 10 %. |
| `not_enough_memory` | Fleet lacks the aggregate VRAM. |
| `needs_fp4` | Model requires FP4-native hardware absent from the fleet. |
| `no_ready_nodes` | No enrolled nodes are in the `ready` state. |

The fit verdict is refreshed automatically after fleet changes (node enrollment,
drain, removal) and after a new model is imported.
