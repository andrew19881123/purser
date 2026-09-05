# Architecture

Purser has two clearly separated planes: a low-volume **control plane** (gRPC + mTLS) and a high-volume **data plane** (engine-to-engine activations across the trusted subnet).

## Components

| Component | Language | Path | Description |
|---|---|---|---|
| **Control Plane** | Go | `go/controlplane` | Registry (SQLite), Planner, Orchestrator, Reconciler, internal PKI, REST API, RegistrationService gRPC |
| **API Gateway** | Rust | `rust/crates/gateway` | OpenAI-compatible `/v1` endpoint, auth, quota, route-sync from Control Plane |
| **Agent** | Rust | `rust/crates/agent` | Per-node daemon: hardware probe, link benchmark, engine supervisor, model cache, mDNS discovery |
| **Engine Adapter** | Rust | `rust/crates/engine-adapter` | `EngineBackend` trait; mock backend (default) and llama.cpp backend |
| **Planner** | Go | `go/planner` | Dynamic-programming optimal layer-split algorithm |
| **Dashboard UI** | TypeScript/React | `ui/` | SPA for fleet view, model catalog, deploy, and chat playground |
| **Proto contracts** | Protobuf | `proto/purser/v1` | Source of truth for gRPC types, generating both Go and Rust bindings |

## Two planes

```
                     OpenAI-compatible HTTP  (/v1/chat/completions, SSE)
┌────────────┐              │
│  Clients   │──────────────┘
│ (OpenAI    │
│  SDK/curl) │
└──────┬─────┘
       │
       ▼
┌─────────────────────────── Control-plane side ───────────────────────────┐
│                                                                          │
│   ┌──────────────┐        ┌──────────────────────────────────────────┐   │
│   │ API Gateway  │◀──────▶│              Control Plane               │   │
│   │  OpenAI /v1  │ routes │   Planner · Orchestrator · Reconciler    │   │
│   │ auth, quota  │  sync  │ Registry (SQLite) · internal PKI · REST  │   │
│   └──────────────┘        └──────────────────────────────────────────┘   │
│                                                │                         │
└────────────────────────────────────────────────┼─────────────────────────┘
                                                  │  gRPC + mTLS — control plane
                                                  │  (low volume: enroll, plan,
                                                  ▼   StartEngine, heartbeats)
              ┌──────────────────────────────┴────────┐
              │                                       │
 ┌──────────────────────────┐         ┌──────────────────────────┐
 │      Node / Agent        │         │      Node / Agent        │
 │  probe · linkbench       │         │  probe · linkbench       │
 │  supervisor · model cache│         │  supervisor · model cache│
 │  Engine Adapter → engine │         │  Engine Adapter → engine │
 └──────────────────────────┘         └──────────────────────────┘
              ▲                                       ▲
              └───────────────────────────────────────┘
  DATA PLANE: engine↔engine activations (~KB/token) over the
  trusted subnet — only activations cross the net (the pipeline)
```

### Control plane

The control plane runs in Kubernetes as three container images:

- **Control Plane** (`ghcr.io/andrew19881123/purser-control-plane`) — the brain. Hosts the SQLite Registry, internal PKI (CA that issues mTLS certificates to agents), the REST `/api/v1` management API, and the gRPC `RegistrationService` (for agent enrollment and heartbeat).
- **API Gateway** (`ghcr.io/andrew19881123/purser-gateway`) — the front door. Exposes the OpenAI-compatible `/v1` endpoint. The Control Plane pushes route updates to it over HTTP, authenticated by a shared internal token.
- **Dashboard UI** (`ghcr.io/andrew19881123/purser-ui`) — the operator interface. A React SPA served by nginx.

Control-plane traffic is low-volume: enrollment, heartbeats, plan delivery, `StartEngine` RPCs.

### Data plane

The data plane stays on the trusted LAN subnet between fleet nodes. When a model is split across multiple nodes, only activations (~KB per token) cross the network between pipeline stages. This is pure node-to-node traffic that never passes through Kubernetes.

!!! warning "Trusted LAN assumption"
    Purser assumes a **trusted LAN**. The Agent's inference engine worker is not sandboxed. Never expose agent ports or the inference engine directly to the public internet.

## Request flow

1. Client sends `POST /v1/chat/completions` to the API Gateway.
2. Gateway authenticates the bearer token (API key).
3. Gateway looks up the route for the requested model and forwards the request to the **pipeline host** node's inference endpoint (`http://<host>:8000/v1/chat/completions`).
4. The pipeline host (Agent on the host node) coordinates with worker nodes; only activations travel between stages.
5. Tokens stream back to the client over SSE.

## Deploy flow

1. **Enroll** — Agent starts, reads `PURSER_JOIN_TOKEN` and `PURSER_CONTROL_PLANE_ADDR`, calls the `RegistrationService` gRPC `Join` RPC. The Control Plane issues an mTLS certificate and the node transitions to `READY`.
2. **Plan** — Operator calls `POST /api/v1/models/{id}/deploy`. The Planner inspects the current fleet (READY nodes, their hardware profiles, the link benchmark matrix) and produces an optimal layer-split `DeploymentPlan`.
3. **Orchestrate** — The Orchestrator calls `StartEngine` on each assigned node: **workers first, then the host** (so the pipeline is ready end-to-end before the host starts accepting requests).
4. **Active** — The deployment transitions to `ACTIVE`. The Control Plane pushes a route update to the Gateway.
5. **Serve** — Clients can now reach the model at the OpenAI-compatible endpoint.

## Engine state

!!! note "Mock engine in v0.1.1"
    The complete *enroll → deploy → chat* flow is demonstrated with the built-in **mock engine** (`PURSER_ENGINE_BACKEND=mock`). The mock engine responds with generated tokens and proves the pipeline mechanics but does not run real model inference.

    The `adapter-llamacpp` Engine Adapter is implemented and unit-tested. Validation on real GPU hardware is still in progress.

## Persistence and HA

The Control Plane's SQLite Registry and internal PKI CA key/cert are stateful and require a PVC (mounted at `/data` in the container). Keep `replicaCount: 1` — SQLite is single-writer.

Multi-replica HA (Raft-replicated Registry + Gateway VIP) is an **Enterprise** feature behind `PURSER_LICENSE_KEY`. See [Enterprise: Open-Core Model](../enterprise/overview.md).

## Port reference

| Component | Port | Protocol | Purpose |
|---|---|---|---|
| Control Plane | 8080 | HTTP | Management REST API (`/api/v1`), Dashboard backend |
| Control Plane | 9443 | gRPC/mTLS | RegistrationService (Agent enrollment + heartbeat) |
| Agent | 50151 | gRPC | AgentService (Control Plane → Agent: StartEngine, etc.) |
| Agent | 8000 | HTTP | Inference endpoint (OpenAI-compatible, served by engine) |
| Agent | 50152 | UDP | Link-benchmark reflector (agent port + 1, best-effort) |
| Gateway | (configured) | HTTP | OpenAI-compatible `/v1` endpoint for clients |
