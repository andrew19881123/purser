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

echo "purser: toolchain ready (root=$PURSER_ROOT)" 1>&2
