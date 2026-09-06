# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.3.0] - 2026-09-06

> **v0.3 — "Enterprise-Ready"** — major feature release.
>
> Highlights: Anthropic Messages API compatibility (Claude Code / Cursor),
> configuration-as-code with GitOps reconciler, inference audit log (AI Act Art.12),
> deployment approval gates (AI Act Art.14), embedded OPA policy engine,
> HA Raft foundation, multi-tenancy chargeback, HTTP proxy + custom CA support,
> backup/restore CLI, full UX audit with bookmarkable URLs and 17 UX fixes,
> comprehensive hardening across all 6 components.

### Added — API & Compatibility
- **Anthropic Messages API** (`POST /v1/messages`) — full translation layer for Claude Code, Cursor, and `@anthropic-ai/sdk`; `x-api-key` auth; streaming SSE in Anthropic format (`content_block_delta`); non-streaming `message` envelope; errors in Anthropic shape
- Quota, rate-limiting, Prometheus metrics all reuse the existing OpenAI inference plane

### Added — Enterprise Network
- **HTTP proxy support** — `PURSER_AGENT_HTTP(S)_PROXY` / `NO_PROXY` / `CA_BUNDLE` for agent and gateway (reqwest client factory); `PURSER_CA_BUNDLE` + `http.ProxyFromEnvironment` for control-plane (`go/controlplane/transport`)
- Enables operation behind corporate proxies and with private CA certificates

### Added — Configuration-as-Code (GitOps)
- **`purser.yaml` schema** (`go/controlplane/config/`) — `ClusterConfig` with models, deployments, quotas, gateway; `Load` / `Validate` / `Diff` functions
- **`POST /api/v1/config/apply`** — idempotent apply from YAML body; `POST /config/diff` dry-run; `GET /config/export` exports current state
- **`--config` flag** / `PURSER_CONFIG` env — applies `purser.yaml` at startup
- **GitOps watcher** — SHA-256 polling loop (30s, `PURSER_CONFIG_INTERVAL`) re-applies on file change; non-fatal on missing file (K8s ConfigMap late-mount)

### Added — Compliance (AI Act / GDPR / DORA)
- **Inference audit log** — `InferenceEvent` proto + `inference_audit_log` SQLite table with 3 compound indexes; prompt content never stored (GDPR Art.5 minimisation); AI Act Art.12
- **`POST /api/v1/inference-events`** (internal, gateway→CP) + **`GET /api/v1/inference-audit`** (enterprise-gated); gateway emits fire-and-forget events after each inference
- **Deployment approval gates** — admin must approve before a model deploys; `GET/POST /api/v1/approvals`; `ApprovalsPage` UI; AI Act Art.14 human oversight; enterprise feature `deployment_approvals`
- **Backup/restore CLI** — `purser backup --output` / `purser restore --input --confirm` via SQLite `VACUUM INTO`; DORA Art.12 compliant; Kubernetes CronJob example in docs

### Added — Policy-as-Code
- **Embedded OPA engine** (`go/controlplane/policy/`) — Rego policies stored in SQLite, atomically reloaded under `sync.RWMutex`; evaluates `data.purser.allow` on every deploy
- **CRUD API** — `GET/PUT/DELETE /api/v1/policies/{name}` + `POST /api/v1/policies/eval` dry-run
- Open-by-default (no policies = allow everything); enterprise feature `policy_engine`

### Added — High Availability
- **Raft consensus foundation** (`hashicorp/raft` + BoltDB) — `PurserFSM` applies log entries to `SQLiteRegistry`; TCP transport; bootstrap + join support; single-node mode preserved when `PURSER_RAFT_NODE_ID` unset
- **`GET /api/v1/cluster/status`** — leader/follower state, Raft stats

### Added — FinOps / Multi-tenancy
- **Chargeback reports** — `GET /api/v1/billing/report` (JSON + CSV) aggregates `inference_audit_log` by tenant+model for configurable windows; `GET /api/v1/billing/summary` (no enterprise gate)
- **`ChargebackPage`** UI — period picker (7/30/90 days), usage table sorted by tokens, CSV export; enterprise feature `billing`
- FOCUS spec alignment and FinOps integration docs

### Added — UI (UX Audit)
- History routing (`createBrowserRouter`) — clean bookmarkable URLs (`/fleet`, not `/#/fleet`); nginx `try_files` already in place
- Audit Log nav link moved from "Use" to "Operate" section
- `ApprovalsPage` and `ChargebackPage` added to sidebar nav
- Model Studio 4-step progress stepper (`done` / `active` / `pending`, `aria-current`)
- Settings `QuickStatsBar` above fold (edition, active keys, monthly requests)
- Catalog: empty state with link to Model Studio; spec-list `title` tooltips (params/layers/context/quant)
- Onboarding: join token expiry shows absolute timestamp on hover
- Playground: "Ignored in mock mode" hidden in production (`import.meta.env.DEV` only)
- Fleet: `ReconcilerStatusCard` shows "Status unknown" on API error instead of disappearing; link quality and FP4 tooltips
- Deployments: subtitle added to `PageHeader`

### Added — Hardening (Security)
- **Rust agent**: AES-256 key `zeroize` on drop; probe benchmark runs outside mutex lock; heartbeat exponential backoff reconnect (1s→60s); `NodeMetrics` populated from sysinfo (was always `None`)
- **Rust gateway**: body size limit 4 MB; constant-time bearer token comparison (`subtle`); sanitized upstream error messages (no IP:port leak); semaphore cleanup on route removal; bounded usage-report spawn (256 permits); mid-stream SSE error frame
- **Go control-plane**: `crypto/subtle.ConstantTimeCompare` for internal token; `GetAPIKeyByHash` O(1) RBAC (was O(n) full scan); `sync.RWMutex` on reconciler tracker; `pkceStateStore` bounded at 1000 entries; rate-limiter LRU eviction; goroutine panic recovery via `defer recover()`
- **Go planner**: `PURSER_PLANNER_ORDERING_THRESHOLD` capped at 20 (OOM guard); `Plan()` accepts `context.Context`; KV SSD offload modeled in fit check
- **CI/Docker**: nginx UI runs as non-root (UID 65532); security headers (X-Content-Type-Options, X-Frame-Options, Referrer-Policy); CI job timeouts; `npm test` step; `dependabot.yml` weekly updates
- **UI**: confirm dialogs for drain/remove/revoke; `handleUnauthorized()` on 401; `sessionStorage` for gateway key; SSE stream error visible to user

### Added — Infrastructure
- OIDC Authorization Code Flow + PKCE: endpoint `/auth/login` e `/auth/callback`; session cookie HttpOnly; browser SSO funzionante end-to-end
- RBAC: mapping claim OIDC (groups/roles) → ruoli Purser via `PURSER_OIDC_GROUP_MAPPINGS`; tenant-scoped list deployments
- TLS opzionale sul management API (`PURSER_TLS_AUTO` usa la PKI interna)
- Rate limiting sul management API (per-IP e per-key, 100/50 RPS default)
- Webhook notification quando il reconciler richiede approvazione manuale (`PURSER_RECONCILER_WEBHOOK_URL`)
- Fleet Capacity Headroom API (`GET /api/v1/fleet/capacity`)
- NodeMetrics → OTEL bridge: 5 gauge per-nodo (CPU, GPU, bandwidth, tok/s, inference alive)
- Reconciler OTEL metrics: counter eventi, gauge pending-approval, histogram loop duration
- Configurable OTEL trace sampler (`OTEL_TRACES_SAMPLER`)
- Reconciler config status endpoint (`GET /api/v1/reconciler/status`)
- Periodic bandwidth re-calibration (`PURSER_AGENT_BW_RECALIBRATE_INTERVAL_HOURS`)
- llama.cpp backend registrato in BackendRegistry (feature-gated `--features llamacpp`)
- ModelCache cablato nel path StartEngine: model_ref risolto a path GGUF locale
- HttpFetcher attivato di default (`PURSER_MODEL_MIRROR_URL`)
- `prefix_caching_factor` e `kv_ssd_offload` in HardwareProfile proto; planner usa le capability reali
- Flash attention come campo esplicito in EngineParams (`flash_attn: bool`)
- ARM64 build matrix: linux/arm64 + darwin/arm64 (Apple Silicon M2/M3)
- `docker-compose.yml` demo mode: `docker compose up`, 3 servizi mock, no GPU
- `devcontainer.json` + `make dev` + good-first-issue guide in CONTRIBUTING.md
- `GET /api/v1/models/{id}` endpoint server-side
- Python SDK: `AsyncPurserClient` + `stream_metrics()` SSE generator
- TypeScript SDK: `@purser/sdk 0.3.0` (native fetch, zero dipendenze runtime)
- Planner micro-benchmark suite con dati baseline reali
- Ansible role `purser_agent` per fleet enrollment
- Webhook notifications + Ansible integration in GitHub Pages docs
- apt/yum hosted repository via Cloudsmith (step CI pronto)
- SLSA L2 provenance + cosign SBOM attestazioni + chart signing
- Production license trust root (chiave reale, modello open-core commercialmente funzionale)
- Fix ROOT hardcoded in e2e scripts: auto-detection via `${BASH_SOURCE[0]}`

### Fixed
- `purser-license keygen` output ora include guida step-by-step con chiave di produzione
- OIDC: `PURSER_OIDC_CLIENT_SECRET` implementato (era documentato ma non letto)
- `bench_test.go`: `Plan()` updated to pass `context.Context` as first argument

### Changed
- Release pipeline: ARM64 artifacts, SLSA provenance, SBOM come attestazioni cosign
- Planner: calibrazione più precisa con prefix caching e KV SSD offload dai profili hardware reali

## [0.2.0] - 2026-09-05

> **v0.2 — "Production-Grade Enterprise"** — major feature release.
>
> Highlights: real-time hardware metrics SSE, RBAC for API keys (admin/viewer/inference),
> model import from HuggingFace Hub / S3 / GCS / Azure Blob / AWS SageMaker /
> GCP VertexAI / Azure ML, Model Studio UI with fleet-split preview, /v1/embeddings
> endpoint, node auto-enrollment bundle, Python management SDK (purser-sdk 0.2.0),
> cosign-signed container images + SBOM, automated release pipeline, mTLS on
> orchestrator→agent gRPC, Gossip SWIM improvements, AES-256-GCM encrypted
> agent secret store, HttpFetcher for model cache, Planner engineCaps awareness,
> OpenAPI 3.0 spec at /api/v1/openapi.json, enterprise license CLI improvements,
> CI toolchain pinning (Rust 1.98.1), OTEL traces/metrics/audit log export,
> MkDocs documentation site on GitHub Pages.

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
  `purser-license` keygen/verify tool. See [LICENSING.md](LICENSING.md).
- **Continuous integration** (`.github/workflows/ci.yml`) — green across all
  four jobs: `proto` (buf lint), `rust` (build + test + clippy `-D warnings` +
  fmt check), `go` (build/vet/test for `planner` and `controlplane`), and `ui`
  (typecheck + build).

[Unreleased]: https://github.com/andrew19881123/purser/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/andrew19881123/purser/releases/tag/v0.1.0
