# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.1.1] - 2026-09-05

> Patch release. All three GHCR images and the Helm chart are updated to `0.1.1`.
> The GHCR `purser-ui:0.1.0` image still serves the old mock-by-default build
> until the image is rebuilt and republished. Given the security-correctness fix
> to the gateway, the next tag is expected to be `0.1.1`.

### Added

- **Tamper-evident hash-chained audit log** (`go/controlplane/audit/`) — new
  package with hash-chain engine and chain verifier. SQLite registry now persists
  `seq`, `prev_hash`, and `hash` per row via an idempotent migration. Enterprise
  feature, gated by `PURSER_LICENSE_KEY` with feature `"audit"`.
- **`GET /api/v1/enterprise/audit-log`** now returns real stored entries plus a
  chain-verification result (`{verified, length, break?}`) instead of the
  previous empty placeholder.
- **`DELETE /api/v1/models/{id}`** — removes a model from the catalog: 404 if
  unknown, 409 if the model is in use by a non-terminal deployment, 204 on
  success.
- **`POST /api/v1/models/{id}/plan`** — read-only Planner dry-run; no
  persist/deploy/audit side-effects; returns `{feasible: true, ...plan}` or
  `{feasible: false, reason}`.
- **`POST /api/v1/nodes/{id}/drain`** — cordons a node (marks it unschedulable);
  does not live-migrate existing deployments.
- **`DELETE /api/v1/nodes/{id}`** — soft-decommissions a node (state →
  `DECOMMISSIONED` + cert revocation); 409 if the node hosts a non-terminal
  deployment. Node restart (`POST /api/v1/nodes/{id}/restart`) is not
  implemented; intentionally deferred pending design decision.
- **UI defaults to real API** — the dashboard now targets the control-plane API
  by default; mock mode is opt-in via `PURSER_UI_MOCK=1`. API base URL is
  runtime-configurable via `PURSER_API_BASE_URL` (default `/api/v1`, same-origin)
  set through `env.js` loaded before the bundle; nginx serves `env.js` with
  `Cache-Control: no-store`. Helm chart wires the env at container start.
- **`gofmt` gate** added to the Go CI job, covering `go/` and
  `enterprise/license`.

### Fixed

- **[Security] Gateway TLS** — removed misleading `TlsConfig` and
  `tls-configured: true` log line. The gateway serves plaintext HTTP with TLS
  terminated at ingress/LB. The previous code implied active TLS termination in
  the process when none was occurring.

### Removed

- **`GET /api/v1/nodes` stub** removed from the API Gateway — this route was
  always empty on the gateway; node data lives in the control plane.

## [0.1.0] - 2026-09-04

First public Community Edition release. Delivers the complete zero-config
vertical — *enroll → deploy → chat* — end-to-end on a single node and split
across a multi-node pipeline, driven by the built-in mock engine.

### Added

- **Polyglot monorepo & proto contracts** — Rust + Go + TypeScript workspaces
  with `.proto` gRPC contracts (`proto/purser/v1`) as the shared source of
  truth, generating both Go and Rust types. Project-local toolchain in
  `.toolchain/` (Rust 1.98, Go 1.27, buf) — nothing installed globally.
- **Agent** (Rust, `rust/crates/agent`) — per-node daemon: hardware probe,
  network link benchmark, engine supervisor, model cache, mDNS discovery,
  self-healing, and a built-in mock inference server.
- **Engine Adapter** (Rust, `rust/crates/engine-adapter` + `adapter-llamacpp`)
  — `EngineBackend` trait with a mock backend and a llama.cpp backend (flag
  builder, GGUF reader, metrics parser), backed by a shared conformance suite.
- **Planner** (Go, `go/planner`) — dynamic-programming optimal layer-split
  algorithm (phases A–F), verified `DP == brute-force` plus property-based
  testing (2000 cases), with **calibrated** memory-bandwidth-bound decode/prefill
  performance estimates and operator-constraint validation (`validatePlanMemory`).
- **Control Plane** (Go, `go/controlplane`) — SQLite registry, orchestrator,
  reconciler, internal PKI, `RegistrationService` (gRPC enrollment via
  join-token), and the `/api/v1` REST API.
- **API Gateway** (Rust, `rust/crates/gateway`) — OpenAI-compatible `/v1`
  endpoint with SSE streaming, authentication, quota enforcement, and route-sync
  from the control plane.
- **Dashboard** (TypeScript/React, `ui/`) — SPA for fleet view, model catalog,
  deploy, and chat playground.
- **Single-node end-to-end deploy** (`tools/e2e_full.sh`) — real binaries:
  CP + Gateway → mint join-token → agent enrollment over gRPC → node READY with
  real hardware profile → model seed → Planner fit → deploy → DP plan →
  orchestrator `StartEngine` → mock engine READY → deployment ACTIVE →
  route-sync CP→Gateway → `GET /v1/models` → real chat (non-stream + SSE
  token-by-token to `[DONE]`).
- **Multi-node end-to-end deploy** (`tools/e2e_multinode.sh`) — pipeline-parallel
  split across 2 worker nodes: distinct advertised addresses per agent
  (`advertised_agent_addr` / `advertised_inference_addr`), a plan with 2
  assignments (HOST + WORKER) and `pipeline_order` of length 2, worker started
  before host, and real chat (non-stream + SSE) proxied to the host's inference
  address.
- **Open-core licensing model** (LiteLLM-style) — MIT-licensed core plus a
  source-available, key-gated `enterprise/` directory. Runtime activation via
  `PURSER_LICENSE_KEY` with fully offline ed25519 verification (no phone-home,
  air-gap friendly); enterprise routes in the control plane return
  `402 Payment Required` without a valid entitlement. Includes the
  `purser-license` keygen/verify tool. See [LICENSING.md](https://github.com/andrew19881123/purser/blob/main/LICENSING.md).
- **Continuous integration** (`.github/workflows/ci.yml`) — green across all
  four jobs: `proto` (buf lint), `rust` (build + test + clippy `-D warnings` +
  fmt check), `go` (build/vet/test for `planner` and `controlplane`), and `ui`
  (typecheck + build).

[Unreleased]: https://github.com/andrew19881123/purser/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/andrew19881123/purser/releases/tag/v0.1.0
