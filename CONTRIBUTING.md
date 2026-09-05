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
