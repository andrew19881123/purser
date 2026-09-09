#!/usr/bin/env bash
# Install the Purser toolchain PROJECT-LOCALLY into .toolchain/.
#
# Nothing is installed globally: $HOME and /usr/local are never touched.
# Safe to re-run (idempotent): tools already present at the pinned version are
# detected and skipped.
#
# Supported platforms (see PLATFORM detection below):
#   darwin/arm64  darwin/amd64  linux/amd64  linux/arm64
# An unsupported OS/arch fails immediately with a message naming what was
# detected, rather than downloading an archive that cannot execute.
#
# Usage:
#   ./tools/setup-toolchain.sh              install everything
#   ./tools/setup-toolchain.sh --skip-rust  skip Rust (~1 GB; not needed for
#                                           Go-only or docs-only work)
#   ./tools/setup-toolchain.sh --dry-run    print the plan, download nothing
#
# NOTE: written for bash 3.2, which is what macOS ships. No associative arrays.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TOOLCHAIN="$ROOT/.toolchain"
BIN="$TOOLCHAIN/bin"
VENV="$TOOLCHAIN/pyvenv"

# --- Pinned versions ------------------------------------------------------
# Bumping a version means bumping its checksums below, in lockstep.
GO_VERSION="go1.27.1"
HELM_VERSION="v4.2.4"

DRY_RUN=0
SKIP_RUST=0

# --- Argument parsing -----------------------------------------------------
while [ $# -gt 0 ]; do
  case "$1" in
    --dry-run)   DRY_RUN=1 ;;
    --skip-rust) SKIP_RUST=1 ;;
    -h|--help)
      # Print the header comment block: every '#' line after the shebang, up to
      # the first line that is not a comment. Deriving the range this way means
      # editing the header cannot desynchronise --help from it.
      awk 'NR==1 {next} /^#/ {sub(/^# ?/, ""); print; next} {exit}' "${BASH_SOURCE[0]}"
      exit 0
      ;;
    *) echo "setup-toolchain: unknown argument '$1' (try --help)" >&2; exit 2 ;;
  esac
  shift
done

# --- Output helpers -------------------------------------------------------
log()  { echo ">> $*"; }
warn() { echo "!! $*" >&2; }
die()  { echo "xx $*" >&2; exit 1; }

# Accumulators for the closing summary. Space-separated strings, not arrays,
# so this stays bash 3.2 clean.
MANUAL_STEPS=""
add_manual() { MANUAL_STEPS="${MANUAL_STEPS}
   - $1"; }

# --- Platform detection ---------------------------------------------------
OS_RAW="$(uname -s)"
ARCH_RAW="$(uname -m)"
case "$OS_RAW" in
  Darwin) OS="darwin" ;;
  Linux)  OS="linux" ;;
  *) die "unsupported OS '$OS_RAW'. Supported: Darwin (macOS), Linux.
   Install the toolchain manually, or open an issue if this platform matters to you." ;;
esac
case "$ARCH_RAW" in
  arm64|aarch64) ARCH="arm64" ;;
  x86_64|amd64)  ARCH="amd64" ;;
  *) die "unsupported architecture '$ARCH_RAW' on $OS_RAW. Supported: arm64/aarch64, x86_64/amd64." ;;
esac
PLATFORM="${OS}-${ARCH}"

# --- Pinned checksums (sha256) -------------------------------------------
# Go: from https://go.dev/dl/?mode=json (the official release index).
# Pinned in-repo rather than fetched alongside the archive: a checksum served
# by the same host it validates adds nothing against a compromised origin. It
# also catches truncated downloads, which is not hypothetical here — see
# docs/postmortems/macos_toolchain_bootstrap.md for the proxy that silently
# truncates large archives.
go_sha256_for() {
  case "$1" in
    darwin-arm64) echo "ee215d57e0ec269c60cc9ceca68e6bda321ba9ee5afe24f4b0988703c2d87d12" ;;
    darwin-amd64) echo "8f8f52c6649542cf027bbc9b9c68d1ec042f9f34808a40413f0b8b3f66f3caa4" ;;
    linux-amd64)  echo "63d339f0da5ab53635a56f2490a7984dfe12dfcff22ad749f63edaf590168445" ;;
    linux-arm64)  echo "3450b45a3f9ee8568792736a5c5e70a1f2e9b36c35a8f74958c03e51d7d92bec" ;;
    *) echo "" ;;
  esac
}

# helm: from the published helm-<ver>-<platform>.tar.gz.sha256sum files.
helm_sha256_for() {
  case "$1" in
    darwin-arm64) echo "d747eb4e28bd2727173d15b759fa0a17822291ec09db7ced3d55af290a3661a2" ;;
    darwin-amd64) echo "6c163d687ca03c3b5c01928e53bbbcf9518278f47ce7a2f249a5a08e8bdaa2bc" ;;
    linux-amd64)  echo "c306b46f719b0a4da32d0f78ee21bf90ce8d602f15b22ab753f0674d1670a7f3" ;;
    linux-arm64)  echo "564de2191b881e9f71b5606b25345821ea1682f06ab90499d3ab22b530176da1" ;;
    *) echo "" ;;
  esac
}

# --- Checksum helper ------------------------------------------------------
# macOS ships `shasum` but not `sha256sum`; most Linux distros ship the
# reverse. Support both rather than assuming either.
sha256_of() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk '{print $1}'
  elif command -v shasum >/dev/null 2>&1; then
    shasum -a 256 "$1" | awk '{print $1}'
  else
    die "no sha256 tool found (looked for sha256sum and shasum); cannot verify downloads"
  fi
}

# fetch_verify <url> <dest> <expected_sha256>
# Downloads to a .part file, verifies, then renames into place. A failed or
# truncated download therefore never leaves a file that looks valid.
fetch_verify() {
  url="$1"; dest="$2"; want="$3"
  [ -n "$want" ] || die "no checksum pinned for $PLATFORM — refusing to install an unverified download"
  log "downloading $(basename "$dest")"
  curl -sSfL --retry 3 --retry-delay 2 -o "${dest}.part" "$url" \
    || die "download failed: $url"
  got="$(sha256_of "${dest}.part")"
  if [ "$got" != "$want" ]; then
    rm -f "${dest}.part"
    die "checksum mismatch for $url
   expected $want
   actual   $got
   A truncated download or a tampered archive both look like this. Re-run; if it
   persists, see docs/postmortems/macos_toolchain_bootstrap.md (proxy truncation)."
  fi
  mv "${dest}.part" "$dest"
}

# --- Preflight ------------------------------------------------------------
command -v curl >/dev/null 2>&1 || die "curl is required but not installed"
command -v tar  >/dev/null 2>&1 || die "tar is required but not installed"

log "platform: $PLATFORM (uname -s=$OS_RAW, uname -m=$ARCH_RAW)"
if [ "$DRY_RUN" = "1" ]; then
  log "DRY RUN — nothing will be downloaded, extracted, or installed."
fi

# Warn if the pinned Go differs from what the Go modules ask for. CI resolves
# Go via go-version-file: go/<module>/go.mod, so a drift here means the local
# toolchain stops matching CI.
GOMOD="$ROOT/go/controlplane/go.mod"
if [ -f "$GOMOD" ]; then
  want_go="$(awk '/^go [0-9]/{print "go"$2; exit}' "$GOMOD" 2>/dev/null || true)"
  if [ -n "$want_go" ] && [ "$want_go" != "$GO_VERSION" ]; then
    warn "pinned GO_VERSION=$GO_VERSION but $GOMOD asks for $want_go."
    warn "Update GO_VERSION and its four checksums in this script to match."
  fi
fi

# A dry run must leave the filesystem untouched, so it creates nothing.
if [ "$DRY_RUN" != "1" ]; then
  mkdir -p "$TOOLCHAIN" "$BIN"
fi

export RUSTUP_HOME="$TOOLCHAIN/rustup"
export CARGO_HOME="$TOOLCHAIN/cargo"
export GOROOT="$TOOLCHAIN/go"
export GOPATH="$TOOLCHAIN/gopath"
export GOBIN="$GOPATH/bin"
export GOCACHE="$TOOLCHAIN/gocache"
export GOENV="$TOOLCHAIN/goenv"
export GOTOOLCHAIN=local
# Keep XDG-based writes (Go telemetry, buf cache) project-local, never in $HOME.
export XDG_CONFIG_HOME="$TOOLCHAIN/xdg/config"
export XDG_CACHE_HOME="$TOOLCHAIN/xdg/cache"
export XDG_DATA_HOME="$TOOLCHAIN/xdg/data"
export PATH="$BIN:$CARGO_HOME/bin:$GOROOT/bin:$GOBIN:$PATH"

# --- Go -------------------------------------------------------------------
# Idempotency is version-aware: an existing but outdated GOROOT is replaced,
# where the old script skipped it and left the wrong Go in place.
go_installed_version=""
if [ -x "$GOROOT/bin/go" ]; then
  go_installed_version="$("$GOROOT/bin/go" env GOVERSION 2>/dev/null || true)"
fi
if [ "$go_installed_version" = "$GO_VERSION" ]; then
  log "Go already present: $GO_VERSION"
elif [ "$DRY_RUN" = "1" ]; then
  log "would install Go $GO_VERSION ($GO_VERSION.${PLATFORM}.tar.gz)"
  [ -n "$go_installed_version" ] && log "   (replacing $go_installed_version)"
else
  [ -n "$go_installed_version" ] && log "replacing Go $go_installed_version with $GO_VERSION"
  archive="${GO_VERSION}.${PLATFORM}.tar.gz"
  fetch_verify "https://go.dev/dl/$archive" "$TOOLCHAIN/$archive" "$(go_sha256_for "$PLATFORM")"
  # Extract to a staging dir and swap, so an interrupted extraction cannot
  # leave a half-populated GOROOT behind.
  stage="$TOOLCHAIN/.go-stage.$$"
  rm -rf "$stage"; mkdir -p "$stage"
  tar -C "$stage" -xzf "$TOOLCHAIN/$archive" || { rm -rf "$stage"; die "extract failed: $archive"; }
  rm -rf "$GOROOT"
  mv "$stage/go" "$GOROOT"
  rm -rf "$stage" "$TOOLCHAIN/$archive"
  log "Go installed: $("$GOROOT/bin/go" version)"
fi

# Disable Go telemetry so it never writes counter files. The mode file lands
# under the (project-local) XDG_CONFIG_HOME set above.
if [ "$DRY_RUN" != "1" ] && [ -x "$GOROOT/bin/go" ]; then
  mkdir -p "$XDG_CONFIG_HOME"
  "$GOROOT/bin/go" telemetry off >/dev/null 2>&1 || true
fi

# --- helm -----------------------------------------------------------------
# Required by the documented `helm lint deploy/helm/purser`. The previous
# script never installed it, so that command failed on every fresh checkout.
helm_installed_version=""
if [ -x "$BIN/helm" ]; then
  helm_installed_version="$("$BIN/helm" version --short 2>/dev/null | sed 's/+.*//' || true)"
fi
if [ "$helm_installed_version" = "$HELM_VERSION" ]; then
  log "helm already present: $HELM_VERSION"
elif [ "$DRY_RUN" = "1" ]; then
  log "would install helm $HELM_VERSION (helm-$HELM_VERSION-$PLATFORM.tar.gz)"
else
  archive="helm-${HELM_VERSION}-${PLATFORM}.tar.gz"
  fetch_verify "https://get.helm.sh/$archive" "$TOOLCHAIN/$archive" "$(helm_sha256_for "$PLATFORM")"
  stage="$TOOLCHAIN/.helm-stage.$$"
  rm -rf "$stage"; mkdir -p "$stage"
  tar -C "$stage" -xzf "$TOOLCHAIN/$archive" || { rm -rf "$stage"; die "extract failed: $archive"; }
  mv "$stage/$PLATFORM/helm" "$BIN/helm"
  chmod +x "$BIN/helm"
  rm -rf "$stage" "$TOOLCHAIN/$archive"
  log "helm installed: $("$BIN/helm" version --short 2>/dev/null || echo "$HELM_VERSION")"
fi

# --- mkdocs (project-local venv) -----------------------------------------
# Required by the documented docs build (`mkdocs build --strict`). Installed
# into a venv under .toolchain/ so no global pip install is needed. Only the
# mkdocs entry point is symlinked onto PATH — adding the whole venv bin/ would
# also shadow the user's python3, which this script has no business doing.
REQ="$ROOT/website/requirements.txt"
if [ ! -f "$REQ" ]; then
  warn "website/requirements.txt not found — skipping mkdocs"
elif ! command -v python3 >/dev/null 2>&1; then
  warn "python3 not found — skipping mkdocs (the docs build will not work)"
  add_manual "install python3, then re-run this script to get mkdocs"
elif [ -x "$VENV/bin/mkdocs" ] && [ -L "$BIN/mkdocs" ]; then
  log "mkdocs already present: $("$VENV/bin/mkdocs" --version 2>/dev/null | head -1)"
elif [ "$DRY_RUN" = "1" ]; then
  log "would create venv at $VENV and pip install -r website/requirements.txt"
else
  log "creating python venv for mkdocs"
  python3 -m venv "$VENV" || die "python3 -m venv failed"
  "$VENV/bin/python" -m pip install --quiet --upgrade pip || warn "pip self-upgrade failed (continuing)"
  if "$VENV/bin/pip" install --quiet -r "$REQ"; then
    ln -sf "$VENV/bin/mkdocs" "$BIN/mkdocs"
    log "mkdocs installed: $("$VENV/bin/mkdocs" --version 2>/dev/null | head -1)"
  else
    warn "pip install -r website/requirements.txt failed — mkdocs unavailable"
    add_manual "retry: $VENV/bin/pip install -r website/requirements.txt"
  fi
fi

# --- Rust (rustup, project-local) ----------------------------------------
# rustup's installer detects the host platform itself, so this path was never
# the macOS problem — it is kept as-is. RUSTUP_HOME/CARGO_HOME point into
# .toolchain/, so nothing lands in $HOME.
if [ "$SKIP_RUST" = "1" ]; then
  log "skipping Rust (--skip-rust)"
  add_manual "Rust was skipped: 'make build' needs cargo. Re-run without --skip-rust."
elif [ -x "$CARGO_HOME/bin/rustc" ]; then
  log "Rust already present: $("$CARGO_HOME/bin/rustc" --version)"
  if [ "$DRY_RUN" != "1" ]; then
    "$CARGO_HOME/bin/rustup" component add clippy rustfmt >/dev/null 2>&1 \
      || warn "could not add clippy/rustfmt components (offline?)"
  fi
elif [ "$DRY_RUN" = "1" ]; then
  log "would install Rust via rustup (minimal profile, stable) into $CARGO_HOME"
else
  log "installing Rust (rustup, project-local)"
  # No checksum pin is possible here: sh.rustup.rs is a rolling installer
  # script. rustup verifies the toolchain archives it then downloads.
  curl --proto '=https' --tlsv1.2 -sSf https://sh.rustup.rs -o "$TOOLCHAIN/rustup-init.sh" \
    || die "could not download rustup installer"
  sh "$TOOLCHAIN/rustup-init.sh" -y --no-modify-path --profile minimal --default-toolchain stable \
    || die "rustup install failed"
  rm -f "$TOOLCHAIN/rustup-init.sh"
  "$CARGO_HOME/bin/rustup" component add clippy rustfmt >/dev/null 2>&1 \
    || warn "could not add clippy/rustfmt components"
  log "Rust installed: $("$CARGO_HOME/bin/rustc" --version)"
fi

# --- buf + Go codegen plugins --------------------------------------------
# Installed with `go install` into $GOBIN, so they are built for the host
# platform automatically. Now skipped when already present, where the old
# script re-ran three network installs on every invocation.
#
# Behind a proxy that truncates module zips, the default GOPROXY fails with
# "unexpected EOF"; GOPROXY=direct fetches from the origin instead. We try the
# proxy first (it is faster and usually fine) and fall back.
go_install_tool() {
  tool_bin="$1"; tool_pkg="$2"
  if [ -x "$GOBIN/$tool_bin" ]; then
    log "$tool_bin already present"
    return 0
  fi
  if [ "$DRY_RUN" = "1" ]; then
    log "would go install $tool_pkg"
    return 0
  fi
  log "installing $tool_bin"
  if ! "$GOROOT/bin/go" install "$tool_pkg" 2>/dev/null; then
    warn "$tool_bin install failed via GOPROXY; retrying with GOPROXY=direct"
    GOPROXY=direct "$GOROOT/bin/go" install "$tool_pkg" \
      || { warn "$tool_bin install failed"; add_manual "retry: GOPROXY=direct go install $tool_pkg"; return 0; }
  fi
}

if [ "$DRY_RUN" = "1" ] || [ -x "$GOROOT/bin/go" ]; then
  # Versions are unpinned (@latest). Pinning them would be more reproducible,
  # but CI resolves buf through bufbuild/buf-setup-action rather than this
  # script, so a pin chosen here could silently diverge from the version CI
  # generates with. Left as a deliberate follow-up.
  go_install_tool buf                github.com/bufbuild/buf/cmd/buf@latest
  go_install_tool protoc-gen-go      google.golang.org/protobuf/cmd/protoc-gen-go@latest
  go_install_tool protoc-gen-go-grpc google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest
else
  warn "Go is not installed — skipping buf and codegen plugins"
fi

# --- Not installed by this script ----------------------------------------
# nfpm is only needed for `make package-agent` (.deb/.rpm), which the release
# pipeline does in CI. Reported rather than installed, to keep the bootstrap
# small — see the summary below.
if ! command -v nfpm >/dev/null 2>&1 && [ ! -x "$GOBIN/nfpm" ]; then
  add_manual "nfpm is absent — only needed for 'make package-agent'; CI does this for releases"
fi

# --- Summary --------------------------------------------------------------
if [ "$DRY_RUN" = "1" ]; then
  echo ""
  log "dry run complete — nothing was changed."
  exit 0
fi

echo ""
log "Toolchain under $TOOLCHAIN ($PLATFORM)"
report() {
  name="$1"; path="$2"; shift 2
  if [ -x "$path" ]; then
    printf '   %-18s %s\n' "$name" "$("$@" 2>/dev/null | head -1)"
  else
    printf '   %-18s MISSING\n' "$name"
  fi
}
report "go"      "$GOROOT/bin/go"          "$GOROOT/bin/go" version
report "rustc"   "$CARGO_HOME/bin/rustc"   "$CARGO_HOME/bin/rustc" --version
report "buf"     "$GOBIN/buf"              "$GOBIN/buf" --version
report "helm"    "$BIN/helm"               "$BIN/helm" version --short
report "mkdocs"  "$VENV/bin/mkdocs"        "$VENV/bin/mkdocs" --version

if [ -n "$MANUAL_STEPS" ]; then
  echo ""
  log "Remaining manual steps:$MANUAL_STEPS"
fi

echo ""
echo "   Next: source ./env.sh"
echo "   Rust codegen uses a vendored protoc (crate protoc-bin-vendored),"
echo "   so no system protoc is required."
