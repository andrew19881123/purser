# Contributing to Purser

Thanks for your interest in contributing! Purser is a polyglot monorepo
(Rust + Go + TypeScript) unified by shared `.proto` contracts.

## Development setup

Everything is **project-local** — no global installs.

```bash
make setup       # installs Rust, Go, buf into .toolchain/
source ./env.sh  # cargo / go / buf on PATH
make build test  # verify a green baseline
```

## Running E2E tests locally

After `make build`, run the single-node end-to-end test (enroll → deploy → chat):

```bash
bash tools/e2e_full.sh
```

The script starts a control-plane, gateway, and agent in-process (no GPU required),
registers a model, deploys it, and exercises the full chat path through the gateway.
Logs land in `/tmp/purser-e2e/`.  A multi-node variant is available at
`tools/e2e_multinode.sh`.

## Repository layout

- `proto/` — the `purser.v1` gRPC contracts. **Source of truth.** Changing a
  contract means running `make gen` and updating both the Go and Rust sides.
- `rust/crates/` — `agent`, `engine-adapter`, `adapter-llamacpp`, `gateway`,
  `purser-proto` (generated).
- `go/` — `planner`, `controlplane`, `gen` (generated), tied together by `go.work`.
- `ui/` — React + Vite dashboard.
- `tools/` — dev/E2E scripts (`e2e_full.sh` runs the whole vertical).

## Conventions

- **Rust**: `cargo fmt` + `cargo clippy -D warnings` must pass.
- **Go**: `gofmt`, `go vet`, and `CGO_ENABLED=0 go test ./...` must pass
  (the SQLite driver is pure-Go on purpose — keep it CGO-free).
- **TypeScript**: `npm run typecheck` + `npm run build` must pass; the bundle
  must stay self-contained (no CDN/runtime external fetches — air-gap).
- Match the style and comment density of the surrounding code.
- Add tests. The Planner in particular is verified against a brute-force
  reference and property-based invariants — keep those green.

## Cutting a release

Releases are fully automated via the `release.yml` CI pipeline. The pipeline
runs on any `vX.Y.Z` tag push and produces:

- Stripped `linux/amd64` binaries for `purser-agent`, `purser-gateway`, and
  `control-plane` (tarball per binary).
- `.deb` and `.rpm` installer packages (via nfpm).
- `SHA256SUMS` for all release assets.
- Docker images pushed to GHCR:
  `ghcr.io/<owner>/purser-control-plane`, `purser-gateway`, `purser-ui`.
- Helm chart pushed to `oci://ghcr.io/<owner>/charts`.
- A GitHub Release with all of the above attached.

**Steps to cut a release:**

1. Update `packaging/nfpm/purser-agent.yaml` — set `version:` to the new
   version number (without the leading `v`).
2. Add a dated entry to `CHANGELOG.md` under the new version heading.
3. Commit, then create and push a signed, annotated tag:
   ```bash
   git tag -s vX.Y.Z -m "Release vX.Y.Z"
   git push origin vX.Y.Z
   ```
4. CI takes over — watch the **Release** workflow in the Actions tab.

To trigger a release manually (e.g. to re-run a failed publish without
re-tagging), use the **workflow_dispatch** trigger in the
[Actions tab](../../actions/workflows/release.yml) and supply the existing
`vX.Y.Z` tag as the input.

## Pull requests

1. Branch from `main`.
2. Keep changes focused; update `.proto` + both codegen sides together.
3. Ensure `make build` and `make test` are green.
4. **Sign off your commits** (Developer Certificate of Origin):
   `git commit -s`.
5. Open the PR against `main` and fill in the template.

By contributing, you agree that your contributions to the **core** are licensed
under the **MIT License**, and your contributions to the **`enterprise/`**
directory are licensed under the **Purser Enterprise License**. In both cases
you sign off your commits under the Developer Certificate of Origin
(`git commit -s`).

## Good first issues

Ready to contribute? Here are concrete, well-scoped tasks with links to the exact code:

1. **Add `GET /api/v1/models/{id}` endpoint** — today `list_models()` in the Python SDK fetches all models and filters client-side. Add the server endpoint in `go/controlplane/server/server.go` (follow `handleGetNode` as a template).

2. **Fix remaining gofmt drift** — run `gofmt -l go/` from repo root and fix any remaining formatting issues.

3. **Add test for SWIM disabled path** — `rust/crates/agent/src/swim.rs` test `swim_disabled_no_bind` exists but could cover more edge cases.

4. **Improve error messages in planner** — `go/planner/plan/plan.go` `FitError` messages are terse; add node-specific context (which node had insufficient memory).

5. **Document PURSER_RECONCILER_* env vars** — the reconciler config is configurable via env (v0.2 feature) but not in `website/docs/configuration/env-vars.md`.

Quick start for all of these: `make dev` starts a local control plane.
