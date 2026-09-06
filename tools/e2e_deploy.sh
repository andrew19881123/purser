#!/usr/bin/env bash
# E2E step 2: full zero-config vertical — enroll -> seed model -> deploy -> orchestrate -> route to gateway.
set -uo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
source "$ROOT/env.sh" >/dev/null 2>&1
CP_HTTP=127.0.0.1:18080; CP_GRPC=127.0.0.1:19443; GW=127.0.0.1:18081
DB=/tmp/purser-e2e.db; PKI=/tmp/purser-e2e-pki; LOGDIR=/tmp/purser-e2e
rm -f "$DB"; rm -rf "$PKI"; mkdir -p "$LOGDIR"
pids=()
cleanup(){ echo "--- cleanup ---"; for p in "${pids[@]:-}"; do kill "$p" 2>/dev/null; done;
  pkill -f 'bin/control-plane' 2>/dev/null; pkill -f 'purser-gateway' 2>/dev/null; pkill -f 'purser-agent' 2>/dev/null; true; }
trap cleanup EXIT

echo "== start gateway + control-plane =="
PURSER_GATEWAY_HOST=127.0.0.1 PURSER_GATEWAY_PORT=18081 PURSER_GATEWAY_INTERNAL_TOKEN=e2e PURSER_GATEWAY_API_KEYS=testkey \
  "$ROOT/rust/target/debug/purser-gateway" >"$LOGDIR/gw.log" 2>&1 & pids+=($!)
PURSER_ADDR=$CP_HTTP PURSER_GRPC_ADDR=$CP_GRPC PURSER_DB=$DB PURSER_PKI_DIR=$PKI \
  PURSER_GATEWAY_ADDR=http://$GW PURSER_GATEWAY_TOKEN=e2e \
  "$ROOT/bin/control-plane" >"$LOGDIR/cp.log" 2>&1 & pids+=($!)
curl --retry 30 --retry-delay 1 --retry-connrefused --max-time 5 -s "http://$CP_HTTP/api/v1/cluster/health" >/dev/null && echo "control-plane UP"
curl --retry 30 --retry-delay 1 --retry-connrefused --max-time 5 -s "http://$GW/healthz" >/dev/null && echo "gateway UP"

echo "== mint token + start agent =="
TOKEN=$(curl -s -X POST "http://$CP_HTTP/api/v1/join-token" -d '{}' | sed -E 's/.*"token"[[:space:]]*:[[:space:]]*"([^"]*)".*/\1/')
PURSER_AGENT_BIND=0.0.0.0:50151 PURSER_CONTROL_PLANE_ADDR="http://$CP_GRPC" \
  PURSER_CLUSTER_ID=default PURSER_JOIN_TOKEN="$TOKEN" \
  "$ROOT/rust/target/debug/purser-agent" >"$LOGDIR/agent.log" 2>&1 & pids+=($!)
for i in $(seq 1 20); do
  curl -s --max-time 5 "http://$CP_HTTP/api/v1/nodes" | grep -qE '"(node_id|id)"' && { echo "node enrolled after ${i}s"; break; }; sleep 1; done

echo "== seed a small model that fits (~7.5GB need vs ~10.9GB avail) =="
read -r -d '' MODEL <<'JSON'
{"modelId":"llama-8b","family":"llama","architecture":"llama","paramsTotalB":8,"paramsActiveB":8,
"layers":32,"hiddenSize":4096,"nKvHeads":8,"headDim":128,"attentionType":"ATTENTION_TYPE_GQA",
"contextMax":8192,"isMoe":false,
"quantizations":[{"name":"q4_k_m","sizeGb":4.5,"requiresFp4":false,"quality":0.9}],
"engine":"llamacpp"}
JSON
echo "create-model resp: $(curl -s -X POST "http://$CP_HTTP/api/v1/models" -H 'content-type: application/json' -d "$MODEL")"
echo "catalog /models: $(curl -s "http://$CP_HTTP/api/v1/models")"

echo "== deploy =="
DEP=$(curl -s -X POST "http://$CP_HTTP/api/v1/models/llama-8b/deploy" -H 'content-type: application/json' -d '{}')
echo "deploy resp: $DEP"

echo "== poll deployments for ACTIVE =="
for i in $(seq 1 20); do
  D=$(curl -s --max-time 5 "http://$CP_HTTP/api/v1/deployments")
  echo "[$i] $D"
  printf '%s' "$D" | grep -qiE 'ACTIVE' && { echo ">>> DEPLOYMENT ACTIVE"; break; }
  printf '%s' "$D" | grep -qiE 'FAILED' && { echo ">>> DEPLOYMENT FAILED"; break; }
  sleep 1
done

echo "== gateway routes (should now serve llama-8b) =="
curl -s -H "Authorization: Bearer testkey" "http://$GW/v1/models"; echo

echo ""; echo "===== agent.log (tail) ====="; tail -25 "$LOGDIR/agent.log"
echo ""; echo "===== control-plane.log (tail) ====="; tail -25 "$LOGDIR/cp.log"
echo ""; echo "===== gateway.log (tail) ====="; tail -15 "$LOGDIR/gw.log"
