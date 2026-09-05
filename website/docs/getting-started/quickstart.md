# Quickstart

## Quickstart — 2 minutes, no GPU required

```bash
# 1. Clone and start
git clone https://github.com/andrew19881123/purser.git
cd purser
docker compose up -d

# 2. Open the dashboard
open http://localhost:3000
```

The demo stack uses the built-in mock engine — real inference comes when you install the Agent on GPU nodes.

Try the OpenAI-compatible Gateway immediately:

```bash
curl http://localhost:8081/v1/models -H 'Authorization: Bearer demo-key-12345'
```

Stop the demo at any time:

```bash
make demo-stop
```

---

## Quickstart (Helm — production)

Get from zero to a working OpenAI-compatible inference endpoint in about 5 minutes. This guide uses the Helm path — the primary deployment model for Purser.

## Prerequisites

- A Kubernetes cluster (k3s, k0s, EKS, GKE, AKS, or any other)
- `helm` v3.8+ (for OCI chart support)
- At least one Linux host **outside** Kubernetes for the Agent (the machine that will run inference)

!!! note "Why the Agent runs outside Kubernetes"
    The Agent must access the host's GPU/accelerators and supervises an inference engine worker that is not sandboxed, so it runs as a native host service — not as a pod. The Control Plane, Gateway, and UI are ordinary networked services and run inside Kubernetes.

---

## Step 1: Install the control plane (Helm)

The chart and images are published as public OCI artifacts on GHCR — no registry login needed.

```bash
helm install purser oci://ghcr.io/andrew19881123/charts/purser --version 0.1.1 \
  --set controlPlane.service.type=LoadBalancer
```

`--set controlPlane.service.type=LoadBalancer` exposes the Control Plane's gRPC RegistrationService (`:9443`) and REST API (`:8080`) so Agents running on the LAN can reach it. With the default `ClusterIP`, the Control Plane is only reachable inside the cluster.

Wait for all pods to be ready:

```bash
kubectl get pods -w
```

You should see three pods come up:

```
NAME                               READY   STATUS    RESTARTS
purser-control-plane-...           1/1     Running   0
purser-gateway-...                 1/1     Running   0
purser-ui-...                      1/1     Running   0
```

Get the Control Plane external IP (if using LoadBalancer):

```bash
kubectl get svc purser-control-plane
# Note the EXTERNAL-IP — this is <control-plane-host> in subsequent steps
```

---

## Step 2: Install an Agent on a fleet node

On the fleet node (Linux), download the package from the [v0.1.0 release](https://github.com/andrew19881123/purser/releases/tag/v0.1.0) and install it:

```bash
# Debian / Ubuntu
sudo apt install ./purser-agent_0.1.0_amd64.deb

# RHEL / Fedora / openSUSE
sudo yum install ./purser-agent-0.1.0-1.x86_64.rpm
```

---

## Step 3: Mint a join token

From any host that can reach the Control Plane REST API:

```bash
curl -sS -X POST http://<control-plane-host>:8080/api/v1/join-token
```

Response:

```json
{
  "token": "psk_...",
  "expires_at": "2026-09-05T01:00:00Z",
  "cluster_id": "default"
}
```

Copy the `token` value.

---

## Step 4: Configure and start the Agent

On the fleet node, edit `/etc/purser/agent.env`:

```bash
sudoedit /etc/purser/agent.env
```

Set at minimum:

```bash
PURSER_CONTROL_PLANE_ADDR=http://<control-plane-host>:9443
PURSER_JOIN_TOKEN=psk_<token-from-step-3>
PURSER_CLUSTER_ID=default
```

Then enable and start the service:

```bash
sudo systemctl enable --now purser-agent
```

Verify the agent enrolled successfully:

```bash
# On the control plane host:
curl -s http://<control-plane-host>:8080/api/v1/nodes | python3 -m json.tool
```

You should see the node with state `NODE_STATE_READY`.

---

## Step 5: Register a model

Create a model spec file `model.json`:

```json
{
  "model_id": "llama-8b",
  "family": "llama",
  "architecture": "transformer",
  "params_total_b": 8.0,
  "engine": "mock"
}
```

Register it:

```bash
curl -sS -X POST http://<control-plane-host>:8080/api/v1/models \
  -H "Content-Type: application/json" \
  -d @model.json
```

---

## Step 6: Deploy the model

```bash
curl -sS -X POST http://<control-plane-host>:8080/api/v1/models/llama-8b/deploy \
  -H "Content-Type: application/json" \
  -d '{}'
```

The Planner automatically computes the optimal layer-split for your fleet. Watch the deployment go `ACTIVE`:

```bash
curl -s http://<control-plane-host>:8080/api/v1/deployments | python3 -m json.tool
```

---

## Step 7: Create an API key and call the Gateway

Create an API key:

```bash
curl -sS -X POST http://<control-plane-host>:8080/api/v1/apikeys \
  -H "Content-Type: application/json" \
  -d '{"name": "my-key", "tenant": "default"}'
```

The response contains the key **once** — store it:

```json
{
  "id": "key-...",
  "name": "my-key",
  "key": "psk_..."
}
```

Get the Gateway external address:

```bash
kubectl get svc purser-gateway
```

Hit the OpenAI-compatible endpoint:

```bash
curl -sS http://<gateway-host>:<port>/v1/chat/completions \
  -H "Authorization: Bearer psk_<your-api-key>" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "llama-8b",
    "messages": [{"role": "user", "content": "Hello"}],
    "stream": false
  }'
```

Or use the OpenAI Python SDK:

```python
from openai import OpenAI

client = OpenAI(
    base_url="http://<gateway-host>:<port>/v1",
    api_key="psk_<your-api-key>"
)

response = client.chat.completions.create(
    model="llama-8b",
    messages=[{"role": "user", "content": "Hello"}],
    stream=True
)

for chunk in response:
    if chunk.choices[0].delta.content:
        print(chunk.choices[0].delta.content, end="", flush=True)
```

!!! note "Mock engine"
    The default engine is the built-in mock engine. It responds with generated tokens to demonstrate the pipeline but does not run real model inference. To use a real engine, set `PURSER_ENGINE_BACKEND=llamacpp` in the agent's environment file. Real GPU validation is still in progress as of v0.1.1.

---

## Next steps

- [Full Kubernetes install guide](../install/kubernetes.md) — values, networking models, persistence
- [Environment variables reference](../configuration/env-vars.md) — all knobs, exhaustively documented
- [Architecture](architecture.md) — two-plane design and request flow
- [Enterprise features](../enterprise/overview.md) — audit log, HA, RBAC/SSO
