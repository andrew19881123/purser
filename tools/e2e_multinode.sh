#!/usr/bin/env bash
# Multi-node E2E: a model too large for ONE node is split across TWO nodes.
#
# This is the multi-node counterpart of tools/e2e_full.sh. It enrolls TWO agents
# on the same host (each with its own advertised AgentService + inference address,
# so the resolver can tell them apart), seeds a model that does NOT fit on one
# node but DOES fit across two, deploys it, and drives a real chat through the
# gateway to the pipeline HOST. It then verifies the money-shot properties:
#
#   1. two nodes enrolled, each with its advertised agent/inference addr;
#   2. the plan has TWO assignments (a HOST + a WORKER) and pipeline_order len 2;
#   3. the deployment reaches ACTIVE;
#   4. the orchestrator started the WORKER before the HOST, on distinct
#      advertised addresses (50151 vs 50161);
#   5. the chat returns tokens (proxied to the host's advertised inference addr);
#   6. the plan's performance estimates are non-null (calibration path exercised).
#
# Join tokens are SINGLE-USE (fleet.go), so we mint one per agent.
set -uo pipefail
ROOT=/home/andrea/ideas/purser
source "$ROOT/env.sh" >/dev/null 2>&1

CP_HTTP=127.0.0.1:18080; CP_GRPC=127.0.0.1:19443; GW=127.0.0.1:18081
DB=/tmp/purser-mn.db; PKI=/tmp/purser-mn-pki; LOGDIR=/tmp/purser-mn
AGENT="$ROOT/rust/target/debug/purser-agent"
GATEWAY="$ROOT/rust/target/debug/purser-gateway"
CP="$ROOT/bin/control-plane"
rm -f "$DB"; rm -rf "$PKI"; mkdir -p "$LOGDIR"

pids=()
cleanup(){
  echo "--- cleanup ---"
  for p in "${pids[@]:-}"; do kill "$p" 2>/dev/null; done
  pkill -f 'bin/control-plane' 2>/dev/null
  pkill -f 'purser-gateway'   2>/dev/null
  pkill -f 'purser-agent'     2>/dev/null
  true
}
trap cleanup EXIT INT TERM

api="http://$CP_HTTP"

# --- tiny JSON helpers (jq is not available; python3 is) --------------------
jtoken(){ python3 -c 'import sys,json;print(json.load(sys.stdin)["token"])'; }
jnodecount(){ python3 -c 'import sys,json;print(len(json.load(sys.stdin).get("nodes",[])))'; }
jready(){ python3 -c 'import sys,json;print(json.load(sys.stdin).get("ready_nodes",0))'; }

# model_fit <modelId>: reads GET /models JSON on stdin, prints "<deployable 0/1> <node_count>"
model_fit(){
  python3 -c '
import sys,json
mid=sys.argv[1]; d=json.load(sys.stdin)
for m in d.get("models",[]):
    if m.get("id")==mid:
        f=m.get("fit") or {}
        print(int(bool(f.get("deployable"))), int(f.get("node_count") or 0)); break
else:
    print(0,0)
' "$1"
}

# ---------------------------------------------------------------------------
echo "== 1. start gateway + control-plane =="
PURSER_GATEWAY_HOST=127.0.0.1 PURSER_GATEWAY_PORT=18081 \
  PURSER_GATEWAY_INTERNAL_TOKEN=e2e PURSER_GATEWAY_API_KEYS=testkey \
  "$GATEWAY" >"$LOGDIR/gw.log" 2>&1 & pids+=($!)
PURSER_ADDR=$CP_HTTP PURSER_GRPC_ADDR=$CP_GRPC PURSER_DB=$DB PURSER_PKI_DIR=$PKI \
  PURSER_GATEWAY_ADDR=http://$GW PURSER_GATEWAY_TOKEN=e2e \
  "$CP" >"$LOGDIR/cp.log" 2>&1 & pids+=($!)
curl --retry 40 --retry-delay 1 --retry-connrefused --max-time 5 -s "$api/api/v1/cluster/health" >/dev/null && echo "control-plane UP"
curl --retry 40 --retry-delay 1 --retry-connrefused --max-time 5 -s "http://$GW/healthz"        >/dev/null && echo "gateway UP"

echo ""
echo "== 2. mint a single-use join token per agent + start 2 agents =="
TOK1=$(curl -s -X POST "$api/api/v1/join-token" -d '{}' | jtoken)
TOK2=$(curl -s -X POST "$api/api/v1/join-token" -d '{}' | jtoken)
echo "token#1=${TOK1:0:16}...  token#2=${TOK2:0:16}...  (single-use → one per agent)"

# agent 1 — AgentService :50151, inference :8000
PURSER_AGENT_BIND=0.0.0.0:50151 PURSER_INFERENCE_PORT=8000 \
  PURSER_AGENT_ADVERTISED_ADDR=127.0.0.1:50151 PURSER_INFERENCE_ADVERTISED_ADDR=127.0.0.1:8000 \
  PURSER_CONTROL_PLANE_ADDR="http://$CP_GRPC" PURSER_CLUSTER_ID=default PURSER_JOIN_TOKEN="$TOK1" \
  "$AGENT" >"$LOGDIR/agent1.log" 2>&1 & pids+=($!)
# agent 2 — AgentService :50161, inference :8001
PURSER_AGENT_BIND=0.0.0.0:50161 PURSER_INFERENCE_PORT=8001 \
  PURSER_AGENT_ADVERTISED_ADDR=127.0.0.1:50161 PURSER_INFERENCE_ADVERTISED_ADDR=127.0.0.1:8001 \
  PURSER_CONTROL_PLANE_ADDR="http://$CP_GRPC" PURSER_CLUSTER_ID=default PURSER_JOIN_TOKEN="$TOK2" \
  "$AGENT" >"$LOGDIR/agent2.log" 2>&1 & pids+=($!)

# wait for BOTH nodes to be present AND READY (planner only places on READY/RUNNING).
NODES_OK=0
for i in $(seq 1 60); do
  N=$(curl -s --max-time 5 "$api/api/v1/nodes" | jnodecount 2>/dev/null || echo 0)
  R=$(curl -s --max-time 5 "$api/api/v1/cluster/health" | jready 2>/dev/null || echo 0)
  if [ "$N" = "2" ] && [ "$R" = "2" ]; then echo "2 nodes enrolled and READY (${i}s)"; NODES_OK=1; break; fi
  sleep 1
done
echo "--- GET /api/v1/nodes (advertised addresses) ---"
curl -s "$api/api/v1/nodes" | python3 -c '
import sys,json
for n in json.load(sys.stdin).get("nodes",[]):
    print("  node=%s state=%s agent_addr=%s inference_addr=%s ram_gb=%.2f"
          % (n.get("id"), n.get("state"), n.get("advertised_agent_addr"),
             n.get("advertised_inference_addr"), n.get("ram_gb",0.0)))
'

echo ""
echo "== 3. find a model size that needs EXACTLY 2 nodes (probe), then seed big-48l =="
# The two nodes each expose ~10.5 GB useful RAM (frozen at Join time), so a model
# is too big for one node yet fits two only within a window. Probe throwaway
# model IDs to discover a size that yields node_count==2 (there is no delete-model
# endpoint), then seed the real "big-48l" with the winning size.
seed_model(){ # <modelId> <sizeGb>
  local mid="$1" sz="$2"
  cat <<JSON | curl -s -X POST "$api/api/v1/models" -H 'content-type: application/json' -d @- >/dev/null
{"modelId":"$mid","family":"test","architecture":"llama","paramsTotalB":30,"paramsActiveB":30,
"layers":48,"hiddenSize":6144,"nKvHeads":8,"headDim":128,"attentionType":"ATTENTION_TYPE_GQA",
"contextMax":8192,"isMoe":false,
"quantizations":[{"name":"q4_k_m","sizeGb":$sz,"requiresFp4":false,"quality":0.9}],"engine":"llamacpp"}
JSON
}

WINSZ=""
for SZ in 12 11 13 10 14 9 15 8 16; do
  PID="probe-${SZ}"
  seed_model "$PID" "$SZ"
  read -r DEP NC < <(curl -s "$api/api/v1/models" | model_fit "$PID")
  echo "  probe sizeGb=$SZ -> deployable=$DEP node_count=$NC"
  if [ "$DEP" = "1" ] && [ "$NC" = "2" ]; then WINSZ="$SZ"; break; fi
done

if [ -z "$WINSZ" ]; then
  echo "!! could not find a size that needs exactly 2 nodes; aborting"
  echo "--- cp.log tail ---"; tail -n 40 "$LOGDIR/cp.log"
  exit 1
fi
echo "chosen sizeGb=$WINSZ (too big for 1 node, fits across 2)"
seed_model "big-48l" "$WINSZ"
read -r DEP NC < <(curl -s "$api/api/v1/models" | model_fit "big-48l")
echo "big-48l fit: deployable=$DEP node_count=$NC"
echo "--- GET /api/v1/models (big-48l fit verdict) ---"
curl -s "$api/api/v1/models" | python3 -c '
import sys,json
for m in json.load(sys.stdin).get("models",[]):
    if m.get("id")=="big-48l":
        print(json.dumps(m.get("fit"), indent=2)); break
'

echo ""
echo "== 4. deploy big-48l (plan-less: control-plane plans from the live fleet) =="
DEPLOY_RESP=$(curl -s -X POST "$api/api/v1/models/big-48l/deploy" -d '{}')
echo "deploy response: $DEPLOY_RESP"
PLAN_ID=$(echo "$DEPLOY_RESP" | python3 -c 'import sys,json;print(json.load(sys.stdin).get("plan_id",""))' 2>/dev/null)
echo "plan_id=$PLAN_ID"

# poll until ACTIVE or FAILED
DEP_STATE=""
for i in $(seq 1 40); do
  DEP_STATE=$(curl -s "$api/api/v1/deployments" | python3 -c '
import sys,json
d=json.load(sys.stdin).get("deployments",[])
print(d[0]["state"] if d else "")' 2>/dev/null)
  case "$DEP_STATE" in
    *ACTIVE*) echo "deployment ACTIVE (${i}s)"; break;;
    *FAILED*) echo "deployment FAILED (${i}s)"; break;;
  esac
  sleep 1
done
echo "deployment state: $DEP_STATE"

echo ""
echo "== 5. plan detail (assignments + pipeline_order + estimated) =="
curl -s "$api/api/v1/plans/$PLAN_ID" | python3 -c '
import sys,json
row=json.load(sys.stdin); p=row.get("plan") or {}
asg=p.get("assignments") or []
order=p.get("pipelineOrder") or p.get("pipeline_order") or []
est=p.get("estimated") or {}
print("  quantization:", p.get("quantization"))
print("  assignments (%d):" % len(asg))
for a in asg:
    print("    node=%s role=%s layers=[%s..%s]" %
          (a.get("nodeId"), a.get("role"), a.get("layerStart",0), a.get("layerEnd",0)))
print("  pipeline_order (%d): %s" % (len(order), order))
print("  estimated:", json.dumps(est))
'

echo ""
echo "== 6. deployment detail (engine start order + advertised agent addrs) =="
curl -s "$api/api/v1/deployments" | python3 -c '
import sys,json
d=json.load(sys.stdin).get("deployments",[])
dep=d[0] if d else {}
det=dep.get("detail") or {}
print("  host_node_id:", det.get("host_node_id"))
print("  endpoint:", det.get("endpoint"))
print("  engines (start order, host is last):")
for e in det.get("engines",[]):
    print("    role=%-6s node=%s agent_addr=%s" % (e.get("role"), e.get("node_id"), e.get("agent_addr")))
'

echo ""
echo "== gateway route table =="
curl -s -H 'Authorization: Bearer testkey' "http://$GW/v1/models"; echo ""

echo ""
echo "===================================================="
echo "== THE MONEY SHOT: real chat through the gateway  =="
echo "==   (proxied to the pipeline HOST's inference)   =="
echo "===================================================="
echo "--- non-stream ---"
CHAT=$(curl -s -X POST "http://$GW/v1/chat/completions" -H 'Authorization: Bearer testkey' \
  -H 'content-type: application/json' \
  -d '{"model":"big-48l","messages":[{"role":"user","content":"Ciao Purser, sei distribuito su due nodi?"}],"stream":false}')
echo "$CHAT"
CHAT_OK=$(echo "$CHAT" | python3 -c '
import sys,json
try:
    d=json.load(sys.stdin)
    c=d["choices"][0]["message"]["content"]
    print(1 if c else 0)
except Exception:
    print(0)
' 2>/dev/null)
echo ""
echo "--- streaming (SSE, first lines) ---"
SSE=$(curl -s -N -X POST "http://$GW/v1/chat/completions" -H 'Authorization: Bearer testkey' \
  -H 'content-type: application/json' \
  -d '{"model":"big-48l","messages":[{"role":"user","content":"Ciao Purser"}],"stream":true}' --max-time 10)
echo "$SSE" | head -n 6
SSE_OK=0; echo "$SSE" | grep -q "data:" && SSE_OK=1

echo ""
echo "== logs (tails) =="
echo "--- control-plane (orchestration order) ---"
grep -E "worker engine ready|deployment active|deployment failed|start engine" "$LOGDIR/cp.log" | tail -n 12
echo "--- agent1 (:50151) start_engine ---"
grep -E "start_engine requested|engine .*ready|READY|inference" "$LOGDIR/agent1.log" | tail -n 6
echo "--- agent2 (:50161) start_engine ---"
grep -E "start_engine requested|engine .*ready|READY|inference" "$LOGDIR/agent2.log" | tail -n 6
echo "--- gateway (chat proxy) ---"
grep -E "route|chat|proxy|upstream|big-48l" "$LOGDIR/gw.log" | tail -n 8

echo ""
echo "===================================================="
echo "== VERDICT =="
echo "===================================================="
python3 - "$LOGDIR" "$DEP_STATE" "$CHAT_OK" "$SSE_OK" "$PLAN_ID" "$WINSZ" <<'PY'
import sys,json,subprocess,urllib.request
logdir, dep_state, chat_ok, sse_ok, plan_id, winsz = sys.argv[1:7]
api="http://127.0.0.1:18080"
def get(path):
    with urllib.request.urlopen(api+path, timeout=5) as r: return json.load(r)

ok=True
def check(name, cond, detail=""):
    global ok
    ok = ok and cond
    print(("  [PASS] " if cond else "  [FAIL] ")+name+(("  -- "+detail) if detail else ""))

# 1. two nodes with advertised addrs
nodes=get("/api/v1/nodes").get("nodes",[])
adv={n.get("advertised_agent_addr") for n in nodes}
check("1. two nodes enrolled with advertised addrs", len(nodes)==2 and adv=={"127.0.0.1:50151","127.0.0.1:50161"},
      "advertised_agent_addrs=%s" % sorted(a for a in adv if a))

# 2. plan: 2 assignments (HOST+WORKER) + pipeline_order len 2
p=get("/api/v1/plans/%s" % plan_id).get("plan",{})
asg=p.get("assignments",[]); roles=sorted(a.get("role") for a in asg)
order=p.get("pipelineOrder") or p.get("pipeline_order") or []
check("2. plan has 2 assignments (HOST+WORKER) and pipeline_order len 2",
      len(asg)==2 and roles==["ROLE_HOST","ROLE_WORKER"] and len(order)==2,
      "roles=%s pipeline_order_len=%d" % (roles, len(order)))

# 3. deployment ACTIVE
check("3. deployment ACTIVE", "ACTIVE" in dep_state, "state=%s" % dep_state)

# 4. worker started before host, on distinct advertised addrs
deps=get("/api/v1/deployments").get("deployments",[])
det=(deps[0].get("detail") if deps else {}) or {}
engines=det.get("engines",[])
eroles=[e.get("role") for e in engines]
eaddrs=[e.get("agent_addr") for e in engines]
worker_first = len(eroles)==2 and eroles[0]=="worker" and eroles[-1]=="host"
distinct = len(set(eaddrs))==2 and set(eaddrs)=={"127.0.0.1:50151","127.0.0.1:50161"}
# corroborate ordering from the control-plane log
cp=open(logdir+"/cp.log").read()
log_order = cp.find("worker engine ready")!=-1 and (cp.find("worker engine ready") < cp.find("deployment active"))
check("4. orchestrator started WORKER then HOST on distinct advertised addrs",
      worker_first and distinct and log_order,
      "engine order=%s addrs=%s log(worker<active)=%s" % (eroles, eaddrs, log_order))

# 5. chat returns tokens
check("5. chat returns tokens (non-stream) and SSE streams", chat_ok=="1" and sse_ok=="1",
      "non_stream_ok=%s sse_ok=%s" % (chat_ok, sse_ok))

# 6. estimates non-null
est=p.get("estimated",{})
nonzero = any(float(est.get(k,0) or 0)>0 for k in
              ("decodeTokSMin","decodeTokSMax","prefillTokSMin","prefillTokSMax"))
check("6. plan performance estimates are non-null", nonzero, "estimated=%s" % json.dumps(est))

print()
print("  OVERALL:", "PASS  (multi-node split deploy is real)" if ok else "FAIL")
sys.exit(0 if ok else 2)
PY
VERDICT=$?
echo ""
echo "verdict exit code: $VERDICT (0=PASS)"
exit $VERDICT
