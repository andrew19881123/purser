#!/bin/sh
# Purser agent entrypoint for the docker-compose demo stack.
#
# Responsibilities:
#   1. Expose the llama-server shared libraries via LD_LIBRARY_PATH so that the
#      agent can spawn llama-server as a subprocess (the .so files land in the
#      same volume directory as the binary, mounted at PURSER_LLAMACPP_BIN).
#   2. Auto-mint a join token when PURSER_JOIN_TOKEN is not set. Uses the
#      control plane's HTTP API — no auth required when OIDC is not configured
#      (the demo stack). The token is single-use and valid for 24 h.
#   3. exec the agent binary, inheriting the full environment.
#
# Env vars consumed here (all optional):
#   PURSER_JOIN_TOKEN         — skip auto-mint if already set.
#   PURSER_CP_HTTP_ADDR       — HTTP address of the control plane for token
#                               minting (default: http://control-plane:8080).
#   PURSER_LLAMACPP_BIN       — directory with llama-server + .so files
#                               (default: /llama-bin).
set -e

# 1. LD_LIBRARY_PATH so llama-server can load its shared libraries at runtime.
LLAMA_BIN_DIR="${PURSER_LLAMACPP_BIN:-/llama-bin}"
export LD_LIBRARY_PATH="${LLAMA_BIN_DIR}${LD_LIBRARY_PATH:+:$LD_LIBRARY_PATH}"

# 2. Auto-mint a join token if none is provided.
if [ -z "${PURSER_JOIN_TOKEN:-}" ]; then
    CP_HTTP="${PURSER_CP_HTTP_ADDR:-http://control-plane:8080}"
    echo "[agent-entrypoint] PURSER_JOIN_TOKEN not set — auto-minting from ${CP_HTTP}"

    # Wait for the control plane to become healthy (up to 120 s).
    HEALTH_URL="${CP_HTTP}/api/v1/cluster/health"
    echo "[agent-entrypoint] Waiting for control plane at ${HEALTH_URL}..."
    i=0
    while [ "$i" -lt 60 ]; do
        if curl -sf "${HEALTH_URL}" >/dev/null 2>&1; then
            echo "[agent-entrypoint] Control plane ready."
            break
        fi
        i=$((i + 1))
        sleep 2
    done

    # Mint a 24-hour single-use join token (no Bearer auth needed when OIDC is
    # disabled — the demo stack ships with OIDC unconfigured).
    RESPONSE="$(curl -sf -X POST "${CP_HTTP}/api/v1/join-token" \
        -H 'Content-Type: application/json' \
        -d '{"ttl_seconds":86400}' 2>/dev/null)" || true

    if [ -n "${RESPONSE}" ]; then
        # Extract "token" field from the JSON response without jq.
        TOKEN="$(printf '%s' "${RESPONSE}" | grep -o '"token":"[^"]*"' | cut -d'"' -f4)"
        if [ -n "${TOKEN}" ]; then
            export PURSER_JOIN_TOKEN="${TOKEN}"
            echo "[agent-entrypoint] Join token obtained."
        else
            echo "[agent-entrypoint] WARNING: could not parse token from response; proceeding without enrollment."
        fi
    else
        echo "[agent-entrypoint] WARNING: join-token request failed; proceeding without enrollment."
    fi
fi

exec /usr/local/bin/purser-agent "$@"
