#!/usr/bin/env bash
# Build stripped release binaries for a Purser v0.1.0 release and stage them,
# together with the packaging assets, under dist/.
#
# Produces (all stripped — the Rust release profile sets strip=true, debug=0;
# the Go build passes -ldflags "-s -w"):
#   dist/bin/purser-agent      (Rust)
#   dist/bin/purser-gateway    (Rust)
#   dist/bin/control-plane     (Go)
#   dist/packaging/...         (systemd/launchd/windows/env units + README)
#
# Everything runs through the PROJECT-LOCAL toolchain in .toolchain/ (nothing
# global). dist/ is git-ignored.
#
# Usage:
#   scripts/build-release.sh
#
# Env:
#   PURSER_ALLOW_LOW_DISK=1   proceed even if free space on / is under 1 GiB.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

# Project-local toolchain (cargo, go on PATH; caches under .toolchain/).
# shellcheck disable=SC1091
source "$ROOT/env.sh" >/dev/null 2>&1 || {
  echo "error: failed to source env.sh (run 'make setup' first)" >&2
  exit 1
}

DIST="$ROOT/dist"
BIN_OUT="$DIST/bin"

# --- Disk guard -------------------------------------------------------------
# A cold Rust release build can consume a few GB under rust/target. Refuse to
# start on a nearly-full disk unless explicitly overridden.
avail_kib="$(df -Pk "$ROOT" | awk 'NR==2 {print $4}')"
min_kib=$((1024 * 1024)) # 1 GiB
if [ "${avail_kib:-0}" -lt "$min_kib" ] && [ "${PURSER_ALLOW_LOW_DISK:-0}" != "1" ]; then
  echo "error: only $((avail_kib / 1024)) MiB free on $(df -Ph "$ROOT" | awk 'NR==2 {print $6}');" >&2
  echo "       a release build needs more headroom. Free space or re-run with" >&2
  echo "       PURSER_ALLOW_LOW_DISK=1 to override." >&2
  exit 1
fi

echo ">> staging dist/ at $DIST"
rm -rf "$DIST"
mkdir -p "$BIN_OUT"

# --- Rust release binaries (stripped via [profile.release] strip=true) ------
# CARGO_INCREMENTAL=0: incremental artifacts are useless for a release build
# and only bloat target/.
echo ">> cargo build --release -p purser-agent -p purser-gateway"
CARGO_INCREMENTAL=0 cargo build --release \
  --manifest-path "$ROOT/rust/Cargo.toml" \
  -p purser-agent -p purser-gateway

install -m 0755 "$ROOT/rust/target/release/purser-agent"   "$BIN_OUT/purser-agent"
install -m 0755 "$ROOT/rust/target/release/purser-gateway" "$BIN_OUT/purser-gateway"

# --- Go control-plane binary (-s -w strips symbol table + DWARF) ------------
echo ">> go build -ldflags \"-s -w\" -o dist/bin/control-plane (go/controlplane)"
( cd "$ROOT/go/controlplane" && go build -trimpath -ldflags "-s -w" -o "$BIN_OUT/control-plane" . )

# --- Stage packaging assets -------------------------------------------------
echo ">> copying packaging/ into dist/"
cp -R "$ROOT/packaging" "$DIST/packaging"

echo ""
echo "Release artifacts staged under dist/:"
( cd "$DIST" && find . -type f | sort | sed 's/^/  /' )
echo ""
echo "Binary sizes:"
ls -lh "$BIN_OUT" | awk 'NR>1 {print "  " $9 "\t" $5}'
echo ""
echo "Done. Binaries install to /usr/local/bin; packaging/ holds the service"
echo "units and env examples (see packaging/README.md)."
