#!/usr/bin/env bash
# Seed the Purser demo stack with a model and optional real-inference deployment.
#
# Default profile (docker compose up):
#   Registers demo-mock-1b in the catalog.  No agent is enrolled so deployment
#   returns 422 (model_does_not_fit) — inference is not available.
#
# Full profile (docker compose --profile full up + this script):
#   Also registers and deploys tinyllama-1b.  When the --profile full agent is
#   READY the deploy goes ACTIVE and real chat completions work.
#
# Usage:
#   make demo-seed                          # default — seeds catalog only
#   PURSER_FULL_INFERENCE=1 make demo-seed  # also deploys TinyLlama 1.1B
#
# Idempotent: re-running is safe (duplicate registration → 409 = ok).
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
# Set PURSER_FULL_INFERENCE=1 to also register + deploy the real TinyLlama model.
FULL_INFERENCE="${PURSER_FULL_INFERENCE:-0}"

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
# 2. Register the mock model (protojson purserv1.ModelSpec — lowerCamelCase)
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
# 2b. Full-inference path: register TinyLlama 1.1B (real llamacpp model)
# ---------------------------------------------------------------------------
TINYLLAMA_ID="tinyllama-1b"
TINYLLAMA_STATE=""
if [ "$FULL_INFERENCE" = "1" ]; then
  TLSPEC='{"modelId":"'"$TINYLLAMA_ID"'","family":"llama","architecture":"llama",
"paramsTotalB":1.1,"paramsActiveB":1.1,
"layers":22,"hiddenSize":2048,"nKvHeads":4,"headDim":64,
"attentionType":"ATTENTION_TYPE_GQA","contextMax":2048,"isMoe":false,
"quantizations":[{"name":"q4_k_m","sizeGb":0.637,"requiresFp4":false,"quality":0.85}],
"engine":"llamacpp"}'

  echo ""
  echo "== 2b. registering real model '$TINYLLAMA_ID' (TinyLlama 1.1B Q4_K_M) =="
  CODE="$(http_post "$API/v1/models" "$TLSPEC")"
  case "$CODE" in
    201) echo "   registered (201)" ;;
    409) echo "   already registered (409) — nothing to do" ;;
    400) die "control plane rejected TinyLlama ModelSpec (400): $(jget message)" ;;
    *)   die "unexpected status $CODE registering TinyLlama: $(cat "$BODY")" ;;
  esac

  # Wait up to WAIT_SECS for at least one agent node to be READY.
  echo ""
  echo "== 2c. waiting for an enrolled agent node to be READY =="
  for i in $(seq 1 "$WAIT_SECS"); do
    if [ "$(http_get "$API/v1/cluster/health")" = "200" ]; then
      READY="$(jready)"
    fi
    if [ "$READY" -ge 1 ] 2>/dev/null; then
      echo "   node READY (${i}s, ready_nodes=${READY})"
      break
    fi
    sleep 2
  done

  if [ "$READY" -lt 1 ] 2>/dev/null; then
    echo "   WARNING: no READY node after ${WAIT_SECS}s — deploy may fail (422)."
    echo "   Is the agent service running? Check: docker compose --profile full ps"
  fi
fi

# ---------------------------------------------------------------------------
# 3. Deploy the mock model
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
    # Expected on plain `make demo` (no agent): nothing to place onto.
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
# 3b. Full-inference path: deploy TinyLlama 1.1B
# ---------------------------------------------------------------------------
TL_DEPLOY_OK=0
if [ "$FULL_INFERENCE" = "1" ] && [ "$READY" -ge 1 ] 2>/dev/null; then
  echo ""
  echo "== 3b. deploying '$TINYLLAMA_ID' onto the enrolled agent =="
  CODE="$(http_post "$API/v1/models/$TINYLLAMA_ID/deploy" '{}')"
  case "$CODE" in
    202)
      echo "   deploy accepted (202), deployment_id=$(jget deployment_id)"
      TL_DEPLOY_OK=1
      ;;
    422)
      echo "   deploy declined (422): $(jget reason)"
      ;;
    501)
      echo "   deploy unavailable (501): no orchestrator configured"
      ;;
    404) die "model '$TINYLLAMA_ID' not found (404)" ;;
    *)   die "unexpected status $CODE deploying TinyLlama: $(cat "$BODY")" ;;
  esac
fi

# ---------------------------------------------------------------------------
# 4. Poll mock model deployment to ACTIVE
# ---------------------------------------------------------------------------
STATE=""
if [ "$DEPLOY_OK" = "1" ]; then
  echo ""
  echo "== 4. waiting for '$MODEL_ID' deployment to go ACTIVE =="
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
# 4b. Poll TinyLlama deployment to ACTIVE (full-inference path only)
# ---------------------------------------------------------------------------
if [ "$TL_DEPLOY_OK" = "1" ]; then
  echo ""
  echo "== 4b. waiting for '$TINYLLAMA_ID' deployment to go ACTIVE =="
  # TinyLlama takes longer: llama-server must load the model weights.
  TL_WAIT=$((WAIT_SECS * 3))
  for i in $(seq 1 "$TL_WAIT"); do
    if [ "$(http_get "$API/v1/deployments")" = "200" ]; then
      TINYLLAMA_STATE="$(jdepstate "$TINYLLAMA_ID")"
    fi
    case "$TINYLLAMA_STATE" in
      *ACTIVE*) echo "   ACTIVE (${i}s)"; break ;;
      *FAILED*) echo "   FAILED (${i}s)"; break ;;
    esac
    sleep 1
  done
  [ -n "$TINYLLAMA_STATE" ] && echo "   deployment state: $TINYLLAMA_STATE"
fi

# ---------------------------------------------------------------------------
# 5. Tell the user exactly where they stand
# ---------------------------------------------------------------------------
echo ""

# Full-inference path result
if [ "$FULL_INFERENCE" = "1" ]; then
  case "$TINYLLAMA_STATE" in
    *ACTIVE*)
      echo "=================================================================="
      echo " TinyLlama 1.1B is ACTIVE — real CPU inference is ready!"
      echo "=================================================================="
      echo ""
      echo "curl -sS $GW/chat/completions \\"
      echo "  -H 'Authorization: Bearer $KEY' \\"
      echo "  -H 'Content-Type: application/json' \\"
      echo "  -d '{\"model\":\"$TINYLLAMA_ID\",\"messages\":[{\"role\":\"user\",\"content\":\"Hello\"}],\"stream\":false}'"
      echo ""
      echo " Dashboard: http://localhost:3000"
      echo ""
      ;;
    *FAILED*)
      echo "=================================================================="
      echo " TinyLlama deployment FAILED. Check agent logs:"
      echo "   docker compose --profile full logs agent"
      echo "=================================================================="
      echo ""
      ;;
    *)
      echo "=================================================================="
      echo " TinyLlama not yet ACTIVE (state: ${TINYLLAMA_STATE:-pending})."
      echo "=================================================================="
      echo ""
      echo " The model may still be loading. Wait a moment then poll:"
      echo "   curl -s $API/v1/deployments | python3 -m json.tool | grep state"
      echo ""
      if [ "$READY" -lt 1 ] 2>/dev/null; then
        echo " No READY node detected. Ensure the agent service is up:"
        echo "   docker compose --profile full up -d"
        echo "   docker compose --profile full logs agent"
        echo ""
      fi
      ;;
  esac
fi

# Mock-model result (always shown)
case "$STATE" in
  *ACTIVE*)
    echo " Mock model '$MODEL_ID' also ACTIVE (canned responses)."
    echo "   curl -sS $GW/chat/completions \\"
    echo "     -H 'Authorization: Bearer $KEY' \\"
    echo "     -H 'Content-Type: application/json' \\"
    echo "     -d '{\"model\":\"$MODEL_ID\",\"messages\":[{\"role\":\"user\",\"content\":\"Hello\"}],\"stream\":false}'"
    echo ""
    ;;
  *)
    if [ "$FULL_INFERENCE" = "1" ]; then
      echo " Mock model '$MODEL_ID' registered but not deployed (no agent for mock engine)."
    else
      echo "=================================================================="
      echo " Catalog seeded — but inference is NOT available on the default stack."
      echo "=================================================================="
      echo ""
      echo " '$MODEL_ID' is registered and visible on the dashboard Catalog page"
      echo " (http://localhost:3000), but no deployment succeeded, so:"
      echo ""
      echo "   GET  $GW/models            -> {\"object\":\"list\",\"data\":[]}"
      echo "   POST $GW/chat/completions  -> 503 \"model not available\""
      echo ""
      echo " For real CPU inference, start the full profile and re-run:"
      echo "   # Build the agent image once:"
      echo "   sudo docker build -f deploy/docker/Dockerfile.agent \\"
      echo "     -t purser-agent:v0.6-llamacpp ."
      echo ""
      echo "   # Start with real inference:"
      echo "   docker compose --profile full up -d"
      echo "   PURSER_FULL_INFERENCE=1 make demo-seed"
      echo ""
      echo " The download is 637 MB (TinyLlama) + ~50 MB (llama-server)."
      echo " Set PURSER_SKIP_MODEL_DOWNLOAD=1 if you have pre-seeded the volumes."
      echo ""
      echo " What you DO have right now: the dashboard, the full control-plane REST"
      echo " API, and '$MODEL_ID' in the catalog to explore."
      echo ""
    fi
    ;;
esac
