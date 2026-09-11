# Post-mortem: the toolchain did not bootstrap on macOS/arm64

> **Status: FIXED** on branch `epic/toolchain-macos`.
> `tools/setup-toolchain.sh` now detects the platform (macOS and Linux, on both
> `arm64` and `amd64`), installs helm and mkdocs alongside Go/Rust/buf, and
> verifies pinned SHA256 checksums before extracting. `env.sh` no longer claims
> readiness unconditionally. **What is still manual is listed under
> "Still manual" below.** The history is kept because the two root causes explain
> why this stayed invisible for so long — but do not follow the old workaround,
> it is obsolete.

## Symptom (historical — before the fix)
On a macOS checkout, `source ./env.sh` printed `purser: toolchain ready` and then
every build command failed:

```
$ source ./env.sh
purser: toolchain ready (root=/Users/.../poc/purser)
$ go build ./...
zsh: command not found: go
```

`gofmt`, `cargo`, `buf`, `helm`, and `mkdocs` are all absent too, and
`.toolchain/` does not exist at all.

## Root cause — two separate problems

### 1. `env.sh` cannot fail
`env.sh` only prepends paths to `$PATH`; it never checks that anything is
installed. It prints "toolchain ready" unconditionally. The success message is
therefore **not** evidence that the toolchain exists — it only means the file was
sourced. Note this also makes `docs/postmortems/worktree_toolchain.md` misleading
on a fresh machine: it assumes `.toolchain/` exists in the main working tree and
is merely missing from worktrees. On a fresh macOS clone it exists nowhere.

### 2. `make setup` downloads Linux binaries
`tools/setup-toolchain.sh` hardcodes the architecture:

```bash
GO_VERSION="go1.27.1"
GO_ARCHIVE="${GO_VERSION}.linux-amd64.tar.gz"
```

The script's own header admits it: *"Currently targets linux/amd64 (the Go tarball
is arch-specific)."* On Apple Silicon (`uname -m` → `arm64`) it fetches an
unexecutable Linux amd64 tarball. It also never installs `helm` or `mkdocs` at
all, despite both being required by the documented verification commands in
`CLAUDE.md`.

## Fix applied
`make setup` now works on macOS. Just run it:

```bash
make setup            # Go, Rust, buf, helm, mkdocs — correct platform, checksummed
make setup --dry-run  # or: ./tools/setup-toolchain.sh --dry-run to see the plan
source ./env.sh
```

What changed:

- **Platform detection** from `uname -s`/`uname -m`, covering `darwin-arm64`,
  `darwin-amd64`, `linux-amd64`, `linux-arm64`. Anything else exits non-zero
  naming what was detected, instead of fetching an archive that cannot run.
  This also un-breaks the devcontainer on Apple Silicon hosts, where
  `ubuntu-24.04` runs as linux/arm64 and the old hardcoded amd64 tarball failed.
- **helm and mkdocs are installed**, so the documented `helm lint` and
  `mkdocs build --strict` work from a fresh checkout. Only the `mkdocs` entry
  point is symlinked into `.toolchain/bin/` — putting the whole venv `bin/` on
  `PATH` would shadow the user's `python3`.
- **Checksums.** Go and helm are pinned to exact versions with in-repo SHA256
  values verified before extraction. Pinned in-repo deliberately: a checksum
  fetched from the same host it validates adds nothing against a compromised
  origin, and this also catches the proxy truncation described below.
- **Atomic installs.** Archives download to `.part`, are verified, then renamed;
  tarballs extract to a staging dir and are moved into place. An interrupted or
  truncated run cannot leave a half-populated `GOROOT`.
- **Version-aware idempotency.** A `GOROOT` at the wrong version is now replaced;
  previously any existing `go` binary was accepted and the wrong Go stayed.
- **`--skip-rust`** for Go-only or docs-only work (Rust is ~1 GB, and a full
  workspace build is 3–4 GB — see `rust_disk_build.md`).

`env.sh` was also fixed: it reports which tools are actually resolvable and names
what is missing, instead of printing "toolchain ready" unconditionally. It still
always returns 0 — it is *sourced*, so exiting would kill the caller's shell and a
non-zero return would break the documented `source ./env.sh && <command>` idiom.

## Still manual
The script deliberately does not install these, and says so in its closing summary:

- **python3** — needed to build the mkdocs venv. Absent means no docs build; the
  script warns and continues rather than failing the whole bootstrap.
- **nfpm** — only for `make package-agent` (`.deb`/`.rpm`); releases do this in CI.
- **Node/npm** — for `ui/`.
- **buf and the protoc plugins are unpinned** (`@latest`). Pinning would be more
  reproducible, but CI resolves buf through `bufbuild/buf-setup-action`, not this
  script, so a pin chosen here could silently diverge from the version CI
  generates with. Deliberate follow-up, not an oversight.
- **No checksum is pinned for the rustup installer** — `sh.rustup.rs` is a rolling
  script. rustup verifies the toolchain archives it subsequently downloads.
- **mkdocs is not hash-pinned**: `website/requirements.txt` uses ranges
  (`mkdocs-material>=9.5`), so `pip --require-hashes` is not possible without a
  lock file. Adding one would close this gap.
- **CI's helm is unpinned.** `.github/workflows/release.yml` installs helm via
  `azure/setup-helm` with no `version:` input, and that action's default is
  `latest`. So the chart is packaged and pushed to the GHCR OCI registry with
  whatever helm is current on the day of the release — in a workflow that
  otherwise pins every action by commit SHA, and for a project that ships SLSA3
  provenance and cosign attestations for its own artefacts. `HELM_VERSION` in
  `tools/setup-toolchain.sh` is pinned to match what that resolves to today
  (v4.2.4), but the two will drift apart the next time helm ships a release.
  Fix belongs in `release.yml` (`with: version: v4.2.4`), not here.

## Corporate proxy breaks `go mod download`
Behind the Unipol proxy (`HTTP_PROXY=http://proxyu.ha.servizi.gr-u.it:80`) the
larger module zips are truncated by the intermediary:

```
github.com/xuri/excelize/v2@v2.11.0: read ".../v2.11.0.zip": unexpected EOF
github.com/jung-kurt/gofpdf@v1.16.2: read ".../v1.16.2.zip": unexpected EOF
github.com/stretchr/testify@v1.12.1: Get ".../v1.12.1.zip": Proxy Authentication Required
```

Small modules succeed, so the failure looks random and is easy to misread as a
broken `go.sum`. It is neither — retrying `proxy.golang.org` fails identically
every time. **Fix: fetch the affected modules from origin instead of the module
proxy.**

```bash
GOPROXY=direct go mod download github.com/xuri/excelize/v2 github.com/jung-kurt/gofpdf github.com/stretchr/testify
```

Once cached in `GOMODCACHE` (`.toolchain/gopath/pkg/mod`), subsequent ordinary
`go build` / `go test` runs need no special flags.

This one is **still live** — it is a property of the network, not of the repo. The
setup script now handles it for its own `go install` calls (it retries with
`GOPROXY=direct` automatically), but an ordinary `go build` in a module with a
large uncached dependency will still fail this way, and needs the command above.

## Why this was invisible (the transferable lesson)
Both root causes were failures that *looked* like successes. `env.sh` printed a
status that could not be false, and `make setup` fetched an archive whose name
nobody checked against the host. Neither produced an error at the point of the
mistake; both surfaced later as a confusing "command not found" attributed to the
wrong cause. When adding a status message, ask what input would make it print
something different — if there is none, it is decoration, not a status.

## Impact if missed (now mostly historical)
- Before the fix: agents read "toolchain ready" as success and then reported a
  build failure that was really a missing compiler — or reported the epic blocked.
- Before the fix: `make setup` on Apple Silicon appeared to work and produced a
  `.toolchain/go` that could not execute.
- **Still current:** the `unexpected EOF` module errors get misdiagnosed as a
  dependency or `go.sum` problem and "fixed" by editing `go.mod`, which is wrong
  and will be reverted by CI. Use `GOPROXY=direct` instead.
