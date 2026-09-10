# Quickstart

## Quickstart — 2 minutes, no GPU required

### 1. Clone and start

```bash
git clone https://github.com/andrew19881123/purser.git
cd purser
docker compose up -d
```

### 2. Check that all five services came up

Do not skip this — if a service failed to start, every command below fails with a confusing connection error instead of a useful one.

```bash
docker compose ps
```

You should see **five** services, all `running`:

```
NAME                     SERVICE         STATUS              PORTS
purser-control-plane-1   control-plane   running             8080/tcp, 9443/tcp
purser-gateway-1         gateway         running             8081/tcp
purser-postgres-1        postgres        running (healthy)   5432/tcp
purser-proxy-1           proxy           running             0.0.0.0:3000->80/tcp
purser-ui-1              ui              running             80/tcp
```

The `NAME` column is prefixed with the Compose project name, which defaults to the directory you cloned into — so yours may read `myclone-gateway-1`. What matters is that five services are listed and none is `exited` or `restarting`. If one is unhealthy, read its logs with `docker compose logs <service>`.

!!! note "Only one port is published"
    `proxy` is the only service with a host port (`3000->80`). The Control Plane (`:8080`) and Gateway (`:8081`) are container-internal; nginx path-routes `/api/` to the Control Plane and `/v1/` to the Gateway. From your machine the addresses are therefore `http://localhost:3000` (dashboard), `http://localhost:3000/api` (Control Plane REST) and `http://localhost:3000/v1` (OpenAI-compatible Gateway) — **not** `:8080` or `:8081`, which will refuse the connection.

### 3. Open the dashboard

```bash
open http://localhost:3000
```

### 4. Seed the catalog

A fresh stack has an empty catalog, so the Catalog page is blank and `GET /v1/models` returns `{"object":"list","data":[]}`. Register a small demo model with one command — it is idempotent, so re-running is safe:

```bash
make demo-seed
```

### What this path gives you — and what it does not

**What you get:** the whole control path. The Control Plane and its full REST API, the dashboard, the Gateway's OpenAI-compatible surface, a Postgres-backed registry, and a model in the catalog. That is enough to explore the API, the Catalog and Playground pages, node pools, API keys and RBAC — everything except a generated token.

**What you do not get: an inference response.** Two independent reasons, both structural:

1. Inference runs in the **Agent**, not the Gateway. The Gateway is a reverse proxy; it holds no weights and no inference code, and even the mock engine lives in the Agent. The compose stack ships no Agent service.
2. You cannot add one to this stack. Agents enrol over the Control Plane's gRPC **RegistrationService on `:9443`**, and compose publishes only port `3000` — nginx proxies HTTP paths only. So the port an Agent would join through is not reachable from your machine, even if you already had an Agent binary.

Concretely:

```bash
curl http://localhost:3000/v1/models -H 'Authorization: Bearer demo-key-12345'
# -> {"object":"list","data":[]}
```

That is expected, not a fault: the Gateway lists and serves a model only once the Control Plane publishes a route for it, which happens after an inference engine reports ready on an enrolled node. Until then a chat call returns `503 "model not available"`.

!!! warning "`make demo-agent` will not work against the compose stack"
    `make demo-agent` targets the **native** `make dev` Control Plane on `:8080`/`:9443`. Under compose neither port is published, so it can neither mint a join token nor enrol. It also runs `./bin/purser-agent`, which does not exist until you build it from source.

**The two paths that do reach inference:**

| Path | What it needs | Use it when |
|---|---|---|
| Native `make dev` + mock Agent | The Rust toolchain, to build `./bin/purser-agent` | You want a canned response locally, no GPU — see [Development setup](#development-setup) |
| Agent package on a Linux host | A host outside the cluster, and a Control Plane that publishes `:9443` | You want real inference — see the [Helm quickstart](#quickstart-helm-production) and [Linux Agent install](../install/linux-agent.md) |

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
helm install purser oci://ghcr.io/andrew19881123/charts/purser --version 0.5.0 \
  --set controlPlane.service.type=LoadBalancer
```

`--set controlPlane.service.type=LoadBalancer` exposes the Control Plane's gRPC RegistrationService (`:9443`) and REST API (`:8080`) so Agents running on the LAN can reach it. With the default `ClusterIP`, the Control Plane is only reachable inside the cluster.

!!! tip "No cloud load balancer?"
    `LoadBalancer` only resolves to an address if something in the cluster provisions one. On a managed cloud cluster that is automatic; on bare metal, k3s, k0s, or kind there is usually no provisioner, so `kubectl get svc purser-control-plane` shows `EXTERNAL-IP: <pending>` forever and Agents can never reach the Control Plane.

    Two escape hatches:

    ```bash
    # A. NodePort — reachable at <any-node-IP>:<nodePort> from the LAN
    helm upgrade purser oci://ghcr.io/andrew19881123/charts/purser --version 0.5.0 \
      --set controlPlane.service.type=NodePort
    kubectl get svc purser-control-plane   # read the :3xxxx ports

    # B. port-forward — quick local test only, not for Agents on other hosts
    kubectl port-forward svc/purser-control-plane 8080:8080 9443:9443
    ```

    k3s ships ServiceLB, so `LoadBalancer` there usually does get a node IP. If you want a real load balancer on bare metal, install MetalLB and keep `LoadBalancer`.

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

On the fleet node (Linux), download the package from the [latest release](https://github.com/andrew19881123/purser/releases/latest) and install it:

```bash
# Debian / Ubuntu (amd64 or arm64)
sudo apt install ./purser-agent_0.5.0_amd64.deb

# RHEL / Fedora / openSUSE
sudo yum install ./purser-agent-0.5.0-1.x86_64.rpm
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
  "token": "eyJleHAiOjE3ODkyMDAwMDAsIm5vbmNlIjoiNGYxYzhhMmJlOWQwNzYzNGE1YzFlOGYyOTBiM2Q3NDYifQ.KuWaWIO9iPAuISxsW5rXuVybY7Vr9BbWA7gGzUkQUSE",
  "expires_at": "2026-09-05T01:00:00Z",
  "cluster_id": "default"
}
```

Copy the `token` value verbatim — it is an opaque signed string with no prefix,
so any added or missing character invalidates it.

---

## Step 4: Configure and start the Agent

On the fleet node, edit `/etc/purser/agent.env`:

```bash
sudoedit /etc/purser/agent.env
```

Set at minimum:

```bash
PURSER_CONTROL_PLANE_ADDR=http://<control-plane-host>:9443
PURSER_JOIN_TOKEN=<token-from-step-3>
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

!!! warning "`"engine": "mock"` returns canned responses — testing only"
    The `mock` engine does not load model weights and does not perform inference. It returns a fixed, canned reply to every request, which makes it useful for validating enrolment, planning, and routing without a GPU — and useless for anything else.

    Setting `mock` on a real GPU node is a common mistake: the deployment goes `ACTIVE`, the Gateway answers `200`, and the replies are nonsense, which reads like a broken product. For real inference use `"engine": "llamacpp"` (the Helm chart default) and install the Agent built with `--features llamacpp`.

Register it:

```bash
curl -sS -X POST http://<control-plane-host>:8080/api/v1/models \
  -H "Content-Type: application/json" \
  -d @model.json
```

---

## Step 6: Deploy the model

### Via the Dashboard

1. Open the **Catalog** page.
2. Click the model you registered (`llama-8b`).
3. Click **Preview Split** to review the node assignment the Planner computed.
4. Click **Deploy** — the deployment moves to `ACTIVE` once all engine workers are running.

### Via the API

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

### Via the Dashboard

1. Open **Settings → API Keys**.
2. Click **New Key**, set name `my-key`, tenant `default`, role `inference`.
3. Copy the key — it is shown only once.

### Via the API

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
  "key": "sk-a3f8bc12de456789abcdef0123456789abcdef01"
}
```

Get the Gateway external address:

```bash
kubectl get svc purser-gateway
```

Hit the OpenAI-compatible endpoint:

```bash
curl -sS http://<gateway-host>:<port>/v1/chat/completions \
  -H "Authorization: Bearer sk-<your-api-key>" \
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
    api_key="sk-<your-api-key>"
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

!!! note "Engine backends"
    **Production default is `llamacpp`** (set by the Helm chart). Both backends live in the Agent, so the `docker-compose.yml` demo stack — which ships no Agent — selects neither and cannot serve inference; the `PURSER_ENGINE_BACKEND: mock` line in that file sits on the control-plane service, which never reads it. A bare agent binary falls back to `mock`, which returns canned responses and must never be used in production. To enable real inference, set `PURSER_ENGINE_BACKEND=llamacpp` and install an agent built with `--features llamacpp`. See [Architecture: Engine backends](architecture.md#engine-backends) for details.

---

---

## Development setup

Want to hack on Purser itself? No GPU or Kubernetes needed.

### GitHub Codespaces / VS Code Dev Containers

Open the repo in a [GitHub Codespace](https://github.com/features/codespaces) or VS Code Dev Container — the `.devcontainer/devcontainer.json` at the repository root provisions Rust 1.98.1, Go 1.27.1, Node 22, and Docker-in-Docker automatically. After the container starts, the post-create command runs `make setup` and appends `source ./env.sh` to your shell profile, so the project-local toolchain is on `PATH` immediately.

### Local development with `make dev`

If you prefer to work locally, `make dev` builds the control plane and starts it with an in-memory SQLite database and the mock engine — no GPU, no real nodes, instant feedback:

```bash
make setup          # installs project-local Go / Rust / buf / helm / mkdocs into .toolchain/ (once)
source ./env.sh     # puts .toolchain/bin on PATH, and reports anything missing
make dev            # builds control-plane and starts it on :8080
```

`make setup` supports macOS (Apple Silicon and Intel) and Linux (`amd64` and `arm64`); it detects the platform, pins Go and helm to exact versions, and verifies their checksums before extracting. Add `--skip-rust` to skip the ~1 GB Rust toolchain if you are only working on Go or the docs. See [CONTRIBUTING.md](https://github.com/andrew19881123/purser/blob/main/CONTRIBUTING.md) for the full matrix and the few prerequisites it does not install.

The control plane listens at `http://localhost:8080`. To run the dashboard alongside it:

```bash
cd ui && npm install && npm run dev   # separate terminal — serves on :5173
```

To enrol a mock Agent against this native stack — this is what gives you a real (canned) chat response without a GPU:

```bash
make build        # produces ./bin/purser-agent
make demo-agent   # mints a join token and starts a mock agent (separate terminal)
```

`make demo-agent` targets the **native** `make dev` stack on `:8080`/`:9443`, not the `make demo` compose stack, which publishes neither port. Once the node is `NODE_STATE_READY` you can seed and deploy against it:

```bash
PURSER_DEMO_API=http://localhost:8080/api ./tools/demo_seed.sh
```

Ports forwarded by the devcontainer:

| Port  | Service                        |
|-------|-------------------------------|
| 8080  | Control Plane REST API         |
| 9443  | RegistrationService (gRPC)     |
| 50151 | Agent (per-node daemon)        |
| 8000  | MkDocs docs preview            |

---

## Security note — Gateway API key storage

The Playground stores the Gateway API key in **`sessionStorage`**, not `localStorage`. The key is automatically cleared when you close the browser tab, so it does not persist between sessions. This is intentional: inference keys are short-lived credentials that should not outlive the browser window.

---

## Next steps

- [Full Kubernetes install guide](../install/kubernetes.md) — values, networking models, persistence
- [Environment variables reference](../configuration/env-vars.md) — all knobs, exhaustively documented
- [Architecture](architecture.md) — two-plane design and request flow
- [Enterprise features](../enterprise/overview.md) — audit log, HA, RBAC/SSO
- [Contributing guide](https://github.com/andrew19881123/purser/blob/main/CONTRIBUTING.md) — good first issues and conventions

## See also

- [Node Pools](../configuration/node-pools.md) — assign GPU nodes to teams and set scheduling constraints
- [Organizations & Teams](../configuration/platform-model.md) — multi-tenant platform model
- [API Key Lifecycle](../configuration/api-keys.md) — create, rotate, and revoke keys
- [RBAC Permissions](../configuration/permissions.md) — built-in roles and custom role definitions
