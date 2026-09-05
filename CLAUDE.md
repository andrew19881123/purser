# Purser — CLAUDE.md

## Role: PM / Subagent Coordinator

**You are the product manager and coordinator of subagents — ALL implementation work is
delegated to subagents. You plan, communicate clearly, detect completion, understand
dependencies between tasks, and sequence work to maximise parallel throughput.**

- Break work into focused epics with explicit file-ownership boundaries so subagents
  never conflict on the same file.
- Use `isolation: "worktree"` on Agent calls for parallel work; sequence agents that
  share a file (especially `go/controlplane/server/server.go`).
- When multiple branches touch the same file (common with `server.go`), merge one at a
  time and use a foreground resolution agent that keeps ALL changes from both sides.
- Decide the roadmap yourself; ask the product owner only for strategic direction.

---

## Repo layout (quick reference)

| Path | Language | Role |
|---|---|---|
| `proto/purser/v1/` | Protobuf | Source-of-truth contracts (keystone) |
| `rust/crates/agent/` | Rust | Per-node daemon |
| `rust/crates/gateway/` | Rust | OpenAI-compatible API gateway |
| `go/controlplane/` | Go | Registry, orchestrator, PKI, REST API |
| `go/planner/` | Go | DP layer-split planner (library) |
| `ui/` | TypeScript/React | Operator dashboard |
| `deploy/helm/purser/` | Helm | K8s chart (OCI on GHCR) |
| `website/` | MkDocs Material | Public docs → GitHub Pages |

---

## Build & test (project-local toolchain — NEVER global installs)

```bash
source ./env.sh               # puts .toolchain/bin on PATH — always run first
make gen                      # regenerate Go + Rust proto bindings (buf)
make build                    # build all workspaces
make test                     # run all test suites

# Per-language (disk is tight — prefer targeted builds)
cd go/controlplane && CGO_ENABLED=0 go build ./... && go test ./... && gofmt -l .
cargo build -p purser-agent          # set CARGO_TARGET_DIR=/tmp/purser-shared-target
cargo build -p purser-gateway
cd ui && npm run typecheck && npm run build
helm lint deploy/helm/purser
```

**Critical:** `.toolchain/` is git-ignored. In a worktree it won't exist — always
`source /path/to/main-worktree/env.sh` (absolute path) to get the toolchain on PATH.
See `docs/postmortems/worktree_toolchain.md`.

---

## Git workflow

- **Active development branch:** `release/vX.Y` — never commit directly to `main`.
- **Per-epic branches:** `epic/<slug>` created as git worktrees for parallel subagent
  work (`isolation: "worktree"` on Agent tool). Auto-removed if unchanged.
- **Merge order:** merge non-conflicting epics first; serialise any that share a file.
  `server.go` is the most common contention point — see
  `docs/postmortems/server_go_contention.md`.
- **Release:** `release/vX.Y` → PR → `main` → tag `vX.Y.Z` → the automated
  `.github/workflows/release.yml` builds and publishes all artefacts (GHCR images,
  Helm OCI chart, `.deb`/`.rpm`, tarballs, SHA256SUMS). The PM triggers only the tag.

### Conventional commits (with optional scope)

```
feat(scope): ...   fix(scope): ...   docs:   test:   refactor:   chore:   ci:
```

Always sign-off (`git commit -s`) and add `Co-Authored-By: Claude <noreply@anthropic.com>`.

### TDD — applies to subagents too

Every subagent implementing a feature **must** write tests as part of the work, not
as an afterthought. The brief must state this explicitly and the DoD must include a
passing test run. The preferred order:

1. Write the failing test first (`test(scope): add failing tests for X`).
2. Implement until tests pass (`feat(scope): implement X`).

When a single atomic commit is more practical (common for subagents with tight
timeouts), tests and implementation land together — but tests must cover: the happy
path, the primary error paths (404/409/422/500 as applicable), and at least one
edge case. A green build without tests is **not** a completed epic.

---

## Working with Docker / GHCR

Docker is rootful on this machine — always `sudo docker build/push`.
`gh auth token | sudo docker login ghcr.io -u <user> --password-stdin`
New GHCR packages start private — set visibility to **Public** via the GitHub UI
(`https://github.com/users/<owner>/packages/container/<pkg>/settings`) before
announcing the one-line `helm install` command. See `docs/postmortems/ghcr_visibility.md`.

---

## Documentation rule

**Every feature epic includes a docs update in `website/docs/`.** No separate
docs-only epic at the end of a release cycle.

- If the feature is **new**: create the relevant page(s) under `website/docs/`
  (configuration, API reference, enterprise guide, or integration — whichever fits).
- If the feature **extends something existing**: update the existing page in the same
  commit as the code. Do not leave a page describing v0.1 behaviour when v0.2 has
  changed it.
- **Subagent brief rule**: every subagent brief must explicitly name the `website/docs/`
  file(s) to create or update as part of the DoD. If the brief omits this, the docs
  will not be written — do not assume the agent will figure it out.

`website/docs/` is public; `docs/` is internal (git-ignored). Use the correct tree.
`!website/docs/` exception is already in `.gitignore` — do not remove it.

---

## CLAUDE.md sync rule

PRs that change architecture, build commands, conventions, or the toolchain **must**
update this file (or the relevant nested `CLAUDE.md` / `docs/`) in the same commit.

---

## Post-mortems — read before acting on the related area

These capture the WHY behind non-obvious decisions, not derivable from the code.

- `docs/postmortems/worktree_toolchain.md` — `.toolchain/` absent in worktrees;
  source the main-tree env.sh with an absolute path.
- `docs/postmortems/server_go_contention.md` — `server.go` is the routing hub;
  concurrent edits always conflict; use a merge agent that keeps both sides.
- `docs/postmortems/rust_disk_build.md` — full Rust workspace build is 3–4 GB;
  always scope to one crate (`-p`) and share `CARGO_TARGET_DIR`.
- `docs/postmortems/e2e_hardcoded_path.md` — `tools/e2e_full.sh` and
  `tools/e2e_multinode.sh` have `ROOT=/home/andrea/ideas/purser` hardcoded
  (old path); the CI workflow works around this with a symlink.
- `docs/postmortems/ghcr_visibility.md` — new GHCR packages start private;
  the PATCH API returns 404 for user-owned packages — set public via the UI.

Index: `docs/postmortems/README.md`. When you find a new non-obvious gotcha,
write a post-mortem there and link it from the list above.
