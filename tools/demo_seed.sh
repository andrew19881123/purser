#!/usr/bin/env bash
# Seed the `make demo` compose stack with a small mock model.
#
# After `make demo` the dashboard is up but the catalog is empty, so the first
# thing a new user tries — GET /v1/models, or a chat call — returns nothing and
# they have to hand-write a protojson ModelSpec to get anywhere. This script
# closes that gap in one command.
#
# Idempotent: re-running is safe (a duplicate registration is a 409, which is
# treated as success).
#
# IMPORTANT — what this script can and cannot achieve:
#   The compose stack ships NO agent (services are postgres, control-plane,
#   gateway, ui, proxy), and mock inference lives only in the agent binary
#   (rust/crates/agent/src/mock_inference.rs) — the gateway is a pure reverse
#   proxy with no inference code of its own. With zero enrolled nodes the
#   planner cannot place the model, so the deploy returns 422
#   model_does_not_fit and no chat completion is possible. This script seeds
#   the catalog, reports that state honestly, and prints the next step rather
#   than pretending a completion is coming.
#
# All URLs go through the single published port (proxy 3000:80); nginx
# path-routes /api/ to the control plane and /v1/ to the gateway. See
# deploy/docker/demo-nginx.conf.
#
# jq is not available in this environment; python3 is. Same convention as
# tools/e2e_multinode.sh.

set -euo pipefail

API="${PURSER_DEMO_API:-http://localhost:3000/api}"
GW="${PURSER_DEMO_GATEWAY:-http://localhost:3000/v1}"
KEY="${PURSER_DEMO_API_KEY:-demo-key-12345}"
MODEL_ID="${PURSER_DEMO_MODEL:-demo-mock-1b}"
WAIT_SECS="${PURSER_DEMO_WAIT:-60}"

BODY="$(mktemp)"
trap 'rm -f "$BODY"' EXIT

# --- tiny JSON helpers (jq is not available; python3 is) --------------------
jget() { # <key> — read one top-level key from $BODY, empty if absent
  python3 -c 'import sys,json
try: print(json.load(open(sys.argv[1])).get(sys.argv[2],"") or "")
except Exception: print("")' "$BODY" "$1" 2>/dev/null || true
}

jready() { # ready node count from GET /v1/cluster/health
  python3 -c 'import sys,json
try: print(json.load(open(sys.argv[1])).get("ready_nodes",0))
except Exception: print(0)' "$BODY" 2>/dev/null || echo 0
}

jdepstate() { # state of the newest deployment for $1, empty if none
  python3 -c 'import sys,json
try:
    d=[x for x in json.load(open(sys.argv[1])).get("deployments",[])
       if x.get("model_id")==sys.argv[2]]
    d.sort(key=lambda x: x.get("created_at",""))
    print(d[-1].get("state","") if d else "")
except Exception: print("")' "$BODY" "$1" 2>/dev/null || true
}

# --- HTTP helpers: body lands in $BODY, status code on stdout ---------------
# curl already prints "000" via %{http_code} when the connection itself fails,
# so the fallback must only cover curl emitting nothing at all — appending an
# unconditional `|| echo 000` would yield "000000".
http_get() {
  local code
  code="$(curl -sS -X GET "$1" -o "$BODY" -w '%{http_code}' --max-time 15 2>/dev/null)" || true
  echo "${code:-000}"
}

http_post() { # <url> [json-body]
  local code
  if [ $# -ge 2 ]; then
    code="$(curl -sS -X POST "$1" -H 'Content-Type: application/json' --data-binary "$2" \
      -o "$BODY" -w '%{http_code}' --max-time 30 2>/dev/null)" || true
  else
    code="$(curl -sS -X POST "$1" -o "$BODY" -w '%{http_code}' --max-time 30 2>/dev/null)" || true
  fi
  echo "${code:-000}"
}

die() { echo "" >&2; echo "!! $*" >&2; exit 1; }

# ---------------------------------------------------------------------------
# 1. Wait for the stack
# ---------------------------------------------------------------------------
echo "== 1. waiting for the demo stack at $API (up to ${WAIT_SECS}s) =="
UP=0
CODE=""
for i in $(seq 1 "$WAIT_SECS"); do
  CODE="$(http_get "$API/v1/nodes")"
  if [ "$CODE" = "200" ]; then
    echo "   control plane reachable (${i}s)"
    UP=1
    break
  fi
  sleep 1
done

if [ "$UP" != "1" ]; then
  {
    echo ""
    echo "!! the control plane did not answer at $API/v1/nodes within ${WAIT_SECS}s"
    echo "   (last curl status: ${CODE:-none})"
    echo ""
    echo "   Start the demo stack first:"
    echo "     make demo"
    echo ""
    echo "   Then check all five services are up:"
    echo "     docker compose ps"
  } >&2
  exit 1
fi

# How many nodes are READY decides whether a deploy can succeed at all.
READY=0
if [ "$(http_get "$API/v1/cluster/health")" = "200" ]; then
  READY="$(jready)"
fi
echo "   ready nodes: $READY"

# ---------------------------------------------------------------------------
# 2. Register the model (protojson purserv1.ModelSpec — lowerCamelCase)
# ---------------------------------------------------------------------------
# The geometry fields are what the planner sizes weights + KV cache from, so a
# complete spec is required for the model to be placeable. Kept deliberately
# tiny (1.1 B params, 0.7 GB q4_k_m) so it fits essentially any node.
SPEC='{"modelId":"'"$MODEL_ID"'","family":"demo","architecture":"llama",
"paramsTotalB":1.1,"paramsActiveB":1.1,
"layers":16,"hiddenSize":2048,"nKvHeads":4,"headDim":64,
"attentionType":"ATTENTION_TYPE_GQA","contextMax":4096,"isMoe":false,
"quantizations":[{"name":"q4_k_m","sizeGb":0.7,"requiresFp4":false,"quality":0.9}],
"engine":"mock"}'

echo ""
echo "== 2. registering model '$MODEL_ID' =="
CODE="$(http_post "$API/v1/models" "$SPEC")"
case "$CODE" in
  201) echo "   registered (201)" ;;
  409) echo "   already registered (409) — nothing to do" ;;
  400) die "control plane rejected the ModelSpec (400): $(jget message)" ;;
  *)   die "unexpected status $CODE registering the model: $(cat "$BODY")" ;;
esac

# ---------------------------------------------------------------------------
# 3. Deploy it
# ---------------------------------------------------------------------------
echo ""
echo "== 3. deploying '$MODEL_ID' (control plane plans from the live fleet) =="
CODE="$(http_post "$API/v1/models/$MODEL_ID/deploy" '{}')"
DEPLOY_OK=0
case "$CODE" in
  202)
    echo "   deploy accepted (202), deployment_id=$(jget deployment_id)"
    DEPLOY_OK=1
    ;;
  422)
    # Expected on a plain `make demo`: no agent, so nothing to place onto.
    echo "   deploy declined (422 model_does_not_fit)"
    echo "   reason: $(jget reason)"
    ;;
  501)
    echo "   deploy unavailable (501): no orchestrator configured on this control plane"
    ;;
  404) die "model '$MODEL_ID' not found (404) — registration did not take effect" ;;
  *)   die "unexpected status $CODE deploying the model: $(cat "$BODY")" ;;
esac

# ---------------------------------------------------------------------------
# 4. Poll to ACTIVE (only meaningful if the deploy was accepted)
# ---------------------------------------------------------------------------
STATE=""
if [ "$DEPLOY_OK" = "1" ]; then
  echo ""
  echo "== 4. waiting for the deployment to go ACTIVE =="
  for i in $(seq 1 "$WAIT_SECS"); do
    if [ "$(http_get "$API/v1/deployments")" = "200" ]; then
      STATE="$(jdepstate "$MODEL_ID")"
    fi
    case "$STATE" in
      *ACTIVE*) echo "   ACTIVE (${i}s)"; break ;;
      *FAILED*) echo "   FAILED (${i}s)"; break ;;
    esac
    sleep 1
  done
  [ -n "$STATE" ] && echo "   deployment state: $STATE"
fi

# ---------------------------------------------------------------------------
# 5. Tell the user exactly where they stand
# ---------------------------------------------------------------------------
echo ""
case "$STATE" in
  *ACTIVE*)
    echo "=================================================================="
    echo " Model '$MODEL_ID' is ACTIVE. Try a chat completion:"
    echo "=================================================================="
    echo ""
    echo "curl -sS $GW/chat/completions \\"
    echo "  -H 'Authorization: Bearer $KEY' \\"
    echo "  -H 'Content-Type: application/json' \\"
    echo "  -d '{\"model\":\"$MODEL_ID\",\"messages\":[{\"role\":\"user\",\"content\":\"Hello\"}],\"stream\":false}'"
    echo ""
    ;;
  *)
    echo "=================================================================="
    echo " Catalog seeded — but inference is NOT available yet."
    echo "=================================================================="
    echo ""
    echo " '$MODEL_ID' is registered and visible on the dashboard Catalog page"
    echo " (http://localhost:3000), but it has no active deployment, so:"
    echo ""
    echo "   GET  $GW/models            -> {\"object\":\"list\",\"data\":[]}"
    echo "   POST $GW/chat/completions  -> 503 \"model not available\""
    echo ""
    echo " Why: the compose stack runs no agent, and mock inference lives in the"
    echo " agent, not in the gateway. The gateway only serves models the control"
    echo " plane published a route for after an engine reported READY on a real"
    echo " node, so at least one enrolled READY node is required."
    echo " Ready nodes right now: $READY."
    echo ""
    echo " To get a real completion, enrol a node against the native dev stack:"
    echo ""
    echo "   make build            # produces ./bin/purser-agent"
    echo "   make dev              # control plane natively on :8080"
    echo "   make demo-agent       # enrols a mock agent (separate terminal)"
    echo ""
    echo " then re-run this script against that stack:"
    echo ""
    echo "   PURSER_DEMO_API=http://localhost:8080/api ./tools/demo_seed.sh"
    echo ""
    ;;
esac
