# Architecture Overview

Purser is a distributed inference platform composed of three main processes:
the **Control Plane**, one or more **Agents** (one per GPU node), and the
**Gateway** (the inference-request router).

```
  ┌──────────────────────────────────────────┐
  │           Control Plane                  │
  │  ┌──────────┐  ┌───────────┐  ┌───────┐ │
  │  │ REST API │  │ Reconciler│  │  PKI  │ │
  │  └──────────┘  └───────────┘  └───────┘ │
  │         │           │             │      │
  │         └─────┬─────┘             │      │
  │           Orchestrator ──mTLS──►  │      │
  └──────────────────────────────────────────┘
              │ gRPC (TLS)
    ┌─────────┴──────────┐
    ▼                    ▼
  Agent (GPU node 1)   Agent (GPU node 2)
    │                    │
    └─────────┬──────────┘
              ▼
           Gateway  ◄──── inference clients
```

## Transport Security

### Control plane → Agent gRPC (mTLS/TLS)

When the internal PKI is active (i.e. `PURSER_PKI_DIR` is set and the CA has
been initialized), the control plane's **Orchestrator** dials agent gRPC
endpoints using **server-side TLS**: the CA cert pool is supplied as the root
of trust, so the agent's server certificate is verified against the internal
CA. Agents receive the CA certificate at enrolment (`Join`) and use it to
issue their own server certificates.

This is a significant security improvement over the previous cleartext
transport and prevents man-in-the-middle attacks on the orchestrator→agent
control channel.

**Fallback (dev mode):** If the CA pool is unavailable (e.g. `PURSER_PKI_DIR`
is absent in a local development environment) the orchestrator falls back to
insecure transport and emits a warning log:

```
WARN orchestrator: no CA pool supplied — using insecure transport (dev mode only)
```

### Agent → Control Plane gRPC

Agents connect to the RegistrationService (`Join`, `Heartbeat`) using the CA
certificate bundle returned at join time for mutual TLS validation.

## Components

### Control Plane

The central coordinator. It runs:

- **Management REST API** — CRUD for models, nodes, deployments, plans.
- **RegistrationService** — gRPC server that handles `Join` and `Heartbeat`
  from agents; issues mTLS certificates via the internal PKI.
- **Orchestrator** — Translates deployment plans into `StartEngine`/`StopEngine`
  gRPC calls to agents; enforces worker-before-host ordering; rolls back on
  failure.
- **Reconciler** — A self-healing control loop that compares desired state
  (active deployments) with real state (heartbeat-derived node health) and
  autonomously restarts engines, proposes failovers, and cleans up orphans.
- **Internal PKI** — Self-signed CA that issues ECDSA leaf certificates for
  agents and the gateway.

### Agent

One process per GPU node. Exposes the `AgentService` gRPC API for
`StartEngine`, `StopEngine`, and streaming `EngineEvent` updates. Sends
periodic heartbeats to the control plane.

### Gateway

The inference-request router. Maintains a routing table of
`model_id → host endpoint` entries pushed by the control plane Orchestrator.
Client requests are proxied to the appropriate host agent.

## Data Flow: Deploying a Model

1. Operator submits a `POST /api/v1/deployments` request to the Control Plane.
2. The Planner generates a `DeploymentPlan` assigning model layers to nodes.
3. The Orchestrator starts **worker** engines first (in parallel), waits for
   each to report `READY`, then starts the **host** engine with the workers as
   peers.
4. On success the Orchestrator publishes the route to the Gateway.
5. The Reconciler watches the deployment; if a node goes unreachable it either
   autonomously restarts the engine (single-node failure) or proposes a
   failover plan (multi-node failure).
