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
