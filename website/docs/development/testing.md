# Testing Architecture

Purser uses a **three-layer test strategy** designed to catch defects at the boundaries where components meet — the most common place bugs hide. This guide covers the test layers, the feature manifest that keeps your changes honest, and how to run tests locally.

---

## The Layer Model

### Why three layers?

Every defect found during v0.6→v0.7 lived at a **junction between components**: a page that calls a router that doesn't load the data it promises, a module boundary where one component sends the wrong type, a control-plane endpoint that the Gateway interprets differently, a feature flagged in code but missing from docs.

Unit tests and mocks by construction do not reach these seams — a mocked gateway will never reveal a real Gateway bug. **Contract tests** exist because they stand at the boundaries:

| Layer | Scope | Speed | Coverage |
|---|---|---|---|
| **Unit** | Single module, mocked dependencies | ~200ms (Go), ~2s (Rust) | Logic paths within a component |
| **Contract** | Routes ↔ API specs, client ↔ page, code ↔ manifest | ~1s (no stack) | Seams: does the API match the client? Does the UI page exist? Is every feature documented? |
| **E2E** | Full stack: request routing, actual service boundaries, restart resilience | ~2min | The pipe: does inference actually flow through Gateway→CP→Agent→mock-engine→back? Does the stack survive a Gateway restart? |

### Failure diagnosis

- **Unit test fails:** fix the module's logic.
- **Contract test fails:** fix the cross-component contract (route definition, client method, page nav, docs, permissions).
- **E2E test fails:** fix integration behavior or test the real stack manually (`docker compose up`).

---

## The Feature Manifest

Every feature you add must have a row in `tests/contract/features.json`. This file is the **source of truth** — it maps each feature across every surface it touches.

### Structure

```json
{
  "name": "api-keys",
  "routes": [
    "GET /api/v1/apikeys",
    "POST /api/v1/apikeys",
    "DELETE /api/v1/apikeys/{id}"
  ],
  "openapi": false,
  "client": [
    "listApiKeys",
    "createApiKey",
    "revokeApiKey"
  ],
  "page": "/api-keys",
  "nav": "nav.apiKeys",
  "docs": "configuration/api-keys.md",
  "perm": "team:keys:create",
  "gated": false
}
```

| Field | Meaning | Example |
|---|---|---|
| `name` | Feature identifier, kebab-case | `"api-keys"` |
| `routes` | Every HTTP route the CP registers for this feature | `"GET /api/v1/apikeys"` |
| `openapi` | Whether these routes appear in the served OpenAPI spec | `true` or `false` |
| `client` | Every client method in the TS SDK that wraps these routes | `"listApiKeys"` |
| `page` | The UI page path for this feature (or `null` if not UI-exposed) | `"/api-keys"` |
| `nav` | The nav label key in `ui/src/constants/nav.ts` (or `null`) | `"nav.apiKeys"` |
| `docs` | Path to the public docs page under `website/docs/` | `"configuration/api-keys.md"` |
| `perm` | The coarse permission that gates the feature (or `null` if unauthenticated) | `"team:keys:create"` |
| `gated` | Whether the feature requires a Purser license/enterprise flag | `false` |

### How to add a row when you add a feature

1. **Add a route to the CP** in `go/controlplane/server/server.go`.
2. **Add the feature row to `tests/contract/features.json`:**
   ```bash
   # Edit the file directly or use jq:
   jq '.+=[{"name":"my-feature","routes":["GET /api/v1/my"],"openapi":false,"client":["getMyThing"],"page":null,"nav":null,"docs":null,"perm":null,"gated":false}]' tests/contract/features.json > tmp && mv tmp tests/contract/features.json
   ```
3. **Run the completeness meta-test** (see [Running tests locally](#running-tests-locally) below). It will tell you if you missed anything.

### The Completeness Meta-Test

The contract test `TestManifestCoversEveryRegisteredRoute` runs every push (see [The Pre-Push Hook](#the-pre-push-hook)) and enforces:

- **Every registered route is in the manifest** — or in the `exemptRoutes` list with a reason.
- **Every route in the manifest is actually registered** in `server.go`.
- **Routes marked `openapi: true` exist in the served OpenAPI spec** at `/api/v1/openapi.json`.
- **Every `client` method exists** in the TS SDK.
- **Every `page` path has a React page** in `ui/src/pages/`.
- **Every `nav` label exists** in `ui/src/constants/nav.ts`.
- **Every `docs` path is a real file** in `website/docs/`.

If you add a new route without a manifest row, your push will fail:

```
TestManifestCoversEveryRegisteredRoute: 2 registered route(s) are neither in features.json nor exempt:
  POST /api/v1/my-new-endpoint
  GET /api/v1/my-new-endpoint/{id}

Add a feature row for each, or add it to exemptRoutes with a reason.
```

**Routes that should not appear in the manifest** go in `exemptRoutes` in `tests/contract/completeness_test.go` with a reason: internal machine-to-machine flows (agent heartbeats), auth flows (OIDC callbacks), health probes, etc.

---

## Running Tests Locally

### Quick contract tests (no stack required)

```bash
source ./env.sh
make contract
```

This runs:
- Go contract tests: route registration, OpenAPI consistency, permission axes.
- TS contract tests: client methods, page definitions, nav labels.
- **Completes in ~1 second.**

### Full verification (all layers, reproduces CI)

```bash
source ./env.sh
make verify
```

This runs:
1. **Contract** (Go + TS)
2. **Unit tests** (Go, Rust, TS)
3. **E2E** on a native mock-engine stack (2–3 minutes)
   - Builds Gateway, Agent, Control Plane binaries
   - Starts a local mock inference engine
   - Runs golden-path tests (enrollment, model deploy, inference request)
   - Validates Gateway restart resilience

### E2E stack only (if binaries are already built)

```bash
source ./env.sh
make e2e
```

---

## The Pre-Push Hook

To avoid committing broken contract tests, you can install a local pre-push gate:

```bash
make install-hooks
```

This installs a git hook at `.git/hooks/pre-push` that runs the fast contract tests before every push. If they fail, the push is blocked:

```
pre-push: contract (Go)…
FAIL: tests/contract

pre-push BLOCKED: contract tests failed. Fix them, or bypass with:
    git push --no-verify
```

### Escape hatch

If you need to push despite a failing contract test (rare — e.g. you've queued a fix in a parallel commit), use:

```bash
git push --no-verify
```

**Important:** `--no-verify` skips the entire hook, including any future hooks you may add. Use it sparingly and always confirm CI will catch the issue.

### Fail-safe

The hook gracefully skips layers if tools are missing:

- Go not found? TS tests will still run.
- `npm install` not run? TS tests are skipped with a warning.

If your toolchain is incomplete, `source ./env.sh && make setup` will fix it.

---

## What E2E Tests Actually Verify (and What They Don't)

### ✓ What is tested

- **The pipe:** A request enters the Gateway, routes through the Control Plane, reaches an Agent, and returns a mocked inference result.
- **Stability:** The Gateway survives a restart and re-learns its route table from the Control Plane.
- **Enrollment:** A new node joins the data plane.
- **Model deployment:** Models register with the catalog and are discoverable.

### ✗ What is NOT tested (and why)

- **Inference quality:** Model output correctness is GPU-bound and requires a real model. E2E uses a deterministic mock engine that returns fixed outputs — this tests the **infrastructure**, not the **results**.
- **Docker Compose deployment realism:** The `docker-compose.yml` stack is a development convenience, not the production Helm chart. The E2E tests use native binaries. Real deployment testing happens in the CI Helm integration suite (separate from this document).
- **Documentation prose validation:** Docs are prose, not code. E2E verifies the doc files exist and are referenced correctly, but does not check that their content matches the code.

---

## Quick Reference

| Task | Command | Time | Checks |
|---|---|---|---|
| Check one feature | `cd tests/contract && CGO_ENABLED=0 go test ./... -run TestManifestCov` | <1s | Is my new route in the manifest? |
| All contracts | `make contract` | ~1s | Routes, APIs, client, page, nav, docs, perms |
| All tests (CI simulation) | `make verify` | 3–5m | unit + contract + e2e + Rust |
| Auto-gate before push | `make install-hooks` | (once) | Installs pre-push hook |
| Emergency push | `git push --no-verify` | — | Bypass hook (use sparingly) |

---

## For Operators and Contributors

- **Adding a feature?** Create a row in `tests/contract/features.json` when you add the route.
- **Fixing a bug?** Run `make contract` before pushing to catch cross-component issues early.
- **Setting up a new clone?** Run `make install-hooks` once to get the pre-push gate. It's opt-in and fail-safe.
- **Debugging a contract failure?** Read the error — it will name the exact mismatch (missing route, missing client method, broken nav label) and point you to the manifest or the code that needs fixing.
