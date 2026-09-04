#!/usr/bin/env bash
# Install the entire Purser toolchain PROJECT-LOCALLY into .toolchain/.
#
# Nothing is installed globally: $HOME and /usr/local are never touched.
# Safe to re-run (idempotent): existing tools are detected and skipped.
#
# Currently targets linux/amd64 (the Go tarball is arch-specific).
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TOOLCHAIN="$ROOT/.toolchain"
GO_VERSION="go1.27.1"
GO_ARCHIVE="${GO_VERSION}.linux-amd64.tar.gz"

mkdir -p "$TOOLCHAIN"

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
export PATH="$CARGO_HOME/bin:$GOROOT/bin:$GOBIN:$PATH"

# --- Rust (rustup + cargo), project-local ---------------------------------
if [ ! -x "$CARGO_HOME/bin/rustc" ]; then
  echo ">> Installing Rust (rustup, project-local) ..."
  curl --proto '=https' --tlsv1.2 -sSf https://sh.rustup.rs -o "$TOOLCHAIN/rustup-init.sh"
  sh "$TOOLCHAIN/rustup-init.sh" -y --no-modify-path --profile minimal --default-toolchain stable
else
  echo ">> Rust already present: $("$CARGO_HOME/bin/rustc" --version)"
fi
"$CARGO_HOME/bin/rustup" component add clippy rustfmt

# --- Go, project-local ----------------------------------------------------
if [ ! -x "$GOROOT/bin/go" ]; then
  echo ">> Installing Go $GO_VERSION (project-local) ..."
  curl -sSfL -o "$TOOLCHAIN/$GO_ARCHIVE" "https://go.dev/dl/$GO_ARCHIVE"
  rm -rf "$GOROOT"
  tar -C "$TOOLCHAIN" -xzf "$TOOLCHAIN/$GO_ARCHIVE"
else
  echo ">> Go already present: $("$GOROOT/bin/go" version)"
fi

# Disable Go telemetry so it never writes counter files. The mode file lands
# under the (project-local) XDG_CONFIG_HOME set above.
mkdir -p "$XDG_CONFIG_HOME"
go telemetry off || true

# --- buf + Go codegen plugins, project-local (into $GOBIN) ----------------
echo ">> Installing buf + protoc-gen-go + protoc-gen-go-grpc (project-local) ..."
go install github.com/bufbuild/buf/cmd/buf@latest
go install google.golang.org/protobuf/cmd/protoc-gen-go@latest
go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest

echo ""
echo ">> Toolchain ready under $TOOLCHAIN"
echo "   rustc: $("$CARGO_HOME/bin/rustc" --version)"
echo "   go:    $("$GOROOT/bin/go" version)"
echo "   buf:   $("$GOBIN/buf" --version)"
echo ""
echo "   Rust codegen uses a vendored protoc (crate protoc-bin-vendored),"
echo "   so no system protoc is required."
