# Post-mortem: the toolchain does not bootstrap on macOS/arm64

## Symptom
On a macOS checkout, `source ./env.sh` prints `purser: toolchain ready` and then
every build command fails:

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

## Workaround (what was actually done, project-local, nothing global)
`.toolchain/` is git-ignored, so populating it by hand is safe and invisible to git.

```bash
# Go — same version as go/controlplane/go.mod requires, correct arch
curl -sSfL -o /tmp/go.tar.gz https://go.dev/dl/go1.27.1.darwin-arm64.tar.gz
mkdir -p .toolchain && tar -C .toolchain -xzf /tmp/go.tar.gz

# helm
curl -sSfL -o /tmp/helm.tar.gz https://get.helm.sh/helm-v3.16.3-darwin-arm64.tar.gz
tar -xzf /tmp/helm.tar.gz -C /tmp
mkdir -p .toolchain/bin && cp /tmp/darwin-arm64/helm .toolchain/bin/helm

# mkdocs — project-local venv, from the pinned requirements file
python3 -m venv .toolchain/pyvenv
.toolchain/pyvenv/bin/pip install -r website/requirements.txt
```

`source ./env.sh` then finds `go`/`gofmt` (via `GOROOT/bin`) and `helm` (via
`.toolchain/bin`). `mkdocs` is **not** on `PATH` — invoke it by absolute path:
`.toolchain/pyvenv/bin/mkdocs build --strict`.

Still not installed by the above: Rust/`cargo`, `buf`, `nfpm`. Add them only when
an epic actually needs them (a full Rust build is 3–4 GB — see
`rust_disk_build.md`).

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

## Impact if missed
- Agents read "toolchain ready" as success, then report a build failure that is
  really a missing compiler — or worse, report the epic blocked.
- `make setup` on Apple Silicon appears to work and produces a `.toolchain/go`
  that cannot execute.
- The `unexpected EOF` module errors get misdiagnosed as a dependency or
  `go.sum` problem and "fixed" by editing `go.mod`, which is wrong and will be
  reverted by CI.
