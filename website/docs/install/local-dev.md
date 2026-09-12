# Local Dev Setup (No Docker, No Kubernetes)

This guide walks through running the full Purser stack — Control Plane and two
agents — on a single developer machine. No GPU, no Docker, no Kubernetes
required. You get a real (canned) inference response at the end.

!!! note "What this gives you vs `docker compose up`"
    `docker compose up` starts the Control Plane, Gateway, and dashboard but
    ships no agent, so inference is impossible from that path. This guide
    uses native binaries for everything — a real end-to-end flow including
    agent enrollment, layer-split planning, and a mock chat response.

---

## Prerequisites

- Linux (amd64 or arm64), macOS, or WSL2
- ~4 GB disk free for the Rust toolchain
- The repo cloned: `git clone https://github.com/andrew19881123/purser.git && cd purser`

---

## Step 1 — Build the stack

```bash
make setup          # installs project-local Go, Rust, buf, helm into .toolchain/
source ./env.sh     # puts .toolchain/bin on PATH
make build          # builds control-plane and purser-agent binaries into ./bin/
```

`make setup` is idempotent — skip it if you ran it before. `source ./env.sh` is
required for any subsequent `make` or `cargo` calls in a new shell.

---

## Step 2 — Start the Control Plane

```bash
make dev
```

`make dev` starts the control plane on `http://localhost:8080` (REST) and
`localhost:9443` (gRPC / agent enrollment) with an in-memory SQLite database and
the mock engine. Leave this running in terminal 1.

The control plane creates a `./pki-state/` directory on first run to hold the
internal CA key. This happens even if you do not set `PURSER_PKI_DIR` — the
default is `pki-state` relative to the working directory. To use a different
path: `PURSER_PKI_DIR=/tmp/purser-pki make dev`.

---

## Step 3 — Enrol agent 1 (default port)

In a new terminal:

```bash
source ./env.sh
make demo-agent
```

`make demo-agent` mints a join token from the Control Plane and starts
`./bin/purser-agent` pointing at `localhost:9443`. The agent runs on port
`50151` (AgentService) by default. Leave it running in terminal 2.

You should see the agent log `node state: READY` within a few seconds.

---

## Step 4 — Enrol agent 2 (different ports — no conflict)

Running a second agent on the same machine requires overriding **two** ports
to avoid "address already in use" errors:

| Env var | Default | Purpose |
|---|---|---|
| `PURSER_AGENT_BIND` | `0.0.0.0:50151` | gRPC AgentService port — the Control Plane dials here |
| `PURSER_SWIM_BIND_ADDR` | `0.0.0.0:7946` | UDP gossip (SWIM) port — only used when `PURSER_SWIM_ENABLED=true` |

In a third terminal:

```bash
source ./env.sh

TOKEN=$(curl -s -X POST http://localhost:8080/api/v1/join-token \
  -H 'Content-Type: application/json' \
  -d '{"ttl_seconds":3600}' \
  | python3 -c 'import sys,json;print(json.load(sys.stdin)["token"])')

PURSER_AGENT_BIND=0.0.0.0:50161 \
PURSER_SWIM_BIND_ADDR=0.0.0.0:7947 \
PURSER_CONTROL_PLANE_ADDR=http://localhost:9443 \
PURSER_JOIN_TOKEN="$TOKEN" \
./bin/purser-agent
```

Once enrolled, `GET /api/v1/nodes` should show two nodes in `NODE_STATE_READY`.

---

## Step 5 — Disk-space warning

The agent runs a self-check at startup and warns (and may block enrollment)
if free disk on its working partition is below the threshold:

| Env var | Default | Description |
|---|---|---|
| `PURSER_DISK_FREE_WARN_GB` | `5.0` | GiB threshold below which the agent blocks deployment |

On a developer machine with a full disk, override this:

```bash
PURSER_DISK_FREE_WARN_GB=0.5 PURSER_AGENT_BIND=0.0.0.0:50161 … ./bin/purser-agent
```

---

## Step 6 — Seed, deploy, and run a chat

```bash
# From a fourth terminal (or with the Control Plane API reachable):
PURSER_DEMO_API=http://localhost:8080/api ./tools/demo_seed.sh

# Or step by step:
# Register a model
curl -s -X POST http://localhost:8080/api/v1/models \
  -H 'Content-Type: application/json' \
  -d '{"model_id":"tinyllama","family":"llama","architecture":"transformer","params_total_b":1.1,"engine":"mock"}'

# Deploy it
curl -s -X POST http://localhost:8080/api/v1/models/tinyllama/deploy \
  -H 'Content-Type: application/json' -d '{}'

# Watch it go ACTIVE
curl -s http://localhost:8080/api/v1/deployments | python3 -m json.tool

# Create an API key
curl -s -X POST http://localhost:8080/api/v1/apikeys \
  -H 'Content-Type: application/json' \
  -d '{"name":"dev-key","tenant":"default"}' \
  | python3 -m json.tool   # copy the "key" field

# Hit the gateway
curl http://localhost:3000/v1/chat/completions \
  -H "Authorization: Bearer <your-key>" \
  -H 'Content-Type: application/json' \
  -d '{"model":"tinyllama","messages":[{"role":"user","content":"Hello"}]}'
```

!!! note "Gateway URL vs Control Plane URL"
    The Gateway (OpenAI `/v1`) runs at `http://localhost:8080/v1` in the native
    dev stack (it is the same process — the Control Plane embeds a lightweight
    gateway in dev mode). In Docker Compose, the Gateway is a separate container
    and all traffic goes through `http://localhost:3000`.

---

## Keeping agents alive after the shell exits

The native agent process dies when its parent shell exits unless you detach it.
Three options, in order of reliability:

### Option A — systemd user service (recommended)

Systemd user services survive shell exits, restart on failure, and integrate
with `journalctl`. Use this for persistent dev setups.

#### Control Plane service

Create `~/.config/systemd/user/purser-cp.service`:

```ini
[Unit]
Description=Purser Control Plane (dev)
After=network.target

[Service]
Type=simple
Environment="PURSER_DB_DRIVER=sqlite"
Environment="PURSER_DB=/home/<user>/.purser/registry.db"
Environment="PURSER_ADDR=:8080"
Environment="PURSER_GRPC_ADDR=:9443"
Environment="PURSER_ENGINE_BACKEND=mock"
Environment="PURSER_AGENT_GRPC_INSECURE=true"
ExecStart=/usr/local/bin/purser-control-plane
Restart=on-failure
RestartSec=3s

[Install]
WantedBy=default.target
```

#### Agent service (node 1)

Create `~/.config/systemd/user/purser-agent-1.service`:

```ini
[Unit]
Description=Purser Agent (node 1)
After=network.target

[Service]
Type=simple
Environment="PURSER_CONTROL_PLANE_ADDR=http://localhost:9443"
Environment="PURSER_JOIN_TOKEN=<your-token>"
Environment="PURSER_ENGINE_BACKEND=cpu"
Environment="PURSER_DISK_FREE_WARN_GB=1.0"
Environment="PURSER_AGENT_METRICS_PORT=9091"
ExecStart=/usr/local/bin/purser-agent
Restart=on-failure
RestartSec=5s

[Install]
WantedBy=default.target
```

For a second agent, copy to `purser-agent-2.service` and change:

```ini
Environment="PURSER_AGENT_BIND=0.0.0.0:50161"
Environment="PURSER_SWIM_BIND_ADDR=0.0.0.0:7947"
Environment="PURSER_AGENT_METRICS_PORT=9092"
```

#### Install and enable

```bash
# Create the directory if it doesn't exist
mkdir -p ~/.config/systemd/user

# Reload unit files after creating or editing service files
systemctl --user daemon-reload

# Enable and start
systemctl --user enable --now purser-cp
systemctl --user enable --now purser-agent-1

# Check status
systemctl --user status purser-cp
systemctl --user status purser-agent-1

# Follow logs
journalctl --user -u purser-agent-1 -f
```

!!! tip "Token rotation with systemd"
    Join tokens expire. When you mint a new token, update `Environment="PURSER_JOIN_TOKEN=<new-token>"` in the service file, then run `systemctl --user daemon-reload && systemctl --user restart purser-agent-1`.

### Option B — tmux (quick-and-dirty)

```bash
tmux new-session -d -s agent1 \
  'source /path/to/purser/env.sh && PURSER_CONTROL_PLANE_ADDR=http://localhost:9443 PURSER_JOIN_TOKEN=<token> ./bin/purser-agent'

tmux new-session -d -s agent2 \
  'source /path/to/purser/env.sh && PURSER_AGENT_BIND=0.0.0.0:50161 PURSER_SWIM_BIND_ADDR=0.0.0.0:7947 PURSER_CONTROL_PLANE_ADDR=http://localhost:9443 PURSER_JOIN_TOKEN=<token2> ./bin/purser-agent'

# Attach to watch logs
tmux attach -t agent1
```

### Option C — nohup

`nohup` keeps the process alive when the terminal closes, but stdout/stderr go
to `nohup.out` in the working directory:

```bash
nohup ./bin/purser-agent \
  > /tmp/purser-agent1.log 2>&1 &
echo "PID: $!"
```

---

## Mixed Docker/native setup (compose CP + native agents)

If you want the UI and the compose stack's Control Plane but native agents for
inference, you need to handle two friction points:

### Port 9443 is now published

Since v0.5, `docker-compose.yml` publishes `9443:9443`. Agents on `localhost`
can enroll with:

```bash
PURSER_CONTROL_PLANE_ADDR=http://localhost:9443 \
PURSER_JOIN_TOKEN=<token> \
./bin/purser-agent
```

Get a token via:

```bash
curl -s -X POST http://localhost:3000/api/v1/join-token \
  -H 'Content-Type: application/json' -d '{"ttl_seconds":3600}' \
  | python3 -c 'import sys,json;print(json.load(sys.stdin)["token"])'
```

### TLS mismatch: compose CP uses PKI, native agents serve plain gRPC

After enrollment the Control Plane connects back to the agent (to issue
`StartEngine`) over TLS using the PKI-issued certificate. Native agents built
without explicit TLS configuration serve plain gRPC, causing a TLS handshake
failure.

Fix: add `PURSER_AGENT_GRPC_INSECURE=true` to the `control-plane` environment
in `docker-compose.yml`:

```yaml
control-plane:
  environment:
    ...
    PURSER_AGENT_GRPC_INSECURE: "true"   # dev only — disables mTLS to agents
```

!!! warning "Never use `PURSER_AGENT_GRPC_INSECURE=true` in production"
    This disables authentication between the Control Plane and agents. Use it
    only on your local dev machine.

### `demo-key-12345` is a Gateway key, not a Control Plane key

`demo-key-12345` is the value of `PURSER_GATEWAY_API_KEYS` in `docker-compose.yml`
— it authenticates requests to the **Gateway** (`/v1/chat/completions`). It does
**not** appear in the Control Plane's API key list (`GET /api/v1/apikeys`) and is
not created via the dashboard. In production, replace it with keys you create
yourself via `POST /api/v1/apikeys`.

---

## Checking stack health

Run a one-glance overview of the full stack at any point:

```bash
make status
```

Or directly:

```bash
./tools/purser-status.sh
```

Output covers: Control Plane reachability, Gateway, Dashboard, fleet nodes (state
and hardware), model catalog (fit check), active deployments, and CP-registered API
keys.

**Custom endpoints and scripting:**

```bash
# Point at a remote control plane
PURSER_CP_URL=http://myserver:8080 PURSER_API_KEY=sk-... ./tools/purser-status.sh

# Machine-readable JSON (no colour codes)
./tools/purser-status.sh --json
```

If the Control Plane is unreachable the script exits cleanly with a hint — no
Python traceback, no hanging curl.

---

## See also

- [Troubleshooting Agent Enrollment](troubleshoot-enrollment.md) — all enrollment failure modes
- [Linux Agent (.deb/.rpm)](linux-agent.md) — fleet installation guide
- [Multi-Node Inference](../getting-started/multi-node-inference.md) — GPU pipeline across multiple machines
- [Environment Variables Reference](../configuration/env-vars.md) — all knobs
