# Purser — Test Architecture Design

**Date:** 2026-09-12
**Status:** approved (design), pending implementation plan
**Author:** PM + product owner (brainstorming session)

---

## Problem

Purser already has substantial test coverage — 123 Go test files, 32 UI test
files, 26 Rust test modules, an E2E workflow (`enroll→deploy→chat`), and even a
contract test (`openapi_consistency_test.go`). **Yet every defect found during
the v0.6→v0.7 work slipped through all of it.** Looking at *where* those defects
lived:

| Defect found | Junction it lived on |
|---|---|
| `ApiKeysPage`/`PlatformUsersPage` built but routed to `ComingSoonPage` | page ↔ **router** |
| Served permission catalog ≠ enforced vocabulary (custom roles granted nothing) | Go module ↔ **Go module** |
| Gateway route table lost on restart (503 until re-deploy) | control plane ↔ **gateway** |
| Docs promised `POST /admin/pki/rotate` etc. that return 404 | doc ↔ **code** |
| Playground showed 3 identical models (fallback not deduped) | UI ↔ **API** |

**Every defect sat on a junction between two components.** Unit tests live
*inside* a component; mocks *simulate* the neighbour instead of verifying it. By
construction neither touches the space between components — which is exactly
where all the defects were born. The one test that *did* prevent a gap
(`openapi_consistency_test`) is not a unit test: it is a **contract test** that
compares two real artifacts (`openapi.yaml` vs `openapi.json`). That is the model
to generalise: tests positioned on the seams, not more tests inside the rooms.

## Goal

An architecture-first, thin slice that crosses every test layer and makes the
five known classes of junction-gap **structurally impossible to reintroduce
silently** — while adding **zero new CI jobs**. Protection runs **locally**: fast
contract checks in an automatic git pre-push hook, heavy E2E as an on-demand
command.

## Constraints (decided during brainstorming)

1. **Architecture-first**: a thin slice across every layer (harness + one test
   per junction + local gate + conventions), then fill in. Not exhaustive
   coverage first.
2. **Local-first, zero new CI jobs**: the existing CI (`ci.yml`, `e2e.yml`)
   is untouched. The point is confidence *before* pushing, so CI becomes
   confirmation rather than the discovery tool.
3. **Gate = automatic pre-push hook (fast) + on-demand command (heavy)**:
   fast contract tests block a push if red; the E2E suite is `make e2e`.
4. **E2E harness = native binaries + mock engine**: deterministic, no rootful
   Docker, already what `tools/e2e_full.sh` does. Real inference quality is
   GPU-blocked and therefore out of scope for now.
5. **Contract tests each live in their natural language**: Go for
   route/openapi/permission/docs axes; TS/vitest for page/nav/client axes. The
   manifest is the only shared file; neither side parses the other's language.
6. **Approach: registry-driven** — a declarative manifest of junctions is the
   source of truth; contract tests verify every feature across all axes. The 5
   known-gap guards are the first fills. A completeness meta-test prevents the
   manifest itself from silently going stale.

## Non-goals (declared, so we do not fake coverage)

- **Real inference quality** → GPU-blocked; a separate phase after hardware
  validation.
- **Docker-compose E2E** (nginx / distroless / network realism) → possible
  phase 2 if deploy-realism is wanted.
- **Docs prose beyond endpoints** (e.g. "Use" vs "Observability" section names)
  → the docs axis stays at "no phantom endpoints in docs".
- **Property-based / fuzz testing** → not part of this architecture.

---

## Architecture

### Layer structure and where each test lives

```
tests/
  contract/                          # fast, no stack — run in the pre-push hook
    features.yaml                    # ← THE MANIFEST (source of truth for junctions)
    features_completeness_test.go    # meta-test: every registered route ∈ manifest
    features_backend_test.go         # Go axes: routes / openapi / perm / docs
  ui/src/contract/                   # TS/vitest — run in the pre-push hook
    features.contract.test.tsx       # TS axes: client / page / nav
  e2e/
    harness.go                       # spin up CP+gateway+agent native, engine=mock
    golden_path_test.go              # enroll→deploy→route→chat(mock)→verify
    gateway_restart_test.go          # CP↔gateway guard: restart gateway, assert recovery
```

Both contract sides **read the same `features.yaml`** — it is the pivot that ties
the two toolchains together without duplicating the truth. The 5 known guards
distribute as: reachability + api-client → `ui/src/contract`; permission-catalog
+ doc-endpoint + route-openapi → `tests/contract` (Go); gateway-restart →
`tests/e2e`.

### The manifest schema

Each feature is one row in `tests/contract/features.yaml`:

```yaml
- name: api-keys                          # unique id
  routes:                                 # one or more API routes (method + path)
    - "GET /api/v1/apikeys"
    - "POST /api/v1/apikeys"
  openapi: true                           # expected in the public contract? (false for internal CP→gw)
  client: [listApiKeys, createApiKey]     # methods on the PurserApi interface (TS)
  page: /api-keys                         # reachable UI route (or null if N/A-UI)
  nav: nav.apiKeys                        # expected sidebar entry (or null)
  docs: configuration/api-keys.md         # docs page (or null)
  perm: team:keys:create                  # enforced permission (or null if not gated)
  gated: false                            # enterprise-gated? (402 expected without a licence)
```

### What each axis verifies — and which defect it closes

| Axis | Contract test verifies | Defect it would have caught |
|---|---|---|
| `routes` | each path is registered in `server.go` **and** every non-exempt registered route is in the manifest (meta-test) | phantom route / untracked feature |
| `openapi` | if `true`, each route ∈ served `openapi.json` | `openapi.yaml`↔`json` drift (caught once already) |
| `client` | each method exists on the `PurserApi` interface | — |
| `page` | if non-null, the route resolves to a real page, **not** `ComingSoonPage`/`NotFoundPage` | **ApiKeys/PlatformUsers → ComingSoon** |
| `nav` | if non-null, the key exists in `Layout.tsx` **and** in both en.ts + it.ts | orphan nav entry / i18n mismatch |
| `docs` | the page exists **and** every endpoint cited in the `.md` is a real route | **PKI docs promising 404s** |
| `perm` | if non-null, the string ∈ **enforced** vocabulary (`permissions/engine.go`), not just `permissions.All()` | **permission catalog ≠ enforcement** |
| `gated` | flag ↔ 402 behaviour coherence (verified in e2e) | compliance panels without a gate |

Plus a global invariant elevated to a permanent contract guard: **`catalog ==
enforced`** (the set-equality the Tier 2a fix already wrote).

The axis split: `page`/`nav`/`client` verify in **TS** (read `features.yaml`
from vitest); `routes`/`openapi`/`perm`/`docs` verify in **Go**. Each side
verifies only the axes it can read robustly in its own language — no side parses
the other's language. This is the anti-fragility lesson from the introspective
approach: adopt introspection only where parsing is robust (Go route table),
never where it is brittle (TS source, markdown).

### The completeness meta-test (closes the gap-in-the-gap)

A manifest has a characteristic failure: it becomes a source of truth that can
itself drift. If someone adds a route without adding the row, the manifest is
incomplete and tests stay green-blind. The defence: `features_completeness_test.go`
enumerates the routes actually registered in `server.go` (extracted reliably —
the same way `grep 'HandleFunc'` already does) and asserts **every non-exempt
route appears in the manifest**. A missing row turns the meta-test red. Routes
deliberately excluded (e.g. `/auth/*`, internal `POST /usage`) are listed in an
explicit exemption set with a reason.

### E2E harness

`tests/e2e/harness.go` is a Go helper each E2E test calls in setup:

```
1. build     → go build control-plane; cargo build -p gateway -p agent (reuse if fresh)
2. ports     → assign EPHEMERAL free ports (never fixed!) and inject via env
3. start     → CP (PURSER_ENGINE_BACKEND=mock, PURSER_AGENT_GRPC_INSECURE=true),
                gateway (PURSER_GATEWAY_API_KEYS set → fail-closed respected),
                agent (mock, enroll via join-token)
4. readiness → poll /health with a timeout — NEVER fixed sleeps (condition-based waiting)
5. teardown  → kill in reverse order, capture logs on failure, clean temp dirs
```

Two reliability principles, both learned painfully this session and
**non-negotiable** in the harness:

- **Ephemeral ports, not fixed.** The 9443 conflict (native CP vs compose) and
  two agents on the same 50151 burned real time. Fixed ports flake the moment
  two runs overlap or a port is occupied. Ask the OS for a free port and inject
  it — this eliminates the whole class.
- **Readiness by condition, never `sleep`.** A `sleep 5` after start is the #1
  cause of flaky E2E: sometimes 5s is enough, sometimes not. Poll `/health`
  until ready (or timeout) — deterministic regardless of machine speed. (From
  the systematic-debugging `condition-based-waiting` technique.)

The harness is written in **Go**, not bash: the tests that consume it are Go
(`testing.M`), process/port/teardown management is far more robust in Go, and
assertions become real assertions instead of `grep` on output.
`tools/e2e_full.sh` stays as-is for manual use and documentation; the versioned
E2E suite does not depend on it.

### The two initial E2E tests (heavy fills)

1. **`golden_path_test.go`** — the path no unit test touches:
   `join-token → enroll agent → deploy tinyllama → route pushed →
   POST /v1/chat/completions → mock response`. Verifies the *whole pipe* holds
   end-to-end. This is exactly the flow that broke at the start of the session
   (gateway with no routes → 503).
2. **`gateway_restart_test.go`** — the CP↔gateway guard as a permanent test:
   active deployment → **kill the gateway** → restart → assert `/v1/models`
   self-repopulates within N seconds (via the reconcile loop) and chat works
   again. The regression test for the most serious bug fixed this session.

Both run only via `make e2e` (on-demand), **not** in the pre-push hook — too
slow (build + stack spin-up). The hook stays on the fast contract tests.

### Local gate: pre-push hook + Make targets

The automatic gate is a git hook installed **locally**, versioned via an
install script so anyone who clones can activate it with one command:

```
tools/hooks/pre-push          # versioned — the actual script
tools/hooks/install.sh        # git config core.hooksPath tools/hooks (one command, opt-in)
```

`core.hooksPath` (not copying into `.git/hooks/`) so the hook is versioned,
updates with a `git pull`, and needs no reinstall on change.

The `pre-push` runs:
```
1. contract Go:  go test ./tests/contract/...        (~2s, no stack)
2. contract TS:  npm --prefix ui test -- contract     (~3s)
3. green → push proceeds;  red → push BLOCKED with an actionable message
```

Three defences so the hook is used rather than disabled (a disabled gate is
worse than none — it gives false confidence):

- **Fast only** — never E2E or heavy builds in pre-push; the budget is "a few
  seconds".
- **Honest escape hatch** — `git push --no-verify` stays possible for a real
  emergency, and the hook says so explicitly in its error message.
- **Fail-safe, not blind fail-closed** — if a toolchain is missing (e.g. `npm`
  not installed on that machine), the hook reports and *skips that layer*
  instead of blocking with a cryptic error.

Make targets (explicit entry points, so tests are runnable without a push):

```makefile
make test          # unit across all workspaces (go + cargo + ui) — already exists
make contract      # only the fast contract tests (what the hook runs)
make e2e           # harness + golden_path + gateway_restart (on-demand, heavy)
make verify        # contract + unit + e2e — "reproduce CI locally" before a PR
make install-hooks # activate the pre-push (core.hooksPath)
```

`make verify` is the direct answer to "I can't reproduce the CI pipeline
locally": one command that runs everything that matters, locally, before opening
a PR — so CI becomes confirmation, not discovery.

The hook is **opt-in** (each dev runs `make install-hooks` once).

---

## Build sequence (a thin slice, each step verifiable)

```
Step 1 — Minimal manifest + completeness meta-test
  features.yaml with ~5 real features (api-keys, roles, dataplanes, compliance, join-token)
  + features_completeness_test.go (every registered Go route ∈ manifest)
  → immediately red on every route not yet in the manifest → populate until green
  → THIS is the test that makes "untracked feature" impossible

Step 2 — Go contract (routes / openapi / perm / docs axes)
  extends openapi_consistency_test; the catalog==enforced guard; doc-endpoint guard

Step 3 — TS contract (page / nav / client axes)
  reads the same features.yaml from vitest; the reachability guard (no ComingSoon)

Step 4 — E2E harness in Go (ephemeral ports + readiness-poll)

Step 5 — golden_path + gateway_restart E2E on top of the harness

Step 6 — pre-push hook + Make targets + install.sh
```

## Definition of Done

- The completeness meta-test is green with **all** non-exempt routes in the
  manifest (or explicitly marked exempt with a reason).
- Each of the 5 known defects has a test that **would fail** if the defect were
  reintroduced — proven by temporarily reintroducing each and showing the red
  (failing-first).
- `make contract` runs in <10s; `make e2e` green on native+mock stack;
  `make verify` reproduces CI locally.
- The hook blocks a push with a red contract and lets it through with
  `--no-verify`.
- Docs: a page explaining the test architecture and how to add a manifest row
  when adding a feature — otherwise the manifest will not be maintained.

## Risks

- **Manifest maintenance burden.** Mitigated by the completeness meta-test (a
  forgotten row turns red) and by documenting the "add a row" step.
- **Two-toolchain hook fragility.** Mitigated by fail-safe skipping and the
  <10s budget.
- **Harness flakiness.** Mitigated by ephemeral ports + condition-based
  readiness; these are the specific failures already seen this session.
- **E2E cannot test inference quality** (GPU-blocked). Explicitly out of scope;
  the golden path tests the *pipe*, not the model output.
