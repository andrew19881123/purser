# Multi-Node Inference

This is the core use case Purser was built for: running a large model across
**two or more GPU machines** on a regular LAN — no NVLink, no InfiniBand, no
cloud required.

If you only have one GPU machine, see the [Quickstart](quickstart.md). This
guide is for setups like:

- 2× machines each with 1–2× RTX 3090 (24 GB VRAM each) → run Llama 3 70B
- 4× machines each with 1× RTX 4090 (24 GB VRAM each) → run Llama 3 70B Q8
- 2× machines each with 1× A100 80 GB → run Llama 3 405B Q4

---

## What Purser does automatically

You don't specify which layers go on which node. The **Planner** (a dynamic
programming optimizer) reads each node's VRAM, bandwidth, and the model's
architecture, then computes the split that **minimises the pipeline bottleneck**.

When you call `POST /api/v1/models/{id}/deploy`:

```
Planner output:
  node-gpu01  layers  0–39   (40 layers, 22.1 GB VRAM)
  node-gpu02  layers 40–79   (40 layers, 22.3 GB VRAM)
  estimated decode throughput: <computed from your fleet's measured bandwidth>
```

!!! warning "No throughput figures are published yet"
    The Planner reports an estimate for *your* fleet, computed from the memory
    and link bandwidth it measures at enrolment. This project publishes **no
    throughput numbers** — not for this example and not elsewhere — because
    GPU validation has not been completed and the cost model has not been
    calibrated against measured hardware. Treat any tok/s figure you see
    attributed to Purser as unverified until calibrated benchmarks ship.

Only **activations** (~KB per token) flow between stages over the LAN. The full
model weights never cross the network.

---

## Prerequisites

- Two (or more) Linux machines on the same LAN, reachable by IP
- Each machine has at least one GPU with VRAM ≥ the per-node split requirement
- A Control Plane the GPU nodes can reach, on one machine or in Kubernetes. It
  must expose both the REST API (`:8080`) and the gRPC RegistrationService
  (`:9443`) to the LAN — the `docker compose` demo stack publishes neither and
  **cannot** be used here (see the note in Step 1), OR
  use the [single-host setup](#single-host-multi-gpu) below
- `purser-agent` installed on every GPU node
- The llama.cpp binaries (`llama-server` + `rpc-server`) on every GPU node

---

## Step 1 — Deploy the Control Plane

On any machine (can be a CPU-only machine or one of the GPU nodes), with Helm on
Kubernetes:

```bash
helm install purser oci://ghcr.io/andrew19881123/charts/purser \
  --version v0.5.0 \
  --set controlPlane.service.type=LoadBalancer
```

`controlPlane.service.type=LoadBalancer` is what exposes the REST API (`:8080`)
and the gRPC RegistrationService (`:9443`) to the LAN, so Agents on the GPU
nodes can enrol. If your cluster has no load-balancer provisioner, use
`NodePort` or a port-forward — see the
[Quickstart Helm section](quickstart.md#step-1-install-the-control-plane-helm).

!!! danger "Do not use `docker compose up -d` for this guide"
    The demo compose stack publishes a single port (`3000`), and its nginx
    proxies HTTP paths only. The gRPC RegistrationService an Agent enrols
    through is not reachable from another machine, so no GPU node can ever
    join a compose-based Control Plane. Use Helm, or run the Control Plane
    natively with `PURSER_ADDR=:8080 PURSER_GRPC_ADDR=:9443` bound to a LAN
    interface.

Note the Control Plane address (e.g. `192.168.1.10`) — the REST API is on
`:8080` and Agents enrol against `:9443`.

---

## Step 2 — Install the Agent on each GPU node

On **every** GPU machine:

```bash
# Download from https://github.com/andrew19881123/purser/releases/latest
sudo apt install ./purser-agent_0.5.0_amd64.deb

# Configure
sudo tee /etc/purser/agent.env <<'EOF'
PURSER_JOIN_TOKEN=<token from control plane>
PURSER_CONTROL_PLANE_ADDR=http://192.168.1.10:9443
PURSER_ENGINE_BACKEND=llamacpp
PURSER_LLAMACPP_BIN=/usr/local/bin   # path to llama-server + rpc-server
EOF

sudo systemctl enable --now purser-agent
```

`PURSER_CONTROL_PLANE_ADDR` is the **gRPC RegistrationService** endpoint
(`:9443`), not the REST API (`:8080`) — pointing it at the REST port makes
enrolment fail. See the [environment variable
reference](../configuration/env-vars.md).

!!! warning "`llamacpp` needs an agent built with `--features llamacpp`"
    The published `.deb` / `.rpm` packages are currently built **without** that
    feature, so setting `PURSER_ENGINE_BACKEND=llamacpp` on a packaged agent
    makes it exit at startup with an error about the missing feature flag. Until
    packages ship with llama.cpp support, build the agent from source on each
    GPU node (`cargo build --release -p purser-agent --features llamacpp`).

To get the join token:

```bash
curl -s -X POST http://192.168.1.10:8080/api/v1/join-token \
  -H 'Authorization: Bearer <admin-key>' | jq -r .token
```

---

## Step 3 — Verify both nodes appear in the fleet

```bash
curl http://192.168.1.10:8080/api/v1/nodes \
  -H 'Authorization: Bearer <admin-key>' | jq '.nodes[].profile.nodeId'
# "node-gpu01"
# "node-gpu02"
```

Or open the dashboard Fleet page at `http://localhost:3000`.

---

## Step 4 — Register the model

```bash
curl -s -X POST http://192.168.1.10:8080/api/v1/models \
  -H 'Authorization: Bearer <admin-key>' \
  -H 'Content-Type: application/json' \
  -d '{
    "id": "llama3-70b",
    "source": {"type": "huggingface", "repo": "bartowski/Meta-Llama-3-70B-Instruct-GGUF"},
    "quantizations": ["Q4_K_M"],
    "max_context_len": 8192
  }'
```

---

## Step 5 — Deploy and watch the Planner assign layers

```bash
curl -s -X POST http://192.168.1.10:8080/api/v1/models/llama3-70b/deploy \
  -H 'Authorization: Bearer <admin-key>' \
  -H 'Content-Type: application/json' \
  -d '{"quantization": "Q4_K_M"}'
```

Watch the deployment status:

```bash
curl http://192.168.1.10:8080/api/v1/deployments \
  -H 'Authorization: Bearer <admin-key>' | jq '.deployments[] | {id, state, plan}'
```

When `state` reaches `DEPLOYMENT_STATE_ACTIVE`, the model is ready.

---

## Step 6 — Run inference

```bash
curl http://localhost:3000/v1/chat/completions \
  -H 'Authorization: Bearer demo-key-12345' \
  -H 'Content-Type: application/json' \
  -d '{
    "model": "llama3-70b",
    "messages": [{"role": "user", "content": "Explain pipeline parallelism in one sentence."}],
    "stream": true
  }'
```

Tokens stream back from the **host node** (the final pipeline stage). All LAN
communication between nodes happens invisibly — from the client's perspective
this is identical to a local OpenAI API call.

---

## Single-host multi-GPU

If you have multiple GPUs on **one machine** (e.g. 2× RTX 3090 in one server),
Purser can still split layers across them — the agents run on the same host but
each binds to a different GPU:

```bash
# Agent 1 — GPU 0
PURSER_GPU_ORDINAL=0 purser-agent

# Agent 2 — GPU 1
PURSER_GPU_ORDINAL=1 purser-agent
```

The Planner treats each GPU-agent as an independent node with its own VRAM
budget.

---

## Troubleshooting

**Node stuck in `REGISTERING`**
: Check that the agent can reach the control plane: `curl http://<cp-addr>/api/v1/cluster/health`

**Planner returns "infeasible"**
: The model doesn't fit across your fleet's combined VRAM. Try a more aggressive quantization (e.g. Q3_K_M instead of Q4_K_M) or add another GPU node.

**Use the What-if Planner API before deploying hardware**:

```bash
curl -X POST http://192.168.1.10:8080/api/v1/planner/what-if \
  -H 'Authorization: Bearer <admin-key>' \
  -d '{
    "model_id": "llama3-70b",
    "hypothetical_nodes": [
      {"node_id": "planned-node", "gpu_vram_gb": 24, "gpu_count": 2, "net_bandwidth_gbps": 10}
    ],
    "include_existing_nodes": true
  }'
```

---

## See also

- [Architecture — Data Plane](architecture.md#data-plane)
- [What-if Planner API](../operations/what-if-planner.md)
- [Fleet management](../operations/reconciler.md)
