#!/usr/bin/env bash
# E2E step 1: real agent enrollment into the control-plane over gRPC.
set -uo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
source "$ROOT/env.sh" >/dev/null 2>&1
CP_HTTP=127.0.0.1:18080
CP_GRPC=127.0.0.1:19443
GW=127.0.0.1:18081
DB=/tmp/purser-e2e.db
PKI=/tmp/purser-e2e-pki
LOGDIR=/tmp/purser-e2e
rm -f "$DB"; rm -rf "$PKI"; mkdir -p "$LOGDIR"
pids=()
cleanup(){ echo "--- cleanup ---"; for p in "${pids[@]:-}"; do kill "$p" 2>/dev/null; done;
  pkill -f 'bin/control-plane' 2>/dev/null; pkill -f 'purser-gateway' 2>/dev/null; pkill -f 'purser-agent' 2>/dev/null; true; }
trap cleanup EXIT

echo "== start gateway =="
PURSER_GATEWAY_HOST=127.0.0.1 PURSER_GATEWAY_PORT=18081 PURSER_GATEWAY_INTERNAL_TOKEN=e2e PURSER_GATEWAY_API_KEYS=testkey \
  "$ROOT/rust/target/debug/purser-gateway" >"$LOGDIR/gw.log" 2>&1 & pids+=($!)

echo "== start control-plane =="
PURSER_ADDR=$CP_HTTP PURSER_GRPC_ADDR=$CP_GRPC PURSER_DB=$DB PURSER_PKI_DIR=$PKI \
  PURSER_GATEWAY_ADDR=http://$GW PURSER_GATEWAY_TOKEN=e2e \
  "$ROOT/bin/control-plane" >"$LOGDIR/cp.log" 2>&1 & pids+=($!)

curl --retry 30 --retry-delay 1 --retry-connrefused --max-time 5 -s "http://$CP_HTTP/api/v1/cluster/health" >/dev/null && echo "control-plane UP"
curl --retry 30 --retry-delay 1 --retry-connrefused --max-time 5 -s "http://$GW/healthz" >/dev/null && echo "gateway UP"

echo "== mint join token =="
TOKRESP=$(curl -s -X POST "http://$CP_HTTP/api/v1/join-token" -H 'content-type: application/json' -d '{}')
echo "response: $TOKRESP"
TOKEN=$(printf '%s' "$TOKRESP" | sed -E 's/.*"token"[[:space:]]*:[[:space:]]*"([^"]*)".*/\1/')
echo "token(head): ${TOKEN:0:24}..."

echo "== start agent (bind 0.0.0.0:50151) =="
PURSER_AGENT_BIND=0.0.0.0:50151 PURSER_CONTROL_PLANE_ADDR="http://$CP_GRPC" \
  PURSER_CLUSTER_ID=default PURSER_JOIN_TOKEN="$TOKEN" \
  "$ROOT/rust/target/debug/purser-agent" >"$LOGDIR/agent.log" 2>&1 & pids+=($!)

echo "== poll /api/v1/nodes for enrollment =="
FOUND=0
for i in $(seq 1 25); do
  NODES=$(curl -s --max-time 5 "http://$CP_HTTP/api/v1/nodes" 2>/dev/null)
  if printf '%s' "$NODES" | grep -qE '"(node_id|id)"'; then
    echo "NODES after ${i}s: $NODES"; FOUND=1; break
  fi
  sleep 1
done
[ "$FOUND" = 0 ] && echo "NO NODE ENROLLED after 25s"

echo ""; echo "===== agent.log (tail) ====="; tail -20 "$LOGDIR/agent.log"
echo ""; echo "===== control-plane.log (tail) ====="; tail -20 "$LOGDIR/cp.log"
