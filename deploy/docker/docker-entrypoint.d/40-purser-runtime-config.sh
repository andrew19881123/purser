#!/bin/sh
# ---------------------------------------------------------------------------
# Generate the Purser UI runtime configuration (env.js) from environment
# variables at container start.
#
# Vite bakes build-time env into the bundle, so the API base URL CANNOT be
# changed after `docker build`. This script writes `env.js` (loaded before the
# app bundle; it sets window.__PURSER_CONFIG__) so operators configure the base
# URL per-deployment WITHOUT rebuilding the image.
#
# It runs from nginx:alpine's default entrypoint, which sources/execs every
# /docker-entrypoint.d/*.sh before starting nginx (we do NOT override CMD).
#
# Environment variables (all optional):
#   PURSER_API_BASE_URL      Control-plane management REST base. Default /api/v1
#                            (same-origin: let an ingress route /api/* to the
#                            control plane). Set an absolute URL only when the
#                            UI is served from a different origin than the API.
#   PURSER_GATEWAY_BASE_URL  OpenAI-compatible Gateway base. Default /v1.
#   PURSER_UI_MOCK           1/true/on/yes -> ship the in-memory mock (demos
#                            only). Default OFF: the UI talks to the real API.
#   PURSER_UI_ROOT           Static root to write into. Default the nginx root.
#
# Air-gap safe: writes one local file, performs no network access.
# ---------------------------------------------------------------------------
set -eu

ROOT="${PURSER_UI_ROOT:-/usr/share/nginx/html}"
OUT="$ROOT/env.js"

API_BASE_URL="${PURSER_API_BASE_URL:-/api/v1}"
GATEWAY_BASE_URL="${PURSER_GATEWAY_BASE_URL:-/v1}"
MOCK="${PURSER_UI_MOCK:-}"

# JSON-string escaping: backslashes first, then double quotes.
esc() { printf '%s' "$1" | sed -e 's/\\/\\\\/g' -e 's/"/\\"/g'; }

# Mock stays off unless explicitly enabled (case-insensitive truthy value).
MOCK_LINE=""
case "$(printf '%s' "$MOCK" | tr '[:upper:]' '[:lower:]')" in
  1 | true | on | yes) MOCK_LINE='  mock: true,' ;;
esac

{
  echo "// Generated at container start by 40-purser-runtime-config.sh."
  echo "// Overrides the baked-in defaults; edit container env, not this file."
  echo "window.__PURSER_CONFIG__ = {"
  echo "  apiBase: \"$(esc "$API_BASE_URL")\","
  echo "  gatewayBase: \"$(esc "$GATEWAY_BASE_URL")\","
  [ -n "$MOCK_LINE" ] && echo "$MOCK_LINE"
  echo "};"
} >"$OUT"

echo "purser-ui: wrote $OUT (apiBase=$API_BASE_URL gatewayBase=$GATEWAY_BASE_URL mock=${MOCK:-0})"
