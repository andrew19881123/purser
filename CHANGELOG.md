# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

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
  `purser-license` keygen/verify tool. See [LICENSING.md](LICENSING.md).
- **Continuous integration** (`.github/workflows/ci.yml`) — green across all
  four jobs: `proto` (buf lint), `rust` (build + test + clippy `-D warnings` +
  fmt check), `go` (build/vet/test for `planner` and `controlplane`), and `ui`
  (typecheck + build).

[Unreleased]: https://github.com/andrew19881123/purser/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/andrew19881123/purser/releases/tag/v0.1.0
