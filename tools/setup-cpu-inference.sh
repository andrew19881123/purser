#!/usr/bin/env bash
# setup-cpu-inference.sh — Download llama.cpp binaries + a small test model for
# CPU-only inference with Purser.
#
# Usage:
#   ./tools/setup-cpu-inference.sh
#
# What it does:
#   1. Looks for llama-server and rpc-server in $PURSER_LLAMACPP_BIN, PATH, and
#      ~/.purser/bin/. If found, skips the binary download.
#   2. Downloads the latest pre-built llama.cpp release for the current platform
#      (linux/amd64, linux/arm64, darwin/arm64) to ~/.purser/bin/.
#   3. Downloads TinyLlama 1.1B Q4_K_M (0.7 GB) from HuggingFace to
#      ~/.purser/models/ if not already present.
#   4. Prints the export commands you need to run the agent.
#
# To use a custom model instead of TinyLlama, set PURSER_MODEL_URL:
#   PURSER_MODEL_URL=https://... ./tools/setup-cpu-inference.sh
#
# The script is idempotent: it skips any step whose output already exists.

set -euo pipefail

PURSER_BIN="${HOME}/.purser/bin"
PURSER_MODELS="${HOME}/.purser/models"
LLAMA_SERVER_BIN="${PURSER_BIN}/llama-server"
RPC_SERVER_BIN="${PURSER_BIN}/rpc-server"
DEFAULT_MODEL_FILENAME="tinyllama-1.1b-chat-v1.0.Q4_K_M.gguf"
DEFAULT_MODEL_PATH="${PURSER_MODELS}/${DEFAULT_MODEL_FILENAME}"
DEFAULT_MODEL_URL="https://huggingface.co/TheBloke/TinyLlama-1.1B-Chat-v1.0-GGUF/resolve/main/${DEFAULT_MODEL_FILENAME}"

log() { echo "[setup-cpu-inference] $*"; }
ok()  { echo "[setup-cpu-inference] OK  $*"; }
info(){ echo "[setup-cpu-inference]     $*"; }

# ── 1. Detect platform ────────────────────────────────────────────────────────

OS="$(uname -s | tr '[:upper:]' '[:lower:]')"
ARCH="$(uname -m)"

case "${OS}" in
  linux)  PLATFORM_OS="linux" ;;
  darwin) PLATFORM_OS="macos" ;;
  *)
    echo "ERROR: unsupported OS '${OS}'. Supported: linux, darwin." >&2
    exit 1
    ;;
esac

case "${ARCH}" in
  x86_64)  PLATFORM_ARCH="x64" ;;
  aarch64|arm64) PLATFORM_ARCH="arm64" ;;
  *)
    echo "ERROR: unsupported architecture '${ARCH}'. Supported: x86_64, aarch64/arm64." >&2
    exit 1
    ;;
esac

log "Platform: ${PLATFORM_OS}/${ARCH}"

# ── 2. Check for existing binaries ───────────────────────────────────────────

find_binary() {
  local name="$1"
  # Check explicit PURSER_LLAMACPP_BIN first.
  if [ -n "${PURSER_LLAMACPP_BIN:-}" ] && [ -x "${PURSER_LLAMACPP_BIN}/${name}" ]; then
    echo "${PURSER_LLAMACPP_BIN}/${name}"
    return 0
  fi
  # Check ~/.purser/bin/.
  if [ -x "${PURSER_BIN}/${name}" ]; then
    echo "${PURSER_BIN}/${name}"
    return 0
  fi
  # Check PATH.
  if command -v "${name}" >/dev/null 2>&1; then
    command -v "${name}"
    return 0
  fi
  return 1
}

LLAMA_SERVER_FOUND=$(find_binary llama-server || true)
RPC_SERVER_FOUND=$(find_binary rpc-server || true)

BINARIES_NEEDED=false
if [ -z "${LLAMA_SERVER_FOUND}" ] || [ -z "${RPC_SERVER_FOUND}" ]; then
  BINARIES_NEEDED=true
fi

# ── 3. Download llama.cpp binaries ───────────────────────────────────────────

if [ "${BINARIES_NEEDED}" = "true" ]; then
  log "llama-server / rpc-server not found — downloading latest release..."

  # Query the GitHub API for the latest release tag.
  LATEST_TAG="$(curl -fsSL "https://api.github.com/repos/ggerganov/llama.cpp/releases/latest" \
    | grep '"tag_name"' | head -1 | sed 's/.*"tag_name": "\(.*\)".*/\1/')"
  if [ -z "${LATEST_TAG}" ]; then
    echo "ERROR: could not determine latest llama.cpp release tag. Check your network." >&2
    exit 1
  fi
  log "Latest llama.cpp release: ${LATEST_TAG}"

  # Build the asset filename that GitHub Releases uses.
  # Pattern: llama-<tag>-bin-<OS>-<arch>-noavx.zip (CPU/no-GPU build)
  # For linux/amd64: llama-b5000-bin-linux-x64.zip (OpenBLAS CPU build)
  # For linux/arm64: llama-b5000-bin-linux-arm64.zip
  # For darwin/arm64: llama-b5000-bin-macos-arm64.zip
  TAG_NUM="${LATEST_TAG#b}"  # strip leading 'b' if present (e.g. b5000 → 5000)

  case "${PLATFORM_OS}-${PLATFORM_ARCH}" in
    linux-x64)
      ASSET_NAME="llama-${LATEST_TAG}-bin-ubuntu-x64.zip"
      ASSET_URL="https://github.com/ggerganov/llama.cpp/releases/download/${LATEST_TAG}/${ASSET_NAME}"
      ;;
    linux-arm64)
      ASSET_NAME="llama-${LATEST_TAG}-bin-ubuntu-arm64.zip"
      ASSET_URL="https://github.com/ggerganov/llama.cpp/releases/download/${LATEST_TAG}/${ASSET_NAME}"
      ;;
    macos-arm64)
      ASSET_NAME="llama-${LATEST_TAG}-bin-macos-arm64.zip"
      ASSET_URL="https://github.com/ggerganov/llama.cpp/releases/download/${LATEST_TAG}/${ASSET_NAME}"
      ;;
    *)
      echo "ERROR: no pre-built binary for ${PLATFORM_OS}/${PLATFORM_ARCH}." >&2
      echo "Build from source: https://github.com/ggerganov/llama.cpp#build" >&2
      exit 1
      ;;
  esac

  TMPDIR="$(mktemp -d)"
  trap 'rm -rf "${TMPDIR}"' EXIT

  log "Downloading ${ASSET_URL} ..."
  if ! curl -fL --progress-bar -o "${TMPDIR}/llama.zip" "${ASSET_URL}"; then
    echo "ERROR: download failed. Try manually from:" >&2
    echo "  https://github.com/ggerganov/llama.cpp/releases/latest" >&2
    exit 1
  fi

  log "Extracting to ${TMPDIR}/llama-extract/ ..."
  mkdir -p "${TMPDIR}/llama-extract"
  unzip -q "${TMPDIR}/llama.zip" -d "${TMPDIR}/llama-extract"

  mkdir -p "${PURSER_BIN}"

  # Extract llama-server and llama-rpc-server (the binary names vary by build).
  install_bin() {
    local name="$1"
    local dest="$2"
    local found
    found=$(find "${TMPDIR}/llama-extract" -name "${name}" -type f 2>/dev/null | head -1)
    if [ -z "${found}" ]; then
      return 1
    fi
    cp "${found}" "${dest}"
    chmod +x "${dest}"
    return 0
  }

  if ! install_bin "llama-server" "${LLAMA_SERVER_BIN}"; then
    # Fallback name used in some releases.
    install_bin "server" "${LLAMA_SERVER_BIN}" || {
      echo "ERROR: llama-server binary not found in the release archive." >&2
      exit 1
    }
  fi
  ok "llama-server → ${LLAMA_SERVER_BIN}"

  if ! install_bin "llama-rpc-server" "${RPC_SERVER_BIN}"; then
    install_bin "rpc-server" "${RPC_SERVER_BIN}" || {
      echo "WARN: rpc-server binary not found (not needed for single-node CPU inference)."
    }
  else
    ok "rpc-server → ${RPC_SERVER_BIN}"
  fi

  LLAMA_SERVER_FOUND="${LLAMA_SERVER_BIN}"
  RPC_SERVER_FOUND="${RPC_SERVER_BIN}"
else
  ok "llama-server found: ${LLAMA_SERVER_FOUND}"
  ok "rpc-server found:   ${RPC_SERVER_FOUND}"
fi

# ── 4. Download default model ─────────────────────────────────────────────────

MODEL_PATH="${DEFAULT_MODEL_PATH}"
if [ -n "${PURSER_MODEL_URL:-}" ]; then
  # Custom model override.
  MODEL_FILENAME="$(basename "${PURSER_MODEL_URL}")"
  MODEL_PATH="${PURSER_MODELS}/${MODEL_FILENAME}"
  MODEL_URL="${PURSER_MODEL_URL}"
else
  MODEL_URL="${DEFAULT_MODEL_URL}"
fi

mkdir -p "${PURSER_MODELS}"

if [ -f "${MODEL_PATH}" ]; then
  ok "Model already present: ${MODEL_PATH}"
else
  log "Downloading ${MODEL_FILENAME:-${DEFAULT_MODEL_FILENAME}} (~0.7 GB) ..."
  log "URL: ${MODEL_URL}"
  if ! curl -fL --progress-bar -o "${MODEL_PATH}.tmp" "${MODEL_URL}"; then
    rm -f "${MODEL_PATH}.tmp"
    echo "ERROR: model download failed." >&2
    exit 1
  fi
  mv "${MODEL_PATH}.tmp" "${MODEL_PATH}"
  ok "Model saved: ${MODEL_PATH}"
fi

# ── 5. Print next steps ───────────────────────────────────────────────────────

cat <<EOF

============================================================
  CPU inference setup complete!
============================================================

To run the Purser agent with CPU-only inference:

  export PURSER_LLAMACPP_BIN="${PURSER_BIN}"
  export PURSER_ENGINE_BACKEND=cpu
  export PURSER_CPU_THREADS=4          # optional: default = num_cpus/2
  export PURSER_CPU_CONTEXT_SIZE=2048  # optional: context window (tokens)

Register the model in the control plane:

  curl -s -X POST http://localhost:8080/api/v1/models/import/cpu \\
    -H 'Content-Type: application/json' \\
    -d '{
      "model_id":  "tinyllama-1.1b",
      "gguf_path": "${MODEL_PATH}",
      "context_max": 2048
    }' | jq .

Expected performance on modern hardware (rough guide):
  - TinyLlama 1.1B Q4_K_M:  4–12 tok/s on 4 CPU cores
  - Llama3 1B Q4_K_M:        4–10 tok/s on 4 CPU cores
  - Phi-3 Mini Q4_K_M:       2–6 tok/s on 4 CPU cores
  - Gemma2 2B Q4_K_M:        2–5 tok/s on 4 CPU cores

For multi-node CPU inference, see:
  website/docs/getting-started/cpu-inference.md

============================================================
EOF
