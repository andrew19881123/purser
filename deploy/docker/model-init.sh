#!/bin/sh
# model-init: downloads TinyLlama 1.1B (Q4_K_M GGUF) and the llama.cpp
# llama-server binary on first run. Subsequent runs are no-ops when the files
# are already present.
#
# Runs as an alpine init container; writes to two named Docker volumes:
#   /models     — model weights (purser-models volume)
#   /llama-bin  — llama-server binary + shared libs (purser-llama-bin volume)
#
# Environment variables:
#   PURSER_SKIP_MODEL_DOWNLOAD=1  — exit 0 immediately (CI / pre-seeded volumes).
#
# Builds of llama.cpp:
#   Binary: llama-b10917-bin-ubuntu-x64.tar.gz (release b10917)
#   Provides: llama-server, ggml-rpc-server, all .so runtime libraries.
#   llama-server is dynamically linked; the Debian-based agent container supplies
#   the glibc ABI; LD_LIBRARY_PATH=/llama-bin is set by agent-entrypoint.sh.
#
# Model:
#   TinyLlama 1.1B Chat v1.0 Q4_K_M  (637 MB)
#   https://huggingface.co/TheBloke/TinyLlama-1.1B-Chat-v1.0-GGUF

set -e

MODEL_DIR=/models
LLAMA_DIR=/llama-bin
MODEL_FILE="${MODEL_DIR}/tinyllama-1.1b-chat-v1.0.Q4_K_M.gguf"
MODEL_URL="https://huggingface.co/TheBloke/TinyLlama-1.1B-Chat-v1.0-GGUF/resolve/main/tinyllama-1.1b-chat-v1.0.Q4_K_M.gguf"
LLAMA_TAR_URL="https://github.com/ggml-org/llama.cpp/releases/download/b10917/llama-b10917-bin-ubuntu-x64.tar.gz"
LLAMA_SERVER_BIN="${LLAMA_DIR}/llama-server"

# ── Skip flag ────────────────────────────────────────────────────────────────
if [ "${PURSER_SKIP_MODEL_DOWNLOAD:-0}" = "1" ]; then
    echo "[model-init] PURSER_SKIP_MODEL_DOWNLOAD=1 — skipping all downloads."
    exit 0
fi

mkdir -p "${MODEL_DIR}" "${LLAMA_DIR}"

# ── Model weights ─────────────────────────────────────────────────────────────
if [ -f "${MODEL_FILE}" ]; then
    echo "[model-init] Model already present: ${MODEL_FILE}"
else
    echo "[model-init] Downloading TinyLlama 1.1B Q4_K_M (637 MB)..."
    echo "[model-init]   from: ${MODEL_URL}"
    echo "[model-init]   to:   ${MODEL_FILE}"
    wget --show-progress -q -O "${MODEL_FILE}.tmp" "${MODEL_URL}"
    mv "${MODEL_FILE}.tmp" "${MODEL_FILE}"
    echo "[model-init] Model downloaded: ${MODEL_FILE}"
fi

# Create a model-id alias that matches the logical name used by demo-seed.sh
# ("tinyllama-1b"). This symlink is followed by llama-server via
# PURSER_LLAMACPP_MODEL_DIR=/models when the CP sends model_ref="tinyllama-1b".
ln -sf "tinyllama-1.1b-chat-v1.0.Q4_K_M.gguf" "${MODEL_DIR}/tinyllama-1b" 2>/dev/null || true
echo "[model-init] Symlink: ${MODEL_DIR}/tinyllama-1b -> tinyllama-1.1b-chat-v1.0.Q4_K_M.gguf"

# ── llama-server binary + shared libraries ────────────────────────────────────
if [ -f "${LLAMA_SERVER_BIN}" ]; then
    echo "[model-init] llama-server already present: ${LLAMA_SERVER_BIN}"
else
    echo "[model-init] Downloading llama.cpp release b10917..."
    echo "[model-init]   from: ${LLAMA_TAR_URL}"
    TMP_DIR="$(mktemp -d /tmp/llama-extract.XXXXXX)"
    wget -q -O "${TMP_DIR}/llama.tar.gz" "${LLAMA_TAR_URL}"

    echo "[model-init] Extracting..."
    tar -xzf "${TMP_DIR}/llama.tar.gz" -C "${TMP_DIR}"

    # The tarball layout varies across releases; find the files we need.
    # Copy llama-server (required for host inference).
    find "${TMP_DIR}" -name "llama-server" -type f | while read -r f; do
        cp -v "$f" "${LLAMA_DIR}/llama-server"
    done

    # Copy rpc-server / ggml-rpc-server (needed for multi-node workers; create
    # an alias so the adapter's DEFAULT_RPC_SERVER_BIN="rpc-server" is found).
    find "${TMP_DIR}" -name "*rpc-server" -type f | while read -r f; do
        name="$(basename "$f")"
        cp -v "$f" "${LLAMA_DIR}/${name}"
        # Ensure "rpc-server" exists regardless of the upstream binary name.
        if [ "${name}" != "rpc-server" ] && [ ! -f "${LLAMA_DIR}/rpc-server" ]; then
            ln -sf "${name}" "${LLAMA_DIR}/rpc-server"
            echo "[model-init] Alias: ${LLAMA_DIR}/rpc-server -> ${name}"
        fi
    done

    # Copy all shared libraries (.so*) so llama-server can be loaded.
    find "${TMP_DIR}" -name "*.so*" -type f | while read -r f; do
        cp -v "$f" "${LLAMA_DIR}/"
    done

    chmod +x "${LLAMA_DIR}/llama-server" 2>/dev/null || true

    rm -rf "${TMP_DIR}"
    echo "[model-init] llama-server ready: ${LLAMA_SERVER_BIN}"
fi

echo "[model-init] Done. Contents of ${LLAMA_DIR}:"
ls "${LLAMA_DIR}" | head -10

echo "[model-init] Contents of ${MODEL_DIR}:"
ls "${MODEL_DIR}"
