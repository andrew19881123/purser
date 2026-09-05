# Deploy a model with Model Studio

Model Studio is the flagship operator page in Purser v0.2. It guides you through finding a model in any supported registry, previewing how it will split across your fleet's nodes, and deploying — all in one place without touching YAML.

---

## Opening Model Studio

Click **Model Studio** in the left navigation (the cube icon, between "Model catalog" and "Deployments"). The page URL is `/#/model-studio`.

---

## Step 1 — Choose an import source

Six import sources are supported. Select a tab at the top of the page:

| Tab | What you provide |
|-----|-----------------|
| **HuggingFace Hub** | Repository name (`user/model`), optional revision (branch, tag, or commit SHA), optional filename glob (`*.gguf`) |
| **Object Storage** | Full URI (`s3://`, `gs://`, or `az://`), a display name, and the model family |
| **SageMaker** | Model group name, optional version |
| **Vertex AI** | Model path (e.g. `projects/my-project/locations/us-central1/models/123`), optional version |
| **Azure ML** | Workspace name, model name, optional version |
| **Purser Catalog** | Pick from models already in your Purser catalog (no import needed — jumps straight to preview) |

Fill in the required fields for the chosen source. Mandatory fields are indicated in the form. Optional fields (revision, version) default to the latest available.

---

## Step 2 — Inspect the model

For external sources, click **Inspect model**. Purser contacts the source registry, fetches the model's architecture metadata, and adds the model to your catalog.

A **Model info card** appears below the source form showing:
- Family and model ID
- Source badge (HuggingFace Hub, SageMaker, etc.)
- Total and active parameter count
- Layer count
- Smallest quantization variant size
- Available quantization names

The model is now in your Purser catalog (visible on the Model Catalog page), even if you decide not to deploy it immediately.

---

## Step 3 — Preview the deployment

Click **Preview deployment**. Purser's planner computes a dry-run plan against your current fleet without writing anything to disk or starting any process.

### If the plan is feasible

A **Fleet split preview** card appears. Each row represents one node:

```
Fleet split preview
┌────────────────────────────────────────────────────────┐
│  Node: gpu-01  [████████████████░░░░]  Layers 1–16     │
│  Node: gpu-02  [████████████░░░░░░░░]  Layers 17–32    │
│                                                         │
│  Estimated throughput: ~45 tok/s (decode)               │
│  Pipeline order: gpu-01 → gpu-02                        │
└────────────────────────────────────────────────────────┘
```

**How to read the split diagram:**

- **Bar width** is proportional to the fraction of layers assigned to that node. A node with 40 % of the layers gets a bar 40 % wide.
- **Layer range** (e.g. `Layers 1–16`) is printed inside or beside each bar.
- **Role badge**: the first node is the pipeline _Host_ (receives requests); subsequent nodes are _Workers_.
- **Pipeline order** below the diagram shows the exact forwarding path for inference tokens: `gpu-01 → gpu-02`.
- **Estimated throughput** is an honest range, not a single number. The range widens with more hops and more fleet heterogeneity. Real throughput also varies with prompt length and concurrent load.

The planner distributes layers in proportion to each node's usable memory (VRAM for GPU nodes; RAM for unified-memory and CPU-only nodes). The fastest-linked node is chosen as the Host.

### If the plan is infeasible

A warning card appears with the reason. Common causes:

| Reason | Meaning | Fix |
|--------|---------|-----|
| `Not enough memory: requires X GB, fleet maximum is Y GB` | Even the smallest quantization variant doesn't fit across all ready nodes combined | Add a node with more VRAM/RAM, or pick a smaller model |
| `Needs FP4-native hardware, which this fleet lacks` | The model only ships in `NVFP4` quantization which requires hardware FP4 support | Add a node with an FP4-capable GPU (e.g. NVIDIA H100/GB200), or pick a model that ships in `Q4_K_M` or similar |
| `No ready nodes — enroll hardware first` | Zero nodes are in the `ready` or `running` state | Go to **Get started** and enroll at least one node |

---

## Step 4 — Override and deploy

Once a feasible preview is shown:

**Quantization selector** (visible when the model has more than one variant): choose the quantization to deploy. Larger variants are higher quality; smaller ones use less memory and may allow single-node deployment.

Click **Deploy** to launch the deployment. Purser runs the planner one final time against the live fleet (taking any last-second fleet changes into account), provisions the model across the assigned nodes, and creates a Deployment record. A success toast shows the Deployment ID. Track progress under **Deployments** in the navigation.

Click **Import only** to keep the model in the catalog without deploying it now. The model remains visible in the Model Catalog with its fit verdict so you can deploy it later from there.

---

## Deploy from HuggingFace in 3 clicks

1. Open **Model Studio** → **HuggingFace Hub** tab.
2. Type the repository name (e.g. `meta-llama/Llama-3.1-8B-Instruct`) and click **Inspect model**.
3. When the fleet split preview appears, click **Deploy**.

That's it. Purser handles fetching the model metadata, computing the optimal layer split, and rolling out the weights to every assigned node.

---

## Troubleshooting "model infeasible" scenarios

### Not enough memory

The planner checked all ready nodes and found that even packing the smallest available quantization across every node leaves a deficit.

**Solutions (in order of disruption):**
1. **Pick a smaller quantization.** If the model ships in `Q3_K_M` as well as `Q4_K_M`, the smaller variant may fit.
2. **Pick a smaller model.** A 7B model typically fits on a single GPU; a 70B model needs several.
3. **Add a node.** Enroll a machine with more VRAM/RAM. Go to **Get started** for the one-command enrollment flow.
4. **Drain an idle deployment.** If another deployment holds memory, undeploy it first (under **Deployments**) to free capacity.

### FP4-only model

Some models (e.g. quantized with `NVFP4`) require hardware FP4 acceleration. If your fleet lacks FP4-capable GPUs, you must either:
- Add a node with an FP4-native GPU (NVIDIA H100 SXM, GB200/GB10), or
- Choose a model that ships in standard quantizations like `Q4_K_M`.

The Fleet page shows each GPU's FP4 capability in the hardware column.

### No ready nodes

If you just enrolled a new node, wait a few seconds for it to appear in the **Fleet** as `Ready`. The planner only considers `ready` and `running` nodes. Nodes in `provisioning`, `degraded`, or `unreachable` states are excluded.
