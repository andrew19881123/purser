#!/usr/bin/env bash
# Full zero-config E2E: enroll -> seed model -> deploy -> orchestrate -> route -> REAL CHAT through the gateway.
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

echo "== mint token + start agent (inference on :8000) =="
TOKEN=$(curl -s -X POST "http://$CP_HTTP/api/v1/join-token" -d '{}' | sed -E 's/.*"token"[[:space:]]*:[[:space:]]*"([^"]*)".*/\1/')
PURSER_AGENT_BIND=0.0.0.0:50151 PURSER_INFERENCE_PORT=8000 PURSER_CONTROL_PLANE_ADDR="http://$CP_GRPC" \
  PURSER_CLUSTER_ID=default PURSER_JOIN_TOKEN="$TOKEN" \
  "$ROOT/rust/target/debug/purser-agent" >"$LOGDIR/agent.log" 2>&1 & pids+=($!)
for i in $(seq 1 20); do curl -s --max-time 5 "http://$CP_HTTP/api/v1/nodes" | grep -qE '"(node_id|id)"' && { echo "node enrolled (${i}s)"; break; }; sleep 1; done

echo "== seed model + deploy =="
read -r -d '' MODEL <<'JSON'
{"modelId":"llama-8b","family":"llama","architecture":"llama","paramsTotalB":8,"paramsActiveB":8,
"layers":32,"hiddenSize":4096,"nKvHeads":8,"headDim":128,"attentionType":"ATTENTION_TYPE_GQA",
"contextMax":8192,"isMoe":false,
"quantizations":[{"name":"q4_k_m","sizeGb":4.5,"requiresFp4":false,"quality":0.9}],"engine":"llamacpp"}
JSON
curl -s -X POST "http://$CP_HTTP/api/v1/models" -H 'content-type: application/json' -d "$MODEL" >/dev/null
curl -s -X POST "http://$CP_HTTP/api/v1/models/llama-8b/deploy" -d '{}' >/dev/null
for i in $(seq 1 20); do
  curl -s --max-time 5 "http://$CP_HTTP/api/v1/deployments" | grep -qiE 'ACTIVE' && { echo "deployment ACTIVE (${i}s)"; break; }; sleep 1; done
echo "gateway /v1/models: $(curl -s -H 'Authorization: Bearer testkey' "http://$GW/v1/models")"

echo ""; echo "===================================================="
echo "== THE MONEY SHOT: real chat through the gateway =="
echo "===================================================="
echo "--- non-stream ---"
curl -s -X POST "http://$GW/v1/chat/completions" -H 'Authorization: Bearer testkey' -H 'content-type: application/json' \
  -d '{"model":"llama-8b","messages":[{"role":"user","content":"Ciao Purser, funzioni?"}],"stream":false}'
echo ""; echo "--- streaming (SSE) ---"
curl -s -N -X POST "http://$GW/v1/chat/completions" -H 'Authorization: Bearer testkey' -H 'content-type: application/json' \
  -d '{"model":"llama-8b","messages":[{"role":"user","content":"Ciao Purser"}],"stream":true}' --max-time 10
echo ""; echo "===================================================="
