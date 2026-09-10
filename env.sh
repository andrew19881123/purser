# Purser — project-local toolchain environment.
#
#   Usage:  source ./env.sh     (run from the repository root)
#
# ABSOLUTE RULE: nothing is installed globally. Every toolchain and cache
# lives under .toolchain/. Sourcing this file never touches $HOME or
# /usr/local — it only prepends project-local paths to $PATH.
#
# Works under bash and zsh. If sourced from a plain POSIX shell, run it from
# the repository root (it falls back to $PWD to locate the project).

# --- Locate the repository root -------------------------------------------
_purser_guess=""
if [ -n "${BASH_SOURCE:-}" ]; then
  _purser_guess="${BASH_SOURCE[0]}"
elif [ -n "${ZSH_VERSION:-}" ]; then
  # zsh: %N expands to the path of the file currently being sourced.
  _purser_guess="${(%):-%N}"
fi
if [ -n "$_purser_guess" ] && [ -f "$_purser_guess" ]; then
  PURSER_ROOT="$(cd "$(dirname "$_purser_guess")" && pwd)"
else
  PURSER_ROOT="$(pwd)"
fi
unset _purser_guess

export PURSER_ROOT
export PURSER_TOOLCHAIN="$PURSER_ROOT/.toolchain"

# --- Rust (rustup + cargo) -------------------------------------------------
export RUSTUP_HOME="$PURSER_TOOLCHAIN/rustup"
export CARGO_HOME="$PURSER_TOOLCHAIN/cargo"

# --- Go --------------------------------------------------------------------
export GOROOT="$PURSER_TOOLCHAIN/go"
export GOPATH="$PURSER_TOOLCHAIN/gopath"
export GOBIN="$GOPATH/bin"
export GOMODCACHE="$GOPATH/pkg/mod"
export GOCACHE="$PURSER_TOOLCHAIN/gocache"
export GOENV="$PURSER_TOOLCHAIN/goenv"
# Never download a different Go toolchain from the network: stay project-local.
export GOTOOLCHAIN=local

# --- XDG redirection (keeps $HOME pristine) --------------------------------
# Several tools ignore the GO*/CARGO_* vars and instead use the XDG base dirs
# (which resolve under $HOME/.cache and $HOME/.config by default):
#   * Go telemetry writes counter files to <config>/go/telemetry
#   * buf caches modules/plugins/well-known-types under <cache>/buf
# Redirecting the XDG base dirs into .toolchain/ keeps every such write
# project-local. (Note: this affects XDG-aware tools in shells that source
# this file — intentional, for a hermetic build environment.)
export XDG_CONFIG_HOME="$PURSER_TOOLCHAIN/xdg/config"
export XDG_CACHE_HOME="$PURSER_TOOLCHAIN/xdg/cache"
export XDG_DATA_HOME="$PURSER_TOOLCHAIN/xdg/data"

# --- PATH (project-local binaries take precedence) -------------------------
export PATH="$PURSER_ROOT/.toolchain/bin:$CARGO_HOME/bin:$GOROOT/bin:$GOBIN:$PATH"

# --- Status report ---------------------------------------------------------
# This used to print "toolchain ready" unconditionally, which meant it said
# "ready" on a machine where .toolchain/ did not exist at all — a status
# message that cannot be false is not a status report. It now reports what is
# actually on PATH and names what is missing.
#
# Deliberately never exits or returns non-zero: this file is *sourced*, so an
# exit would terminate the caller's interactive shell, and a non-zero return
# would break the documented `source ./env.sh && <command>` idiom.
_purser_missing=""
for _purser_tool in go cargo buf helm mkdocs; do
  command -v "$_purser_tool" >/dev/null 2>&1 || _purser_missing="$_purser_missing $_purser_tool"
done

if [ -z "$_purser_missing" ]; then
  echo "purser: toolchain ready (root=$PURSER_ROOT)" 1>&2
elif [ ! -d "$PURSER_TOOLCHAIN" ]; then
  echo "purser: PATH set (root=$PURSER_ROOT) — but .toolchain/ does not exist." 1>&2
  echo "purser: nothing is installed. Run: make setup" 1>&2
else
  echo "purser: PATH set (root=$PURSER_ROOT) — incomplete toolchain." 1>&2
  echo "purser: missing:$_purser_missing" 1>&2
  echo "purser: run 'make setup' to install what is missing." 1>&2
fi
unset _purser_missing _purser_tool
